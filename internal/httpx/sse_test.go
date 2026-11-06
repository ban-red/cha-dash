package httpx

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// events reads an event stream until it closes: "name data" per event,
// and "ping" per comment.
func events(t *testing.T, resp *http.Response) []string {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(resp.Body)
	name := ""
	for sc.Scan() {
		l := sc.Text()
		switch {
		case l == ": ping":
			out = append(out, "ping")
		case strings.HasPrefix(l, "event: "):
			name = strings.TrimPrefix(l, "event: ")
		case strings.HasPrefix(l, "data: "):
			out = append(out, name+" "+strings.TrimPrefix(l, "data: "))
		}
	}
	return out
}

// A followed stream sends each line, then why it ended: done when produce
// returns, error with produce's sentence, limit once the clock passes it
// (a test clock jumping 30 minutes ends it at the next ping).
func TestFollow(t *testing.T) {
	var mu sync.Mutex
	clock := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	release := make(chan struct{})
	var produce func(ctx context.Context, emit func(any)) error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Follow(w, r, FollowOptions{Now: now, Ping: 10 * time.Millisecond, LimitMessage: "Following stops after 30 minutes.",
			ErrorText: func(err error) string { return "It broke: " + err.Error() }}, produce)
	}))
	defer srv.Close()
	get := func() (*http.Response, []string) {
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp, events(t, resp)
	}
	lines := func(got []string) []string {
		var out []string
		for _, e := range got {
			if e != "ping" {
				out = append(out, e)
			}
		}
		return out
	}

	produce = func(_ context.Context, emit func(any)) error {
		emit(map[string]string{"line": "one"})
		emit(map[string]string{"line": "two"})
		return nil
	}
	resp, got := get()
	if resp.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers %v", resp.Header)
	}
	if want := []string{`line {"line":"one"}`, `line {"line":"two"}`, `end {"reason":"done"}`}; strings.Join(lines(got), "|") != strings.Join(want, "|") {
		t.Errorf("done: %q", got)
	}

	produce = func(context.Context, func(any)) error { return errors.New("node gone") }
	if _, got := get(); strings.Join(lines(got), "|") != `end {"reason":"error","message":"It broke: node gone"}` {
		t.Errorf("error: %q", got)
	}

	// Follow until the clock passes 30 minutes; pings come meanwhile.
	produce = func(ctx context.Context, emit func(any)) error {
		emit(map[string]string{"line": "first"})
		close(release)
		<-ctx.Done()
		return ctx.Err()
	}
	go func() {
		<-release
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		clock = clock.Add(30 * time.Minute)
		mu.Unlock()
	}()
	_, got = get()
	if l := lines(got); strings.Join(l, "|") != `line {"line":"first"}|end {"reason":"limit","message":"Following stops after 30 minutes."}` {
		t.Errorf("limit: %q", got)
	}
	if !strings.Contains(strings.Join(got, "|"), "ping") {
		t.Errorf("no ping while idle: %q", got)
	}
}

// A client that leaves cancels produce.
func TestFollowClientLeaves(t *testing.T) {
	stopped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Follow(w, r, FollowOptions{}, func(ctx context.Context, emit func(any)) error {
			emit("hello")
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		})
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(resp.Body)
	if l, _ := br.ReadString('\n'); l != "event: line\n" {
		t.Fatalf("first line %q", l)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("produce kept running after the client left")
	}
}

// An error before the first line is answered by Refuse, as JSON with its
// status, not as a stream.
func TestFollowRefusesBeforeStarting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Follow(w, r, FollowOptions{Refuse: func(w http.ResponseWriter, err error) {
			Error(w, http.StatusNotFound, "no_logs", err.Error())
		}}, func(context.Context, func(any)) error { return errors.New("no such log") })
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
