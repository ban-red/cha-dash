package onboarding

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
)

// A lead is checked with one GET on its own URL (plus the recipe's hint
// path), and nothing else is probed.
func TestDiscoverChecksLeads(t *testing.T) {
	reg := integration.NewRegistry()
	for id, hints := range map[string]string{
		"sonarr": "  - {scheme: http, port: 8989, path: /, match: Sonarr}\n",
		"radarr": "  - {scheme: http, port: 7878, path: /, match: Radarr}\n",
	} {
		if err := reg.Register(app{manifest(id, strings.ToUpper(id[:1])+id[1:])}); err != nil {
			t.Fatal(err)
		}
		if err := reg.AddRecipe(id, recipe(t, hints)); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.Host+r.URL.Path)
		mu.Unlock()
		if strings.HasPrefix(r.Host, "sonarr.") {
			w.Write([]byte("<title>Sonarr</title>"))
			return
		}
		w.Write([]byte("<title>nginx</title>"))
	}))
	defer srv.Close()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); !strings.HasSuffix(host, ".lab.example") {
			return nil, errors.New("connection refused")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	s, _ := New(Options{Store: newStore(t), Integrations: &fakeIntegrations{}, Registry: reg, Dial: dial})
	res, err := s.Discover(t.Context(), DiscoverRequest{URLs: []URLCheck{
		{Integration: "sonarr", URL: "http://sonarr.lab.example/?x=1"},
		{Integration: "radarr", URL: "http://radarr.lab.example"},
		{Integration: "nope", URL: "http://nope.lab.example"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Found) != 1 || res.Found[0].Integration != "sonarr" || res.Found[0].Config["url"] != "http://sonarr.lab.example" || res.Found[0].Source != "lead" {
		t.Fatalf("found: %+v", res.Found)
	}
	why := map[string]string{}
	for _, c := range res.Checked {
		why[c.Target] = c.Error
	}
	if !strings.Contains(why["http://radarr.lab.example"], "not as Radarr") || !strings.Contains(why["http://nope.lab.example"], "no such integration") {
		t.Fatalf("checked: %+v", res.Checked)
	}
	mu.Lock()
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"GET radarr.lab.example/", "GET sonarr.lab.example/"}) {
		t.Fatalf("requests: %v", seen)
	}
	mu.Unlock()

	for _, bad := range []string{"ftp://x.lab.example", "http://10.0.0.1-50", "http://user:pw@x.lab.example", "http://"} {
		if _, err := s.Discover(t.Context(), DiscoverRequest{URLs: []URLCheck{{Integration: "sonarr", URL: bad}}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	many := make([]URLCheck, MaxURLs+1)
	for i := range many {
		many[i] = URLCheck{Integration: "sonarr", URL: "http://a.lab.example"}
	}
	if _, err := s.Discover(t.Context(), DiscoverRequest{URLs: many}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too many: %v", err)
	}
}

func TestDismissals(t *testing.T) {
	s, _ := New(Options{Store: newStore(t), Integrations: &fakeIntegrations{}})
	ctx := t.Context()
	if err := s.Dismiss(ctx, []string{"sonarr|http://a", "plex|http://b"}, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Dismiss(ctx, []string{"sonarr|http://a"}, "u1"); err != nil { // idempotent
		t.Fatal(err)
	}
	got, _ := s.Dismissed(ctx)
	slices.Sort(got)
	if !slices.Equal(got, []string{"plex|http://b", "sonarr|http://a"}) {
		t.Fatalf("dismissed = %v", got)
	}
	if err := s.Restore(ctx, []string{"plex|http://b"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Dismissed(ctx); !slices.Equal(got, []string{"sonarr|http://a"}) {
		t.Fatalf("after restore = %v", got)
	}
	if err := s.Dismiss(ctx, nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no keys: %v", err)
	}
	if err := s.Dismiss(ctx, []string{strings.Repeat("x", maxKeyLen+1)}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long key: %v", err)
	}
}
