package server

import (
	"net/http"
	"strings"
	"testing"

	"dash.cha.sh/internal/auth"
)

func TestCleanDozzleURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                                "",
		"  ":                              "",
		"https://logs.home.example":       "https://logs.home.example",
		"https://logs.home.example/":      "https://logs.home.example",
		"http://10.20.30.5:8080/dozzle//": "http://10.20.30.5:8080/dozzle",
	} {
		if got, ok := CleanDozzleURL(in); !ok || got != want {
			t.Errorf("%q = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"logs.home.example", "ftp://logs.home.example", "javascript:alert(1)", "https://sam:pw@logs.home.example",
		"https://logs.home.example/?x=1", "https://logs.home.example/#c", "https://"} {
		if got, ok := CleanDozzleURL(bad); ok {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

// TestDozzleSetting: viewers read Dozzle's address, admins set it (audited),
// and a bad address is refused with a sentence.
func TestDozzleSetting(t *testing.T) {
	e, st := fullEnv(t)
	owner := signIn(t, e)
	_, viewer := addUser(t, e, owner, "vic", auth.RoleViewer)
	_, op := addUser(t, e, owner, "otto", auth.RoleOperator)

	if status, _, b := e.call(t, viewer, "GET", "/api/settings/dozzle", nil); status != http.StatusOK || string(b) != `{"url":""}`+"\n" {
		t.Fatalf("GET before: %d %s", status, b)
	}
	for _, c := range []*http.Client{viewer, op} {
		if status, _, _ := e.call(t, c, "PUT", "/api/settings/dozzle", map[string]string{"url": "https://logs.home.example"}); status != http.StatusForbidden {
			t.Fatalf("PUT below admin: %d", status)
		}
	}
	if status, code, b := e.call(t, owner, "PUT", "/api/settings/dozzle", map[string]string{"url": "https://sam:pw@logs.home.example"}); status != http.StatusUnprocessableEntity || code != "invalid" || !strings.Contains(string(b), `"id":"dozzle.bad_url"`) {
		t.Fatalf("a user in the URL: %d %s", status, b)
	}
	if status, code, _ := e.call(t, owner, "PUT", "/api/settings/dozzle", map[string]any{}); status != http.StatusUnprocessableEntity || code != "invalid" {
		t.Fatalf("no url: %d %s", status, code)
	}
	if status, _, b := e.call(t, owner, "PUT", "/api/settings/dozzle", map[string]string{"url": "https://logs.home.example/"}); status != http.StatusOK || string(b) != `{"url":"https://logs.home.example"}`+"\n" {
		t.Fatalf("PUT: %d %s", status, b)
	}
	if ev, ok := lastAudit(t, st, "settings.dozzle.update"); !ok || ev.Target != "settings:dozzle" || !strings.Contains(string(ev.Params), `"url":"https://logs.home.example"`) {
		t.Fatalf("the change isn't audited: %+v", ev)
	}
	if status, _, b := e.call(t, viewer, "GET", "/api/settings/dozzle", nil); status != http.StatusOK || !strings.Contains(string(b), "logs.home.example") {
		t.Fatalf("GET after: %d %s", status, b)
	}
	if status, _, b := e.call(t, owner, "PUT", "/api/settings/dozzle", map[string]string{"url": ""}); status != http.StatusOK || string(b) != `{"url":""}`+"\n" {
		t.Fatalf("turning it off: %d %s", status, b)
	}
}
