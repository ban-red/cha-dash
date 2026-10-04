package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/topics"
)

// budgetDiffBytesPerMinute is the realtime budget for one browser tab on the
// Demo Home: diff frames per simulated minute, as sent (decimal kB). About
// 47 kB today (internal/README.md); the budget leaves ~15% for growth.
// Enforced in CI's "Performance budgets" step (not under -race, which slows
// the delivery this test waits on).
const budgetDiffBytesPerMinute = 55_000

// tab is a browser tab as the web app opens one: a subscription per topic
// (web/src/realtime/client.ts), counting what each receives.
type tab struct {
	mu      sync.Mutex
	seq     map[int64]uint64 // per subscription: the version its last frame brought it to
	bytes   map[int64]int    // diff bytes per subscription
	frames  int
	snaps   int
	resyncs int
	changed chan struct{}
}

func (tb *tab) read(ctx context.Context, conn *websocket.Conn) {
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var f struct {
			Op     string `json:"op"`
			ID     int64  `json:"id"`
			Seq    uint64 `json:"seq"`
			Resync bool   `json:"resync"`
		}
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		tb.mu.Lock()
		switch f.Op {
		case "snap":
			tb.snaps++
			if f.Resync {
				tb.resyncs++
			}
			tb.seq[f.ID] = f.Seq
		case "diff":
			tb.frames++
			tb.bytes[f.ID] += len(b)
			tb.seq[f.ID] = f.Seq
		}
		tb.mu.Unlock()
		select {
		case tb.changed <- struct{}{}:
		default:
		}
	}
}

// caughtUp reports whether every subscription has reached its version.
func (tb *tab) caughtUp(want map[int64]uint64) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	for id, v := range want {
		if s, ok := tb.seq[id]; !ok || s < v {
			return false
		}
	}
	return true
}

// TestBudgetDiffRate measures what one tab receives over the real gateway
// and a WebSocket, through 13:00–20:30 of the demo day (900 sample ticks,
// the footprint spike's window). Each tick's diffs are delivered before the
// next tick, as in real time, where ticks are 30 s apart and the gateway
// coalesces within 250 ms; the coalescing window is shortened to match the
// faster clock.
func TestBudgetDiffRate(t *testing.T) {
	if raceEnabled {
		t.Skip("paced in real time: CI runs it without -race")
	}
	const ticks = 900
	d, tp, clock := newDriver(t, Options{Seed: 1, At: 13 * time.Hour})
	hub := realtime.New(tp.Source(), realtime.Options{Coalesce: time.Millisecond, PingInterval: time.Hour})
	srv := httptest.NewServer(hub)
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(-1)
	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	send(map[string]any{"op": "hello", "v": realtime.ProtocolVersion})
	if _, _, err := conn.Read(ctx); err != nil { // the hub's hello
		t.Fatal(err)
	}
	subs := map[int64]string{1: topics.Graph, 2: topics.Metrics, 3: topics.Inbox}
	for id, topic := range subs {
		send(map[string]any{"op": "sub", "id": id, "topics": []string{topic}})
	}
	tb := &tab{seq: map[int64]uint64{}, bytes: map[int64]int{}, changed: make(chan struct{}, 1)}
	go tb.read(ctx, conn)

	versions := func() map[int64]uint64 {
		return map[int64]uint64{1: tp.Graph.Version(), 2: tp.Metrics.Version(), 3: tp.Inbox.Version()}
	}
	wait := func(want map[int64]uint64) bool {
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		for !tb.caughtUp(want) {
			select {
			case <-tb.changed:
			case <-deadline.C:
				return false
			}
		}
		return true
	}
	if !wait(versions()) {
		t.Fatal("no snapshots")
	}
	late := 0
	for range ticks {
		clock.Advance(30 * time.Second)
		d.Step()
		if !wait(versions()) {
			late++ // e.g. changes that cancelled out and sent nothing
		}
	}
	time.Sleep(20 * time.Millisecond) // anything still in flight

	tb.mu.Lock()
	defer tb.mu.Unlock()
	total := 0
	var per []string
	for id := range int64(len(subs)) {
		total += tb.bytes[id+1]
		per = append(per, subs[id+1]+" "+kB(tb.bytes[id+1]/(ticks/2)))
	}
	perMinute := total / (ticks / 2)
	t.Logf("one tab: %s per simulated minute over %d diff frames (%s); budget %s", kB(perMinute), tb.frames, strings.Join(per, ", "), kB(budgetDiffBytesPerMinute))
	if tb.resyncs > 0 || late > ticks/100 {
		t.Fatalf("%d resyncs and %d ticks not delivered in time: the measurement isn't clean", tb.resyncs, late)
	}
	if perMinute > budgetDiffBytesPerMinute {
		t.Errorf("a tab receives %s per simulated minute, over the %s budget; are whole series back?", kB(perMinute), kB(budgetDiffBytesPerMinute))
	}
}

func kB(b int) string { return fmt.Sprintf("%.1f kB", float64(b)/1000) }
