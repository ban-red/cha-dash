package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/jsonpatch"
)

type frame = map[string]any

type testClient struct {
	t  *testing.T
	ws *websocket.Conn
}

func newServer(t *testing.T, src Source, opts Options) (*Hub, string) {
	t.Helper()
	h := New(src, opts)
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		h.Close()
		srv.Close()
	})
	return h, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dial(t *testing.T, url string) *testClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return &testClient{t: t, ws: ws}
}

func (c *testClient) send(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	if err := c.ws.Write(c.t.Context(), websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

// next returns the next frame, skipping server pings.
func (c *testClient) next() frame {
	c.t.Helper()
	for {
		f, err := c.read(5 * time.Second)
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		if f["op"] != "ping" {
			return f
		}
	}
}

func (c *testClient) read(timeout time.Duration) (frame, error) {
	ctx, cancel := context.WithTimeout(c.t.Context(), timeout)
	defer cancel()
	_, b, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	var f frame
	if err := json.Unmarshal(b, &f); err != nil {
		c.t.Fatalf("bad frame %s: %v", b, err)
	}
	return f, nil
}

func (c *testClient) hello() frame {
	c.t.Helper()
	c.send(frame{"op": "hello", "v": 1})
	f := c.next()
	if f["op"] != "hello" || f["v"] != 1.0 || f["epoch"] == "" {
		c.t.Fatalf("hello reply = %v", f)
	}
	return f
}

func expectOp(t *testing.T, f frame, op string) {
	t.Helper()
	if f["op"] != op {
		t.Fatalf("got %v, want op %q", f, op)
	}
}

func upsert(t *testing.T, g *graph.Graph, rs ...graph.Resource) {
	t.Helper()
	if _, err := g.Update(func(tx *graph.Tx) error {
		for _, r := range rs {
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func generic(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	json.Unmarshal(b, &out)
	return out
}

// apply applies a diff frame's patch to a client-side document.
func apply(t *testing.T, doc frame, f frame) {
	t.Helper()
	b, _ := json.Marshal(f["patch"])
	var ops []jsonpatch.Op
	if err := json.Unmarshal(b, &ops); err != nil {
		t.Fatal(err)
	}
	if err := jsonpatch.Apply(doc, ops); err != nil {
		t.Fatalf("apply %v: %v", f, err)
	}
}

func TestHelloSubSnapDiff(t *testing.T) {
	g := graph.New()
	upsert(t, g, graph.Resource{ID: "host:jupiter", Kind: "host", Name: "jupiter"})
	_, url := newServer(t, GraphSource{Graph: g}, Options{Coalesce: 50 * time.Millisecond, Server: "chadash test"})
	c := dial(t, url)

	if h := c.hello(); h["server"] != "chadash test" || h["ping"] != 30.0 {
		t.Fatalf("hello = %v", h)
	}

	c.send(frame{"op": "sub", "id": 1, "topics": []string{"graph", "graph:kind/container"}})
	snap := c.next()
	expectOp(t, snap, "snap")
	if snap["id"] != 1.0 || snap["seq"] != 1.0 {
		t.Fatalf("snap header = %v", snap)
	}
	doc := snap["data"].(frame)
	if _, ok := doc["graph"].(frame)["resources"].(frame)["host:jupiter"]; !ok {
		t.Fatalf("snapshot missing the host: %v", doc)
	}
	if n := len(doc["graph:kind/container"].(frame)["resources"].(frame)); n != 0 {
		t.Fatalf("container view should be empty, has %d", n)
	}

	// One change: one diff, with paths under each topic's member.
	upsert(t, g, graph.Resource{ID: "container:plex", Kind: "container", State: "running"})
	d := c.next()
	expectOp(t, d, "diff")
	if d["id"] != 1.0 || d["seq"] != 2.0 {
		t.Fatalf("diff header = %v", d)
	}
	var paths []string
	for _, op := range d["patch"].([]any) {
		paths = append(paths, op.(frame)["path"].(string))
	}
	want := []string{"/graph/resources/container:plex", "/graph:kind~1container/resources/container:plex"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("diff paths = %v, want %v", paths, want)
	}
	apply(t, doc, d)

	// A burst is coalesced into fewer frames, and the client converges.
	for i := range 20 {
		upsert(t, g, graph.Resource{ID: "container:plex", Kind: "container", State: fmt.Sprintf("tick-%d", i)})
	}
	frames := 0
	for g.Version() != uint64(snapSeq(t, d)) || frames == 0 {
		d = c.next()
		expectOp(t, d, "diff")
		apply(t, doc, d)
		frames++
	}
	if frames >= 20 {
		t.Fatalf("20 updates produced %d frames; expected coalescing", frames)
	}
	_, all := g.Snapshot(nil)
	_, containers := g.Snapshot(graph.KindFilter("container"))
	if !reflect.DeepEqual(generic(t, doc), generic(t, map[string]any{"graph": all, "graph:kind/container": containers})) {
		t.Fatalf("client document diverged:\n%v", doc)
	}
}

func snapSeq(t *testing.T, f frame) int {
	t.Helper()
	return int(f["seq"].(float64))
}

func TestResyncOnLag(t *testing.T) {
	g := graph.New()
	// Tiny feed buffer and a long coalescing window: a burst during the window
	// overflows the feed, which must turn into a fresh snapshot.
	_, url := newServer(t, GraphSource{Graph: g, MaxPending: 4}, Options{Coalesce: 300 * time.Millisecond})
	c := dial(t, url)
	c.hello()
	c.send(frame{"op": "sub", "id": 7, "topics": []string{"graph"}})
	expectOp(t, c.next(), "snap")

	upsert(t, g, graph.Resource{ID: "app:0", Kind: "app"})
	expectOp(t, c.next(), "diff") // first change goes out immediately

	for i := range 20 {
		upsert(t, g, graph.Resource{ID: graph.RID(fmt.Sprintf("app:%d", i+1)), Kind: "app"})
	}
	f := c.next()
	expectOp(t, f, "snap")
	if f["resync"] != true || f["id"] != 7.0 {
		t.Fatalf("expected a resync snapshot, got %v", f)
	}
	if uint64(snapSeq(t, f)) != g.Version() {
		t.Fatalf("resync seq %v, graph at %d", f["seq"], g.Version())
	}
	if n := len(f["data"].(frame)["graph"].(frame)["resources"].(frame)); n != 21 {
		t.Fatalf("resync snapshot has %d resources, want 21", n)
	}

	// The subscription carries on with diffs afterwards.
	upsert(t, g, graph.Resource{ID: "app:after", Kind: "app"})
	expectOp(t, c.next(), "diff")
}

// logWatch is a log sink a test can poll for a message.
type logWatch struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *logWatch) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logWatch) contains(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(l.buf.String(), s)
}

// A full send queue drops the diff and resyncs, without the WebSocket involved.
func TestDropToResyncWhenSendQueueFull(t *testing.T) {
	g := graph.New()
	logs := &logWatch{}
	h := New(GraphSource{Graph: g}, Options{Coalesce: time.Millisecond, Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &conn{h: h, ctx: ctx, cancel: cancel, out: make(chan []byte, 1)}
	s := &sub{id: 3, ctx: ctx, cancel: cancel, notify: make(chan struct{}, 1)}
	f, err := h.src.Open("graph", s.notify)
	if err != nil {
		t.Fatal(err)
	}
	s.feeds = []topicFeed{{topic: "graph", prefix: "/graph", feed: f}}
	s.snapshot()

	c.out <- []byte(`{"op":"filler"}`) // the queue is now full
	done := make(chan struct{})
	go func() { s.run(c); close(done) }()
	upsert(t, g, graph.Resource{ID: "app:x", Kind: "app"})
	// Only drain the queue once the diff has been dropped.
	eventually(t, func() bool { return logs.contains("client behind") })

	var got []frame
	for len(got) < 2 {
		select {
		case b := <-c.out:
			var fr frame
			json.Unmarshal(b, &fr)
			got = append(got, fr)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out; frames so far: %v", got)
		}
	}
	if got[0]["op"] != "filler" || got[1]["op"] != "snap" || got[1]["resync"] != true {
		t.Fatalf("frames = %v; want filler then a resync snap", got)
	}
	if _, ok := got[1]["data"].(frame)["graph"].(frame)["resources"].(frame)["app:x"]; !ok {
		t.Fatalf("resync snapshot lacks the dropped change: %v", got[1])
	}
	cancel()
	<-done
}

func TestProtocolErrorsKeepConnection(t *testing.T) {
	_, url := newServer(t, GraphSource{Graph: graph.New()}, Options{})
	c := dial(t, url)
	c.hello()

	check := func(send any, code string) {
		t.Helper()
		if s, ok := send.(string); ok {
			c.ws.Write(t.Context(), websocket.MessageText, []byte(s))
		} else {
			c.send(send)
		}
		f := c.next()
		if f["op"] != "error" || f["code"] != code {
			t.Fatalf("sent %v: got %v, want error %q", send, f, code)
		}
	}
	check(frame{"op": "sub", "id": 1, "topics": []string{"metric:host/x/cpu"}}, "unknown_topic")
	check(frame{"op": "sub", "id": 1, "topics": []string{}}, "bad_request")
	check(frame{"op": "sub", "id": 0, "topics": []string{"graph"}}, "bad_request")
	check(frame{"op": "unsub", "id": 99}, "unknown_sub")
	check(frame{"op": "launch"}, "unknown_op")
	check(frame{"op": "hello", "v": 1}, "bad_request")
	check(`{not json`, "bad_request")

	c.send(frame{"op": "sub", "id": 1, "topics": []string{"graph"}})
	expectOp(t, c.next(), "snap")
	check(frame{"op": "sub", "id": 1, "topics": []string{"graph"}}, "duplicate_id")

	c.send(frame{"op": "ping"})
	expectOp(t, c.next(), "pong")
}

func closeStatus(t *testing.T, c *testClient) websocket.StatusCode {
	t.Helper()
	for {
		_, err := c.read(5 * time.Second)
		if err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func TestHandshakeRules(t *testing.T) {
	_, url := newServer(t, GraphSource{Graph: graph.New()}, Options{HelloTimeout: 200 * time.Millisecond})

	t.Run("sub before hello", func(t *testing.T) {
		c := dial(t, url)
		c.send(frame{"op": "sub", "id": 1, "topics": []string{"graph"}})
		if s := closeStatus(t, c); s != websocket.StatusPolicyViolation {
			t.Fatalf("close status = %v", s)
		}
	})
	t.Run("wrong version", func(t *testing.T) {
		c := dial(t, url)
		c.send(frame{"op": "hello", "v": 2})
		f := c.next()
		if f["op"] != "error" || f["code"] != "unsupported_version" {
			t.Fatalf("got %v", f)
		}
		if s := closeStatus(t, c); s != websocket.StatusPolicyViolation {
			t.Fatalf("close status = %v", s)
		}
	})
	t.Run("silent client", func(t *testing.T) {
		c := dial(t, url)
		if s := closeStatus(t, c); s != websocket.StatusPolicyViolation {
			t.Fatalf("close status = %v", s)
		}
	})
}

func TestOriginCheck(t *testing.T) {
	_, url := newServer(t, GraphSource{Graph: graph.New()}, Options{OriginPatterns: []string{"https://dash.example.com"}})
	try := func(origin string) (int, error) {
		hdr := http.Header{}
		hdr.Set("Origin", origin)
		ws, resp, err := websocket.Dial(t.Context(), url, &websocket.DialOptions{HTTPHeader: hdr})
		if err == nil {
			ws.CloseNow()
		}
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		return code, err
	}
	if code, err := try("https://evil.example"); err == nil || code != http.StatusForbidden {
		t.Fatalf("foreign origin: code=%d err=%v", code, err)
	}
	if _, err := try("https://dash.example.com"); err != nil {
		t.Fatalf("allowed origin rejected: %v", err)
	}
	same := "http" + strings.TrimPrefix(url, "ws")
	if _, err := try(same); err != nil {
		t.Fatalf("same origin rejected: %v", err)
	}
}

func TestPing(t *testing.T) {
	_, url := newServer(t, GraphSource{Graph: graph.New()}, Options{PingInterval: 50 * time.Millisecond})
	c := dial(t, url)
	c.hello()
	f, err := c.read(5 * time.Second)
	if err != nil || f["op"] != "ping" {
		t.Fatalf("expected a ping, got %v %v", f, err)
	}
}

// countingSource wraps a Source and counts open feeds.
type countingSource struct {
	Source
	open atomic.Int64
}

type countedFeed struct {
	Feed
	src *countingSource
}

func (c *countingSource) Open(topic string, notify chan<- struct{}) (Feed, error) {
	f, err := c.Source.Open(topic, notify)
	if err != nil {
		return nil, err
	}
	c.open.Add(1)
	return countedFeed{f, c}, nil
}

func (f countedFeed) Close() {
	f.src.open.Add(-1)
	f.Feed.Close()
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFeedsReleased(t *testing.T) {
	g := graph.New()
	src := &countingSource{Source: GraphSource{Graph: g}}
	_, url := newServer(t, src, Options{})
	c := dial(t, url)
	c.hello()

	c.send(frame{"op": "sub", "id": 1, "topics": []string{"graph", "graph:kind/app"}})
	expectOp(t, c.next(), "snap")
	c.send(frame{"op": "sub", "id": 2, "topics": []string{"graph", "nope"}}) // fails halfway
	expectOp(t, c.next(), "error")
	if n := src.open.Load(); n != 2 {
		t.Fatalf("open feeds = %d, want 2", n)
	}

	c.send(frame{"op": "unsub", "id": 1})
	eventually(t, func() bool { return src.open.Load() == 0 })
	upsert(t, g, graph.Resource{ID: "app:x", Kind: "app"})
	c.send(frame{"op": "ping"})
	expectOp(t, c.next(), "pong") // no diff for the dropped subscription came first

	c.send(frame{"op": "sub", "id": 3, "topics": []string{"graph"}})
	expectOp(t, c.next(), "snap")
	c.ws.Close(websocket.StatusNormalClosure, "")
	eventually(t, func() bool { return src.open.Load() == 0 })
}

func TestHubCloseSendsGoingAway(t *testing.T) {
	h, url := newServer(t, GraphSource{Graph: graph.New()}, Options{})
	c := dial(t, url)
	c.hello()
	go h.Close()
	if s := closeStatus(t, c); s != websocket.StatusGoingAway {
		t.Fatalf("close status = %v, want going away", s)
	}
	// New connections are turned away once closed.
	eventually(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.closed })
	c2 := dial(t, url)
	if s := closeStatus(t, c2); s != websocket.StatusTryAgainLater {
		t.Fatalf("after close: status = %v", s)
	}
}

func TestUnknownTopicErrorIsTyped(t *testing.T) {
	_, err := GraphSource{Graph: graph.New()}.Open("graph:kind/", nil)
	if !errors.Is(err, ErrUnknownTopic) {
		t.Fatalf("err = %v", err)
	}
}
