package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/distribution/reference"
)

// Fake credentials only.
const (
	user     = "robot"
	password = "fake-pw-not-real-0000"
	digest   = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func writeConfig(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The logins are the config's, for the registries the policy lists; the
// file is read again when it changes; a credential helper isn't supported
// and says so once; a login never prints.
func TestLogins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docker-config.json")
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, nil))
	l := NewLogins(path, []string{"ghcr.io", "docker.io", "registry.lab.example:5000"}, log)
	if _, ok := l.For("ghcr.io"); ok {
		t.Fatal("a missing file has no logins")
	}
	writeConfig(t, path, map[string]any{"auths": map[string]any{
		"ghcr.io":                     map[string]string{"auth": b64(user + ":" + password)},
		"https://index.docker.io/v1/": map[string]string{"username": "hubuser", "password": "hub-pw-fake"},
		"registry.lab.example:5000":   map[string]string{"auth": "!!not base64"},
		"other.registry.example":      map[string]string{"auth": b64("x:other-pw-fake")},
	}})
	if lg, ok := l.For("ghcr.io"); !ok || lg.Username != user || lg.Password != password {
		t.Fatalf("ghcr.io: %v %v", lg.Username, ok)
	}
	if lg, ok := l.For("docker.io"); !ok || lg.Username != "hubuser" {
		t.Fatalf("Docker Hub, filed under its v1 index URL: %v %v", lg.Username, ok)
	}
	if _, ok := l.For("registry.lab.example:5000"); ok {
		t.Error("an auth that doesn't decode is no login")
	}
	if _, ok := l.For("other.registry.example"); ok {
		t.Error("a registry the policy doesn't list gets no login")
	}
	if all := l.All(); len(all) != 2 {
		t.Errorf("All: %d logins", len(all))
	}
	var hdr struct{ Username, Password, ServerAddress string }
	raw, _ := base64.URLEncoding.DecodeString(l.EncodedAuth("docker.io"))
	if json.Unmarshal(raw, &hdr) != nil || hdr.Username != "hubuser" || hdr.ServerAddress != "https://index.docker.io/v1/" {
		t.Errorf("X-Registry-Auth for Docker Hub: %s", raw)
	}
	if l.EncodedAuth("other.registry.example") != "" {
		t.Error("X-Registry-Auth for an unlisted registry")
	}

	// A login never prints, however it's printed.
	lg, _ := l.For("ghcr.io")
	j, _ := json.Marshal(map[string]any{"login": lg})
	var sl strings.Builder
	slog.New(slog.NewJSONHandler(&sl, nil)).Info("x", "login", lg)
	for _, s := range []string{fmt.Sprint(lg), fmt.Sprintf("%+v", lg), fmt.Sprintf("%#v", lg), string(j), sl.String()} {
		if strings.Contains(s, password) || strings.Contains(s, user) {
			t.Errorf("a login printed: %s", s)
		}
	}

	// A credential helper: its logins aren't readable, so none is used.
	writeConfig(t, path, map[string]any{"credsStore": "desktop", "auths": map[string]any{"ghcr.io": map[string]string{}}})
	if _, ok := l.For("ghcr.io"); ok {
		t.Error("a credsStore entry gave a login")
	}
	if !strings.Contains(logs.String(), "credential helper") {
		t.Errorf("the log should say a credential helper isn't supported: %s", logs.String())
	}
	if strings.Contains(logs.String(), password) || strings.Contains(logs.String(), "hub-pw-fake") || strings.Contains(logs.String(), "other-pw-fake") {
		t.Errorf("a log line carries a password: %s", logs.String())
	}
	// Gone again: none.
	os.Remove(path)
	if _, ok := l.For("docker.io"); ok {
		t.Error("a removed file still gives logins")
	}
	// None allowed: none, whatever the file holds.
	writeConfig(t, path, map[string]any{"auths": map[string]any{"ghcr.io": map[string]string{"auth": b64(user + ":" + password)}}})
	if _, ok := NewLogins(path, nil, nil).For("ghcr.io"); ok {
		t.Error("an empty images.registries sends a login")
	}
	var none *Logins
	if _, ok := none.For("ghcr.io"); ok || len(none.All()) != 0 || none.EncodedAuth("ghcr.io") != "" {
		t.Error("a nil *Logins has logins")
	}
	for in, want := range map[string]string{"https://index.docker.io/v1/": "docker.io", "GHCR.io": "ghcr.io", "http://registry.lab.example:5000/v2/": "registry.lab.example:5000"} {
		if got := Host(in); got != want {
			t.Errorf("Host(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeRegistry is a registry: Basic (as registry:2 with htpasswd) or a
// Bearer realm on its own origin. It records every Authorization header.
type fakeRegistry struct {
	srv    *httptest.Server
	bearer bool
	realm  string // overrides the Bearer realm
	seen   []string
}

func newFakeRegistry(t *testing.T, bearer bool) *fakeRegistry {
	f := &fakeRegistry{bearer: bearer}
	good := "Basic " + b64(user+":"+password)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		f.seen = append(f.seen, a)
		if r.URL.Path == "/token" {
			if a != good {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "t0ken"})
			return
		}
		ok := a == good
		if f.bearer {
			ok = a == "Bearer t0ken"
		}
		if !ok {
			if f.bearer {
				realm := f.realm
				if realm == "" {
					realm = f.srv.URL + "/token"
				}
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="test",scope="repository:app:pull"`)
			} else {
				w.Header().Set("WWW-Authenticate", `Basic realm="chadash-test"`)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func tagged(t *testing.T, ref string) reference.NamedTagged {
	t.Helper()
	n, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		t.Fatal(err)
	}
	return n.(reference.NamedTagged)
}

func loginsFor(t *testing.T, host, pw string, allowed ...string) *Logins {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker-config.json")
	writeConfig(t, path, map[string]any{"auths": map[string]any{host: map[string]string{"auth": b64(user + ":" + pw)}}})
	return NewLogins(path, allowed, nil)
}

// A HEAD with no login says the registry needs one, in a sentence; with
// one it gets the digest, by Basic or through a token; a wrong one is
// refused, saying so.
func TestHeadWithLogin(t *testing.T) {
	for _, bearer := range []bool{false, true} {
		t.Run(fmt.Sprintf("bearer=%v", bearer), func(t *testing.T) {
			f := newFakeRegistry(t, bearer)
			ref := tagged(t, f.host()+"/app:1")
			c := Client{Base: func(string) string { return f.srv.URL }}
			_, _, err := c.Head(context.Background(), ref)
			if err == nil || err.Error() != f.host()+" needs a login, and this node has none for it." {
				t.Fatalf("no login: %v", err)
			}
			// A login the policy doesn't list isn't sent.
			c.Logins = loginsFor(t, f.host(), password, "ghcr.io")
			if _, _, err := c.Head(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "has none") {
				t.Fatalf("unlisted: %v", err)
			}
			for _, a := range f.seen {
				if strings.HasPrefix(a, "Basic") {
					t.Fatalf("an unlisted registry got credentials: %q", f.seen)
				}
			}
			c.Logins = loginsFor(t, f.host(), password, f.host())
			d, used, err := c.Head(context.Background(), ref)
			if err != nil || d != digest || !used {
				t.Fatalf("with the login: %q %v %v", d, used, err)
			}
			c.Logins = loginsFor(t, f.host(), "wrong-pw-fake", f.host())
			_, _, err = c.Head(context.Background(), ref)
			if err == nil || err.Error() != f.host()+" refused this node's login for it." || strings.Contains(err.Error(), "wrong-pw-fake") {
				t.Fatalf("a wrong login: %v", err)
			}
		})
	}
}

// A Bearer realm on another host over plain http never gets the login.
func TestTokenRealmElsewhereGetsNoLogin(t *testing.T) {
	realm := newFakeRegistry(t, true)
	f := newFakeRegistry(t, true)
	f.realm = realm.srv.URL + "/token"
	c := Client{Base: func(string) string { return f.srv.URL }, Logins: loginsFor(t, f.host(), password, f.host())}
	if _, _, err := c.Head(context.Background(), tagged(t, f.host()+"/app:1")); err == nil {
		t.Fatal("the realm can't give a token without the login")
	}
	for _, a := range realm.seen {
		if a != "" {
			t.Fatalf("a plain-http realm on another host got %q", a)
		}
	}
}

// The checker keeps an answer for CheckEvery, says which login it used,
// and asks again once a login appears.
func TestCheckerLogins(t *testing.T) {
	f := newFakeRegistry(t, false)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "docker-config.json")
	c := &Checker{Client: Client{Base: func(string) string { return f.srv.URL }, Logins: NewLogins(path, []string{f.host()}, nil)}, Now: func() time.Time { return now }}
	ref := tagged(t, f.host()+"/app:1")
	a := c.Ask(context.Background(), ref, false)
	if a.Digest != "" || a.Login != LoginNeeded || a.Reason != NeedsLogin(f.host()) {
		t.Fatalf("no login: %+v", a)
	}
	n := len(f.seen)
	if a := c.Ask(context.Background(), ref, false); a.Login != LoginNeeded || len(f.seen) != n {
		t.Fatalf("within CheckEvery it's kept: %+v, %d asks", a, len(f.seen)-n)
	}
	writeConfig(t, path, map[string]any{"auths": map[string]any{f.host(): map[string]string{"auth": b64(user + ":" + password)}}})
	if a := c.Ask(context.Background(), ref, false); a.Digest != digest || a.Login != LoginUsed {
		t.Fatalf("a login appeared: %+v", a)
	}
}
