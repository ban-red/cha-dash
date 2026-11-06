package integrationtest_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"

	_ "dash.cha.sh/integrations/checks"
)

// Wrap sees every request an instance makes through its HTTP clients
// (internal/liveverify reads the lab's answers this way).
func TestWrapSeesTheInstancesRequests(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	var seen atomic.Int32
	h := integrationtest.New(t)
	h.Wrap = func(next http.RoundTripper) http.RoundTripper {
		return roundTripper(func(r *http.Request) (*http.Response, error) {
			seen.Add(1)
			return next.RoundTrip(r)
		})
	}
	integ, _, _ := integration.Default.Get("checks")
	in := h.Connect(integ, map[string]any{"targets": []any{map[string]any{"name": "Up", "kind": "http", "url": up.URL}}}, nil)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if seen.Load() == 0 {
		t.Fatal("the wrapped transport saw no request")
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
