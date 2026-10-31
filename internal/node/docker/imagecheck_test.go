package docker

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
)

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// image.check: has each running container's tag moved in its registry, and
// why not when it can't tell.
func TestImageCheck(t *testing.T) {
	f, host := newFakeDocker(t)
	p := node.DefaultPolicy()
	m, err := New(Options{Host: host, Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.registry = f.fakeRegistry(t, "")
	d, err := node.NewDispatcher(p, m)
	if err != nil {
		t.Fatal(err)
	}

	image := func(id string, repoDigests ...string) string {
		f.repoDigests[digest(id)] = repoDigests
		return digest(id)
	}
	f.add(&fakeContainer{Name: "sonarr", Image: "lscr.io/linuxserver/sonarr:4", ImageID: image("a", "lscr.io/linuxserver/sonarr@"+digest("1"))})
	f.add(&fakeContainer{Name: "web", Image: "nginx:1.27", ImageID: image("b", "nginx@"+digest("2"))})
	f.add(&fakeContainer{Name: "app", Image: "myapp:dev", ImageID: image("c")})
	f.add(&fakeContainer{Name: "cache", Image: "redis@" + digest("4"), ImageID: image("d", "redis@"+digest("4"))})
	f.add(&fakeContainer{Name: "secret", Image: "ghcr.io/example/private:1", ImageID: image("e", "ghcr.io/example/private@"+digest("5"))})
	f.add(&fakeContainer{Name: "old", Image: "nginx:1.25", ImageID: image("f", "nginx@"+digest("6")), State: "exited"})
	// What container.update leaves: the tag with the digest it pulled.
	f.add(&fakeContainer{Name: "updated", Image: "lscr.io/linuxserver/sonarr:4@" + digest("7"), ImageID: image("g", "lscr.io/linuxserver/sonarr@"+digest("7"))})
	f.registry["lscr.io/linuxserver/sonarr:4"] = digest("7")
	f.registry["docker.io/library/nginx:1.27"] = digest("2")
	f.registry["ghcr.io/example/private:1"] = "401"

	check := func() map[string]imageUpdate {
		t.Helper()
		res, _, err := call(t, d, "image.check", nil)
		if err != nil {
			t.Fatal(err)
		}
		var out struct{ Images []imageUpdate }
		if err := json.Unmarshal(res, &out); err != nil {
			t.Fatal(err)
		}
		by := map[string]imageUpdate{}
		for _, u := range out.Images {
			by[f.find(u.ID).Name] = u
		}
		return by
	}
	got := check()
	if _, ok := got["old"]; ok || len(got) != 6 {
		t.Fatalf("checked %v, want the six running containers", slices.Sorted(func(yield func(string) bool) {
			for k := range got {
				yield(k)
			}
		}))
	}
	if u := got["sonarr"]; u.State != updateAvailable || u.Current != digest("1") || u.Latest != digest("7") || !u.CheckedAt.Equal(now) || u.Image != "lscr.io/linuxserver/sonarr:4" {
		t.Errorf("sonarr: %+v", u)
	}
	if u := got["updated"]; u.State != updateCurrent || u.Latest != digest("7") {
		t.Errorf("updated: %+v, want its tag checked", u)
	}
	// Docker Hub's short names normalise: nginx:1.27 is docker.io/library/nginx:1.27.
	if u := got["web"]; u.State != updateCurrent || u.Current != digest("2") || u.Latest != digest("2") {
		t.Errorf("web: %+v", u)
	}
	for name, why := range map[string]string{
		"app":    "built or loaded on this host",
		"cache":  "pinned by digest",
		"secret": "needs a login",
	} {
		if u := got[name]; u.State != updateCantCheck || !strings.Contains(u.Reason, why) || u.Latest != "" {
			t.Errorf("%s: %+v, want cant_check because %s", name, u, why)
		}
	}
	slices.Sort(f.asked)
	if !slices.Equal(f.asked, []string{"docker.io/library/nginx:1.27", "ghcr.io/example/private:1", "lscr.io/linuxserver/sonarr:4"}) {
		t.Fatalf("asked %q: only tags with a registry digest go to a registry", f.asked)
	}
	// Each a HEAD with no credentials but Docker Hub's anonymous token, and
	// Docker's own lookup (a pull on Docker Hub) never.
	for _, h := range f.headers {
		if a := h.Get("Authorization"); a != "" && a != "Bearer anon" || !strings.Contains(h.Get("Accept"), "image.index") {
			t.Fatalf("a HEAD sent %v", h)
		}
	}
	if len(f.inspected) != 0 {
		t.Fatalf("asked Docker about %q", f.inspected)
	}

	// Within CheckEvery the answers come from memory, the login refusal too.
	f.asked = nil
	now = now.Add(CheckEvery - time.Minute)
	if got := check(); got["sonarr"].Latest != digest("7") || len(f.asked) != 0 {
		t.Fatalf("asked %q again within %s", f.asked, CheckEvery)
	}
	// After it, the registries are asked again, and a moved tag shows.
	f.registry["docker.io/library/nginx:1.27"] = digest("8")
	now = now.Add(2 * time.Minute)
	if got := check(); got["web"].State != updateAvailable || got["web"].Latest != digest("8") || len(f.asked) != 3 {
		t.Fatalf("after %s: web %+v, asked %q", CheckEvery, got["web"], f.asked)
	}

	// A registry that didn't answer is asked again sooner.
	f.registry["lscr.io/linuxserver/sonarr:4"] = ""
	now = now.Add(CheckEvery + time.Minute)
	if got := check(); got["sonarr"].State != updateCantCheck || !strings.Contains(got["sonarr"].Reason, "doesn't have that tag") {
		t.Fatalf("sonarr gone from its registry: %+v", got["sonarr"])
	}
	f.asked = nil
	f.registry["lscr.io/linuxserver/sonarr:4"] = digest("7")
	now = now.Add(checkRetry + time.Minute)
	if got := check(); got["sonarr"].State != updateAvailable || !slices.Contains(f.asked, "lscr.io/linuxserver/sonarr:4") {
		t.Fatalf("sonarr after %s: %+v, asked %q", checkRetry, got["sonarr"], f.asked)
	}

	// Check now asks again, but not twice within a minute.
	f.asked = nil
	now = now.Add(time.Hour)
	fresh := func() {
		t.Helper()
		if _, _, err := call(t, d, "image.check", map[string]any{"fresh": true}); err != nil {
			t.Fatal(err)
		}
	}
	fresh()
	if len(f.asked) != 3 {
		t.Fatalf("a fresh check asked %q", f.asked)
	}
	now = now.Add(30 * time.Second)
	fresh()
	if len(f.asked) != 3 {
		t.Fatalf("a second fresh check within %s asked %q", freshAfter, f.asked)
	}

	// One container by name, and a bad ID.
	if res, _, err := call(t, d, "image.check", map[string]any{"ids": []string{"web"}}); err != nil || !strings.Contains(string(res), `"image":"nginx:1.27"`) || strings.Contains(string(res), "sonarr") {
		t.Fatalf("by name: %s %v", res, err)
	}
	if _, _, err := call(t, d, "image.check", map[string]any{"ids": []string{"../x"}}); nodeproto.CodeOf(err) != nodeproto.CodeInvalid {
		t.Fatalf("bad id = %v", err)
	}
}

// A policy without images.check refuses the verb before anything is asked.
func TestImageCheckOff(t *testing.T) {
	f, d := setup(t, "images: { allow: [] }\n")
	if _, _, err := call(t, d, "image.check", nil); nodeproto.CodeOf(err) != nodeproto.CodeForbidden {
		t.Fatalf("image.check with images.check off = %v", err)
	}
	if len(f.asked) != 0 {
		t.Fatalf("asked %q", f.asked)
	}
}

// A registry the node can't reach directly (plain http, one only the
// daemon's config knows) is asked through Docker instead.
func TestImageCheckThroughDocker(t *testing.T) {
	f, host := newFakeDocker(t)
	p := node.DefaultPolicy()
	m, err := New(Options{Host: host, Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	m.registry = f.fakeRegistry(t, "registry.lab.example:5000")
	d, err := node.NewDispatcher(p, m)
	if err != nil {
		t.Fatal(err)
	}
	f.repoDigests[digest("a")] = []string{"registry.lab.example:5000/tools@" + digest("1")}
	f.add(&fakeContainer{Name: "tools", Image: "registry.lab.example:5000/tools:1", ImageID: digest("a")})
	f.registry["registry.lab.example:5000/tools:1"] = digest("2")
	res, _, err := call(t, d, "image.check", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), `"state":"available"`) || !slices.Equal(f.inspected, []string{"registry.lab.example:5000/tools:1"}) || len(f.asked) != 0 {
		t.Fatalf("%s; Docker asked %q, the registry %q", res, f.inspected, f.asked)
	}
}
