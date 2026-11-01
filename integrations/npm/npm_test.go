package npm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

const (
	email    = "chadash@example.com"
	password = "fake-npm-password"
	token    = "fake-npm-token-not-a-real-jwt"
)

// fakeNPM answers /api/ to anyone, /api/tokens to the right email and
// password, and the rest to the token.
func fakeNPM(t *testing.T) *fixture.Server {
	return fixture.New(t, fixture.Dir(t, ID), func(r *http.Request) bool {
		switch r.URL.Path {
		case "/api/":
			return true
		case "/api/tokens":
			var b map[string]string
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &b)
			return b["identity"] == email && b["secret"] == password
		}
		return r.Header.Get("Authorization") == "Bearer "+token
	}, map[string]fixture.Route{
		"GET /api/":                  {File: "status.json"},
		"POST /api/tokens":           {File: "token.json"},
		"GET /api/nginx/proxy-hosts": {File: "proxy_hosts.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url, pass string) integration.Instance {
	return h.Connect(Integration{}, map[string]any{"url": url, "email": email}, map[string]string{"password": pass})
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Nginx Proxy Manager" || !reflect.DeepEqual(m.SecretFields(), []string{"password"}) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	up := fakeNPM(t)
	h := integrationtest.New(t)
	inst := connect(t, h, up.URL, password)
	if res := inst.Test(t.Context()); !res.OK || res.Version != "2.12.6" || len(res.Warnings) != 1 {
		t.Fatalf("test = %+v", res)
	}
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]map[string]any{
		"1": {"url": "https://sonarr.lab.example", "upstream": "http://10.20.30.42:8989", "upstream_addr": "10.20.30.42:8989", "tls": true,
			"rule": "host sonarr.lab.example", "proxy_host": 1.0},
		"1-1": {"url": "https://sonarr.lab.example/api", "upstream_addr": "10.20.30.43:8990", "path": "/api/", "rule": "host sonarr.lab.example, path /api/"},
		"2": {"url": "http://nas.lab.example", "upstream": "https://nas:5001", "upstream_addr": "nas:5001", "auth": "access list",
			"hosts": []any{"nas.lab.example", "files.lab.example"}},
		"3": {"upstream_addr": "10.20.30.99:8080"},
	} {
		r, ok := h.Resource("npm:i_test/route/" + key)
		if !ok || r.Kind != "route" {
			t.Fatalf("no route %s", key)
		}
		for k, v := range want {
			if !reflect.DeepEqual(r.Attrs[k], v) {
				t.Errorf("route %s: %s = %#v, want %#v", key, k, r.Attrs[k], v)
			}
		}
		if h.Graph().Relations["npm:i_test/route/"+key+" part_of npm:i_test/app"] == nil {
			t.Errorf("route %s isn't part of the app", key)
		}
	}
	if r, _ := h.Resource("npm:i_test/route/3"); r.State != "disabled" {
		t.Fatalf("disabled host: %+v", r)
	}
	app, ok := h.Resource("npm:i_test/app")
	if !ok || app.Attrs["type"] != "npm" || app.Attrs["version"] != "2.12.6" || app.Attrs["proxy_hosts"] != 3.0 {
		t.Fatalf("app = %+v", app)
	}
	// The advanced config and the token never reach the graph.
	b, _ := json.Marshal(h.Graph())
	if strings.Contains(string(b), "fake-value-not-real") || strings.Contains(string(b), token) {
		t.Fatal("the graph carries the advanced config or the token")
	}
	// One sign-in serves both reads.
	if n := len(up.Requests("/api/tokens")); n != 1 {
		t.Fatalf("%d sign-ins", n)
	}
}

// The token is renewed an hour before it lapses, and once more when the
// upstream refuses it.
func TestTokenRenewal(t *testing.T) {
	up := fakeNPM(t)
	h := integrationtest.New(t)
	inst := connect(t, h, up.URL, password)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	h.Advance(22 * time.Hour)
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if n := len(up.Requests("/api/tokens")); n != 1 {
		t.Fatalf("renewed early: %d sign-ins", n)
	}
	h.Advance(90 * time.Minute) // within the hour before it lapses
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if n := len(up.Requests("/api/tokens")); n != 2 {
		t.Fatalf("not renewed: %d sign-ins", n)
	}
	// Revoked: a 401 signs in again, once.
	in := inst.(*instance)
	in.token = "revoked"
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	if n := len(up.Requests("/api/tokens")); n != 3 {
		t.Fatalf("after a refusal: %d sign-ins", n)
	}
}

func TestFailures(t *testing.T) {
	up := fakeNPM(t)
	h := integrationtest.New(t)
	err := connect(t, h, up.URL, "wrong").Sync(t.Context(), integrationtest.Watched)
	var ae *integration.AuthError
	if !errors.As(err, &ae) || ae.Field != "password" {
		t.Fatalf("wrong password: %v", err)
	}
	up.Set("GET /api/nginx/proxy-hosts", fixture.Route{Status: 403, Body: `{"error":{"message":"Permission Denied"}}`})
	err = connect(t, h, up.URL, password).Sync(t.Context(), integrationtest.Watched)
	if !errors.As(err, &ae) || ae.Field != "email" {
		t.Fatalf("no permission: %v", err)
	}
}
