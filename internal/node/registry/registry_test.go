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
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
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
// file is read again when it changes; a credential helper the node can't
// run gives none; a login never prints.
func TestLogins(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no credential helpers
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

	// A credential helper the node can't run: none is used.
	writeConfig(t, path, map[string]any{"credsStore": "desktop", "auths": map[string]any{"ghcr.io": map[string]string{}}})
	if _, ok := l.For("ghcr.io"); ok {
		t.Error("a credsStore entry gave a login")
	}
	if !strings.Contains(logs.String(), "credential helper") {
		t.Errorf("the log should mention the credential helper: %s", logs.String())
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
	c := &Checker{Base: func(string) string { return f.srv.URL }, Logins: NewLogins(path, []string{f.host()}, nil), Now: func() time.Time { return now }}
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

// Digest reads RepoDigests, else the containerd store's descriptor; the
// classic store's image with none was built on the host.
func TestDigest(t *testing.T) {
	const d1, d2 = "sha256:1111111111111111111111111111111111111111111111111111111111111111", "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	desc := &ocispec.Descriptor{Digest: d2}
	for _, c := range []struct {
		name, want string
		rds        []string
		desc       *ocispec.Descriptor
	}{
		{"nginx", d1, []string{"nginx@" + d1}, desc},
		{"docker.io/library/nginx", d1, []string{"nginx@" + d1}, nil},
		{"127.0.0.1:5000/app", d2, []string{"127.0.0.1:5000/other@" + d1}, desc}, // another repository: the image itself
		{"127.0.0.1:5000/app", "", []string{"127.0.0.1:5000/other@" + d1}, nil},
		{"127.0.0.1:5000/app", d2, nil, desc}, // the containerd store, its tag moved on
		{"127.0.0.1:5000/app", "", nil, nil},  // the classic store: built on the host
		{"Not A Name", "", nil, desc},
	} {
		if got := Digest(c.rds, c.desc, c.name); got != c.want {
			t.Errorf("Digest(%v, %v, %q) = %q, want %q", c.rds, c.desc != nil, c.name, got, c.want)
		}
	}
}

// Docker Hub says the same for a missing image as for a private one: the
// sentence says both.
func TestLoginErrorFor(t *testing.T) {
	missing := fmt.Errorf("pull access denied for sha256, repository does not exist or may require 'docker login'")
	if got := LoginErrorFor(missing, "docker.io", false).Error(); got != "Docker couldn't pull it from docker.io: the image isn't there, or it needs a login this node hasn't got." {
		t.Errorf("missing or private: %q", got)
	}
	if got := LoginErrorFor(fmt.Errorf("unauthorized: authentication required"), "ghcr.io", false).Error(); got != NeedsLogin("ghcr.io") {
		t.Errorf("a plain 401: %q", got)
	}
}

// fakeHelper writes docker-credential-<name> into a directory put on PATH:
// it answers ghcr.io's login, "<token>" for quay.io, fails otherwise, and
// counts its calls in calls.
func fakeHelper(t *testing.T, name string) (calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	script := `#!/bin/sh
read server
echo "$server" >> ` + calls + `
case "$server" in
  ghcr.io) printf '{"ServerURL":"ghcr.io","Username":"` + user + `","Secret":"` + password + `"}' ;;
  quay.io) printf '{"ServerURL":"quay.io","Username":"<token>","Secret":"fake-identity-token"}' ;;
  *) echo "credentials not found in native keychain" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker-credential-"+name), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/bin:/usr/bin")
	return calls
}

// A login in a credential helper: credHelpers for a host before
// credsStore, asked only for the registries the policy lists, at most every
// few minutes, and never logged. One the node can't run says which and
// what to do instead.
func TestLoginsFromCredentialHelper(t *testing.T) {
	calls := fakeHelper(t, "fake")
	path := filepath.Join(t.TempDir(), "docker-config.json")
	var logs strings.Builder
	l := NewLogins(path, []string{"ghcr.io", "quay.io", "registry.lab.example"}, slog.New(slog.NewTextHandler(&logs, nil)))
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	writeConfig(t, path, map[string]any{"credsStore": "fake", "credHelpers": map[string]string{"registry.lab.example": "absent"}, "auths": map[string]any{"ghcr.io": map[string]string{}}})

	if lg, ok := l.For("ghcr.io"); !ok || lg.Username != user || lg.Password != password {
		t.Fatalf("ghcr.io from the helper: %v", ok)
	}
	if _, ok := l.For("ghcr.io"); !ok {
		t.Fatal("asked again")
	}
	if _, ok := l.For("other.registry.example"); ok {
		t.Fatal("a registry the policy doesn't list")
	}
	if _, ok := l.For("quay.io"); ok {
		t.Fatal("an identity token isn't a login")
	}
	if all := l.All(); len(all) != 1 || all["ghcr.io"].Username != user {
		t.Fatalf("All = %v", len(all))
	}
	if b, _ := os.ReadFile(calls); string(b) != "ghcr.io\nquay.io\n" {
		t.Fatalf("the helper was asked %q: once per host, never for an unlisted one", b)
	}
	now = now.Add(helperKeep + time.Second)
	l.For("ghcr.io")
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "ghcr.io") != 2 {
		t.Fatalf("after %v it's asked again: %q", helperKeep, b)
	}

	// registry.lab.example's helper isn't installed.
	if _, ok := l.For("registry.lab.example"); ok {
		t.Fatal("a missing helper gave a login")
	}
	if got := l.Missing("registry.lab.example"); got != "absent" {
		t.Fatalf("Missing = %q", got)
	}
	if got := l.Missing("ghcr.io"); got != "" {
		t.Fatalf("ghcr.io's helper runs: %q", got)
	}
	e := l.Describe(&LoginError{Registry: "registry.lab.example"})
	if !strings.Contains(e.Error(), "registry.lab.example needs a login, and this node has none for it.") ||
		!strings.Contains(e.Error(), "docker-credential-absent, which isn't installed in the node") || !strings.Contains(e.Error(), "docker --config <dir> login registry.lab.example") {
		t.Fatalf("message: %s", e.Error())
	}
	if strings.Contains(logs.String(), password) || strings.Contains(logs.String(), "fake-identity-token") {
		t.Fatalf("a secret in the log: %s", logs.String())
	}
}
