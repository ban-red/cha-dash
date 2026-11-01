package caddy

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

func fakeCaddy(t *testing.T) *fixture.Server {
	return fixture.New(t, fixture.Dir(t, ID), func(*http.Request) bool { return true }, map[string]fixture.Route{
		"GET " + serversPath: {File: "servers.json"},
	})
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Caddy" || len(m.SecretFields()) != 0 || m.Capabilities[0] != "proxy.routes" {
		t.Fatalf("manifest = %+v", m)
	}
	if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "caddy:2019"}, nil); len(errs) == 0 {
		t.Fatal("a URL without a scheme passed")
	}
}

func TestSync(t *testing.T) {
	up := fakeCaddy(t)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": up.URL}, nil)
	if res := inst.Test(t.Context()); !res.OK || len(res.Warnings) != 1 || res.Warnings[0].Code != "admin_open" {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	route := func(key string) map[string]any {
		t.Helper()
		r, ok := h.Resource("caddy:i_test/route/" + key)
		if !ok || r.Kind != "route" {
			t.Fatalf("no route %s", key)
		}
		if h.Graph().Relations["caddy:i_test/route/"+key+" part_of caddy:i_test/app"] == nil {
			t.Fatalf("%s isn't part of the app", key)
		}
		return r.Attrs
	}
	// The shape Traefik's routes have: an open URL and an upstream address.
	for key, want := range map[string]map[string]any{
		"sonarr.lab.example": {"url": "https://sonarr.lab.example", "upstream": "http://10.20.30.42:8989", "upstream_addr": "10.20.30.42:8989",
			"rule": "host sonarr.lab.example", "entrypoint": "srv0", "tls": true},
		"home.lab.example-bazarr": {"url": "https://home.lab.example/bazarr", "upstream": "http://bazarr:6767", "upstream_addr": "bazarr:6767",
			"rule": "host home.lab.example, path /bazarr*", "path": "/bazarr*"},
		"nas.lab.example": {"url": "https://nas.lab.example", "upstream": "https://10.20.30.10:5001", "upstream_addr": "10.20.30.10:5001",
			"hosts": []any{"nas.lab.example", "files.lab.example"}},
		// A placeholder upstream is shown, never joined; a wildcard host has
		// no URL to open.
		"apps.lab.example": {"upstream": "{http.request.host.labels.3}:80", "url": nil, "upstream_addr": nil},
		// No host match: no URL. Two upstreams are one route: one site,
		// joined by the first, with both listed.
		"srv1": {"upstream_addr": "localhost:3000", "url": nil, "rule": "all requests", "entrypoint": "srv1",
			"servers": []any{"http://localhost:3000", "http://localhost:3001"}},
	} {
		attrs := route(key)
		for k, v := range want {
			if !reflect.DeepEqual(attrs[k], v) {
				t.Errorf("%s.%s = %#v, want %#v", key, k, attrs[k], v)
			}
		}
	}
	app, ok := h.Resource("caddy:i_test/app")
	if !ok || app.Kind != "app" || app.Attrs["type"] != "caddy" || app.Attrs["routes"] != 5.0 || app.Attrs["servers"] != 2.0 {
		t.Fatalf("app = %+v", app)
	}
	// Nothing of the handlers but the upstreams: not the basic-auth hash.
	for _, r := range h.Graph().Resources {
		for _, v := range r.Attrs {
			if s, _ := v.(string); strings.Contains(s, "bcrypt") {
				t.Fatalf("%s carries the auth hash", r.ID)
			}
		}
	}
	// The open admin API is a security finding on the app.
	inc, ok := h.Incident(hintAdmin)
	if !ok || inc.Category != integration.CategorySecurity || inc.Severity != integration.SeverityAttention || inc.Target != "caddy:i_test/app" {
		t.Fatalf("finding = %+v", inc)
	}
	if _, ok := h.Resource("caddy:i_test/route/srv1-2"); ok {
		t.Fatal("a second upstream became a second route")
	}
	if reqs := up.Requests(""); len(reqs) == 0 || reqs[0].Method != http.MethodGet {
		t.Fatalf("requests = %v", reqs)
	}
}

func TestFailures(t *testing.T) {
	up := fakeCaddy(t)
	h := integrationtest.New(t)
	up.Set("GET "+serversPath, fixture.Route{Status: 404, Body: "{}"})
	inst := h.Connect(Integration{}, map[string]any{"url": up.URL}, nil)
	err := inst.Sync(t.Context(), integrationtest.Watched)
	var re *integration.RepairError
	if !errors.As(err, &re) || re.Code != integration.CodeAPIChanged {
		t.Fatalf("404: %v", err)
	}
	// No http app: null, and no routes.
	up.Set("GET "+serversPath, fixture.Route{Body: "null"})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if app, _ := h.Resource("caddy:i_test/app"); app.Attrs["routes"] != 0.0 {
		t.Fatalf("app = %+v", app)
	}
}

// Route IDs never repeat, even when a label slugs to another's suffix.
func TestRouteIDsAreUnique(t *testing.T) {
	in := &instance{key: "i_test"}
	ids := map[string]int{}
	seen := map[string]bool{}
	for _, p := range []proxy{{server: "s", hosts: []string{"a.example"}}, {server: "s", hosts: []string{"a.example"}}, {server: "s", hosts: []string{"a.example"}, path: "/2"}} {
		id := in.route(p, ids).ID
		if seen[id] {
			t.Fatalf("%s twice", id)
		}
		seen[id] = true
	}
}
