package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/topics"
)

// The Demo Home's fixes through the real stack: a session, the engine
// under OwnerOnly, the tasks topic over the WebSocket, and the audit log.
func TestDemoFixThroughTheEngine(t *testing.T) {
	// The demo's clock, moved by the test and read by handlers.
	var mu sync.Mutex
	wall := time.Now()
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return wall }
	tp := topics.New(graph.New(), wall)
	drv, err := demo.New(tp, demo.Options{Seed: 5, At: demo.DefaultAt, Clock: now})
	if err != nil {
		t.Fatal(err)
	}
	var eng *action.Engine
	e := newEnvWith(t, func(d *Deps) {
		reg := action.NewRegistry()
		if err := reg.Register(demo.ActionSource, drv.Actions()...); err != nil {
			t.Fatal(err)
		}
		eng, err = action.New(t.Context(), action.Options{Store: d.Store, Audit: audit.New(d.Store), Registry: reg, Policy: action.OwnerOnly{}, Graph: tp.Graph, Log: d.Log})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(eng.Close)
		hub := realtime.New(tp.SourceWith(realtime.Mux{action.TasksTopic: eng.TasksSource()}), realtime.Options{Coalesce: 10 * time.Millisecond, Log: d.Log})
		t.Cleanup(hub.Close)
		d.Realtime, d.Demo, d.Actions = hub, drv, eng
	})

	anon := &http.Client{}
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/actions/plan"}, {"POST", "/api/plans/p_x/approve"}, {"GET", "/api/tasks/t_x"},
		{"GET", "/api/audit"}, {"GET", "/api/audit/export"}, {"GET", "/api/audit/verify"},
	} {
		if resp := e.request(t, anon, r.method, r.path, map[string]any{}, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: %d", r.method, r.path, resp.StatusCode)
		}
	}

	c := signIn(t, e)
	// Watch the tasks topic first, so the task arrives as a diff.
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: c})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	write := func(v any) {
		b, _ := json.Marshal(v)
		if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var f map[string]any
			json.Unmarshal(b, &f)
			if f["op"] != "ping" {
				return f
			}
		}
	}
	write(map[string]any{"op": "hello", "v": 1})
	read()
	write(map[string]any{"op": "sub", "id": 1, "topics": []string{"tasks"}})
	if snap := read(); snap["op"] != "snap" || len(snap["data"].(map[string]any)["tasks"].(map[string]any)["tasks"].(map[string]any)) != 0 {
		t.Fatalf("tasks snapshot = %v", snap)
	}

	// Plex's Reconnect is T0: plan, then approve straight away.
	plan := decode(t, e.request(t, c, "POST", "/api/actions/plan", map[string]any{"action": "integration.reconnect", "target": "integration:plex"}, nil))
	if plan["tier"] != "T0" || plan["confirm"] != "none" || plan["label"] != "Reconnect Plex" {
		t.Fatalf("plan = %v", plan)
	}
	resp := e.request(t, c, "POST", "/api/plans/"+plan["id"].(string)+"/approve", map[string]any{"hash": plan["hash"]}, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("approve: %d", resp.StatusCode)
	}
	task := decode(t, resp)["task"].(map[string]any)
	id := task["id"].(string)

	diff := read()
	if diff["op"] != "diff" || !strings.Contains(mustJSON(diff["patch"]), "/tasks/tasks/"+id) {
		t.Fatalf("the task didn't arrive on the topic: %v", diff)
	}
	if got := decode(t, e.request(t, c, "GET", "/api/tasks/"+id, nil, nil)); got["plan"] != plan["id"] {
		t.Fatalf("task = %v", got)
	}
	// The repair resolves 10 s after the fix (18:00:10 in the script, here
	// relative to now): step the demo along until the task finishes.
	for range 10 {
		mu.Lock()
		wall = wall.Add(30 * time.Second)
		mu.Unlock()
		drv.Step()
		got := decode(t, e.request(t, c, "GET", "/api/tasks/"+id, nil, nil))
		if got["state"] == "succeeded" && got["audit"] != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := decode(t, e.request(t, c, "GET", "/api/tasks/"+id, nil, nil))
	if got["state"] != "succeeded" {
		t.Fatalf("task = %v", got)
	}
	events := decode(t, e.request(t, c, "GET", "/api/audit?action=integration.reconnect", nil, nil))["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["task"] != id || events[0].(map[string]any)["actor"].(map[string]any)["name"] != "owner" {
		t.Fatalf("audit = %v", events)
	}
	if v := decode(t, e.request(t, c, "GET", "/api/audit/verify", nil, nil)); v["ok"] != true || v["checked"] != 1.0 {
		t.Fatalf("verify = %v", v)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
