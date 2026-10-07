package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/jsonpatch"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/store"
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

// budgetTaskLogBytes is the realtime budget for a log-heavy task: the diff
// bytes one tab receives on the tasks topic while a task logs taskLogLines
// lines of about 80 bytes, each delivered before the next (a pull's
// progress, say). Appends (add …/log/-) keep most lines to one small op:
// about 53 kB today. Resending the whole log with every line came to about
// 790 kB.
const (
	budgetTaskLogBytes = 80_000
	taskLogLines       = 200
)

func TestBudgetTaskLog(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	step := make(chan struct{})
	reg := action.NewRegistry()
	if err := reg.Register("budget", action.Def{ID: "budget.pull", Title: "Pull", Tier: action.T1,
		Resolve: func(_ context.Context, target string) (action.Target, error) {
			return action.Target{ID: target, Kind: "app", Name: "web"}, nil
		},
		Execute: func(ctx context.Context, _ action.Request, run *action.TaskRun) error {
			for i := range taskLogLines + 1 {
				select {
				case <-step:
				case <-ctx.Done():
					return ctx.Err()
				}
				if i < taskLogLines {
					run.Logf("pulling layer %04d: %5.1f MB of 812.4 MB, %3d%% done, eta %3ds, from registry.example", i, float64(i)*4.06, i/2, 200-i)
				}
			}
			return nil
		}}); err != nil {
		t.Fatal(err)
	}
	owner := auth.Identity{User: auth.User{ID: demoOwner.ID, Username: demoOwner.Name, Role: "owner"}, Session: auth.Session{ID: demoOwner.Session}}
	e, err := action.New(t.Context(), action.Options{Store: st, Audit: audit.New(st), Registry: reg, Policy: action.OwnerOnly{},
		Sessions: action.Sessions{Lookup: func(context.Context) (auth.Identity, bool) { return owner, true }}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)

	hub := realtime.New(e.TasksSource(), realtime.Options{Coalesce: time.Millisecond, PingInterval: time.Hour})
	srv := httptest.NewServer(hub)
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
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
	send(map[string]any{"op": "sub", "id": 1, "topics": []string{action.TasksTopic}})

	// The tab applies every frame, as the web app does, and counts diff bytes.
	frames := make(chan int, 16) // each diff frame's size
	var mu sync.Mutex
	var doc map[string]any
	var applyErr error
	go func() {
		for {
			_, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var f struct {
				Op    string         `json:"op"`
				Data  map[string]any `json:"data"`
				Patch []jsonpatch.Op `json:"patch"`
			}
			if json.Unmarshal(b, &f) != nil {
				continue
			}
			mu.Lock()
			switch f.Op {
			case "snap":
				doc = f.Data
			case "diff":
				if err := jsonpatch.Apply(doc, f.Patch); err != nil && applyErr == nil {
					applyErr = err
				}
			}
			mu.Unlock()
			if f.Op == "diff" {
				frames <- len(b)
			}
		}
	}()
	next := func() int {
		t.Helper()
		select {
		case n := <-frames:
			return n
		case <-time.After(5 * time.Second):
			t.Fatal("no diff frame")
			return 0
		}
	}
	quiet := func() { // drain whatever is still in flight
		for {
			select {
			case <-frames:
			case <-time.After(100 * time.Millisecond):
				return
			}
		}
	}
	quiet() // the snapshot

	p, err := e.Plan(ctx, demoOwner, action.PlanRequest{Action: "budget.pull", Target: "app:web"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := e.Approve(ctx, demoOwner, p.ID, action.ApproveRequest{Hash: p.Hash}, "")
	if err != nil {
		t.Fatal(err)
	}
	quiet() // the task appearing and starting
	total := 0
	for range taskLogLines {
		step <- struct{}{}
		total += next()
	}

	// What the tab holds is the task's log.
	mu.Lock()
	var got []any
	if tasks, ok := doc[action.TasksTopic].(map[string]any)["tasks"].(map[string]any); ok {
		if tk, ok := tasks[task.ID].(map[string]any); ok {
			got, _ = tk["log"].([]any)
		}
	}
	err = applyErr
	mu.Unlock()
	if err != nil {
		t.Fatalf("applying a diff: %v", err)
	}
	want, err := e.Task(ctx, demoOwner, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || len(got) != len(want.Log) || got[0] != want.Log[0] || got[len(got)-1] != want.Log[len(want.Log)-1] {
		t.Fatalf("the tab's log (%d lines) isn't the task's (%d lines)", len(got), len(want.Log))
	}
	step <- struct{}{} // let it finish

	t.Logf("one tab: %s of diffs while a task logs %d lines; budget %s", kB(total), taskLogLines, kB(budgetTaskLogBytes))
	if total > budgetTaskLogBytes {
		t.Errorf("a tab receives %s while a task logs %d lines, over the %s budget; is the whole log back on every line?", kB(total), taskLogLines, kB(budgetTaskLogBytes))
	}
}
