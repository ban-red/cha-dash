package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
)

// cam is a fake camera server integration whose instance is a Picturer.
// Its answer is set per test; it counts the calls.
type cam struct {
	mu     sync.Mutex
	pic    integration.Picture
	err    error
	calls  int
	height int
}

func (c *cam) Manifest() integration.Manifest {
	return integration.Manifest{ID: "cam", Name: "Cam", Icon: "cam", Version: "0.1.0", Tier: integration.Bronze,
		ConfigSchema: integration.Object(integration.URLProp(), integration.TLSProp()).Require("url"),
		Poll:         integration.Poll{Interval: time.Minute, Idle: time.Minute}}
}

func (c *cam) Connect(context.Context, integration.Config, integration.Deps) (integration.Instance, error) {
	return &camInst{c}, nil
}

func (c *cam) set(pic integration.Picture, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pic, c.err = pic, err
}

func (c *cam) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type camInst struct{ c *cam }

func (i *camInst) Test(context.Context) integration.TestResult {
	return integration.TestResult{OK: true}
}
func (i *camInst) Sync(context.Context, integration.Interest) error { return nil }
func (i *camInst) Close() error                                     { return nil }
func (i *camInst) Picture(_ context.Context, rid string, o integration.PictureOptions) (integration.Picture, error) {
	i.c.mu.Lock()
	defer i.c.mu.Unlock()
	i.c.calls++
	i.c.height = o.Height
	if rid != "cam:front" {
		return integration.Picture{}, integration.ErrNoPicture
	}
	return i.c.pic, i.c.err
}

// get fetches a picture and returns the status, headers and body.
func (e *env) get(t *testing.T, path string) (int, http.Header, string) {
	t.Helper()
	resp, err := http.Get(e.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func errCode(t *testing.T, body string) string {
	t.Helper()
	var v struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return v.Error.Code
}

func TestPicture(t *testing.T) {
	c := &cam{}
	e := newEnv(t, c)
	id, err := e.m.Create(t.Context(), CreateRequest{Integration: "cam", Name: "Frigate", Config: map[string]any{"url": "http://cam.lab.example"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a sync", func() bool { return e.status(id) == StatusOK })
	at := time.Date(2026, 10, 29, 8, 30, 0, 0, time.UTC)
	c.set(integration.Picture{Body: []byte("JPEGDATA"), ContentType: "image/jpeg", At: at}, nil)
	base := "/api/integrations/instances/" + id + "/picture?resource=cam:front"

	// Success, with the headers; the height is clamped.
	code, h, body := e.get(t, base+"&h=4000")
	if code != 200 || body != "JPEGDATA" {
		t.Fatalf("picture: %d %q", code, body)
	}
	for k, want := range map[string]string{
		"Content-Type": "image/jpeg", "Cache-Control": "private, no-store",
		"X-Content-Type-Options": "nosniff", "Last-Modified": at.Format(http.TimeFormat),
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	c.mu.Lock()
	height := c.height
	c.mu.Unlock()
	if height != MaxPictureHeight {
		t.Errorf("height %d, want clamped to %d", height, MaxPictureHeight)
	}

	// Within the TTL, the same picture comes from the cache.
	if code, _, _ := e.get(t, base+"&h=4000"); code != 200 || c.count() != 1 {
		t.Fatalf("second ask: %d, %d calls; want a cache hit", code, c.count())
	}
	// Another height is another fetch.
	if code, _, _ := e.get(t, base+"&h=240"); code != 200 || c.count() != 2 {
		t.Fatalf("other height: %d, %d calls", code, c.count())
	}

	// Not this instance's picture.
	if code, _, body := e.get(t, "/api/integrations/instances/"+id+"/picture?resource=cam:back"); code != 404 || errCode(t, body) != "no_picture" {
		t.Fatalf("no picture: %d %s", code, body)
	}
	if code, _, body := e.get(t, "/api/integrations/instances/"+id+"/picture"); code != 404 || errCode(t, body) != "no_picture" {
		t.Fatalf("no resource: %d %s", code, body)
	}
	// Unknown instance.
	if code, _, body := e.get(t, "/api/integrations/instances/i_nope/picture?resource=cam:front"); code != 404 || errCode(t, body) != "not_found" {
		t.Fatalf("unknown instance: %d %s", code, body)
	}
	if code, _, _ := e.get(t, base+"&h=tall"); code != 400 {
		t.Fatalf("bad h: %d", code)
	}

	// Answers the hub refuses are 502s; each height below misses the cache.
	cases := []struct {
		name string
		pic  integration.Picture
		err  error
		h    string
	}{
		{"html", integration.Picture{Body: []byte("<script>"), ContentType: "text/html"}, nil, "1"},
		{"svg", integration.Picture{Body: []byte("<svg/>"), ContentType: "image/svg+xml"}, nil, "2"},
		{"oversize", integration.Picture{Body: make([]byte, integration.MaxPictureBytes+1), ContentType: "image/jpeg"}, nil, "3"},
		{"failure", integration.Picture{}, &integration.StatusError{Code: 500}, "4"},
	}
	for _, tc := range cases {
		c.set(tc.pic, tc.err)
		code, h, body := e.get(t, base+"&h="+tc.h)
		if code != 502 || errCode(t, body) != "upstream" || !strings.Contains(body, "Frigate") {
			t.Errorf("%s: %d %s", tc.name, code, body)
		}
		if ct := h.Get("Content-Type"); strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: served as %q", tc.name, ct)
		}
	}

	// A disabled instance isn't running.
	off := false
	if err := e.m.Update(t.Context(), id, UpdateRequest{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.get(t, base+"&h=5"); code != 409 || errCode(t, body) != "not_running" {
		t.Fatalf("disabled: %d %s", code, body)
	}
}

// TestPictureNotAPicturer: an integration without pictures answers 404
// no_picture.
func TestPictureNotAPicturer(t *testing.T) {
	e := newEnv(t)
	up := newUpstream(t, secret, false)
	id, err := e.m.Create(t.Context(), CreateRequest{Integration: "echo", Name: "Echo", Config: map[string]any{"url": up.URL}, Secrets: map[string]any{"api_key": secret}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a sync", func() bool { return e.status(id) == StatusOK })
	if code, _, body := e.get(t, "/api/integrations/instances/"+id+"/picture?resource=echo:x"); code != 404 || errCode(t, body) != "no_picture" {
		t.Fatalf("not a Picturer: %d %s", code, body)
	}
}

// TestPictureCacheBounded: the cache never holds more than its size.
func TestPictureCacheBounded(t *testing.T) {
	var pc pictureCache
	pc.entries = map[pictureKey]*pictureEntry{}
	now := time.Now()
	for i := range pictureCacheSize + 10 {
		pc.put(pictureKey{"i", "r", i}, &pictureEntry{at: now}, now)
	}
	if len(pc.entries) != pictureCacheSize || len(pc.order) != pictureCacheSize {
		t.Fatalf("%d entries, %d keys", len(pc.entries), len(pc.order))
	}
	if _, ok := pc.entries[pictureKey{"i", "r", 0}]; ok {
		t.Fatal("the oldest entry stayed")
	}
	// Expired entries go on the next put.
	pc.put(pictureKey{"i", "r", -1}, &pictureEntry{at: now}, now.Add(pictureTTL))
	if len(pc.entries) != 1 {
		t.Fatalf("%d entries after expiry", len(pc.entries))
	}
}
