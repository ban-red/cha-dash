package opnsense

import (
	"context"
	"encoding/json"
	"math"
	"time"
)

// OPNsense's byte counters run from boot, so the 30-day totals are the
// hub's: each counter read adds what passed since the last one to the day
// it passed in. The days and the last read are kept in the instance's state
// (integration.State), so a hub restart neither loses the month nor misses
// what passed while the hub was down: the first read after it counts that
// too, spread over the gap.
const (
	stateKey     = "wan_totals"
	stateVersion = 1
	totalsWindow = 30 * 24 * time.Hour
	keepDays     = 31              // days kept: the window, and the day it slides past
	saveEvery    = 5 * time.Minute // and whenever the day changes, and on Close
)

// totals are the WAN's traffic per UTC day, and the last counter read.
type totals struct {
	V         int                   `json:"v"`
	Interface string                `json:"interface"` // the WAN interface counted
	Since     time.Time             `json:"since"`     // the first read: totals cover from here
	At        time.Time             `json:"at"`        // the last read, by the hub's clock
	RX        uint64                `json:"rx_bytes"`  // the last read's counters
	TX        uint64                `json:"tx_bytes"`
	Days      map[string][2]float64 `json:"days"` // "2026-10-03" → bytes received, transmitted
}

func dayOf(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// counting reports whether anything has been counted since the first read.
func (t *totals) counting() bool { return t.At.After(t.Since) }

// add counts a counter read. The first read, or a read of another
// interface (the config changed), starts over.
func (t *totals) add(now time.Time, iface string, c *counters) {
	if t.At.IsZero() || t.Interface != iface || t.V != stateVersion {
		*t = totals{V: stateVersion, Interface: iface, Since: now, At: now, RX: c.rx, TX: c.tx, Days: map[string][2]float64{}}
		return
	}
	t.spread(t.At, now, float64(delta(t.RX, c.rx)), float64(delta(t.TX, c.tx)))
	t.At, t.RX, t.TX = now, c.rx, c.tx
	t.prune(now)
}

// delta is what passed between two counter reads. A counter that went down
// was reset (the router rebooted), so it counts from zero.
func delta(prev, cur uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return cur
}

// spread adds bytes that passed between from and to to the days they
// passed in, in proportion to time. Bytes from before the kept days fall
// out.
func (t *totals) spread(from, to time.Time, rx, tx float64) {
	if !to.After(from) {
		t.addDay(dayOf(to), rx, tx)
		return
	}
	total := to.Sub(from).Seconds()
	cur := from.UTC()
	if first := to.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(keepDays - 1)); cur.Before(first) {
		cur = first
	}
	for cur.Before(to) {
		end := cur.Truncate(24 * time.Hour).Add(24 * time.Hour)
		if end.After(to) {
			end = to.UTC()
		}
		f := end.Sub(cur).Seconds() / total
		t.addDay(dayOf(cur), rx*f, tx*f)
		cur = end
	}
}

func (t *totals) addDay(day string, rx, tx float64) {
	d := t.Days[day]
	t.Days[day] = [2]float64{d[0] + rx, d[1] + tx}
}

func (t *totals) prune(now time.Time) {
	oldest := dayOf(now.AddDate(0, 0, -(keepDays - 1)))
	for day := range t.Days {
		if day < oldest {
			delete(t.Days, day)
		}
	}
}

// sum is the bytes received and transmitted over the last 30 UTC days,
// today included.
func (t *totals) sum(now time.Time) (rx, tx float64) {
	oldest := dayOf(now.AddDate(0, 0, -29))
	for day, v := range t.Days {
		if day >= oldest {
			rx, tx = rx+v[0], tx+v[1]
		}
	}
	return rx, tx
}

// load reads the totals from the instance's state, once. A state that can't
// be read is tried again at the next sync; until then nothing is counted,
// so nothing saved can overwrite it.
func (in *instance) load(ctx context.Context) {
	if in.totals != nil {
		return
	}
	b, err := in.d.State.Load(ctx, stateKey)
	if err != nil {
		in.d.Log.Warn("couldn't load the WAN totals; trying again at the next sync", "err", err)
		return
	}
	t := &totals{}
	if b != nil {
		if err := json.Unmarshal(b, t); err != nil || t.V != stateVersion {
			in.d.Log.Warn("the saved WAN totals are unreadable; counting from now", "err", err)
			t = &totals{}
		}
	}
	if t.Days == nil {
		t.Days = map[string][2]float64{}
	}
	in.totals = t
}

// save writes the totals to the instance's state.
func (in *instance) save(ctx context.Context, now time.Time) {
	if in.totals == nil {
		return
	}
	b, err := json.Marshal(in.totals)
	if err == nil {
		err = in.d.State.Save(ctx, stateKey, b)
	}
	if err != nil {
		in.d.Log.Warn("couldn't save the WAN totals", "err", err)
		return
	}
	in.savedAt, in.dirty = now, false
}

func roundTo(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
