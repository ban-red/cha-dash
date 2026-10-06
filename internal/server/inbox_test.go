package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/topics"
)

// The Demo Home's inbox over the WebSocket: the owner gets every incident
// whole; a household account may open the socket and read the graph and
// metrics its boards need, but gets the household projection of the inbox.
func TestHouseholdInboxOverRealtime(t *testing.T) {
	wall := time.Now()
	tp := topics.New(graph.New(), wall)
	drv, err := demo.New(tp, demo.Options{Seed: 1, At: demo.DefaultAt, Clock: func() time.Time { return wall }})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, func(d *Deps) {
		hub := realtime.New(tp.SourceWith(realtime.Mux{topics.Inbox: tp.InboxSource(InboxViewer(boards.New(d.Store), tp.Graph, d.Log))}),
			realtime.Options{Coalesce: 10 * time.Millisecond, Log: d.Log})
		t.Cleanup(hub.Close)
		d.Realtime, d.Demo = hub, drv
	})
	owner := signIn(t, e)
	_, household := addUser(t, e, owner, "kid", "household")

	snapshot := func(c *http.Client, topic string) map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", &websocket.DialOptions{HTTPClient: c})
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		ws.SetReadLimit(16 << 20)
		for _, m := range []map[string]any{{"op": "hello", "v": 1}, {"op": "sub", "id": 1, "topics": []string{topic}}} {
			b, _ := json.Marshal(m)
			if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
				t.Fatal(err)
			}
		}
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var f map[string]any
			json.Unmarshal(b, &f)
			if f["op"] == "snap" {
				return f["data"].(map[string]any)[topic].(map[string]any)
			}
		}
	}
	all := snapshot(owner, "inbox")["incidents"].(map[string]any)
	mine := snapshot(household, "inbox")["incidents"].(map[string]any)
	if len(all) == 0 || len(mine) == 0 || len(mine) >= len(all) {
		t.Fatalf("owner sees %d incidents, household %d", len(all), len(mine))
	}
	withFacts := 0
	for _, v := range all {
		if v.(map[string]any)["facts"] != nil {
			withFacts++
		}
	}
	if withFacts == 0 {
		t.Fatal("the owner's incidents carry no facts; the test proves nothing")
	}
	for id, v := range mine {
		in := v.(map[string]any)
		if in["class"] != "problem" || in["category"] == "security" {
			t.Errorf("household sees %s (%v, %v)", id, in["class"], in["category"])
		}
		if in["facts"] != nil || in["summary"] != "" || len(in["causes"].([]any)) != 0 || len(in["blast_radius"].([]any)) != 0 {
			t.Errorf("household's %s carries detail: %v", id, in)
		}
		if all[id] == nil {
			t.Errorf("household sees %s, which the owner doesn't", id)
		}
	}
	// Boards need the graph and metrics: both stay open to household.
	if len(snapshot(household, "graph")["resources"].(map[string]any)) == 0 {
		t.Fatal("household gets no graph")
	}
	if len(snapshot(household, "metrics")["series"].(map[string]any)) == 0 {
		t.Fatal("household gets no metrics")
	}
}
