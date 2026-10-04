package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/onboarding"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

func TestOnboardingRoutes(t *testing.T) {
	e := newEnvWith(t, func(d *Deps) {
		v, err := vault.New(d.Store, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		reg := integration.NewRegistry()
		m, err := runtime.New(runtime.Options{Store: d.Store, Vault: v, Registry: reg, Topics: topics.New(graph.New(), time.Now()), Log: d.Log})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		ob, err := onboarding.New(onboarding.Options{Store: d.Store, Integrations: m, Registry: reg, Log: d.Log})
		if err != nil {
			t.Fatal(err)
		}
		d.Vault, d.Integrations, d.Onboarding = v, m, ob
	})
	anon := &http.Client{}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/onboarding"}, {"PUT", "/api/onboarding"}, {"POST", "/api/onboarding/discover"},
	} {
		if resp := e.request(t, anon, r.method, r.path, map[string]any{}, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session: %d", r.method, r.path, resp.StatusCode)
		}
	}
	c := signIn(t, e)
	if st := decode(t, e.request(t, c, "GET", "/api/onboarding", nil, nil)); st["status"] != "new" || st["show"] != true {
		t.Fatalf("state: %v", st)
	}
	if resp := e.request(t, c, "PUT", "/api/onboarding", map[string]string{"status": "skipped"}, nil); resp.StatusCode != 200 {
		t.Fatalf("skip: %d", resp.StatusCode)
	}
	if st := decode(t, e.request(t, c, "GET", "/api/onboarding", nil, nil)); st["status"] != "skipped" || st["show"] != false {
		t.Fatalf("after skipping: %v", st)
	}
	// Changes from another origin are refused like every other change.
	if resp := e.request(t, c, "PUT", "/api/onboarding", map[string]string{"status": "done"}, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin PUT: %d", resp.StatusCode)
	}
}
