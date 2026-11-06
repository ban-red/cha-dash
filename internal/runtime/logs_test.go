package runtime

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
)

// tail is an integration whose instance streams a log for "tail:app".
type tail struct{}

func (tail) Manifest() integration.Manifest {
	m := (&cam{}).Manifest()
	m.ID, m.Name = "tail", "Tail"
	return m
}

func (tail) Connect(context.Context, integration.Config, integration.Deps) (integration.Instance, error) {
	return tailInst{}, nil
}

type tailInst struct{}

func (tailInst) Test(context.Context) integration.TestResult      { return integration.TestResult{OK: true} }
func (tailInst) Sync(context.Context, integration.Interest) error { return nil }
func (tailInst) Close() error                                     { return nil }
func (tailInst) Logs(ctx context.Context, rid string, o integration.LogOptions, emit func(integration.LogLine)) error {
	if rid != "tail:app" {
		return integration.ErrNoLogs
	}
	for i := range min(o.Tail, 3) {
		emit(integration.LogLine{Stream: []string{"stdout", "stderr"}[i%2], Line: "line " + string(rune('a'+i))})
	}
	if o.Follow {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

// The logs route: a tail as events then end done; refusals as JSON before
// the stream; a followed stream ends when the instance is disabled.
func TestLogsRoute(t *testing.T) {
	e := newEnv(t, tail{}, &cam{})
	id, err := e.m.Create(t.Context(), CreateRequest{Integration: "tail", Name: "Tail", Config: map[string]any{"url": "http://tail.lab.example"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a sync", func() bool { return e.status(id) == StatusOK })
	base := "/api/integrations/instances/" + id + "/logs?resource="

	code, h, body := e.get(t, base+"tail:app&tail=2")
	if code != 200 || h.Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		body != "event: line\ndata: {\"stream\":\"stdout\",\"line\":\"line a\"}\n\nevent: line\ndata: {\"stream\":\"stderr\",\"line\":\"line b\"}\n\nevent: end\ndata: {\"reason\":\"done\"}\n\n" {
		t.Fatalf("tail: %d %v %q", code, h, body)
	}
	if code, _, body := e.get(t, base+"tail:other"); code != 404 || errCode(t, body) != "no_logs" {
		t.Errorf("no such log: %d %s", code, body)
	}
	if code, _, body := e.get(t, base+"tail:app&tail=-1"); code != 400 || errCode(t, body) != "bad_request" {
		t.Errorf("bad tail: %d %s", code, body)
	}
	if code, _, body := e.get(t, "/api/integrations/instances/nope/logs?resource=tail:app"); code != 404 || errCode(t, body) != "not_found" {
		t.Errorf("no instance: %d %s", code, body)
	}
	cid, err := e.m.Create(t.Context(), CreateRequest{Integration: "cam", Name: "Cam", Config: map[string]any{"url": "http://cam.lab.example"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the cam's sync", func() bool { return e.status(cid) == StatusOK })
	if code, _, body := e.get(t, "/api/integrations/instances/"+cid+"/logs?resource=cam:front"); code != 404 || errCode(t, body) != "no_logs" {
		t.Errorf("not a log streamer: %d %s", code, body)
	}

	// Follow, then disable the instance: the stream ends.
	resp, err := http.Get(e.srv.URL + base + "tail:app&follow=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	seen := 0
	for seen < 3 && sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			seen++
		}
	}
	off := false
	if err := e.m.Update(t.Context(), id, UpdateRequest{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		var rest []string
		for sc.Scan() {
			rest = append(rest, sc.Text())
		}
		done <- strings.Join(rest, "\n")
	}()
	select {
	case rest := <-done:
		if !strings.Contains(rest, "event: end") {
			t.Errorf("after disabling: %q", rest)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived its instance")
	}
}
