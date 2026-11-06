package docker

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/node/registry"
)

// dockerConfig writes a Docker config file with the fake registry's login
// for each host, as `docker login` leaves one.
func dockerConfig(t *testing.T, hosts ...string) string {
	t.Helper()
	auths := map[string]any{}
	for _, h := range hosts {
		auths[h] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(fakeUser + ":" + fakePassword))}
	}
	b, _ := json.Marshal(map[string]any{"auths": auths})
	path := filepath.Join(t.TempDir(), "docker-config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The node logs in only to the registries images.registries lists, with
// the login its Docker config has: on a pull and on image.check. A
// registry that needs a login it hasn't got says so in one sentence, and
// the password is in no reply, item or log line.
func TestRegistryLogins(t *testing.T) {
	f, host := newFakeDocker(t)
	p, err := node.ParsePolicy([]byte("images: { registries: [ghcr.io] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	// The config has a login for a registry the policy doesn't list too.
	logins := registry.NewLogins(dockerConfig(t, "ghcr.io", "other.registry.example"), p.Images.Registries, log)
	m, err := New(Options{Host: host, Policy: p, SelfID: "self00000000", Logins: logins, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	m.registry = f.fakeRegistry(t, "")
	d, err := node.NewDispatcher(p, m)
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder // every reply and item
	run := func(verb string, req any) (json.RawMessage, error) {
		t.Helper()
		res, items, err := call(t, d, verb, req)
		seen.Write(res)
		for _, it := range items {
			seen.Write(it)
		}
		if err != nil {
			seen.WriteString(err.Error())
		}
		return res, err
	}

	f.repoDigests[digest("a")] = []string{"ghcr.io/example/private@" + digest("1")}
	f.add(&fakeContainer{Name: "app", Image: "ghcr.io/example/private:1", ImageID: digest("a")})
	f.repoDigests[digest("b")] = []string{"other.registry.example/example/private@" + digest("3")}
	f.add(&fakeContainer{Name: "other", Image: "other.registry.example/example/private:1", ImageID: digest("b")})
	f.registry["ghcr.io/example/private:1"] = "login:" + digest("2")
	f.registry["other.registry.example/example/private:1"] = "login:" + digest("4")

	// A pull carries the login for ghcr.io, and none for the other.
	if _, err := run("container.pull", map[string]string{"id": "app"}); err != nil {
		t.Fatalf("pull from ghcr.io with its login: %v", err)
	}
	var sent struct{ Username, Password, ServerAddress string }
	raw, _ := base64.URLEncoding.DecodeString(f.pullAuth["ghcr.io/example/private:1"])
	if err := json.Unmarshal(raw, &sent); err != nil || sent.Username != fakeUser || sent.Password != fakePassword || sent.ServerAddress != "ghcr.io" {
		t.Fatalf("the pull's X-Registry-Auth: %s (%v)", raw, err)
	}
	_, err = run("container.pull", map[string]string{"id": "other"})
	if err == nil || err.Error() != "other.registry.example needs a login, and this node has none for it." {
		t.Fatalf("pull from a registry the policy doesn't list: %v", err)
	}
	if a := f.pullAuth["other.registry.example/example/private:1"]; a != "" {
		t.Fatalf("a registry not in images.registries got a login: %q", a)
	}

	// image.check logs in to ghcr.io (the token for the login) and not to
	// the other.
	res, err := run("image.check", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Images []imageUpdate }
	_ = json.Unmarshal(res, &out)
	by := map[string]imageUpdate{}
	for _, u := range out.Images {
		by[f.find(u.ID).Name] = u
	}
	if u := by["app"]; u.State != updateAvailable || u.Latest != digest("2") {
		t.Errorf("app on ghcr.io with its login: %+v", u)
	}
	if u := by["other"]; u.State != updateCantCheck || u.Reason != "other.registry.example needs a login, and this node has none for it." {
		t.Errorf("other: %+v", u)
	}
	for _, h := range f.headers {
		if a := h.Get("Authorization"); strings.HasPrefix(a, "Basic") {
			t.Errorf("a HEAD sent Basic credentials to a Bearer registry: %v", h)
		}
	}

	b64 := base64.StdEncoding.EncodeToString([]byte(fakeUser + ":" + fakePassword))
	for _, secret := range []string{fakePassword, b64} {
		if strings.Contains(seen.String(), secret) {
			t.Errorf("a reply or item carries the login: %s", seen.String())
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("a log line carries the login: %s", logs.String())
		}
	}
	if !strings.Contains(logs.String(), "other.registry.example") {
		t.Errorf("the log should say the unlisted registry's login is never sent: %s", logs.String())
	}
}
