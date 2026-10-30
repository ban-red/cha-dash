package history

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"dash.cha.sh/internal/store"
)

// t0 is a Monday 10:00 UTC, aligned to every chunk but the 30-day one.
var t0 = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func openStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.OpenHistory(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func openRecorder(t *testing.T, st *store.Store, level Level, c *clock) *Recorder {
	t.Helper()
	r, err := Open(t.Context(), Options{Store: st, Level: level, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// feed records f(t) every 30 s from c.t for d, ticking each minute.
func feed(t *testing.T, r *Recorder, c *clock, d time.Duration, keys []string, f func(key string, at time.Time) float64) {
	t.Helper()
	end := c.t.Add(d)
	for ; c.t.Before(end); c.t = c.t.Add(30 * time.Second) {
		for _, k := range keys {
			r.Record(k, "i1", "%", c.t, f(k, c.t))
		}
		if c.t.Second() == 0 {
			r.tick(t.Context(), c.t)
		}
	}
}

func query(t *testing.T, r *Recorder, key string, tier Tier, from, to time.Time) []Point {
	t.Helper()
	pts, err := r.points(t.Context(), key, tier, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return pts
}

// minutes is the sample's minute of the day, so each minute's two samples
// are m and m+0.5.
func minutes(_ string, at time.Time) float64 {
	return float64(at.Hour()*60+at.Minute()) + float64(at.Second())/60
}

func TestRollsUpMinutesAndQuarters(t *testing.T) {
	c := &clock{t0}
	r := openRecorder(t, openStore(t, filepath.Join(t.TempDir(), "history.db")), Standard, c)
	feed(t, r, c, 2*time.Hour, []string{"vm:a#cpu"}, minutes)
	r.tick(t.Context(), c.t)

	m := query(t, r, "vm:a#cpu", Minute, t0, t0.Add(2*time.Hour))
	if len(m) != 120 {
		t.Fatalf("%d minute points", len(m))
	}
	if p := m[5]; p != (Point{Avg: 605.25, Min: 605, Max: 605.5, N: 2}) {
		t.Fatalf("10:05 = %+v", p)
	}
	q := query(t, r, "vm:a#cpu", Quarter, t0, t0.Add(2*time.Hour))
	// 10:15–10:29: 30 samples, 615 to 629.5.
	if p := q[1]; p != (Point{Avg: 622.25, Min: 615, Max: 629.5, N: 30}) {
		t.Fatalf("10:15 = %+v", p)
	}
	for i, p := range q {
		if p.N != 30 {
			t.Fatalf("quarter %d has %d samples", i, p.N)
		}
	}
	// Everything is on disk after Close, and the open hour is too.
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	var chunks int
	if err := r.st.Reader().QueryRow(`SELECT count(*) FROM chunks`).Scan(&chunks); err != nil || chunks != 2+1 {
		t.Fatalf("%d chunks on disk (%v), want two minute chunks and one quarter chunk", chunks, err)
	}
}

func TestPicksUpAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	st := openStore(t, path)
	c := &clock{t0}
	r := openRecorder(t, st, Standard, c)
	key := "vm:a#cpu"
	feed(t, r, c, 20*time.Minute, []string{key}, minutes)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Down for two minutes; back at 10:22.
	c.t = t0.Add(22 * time.Minute)
	r = openRecorder(t, openStore(t, path), Standard, c)
	feed(t, r, c, 40*time.Minute, []string{key}, minutes)
	r.tick(t.Context(), c.t)

	m := query(t, r, key, Minute, t0, t0.Add(time.Hour))
	for i, p := range m {
		gap := i == 20 || i == 21
		if (p.N == 0) != gap {
			t.Fatalf("minute %d: %+v", i, p)
		}
	}
	q := query(t, r, key, Quarter, t0, t0.Add(time.Hour))
	// The quarter the restart fell in folds minutes from both sides of it.
	if q[1].N != 26 || q[0].N != 30 || q[2].N != 30 {
		t.Fatalf("quarters = %+v", q)
	}
	// 09:30 is before the first sample; 10:00 is 56 samples, 10:20 and 10:21 missing.
	want := (30*607.25 + 10*617.25 + 16*625.75) / 56
	if got := r.Sparkline(key, t0.Add(30*time.Minute), 3, 30*time.Minute); !math.IsNaN(got[0]) || math.Abs(got[1]-want) > 1e-3 || got[2] != 644.75 {
		t.Fatalf("sparkline = %v", got)
	}
}

func TestGapsStayGaps(t *testing.T) {
	c := &clock{t0}
	r := openRecorder(t, openStore(t, filepath.Join(t.TempDir(), "history.db")), Standard, c)
	key := "vm:a#cpu"
	feed(t, r, c, 5*time.Minute, []string{key}, minutes)
	c.t = t0.Add(90 * time.Minute)
	feed(t, r, c, time.Minute, []string{key}, minutes)
	r.Record(key, "i1", "%", c.t, math.NaN())
	r.tick(t.Context(), c.t.Add(time.Hour))

	q := query(t, r, key, Quarter, t0, t0.Add(2*time.Hour))
	var present []int
	for i, p := range q {
		if p.N > 0 {
			present = append(present, i)
		}
	}
	if len(present) != 2 || present[0] != 0 || present[1] != 6 || q[0].N != 10 || q[6].N != 2 {
		t.Fatalf("quarters present at %v: %+v", present, q)
	}
	// A sample from before the open minute is dropped.
	r.Record(key, "i1", "%", t0, 1)
	if m := query(t, r, key, Minute, t0, t0.Add(time.Minute)); m[0].N != 2 {
		t.Fatalf("an old sample changed 10:00: %+v", m[0])
	}
}

func TestLongTermFoldsHoursAndDays(t *testing.T) {
	c := &clock{t0}
	r := openRecorder(t, openStore(t, filepath.Join(t.TempDir(), "history.db")), LongTerm, c)
	key := "vm:a#cpu"
	// Two days, one sample a minute of 1 (so counts are minutes).
	end := t0.Add(48 * time.Hour)
	for ; c.t.Before(end); c.t = c.t.Add(time.Minute) {
		r.Record(key, "i1", "%", c.t, 1)
		r.tick(t.Context(), c.t)
	}
	r.tick(t.Context(), c.t.Add(time.Minute))
	h := query(t, r, key, Hour, t0, t0.Add(4*time.Hour))
	for i, p := range h {
		if p.N != 60 {
			t.Fatalf("hour %d = %+v", i, p)
		}
	}
	// Tuesday is the first whole UTC day.
	d := query(t, r, key, Day, t0.Truncate(day).Add(day), t0.Truncate(day).Add(2*day))
	if len(d) != 1 || d[0].N != 1440 || d[0].Avg != 1 {
		t.Fatalf("days = %+v", d)
	}
	rg, err := r.Query(t.Context(), key, t0, t0.Add(48*time.Hour), 100)
	if err != nil || rg.Step != time.Hour || len(rg.Points) != 48 {
		t.Fatalf("Query picked %v with %d points (%v)", rg.Step, len(rg.Points), err)
	}
}

func TestRetentionDropsOldChunksAndForgetsSeries(t *testing.T) {
	c := &clock{t0}
	r := openRecorder(t, openStore(t, filepath.Join(t.TempDir(), "history.db")), Standard, c)
	feed(t, r, c, 2*time.Hour, []string{"vm:gone#cpu"}, minutes)
	r.tick(t.Context(), c.t.Add(time.Hour))
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	count := func(q string) (n int) {
		if err := r.st.Reader().QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := r.retain(t.Context(), t0.Add(3*day)); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM chunks WHERE tier = 1`); n != 0 {
		t.Fatalf("%d minute chunks after 3 days", n)
	}
	if n := count(`SELECT count(*) FROM chunks WHERE tier = 2`); n != 1 {
		t.Fatalf("%d quarter chunks after 3 days", n)
	}
	if err := r.retain(t.Context(), t0.Add(15*day)); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM series`); n != 0 || len(r.series) != 0 {
		t.Fatalf("%d series rows and %d in memory after 15 days", n, len(r.series))
	}
}

func TestOffRecordsNothing(t *testing.T) {
	if _, err := Open(context.Background(), Options{Level: Off}); err == nil {
		t.Fatal("Open at Off should fail")
	}
	if _, err := ParseLevel("forever"); err == nil {
		t.Fatal("ParseLevel accepted forever")
	}
}

// TestBudgetAtStandard records 1,000 series every 30 s for 48 hours, each
// changing on every sample: the worst case for the encoding, which gets 4
// bytes a point on the Demo Home but about 10 here. It checks the disk
// budget in 13 · History. Run with -v for the numbers.
func TestBudgetAtStandard(t *testing.T) {
	if testing.Short() {
		t.Skip("records 5.8 million samples")
	}
	st := openStore(t, filepath.Join(t.TempDir(), "history.db"))
	c := &clock{t0}
	r := openRecorder(t, st, Standard, c)
	keys := make([]string, 1000)
	walk := make(map[string]float64, len(keys))
	for i := range keys {
		keys[i] = fmt.Sprintf("vm:%d#cpu", i)
		walk[keys[i]] = float64(i % 100)
	}
	var steps uint64 = 1
	start := time.Now()
	feed(t, r, c, 48*time.Hour, keys, func(k string, _ time.Time) float64 {
		// A random walk, rounded like most gauges.
		steps = steps*6364136223846793005 + 1442695040888963407
		walk[k] += float64(int64(steps>>60)-8) / 10
		return math.Round(walk[k]*10) / 10
	})
	took := time.Since(start)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	var pages, pageSize, data int64
	_ = st.Reader().QueryRow(`PRAGMA page_count`).Scan(&pages)
	_ = st.Reader().QueryRow(`PRAGMA page_size`).Scan(&pageSize)
	_ = st.Reader().QueryRow(`SELECT sum(length(data)) FROM chunks`).Scan(&data)
	mb := float64(pages*pageSize) / 1e6
	t.Logf("1,000 series for 48 hours: %.1f MB on disk, %.1f MB of it chunk data; %.0f ns a sample, rollups and writes included",
		mb, float64(data)/1e6, float64(took.Nanoseconds())/float64(48*120*len(keys)))
	if mb > 45 {
		t.Errorf("%.1f MB is over the 45 MB budget", mb)
	}
}
