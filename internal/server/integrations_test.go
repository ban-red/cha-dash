package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dash.cha.sh/integrations/checks"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

func TestIntegrationRoutes(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	e := newEnvWith(t, func(d *Deps) {
		v, err := vault.New(d.Store, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		reg := integration.NewRegistry()
		if err := reg.Register(checks.Integration{}); err != nil {
			t.Fatal(err)
		}
		m, err := runtime.New(runtime.Options{Store: d.Store, Vault: v, Registry: reg, Topics: topics.New(graph.New(), time.Now()), Log: d.Log})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Close)
		d.Vault, d.Integrations = v, m
	})
	anon := &http.Client{}
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/integrations/catalog"}, {"GET", "/api/integrations/instances"}, {"POST", "/api/integrations/instances"},
		{"GET", "/api/integrations/instances/i_x"}, {"PUT", "/api/integrations/instances/i_x"}, {"DELETE", "/api/integrations/instances/i_x"},
		{"POST", "/api/integrations/test"}, {"POST", "/api/integrations/instances/i_x/test"}, {"GET", "/api/integrations/instances/i_x/diagnostics"},
	} {
		if resp := e.request(t, anon, r.method, r.path, map[string]any{}, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session: %d", r.method, r.path, resp.StatusCode)
		}
	}

	c := signIn(t, e)
	resp := e.request(t, c, "GET", "/api/integrations/catalog", nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("catalog: %d", resp.StatusCode)
	}

	// An applied Homepage import starts its instances right away.
	body := map[string]string{"services_yaml": "- Apps:\n  - Status:\n      href: " + up.URL + "\n      siteMonitor: " + up.URL + "\n"}
	if resp := e.request(t, c, "POST", "/api/import/homepage", body, nil); resp.StatusCode >= 300 {
		t.Fatalf("import: %d", resp.StatusCode)
	}
	// The importer's stored shape validates and its checks run.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		resp = e.request(t, c, "GET", "/api/integrations/instances", nil, nil)
		var list []runtime.InstanceView
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || len(list) != 1 || list[0].Integration != "checks" {
			t.Fatalf("instances after import: %+v %v", list, err)
		}
		if list[0].Status == runtime.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the imported checks never synced: %+v", list[0])
		}
	}
}
