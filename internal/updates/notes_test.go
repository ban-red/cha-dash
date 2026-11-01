package updates

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
)

func TestGithubRepo(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/linuxserver/docker-sonarr":      "https://github.com/linuxserver/docker-sonarr",
		"https://github.com/linuxserver/docker-sonarr.git":  "https://github.com/linuxserver/docker-sonarr",
		"git@github.com:home-assistant/core.git":            "https://github.com/home-assistant/core",
		"https://www.github.com/a/b/":                       "https://github.com/a/b",
		"https://github.com/a/b/tree/main":                  "",
		"https://gitlab.com/a/b":                            "",
		"https://github.com.evil.example/a/b":               "",
		"https://github.com/a/..":                           "",
		"":                                                  "",
		"javascript:alert(1)//github.com/a/b":               "",
		"https://github.com/a/b?x=https://evil.example/a/b": "",
	} {
		if got := githubRepo(in); got != want {
			t.Errorf("githubRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

// Release notes are untrusted text: no HTML, no images, links only into the
// same repository, no control or direction characters, bounded.
func TestSanitizeNotes(t *testing.T) {
	const repo = "https://github.com/acme/app"
	in := "## What's new\r\n<script>alert(1)</script><img src=x onerror=alert(1)>**Fast** <b>scans</b>\n" +
		"- Fixed [#12](https://github.com/acme/app/pull/12) and [the docs](https://docs.evil.example/x)\n" +
		"- See https://github.com/acme/app/releases/tag/v2.0.0, https://github.com/acme/application/x and http://github.com/acme/app/x\n" +
		"- ![logo](https://github.com/acme/app/logo.png) [click](javascript:alert(1)) www.evil.example data:text/html,hi\n" +
		"- Sneaky https://github.com/acme/app/../../evil/x ‮evil\u0007\n" +
		"<!-- hidden -->Done"
	got, cut := SanitizeNotes(in, repo)
	want := "## What's new\nalert(1)**Fast** scans\n" +
		"- Fixed #12 (https://github.com/acme/app/pull/12) and the docs (link removed)\n" +
		"- See https://github.com/acme/app/releases/tag/v2.0.0, (link removed) and (link removed)\n" +
		"- logo click (link removed)) (link removed) (link removed)\n" + // the link ended at its first )
		"- Sneaky (link removed) evil\n" +
		"Done"
	if got != want || cut {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	long, cut := SanitizeNotes(strings.Repeat("é", MaxNoteBytes), repo)
	if !cut || len(long) > MaxNoteBytes || !strings.HasSuffix(long, "é") {
		t.Fatalf("long: %d bytes, cut %v", len(long), cut)
	}
	if got := plainLine("<b>v2</b>\n beta\u0000", 100); got != "v2 beta" {
		t.Fatalf("plainLine = %q", got)
	}
}

func TestRisk(t *testing.T) {
	for _, c := range [][3]string{
		{"8.4.1", "8.4.5", RiskPatch},
		{"v4.0.1", "v4.1.0", RiskMinor},
		{"4.0.15.2941-ls299", "5.0.0", RiskMajor},
		{"2.36-9+deb12u7", "2.36-9+deb12u9", RiskPatch},
		{"6.8.12-4", "6.8.12-5", RiskPatch},
		{"1:2.0", "2:1.0", RiskMajor},
		{"latest", "1.2.3", RiskUnknown},
		{"1.2.3", "1.2.3", RiskUnknown},
		{"2.0", "1.9", RiskUnknown},
		{"", "", RiskUnknown},
	} {
		if got := Risk(c[0], c[1]); got != c[2] {
			t.Errorf("Risk(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	if higherRisk(RiskPatch, RiskMajor) != RiskMajor || higherRisk(RiskUnknown, RiskMinor) != RiskUnknown {
		t.Fatal("higherRisk")
	}
}

// The hub reads releases from GitHub only when the setting is on, once per
// repository for 6 hours, and picks those newer than the running version.
func TestReleaseNotes(t *testing.T) {
	var calls atomic.Int32
	var ua, auth string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ua, auth = r.Header.Get("User-Agent"), r.Header.Get("Authorization")
		if r.URL.Path != "/repos/acme/app/releases" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"tag_name":"v2.1.0-rc1","prerelease":true,"body":"rc"},
			{"tag_name":"v2.0.1","name":"Two oh one","body":"Fixes <b>it</b> [x](https://evil.example)","html_url":"https://github.com/acme/app/releases/tag/v2.0.1","published_at":"2026-10-30T10:00:00Z"},
			{"tag_name":"v2.0.0","body":"Big","html_url":"https://evil.example/releases/v2.0.0"},
			{"tag_name":"v1.9.0","body":"Old"},
			{"tag_name":"v1.8.0","draft":true,"body":"draft"}
		]`))
	}))
	defer gh.Close()

	e := newEnv(t)
	e.s.notes = newNotesCache(gh.URL, gh.Client())
	e.s.notes.now = func() time.Time { return e.now }
	id := e.container("app", integration.UpdateAvailable, "sha256:bb")
	e.upsert(graph.Resource{ID: graph.RID(id), Kind: "container", Name: "app",
		Labels: map[string]string{LabelSource: "https://github.com/acme/app.git", LabelVersion: "1.9.0"},
		Attrs:  map[string]any{"update": map[string]any{"state": "available", "current": "sha256:aa", "latest": "sha256:bb"}}})

	if _, err := e.s.ReleaseNotes(t.Context(), id); err != ErrNotesOff || calls.Load() != 0 {
		t.Fatalf("off: %v, %d calls", err, calls.Load())
	}
	if r := e.list()["app"]; r.Risk != RiskUnknown || r.Source != "https://github.com/acme/app" || r.Version != "1.9.0" {
		t.Fatalf("row before = %+v", r)
	}
	if err := e.s.SetNotesEnabled(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	n, err := e.s.ReleaseNotes(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, r := range n.Releases {
		tags = append(tags, r.Tag)
	}
	if !slices.Equal(tags, []string{"v2.0.1", "v2.0.0"}) || !n.Newer || n.From != "1.9.0" || n.Repo != "https://github.com/acme/app" {
		t.Fatalf("notes = %+v", n)
	}
	if r := n.Releases[0]; r.Notes != "Fixes it x (link removed)" || r.URL != "https://github.com/acme/app/releases/tag/v2.0.1" || r.Name != "Two oh one" {
		t.Fatalf("release = %+v", r)
	}
	if n.Releases[1].URL != "" {
		t.Fatalf("a link out of the repo was kept: %+v", n.Releases[1])
	}
	if !strings.HasPrefix(ua, "chadash/") || auth != "" {
		t.Fatalf("user agent %q, authorization %q", ua, auth)
	}
	// Cached: the list's risk comes from it, and GitHub isn't asked again.
	if r := e.list()["app"]; r.Risk != RiskMajor {
		t.Fatalf("risk = %q", r.Risk)
	}
	if _, err := e.s.ReleaseNotes(t.Context(), id); err != nil || calls.Load() != 1 {
		t.Fatalf("cache: %v, %d calls", err, calls.Load())
	}
	e.now = e.now.Add(notesTTL + time.Minute)
	if _, err := e.s.ReleaseNotes(t.Context(), id); err != nil || calls.Load() != 2 {
		t.Fatalf("after the TTL: %v, %d calls", err, calls.Load())
	}
}

// PVE hosts list their pending packages, with the highest risk, and Unraid
// its version; both link to their own pages.
func TestHostRows(t *testing.T) {
	e := newEnv(t)
	e.upsert(graph.Resource{ID: "proxmox:i_p/node/pve-a", Kind: "host", Name: "pve-a", Attrs: map[string]any{
		"platform": "pve", "updates": 3,
		"apt": []any{
			map[string]any{"package": "libc6", "from": "2.36-9+deb12u7", "to": "2.36-9+deb12u9"},
			map[string]any{"package": "pve-manager", "from": "8.4.1", "to": "8.5.0"},
		},
		"links": []any{map[string]any{"title": "Proxmox", "url": "https://pve-a.lab.example:8006/#v1:0:=node%2Fpve-a"}},
	}})
	e.upsert(graph.Resource{ID: "proxmox:i_p/node/pve-b", Kind: "host", Name: "pve-b", Attrs: map[string]any{"platform": "pve", "updates": 0, "apt": []any{}}})
	e.upsert(graph.Resource{ID: "proxmox:i_q/node/pve-c", Kind: "host", Name: "pve-c", Attrs: map[string]any{"platform": "pve"}})
	e.upsert(graph.Resource{ID: "unraid:i_u/host", Kind: "host", Name: "jupiter", Attrs: map[string]any{
		"platform": "unraid", "version": "7.1.4", "links": []any{map[string]any{"title": "Unraid", "url": "https://jupiter.lab.example"}}}})
	rows := e.list()
	a := rows["pve-a"]
	if a.Kind != KindApt || a.State != integration.UpdateAvailable || a.Latest != "3 packages" || len(a.Packages) != 2 ||
		a.Packages[1].Risk != RiskMinor || a.Risk != RiskUnknown || a.Update != nil || a.Link == nil || !strings.HasPrefix(a.Link.URL, "https://pve-a") {
		t.Fatalf("pve-a = %+v", a)
	}
	if b := rows["pve-b"]; b.State != integration.UpdateCurrent || b.Kind != KindApt {
		t.Fatalf("pve-b = %+v", b)
	}
	if _, ok := rows["pve-c"]; ok {
		t.Fatal("a host without Sys.Modify has a row")
	}
	if u := rows["jupiter"]; u.Kind != KindOS || u.Current != "7.1.4" || u.State != integration.UpdateCantCheck || u.Link == nil || u.Link.URL != "https://jupiter.lab.example" {
		t.Fatalf("jupiter = %+v", u)
	}
	// Holds don't apply to a host, and the Inbox item counts pve-a.
	if _, err := e.s.Hold(t.Context(), "proxmox:i_p/node/pve-a", KindHold, ""); err != ErrNotFound {
		t.Fatalf("hold a host: %v", err)
	}
	e.s.Recount(t.Context())
	if it, ok := e.item(); !ok || it.Title != "1 update available" {
		t.Fatalf("item = %+v", it)
	}
	// A rollout leaves the host out, and says so.
	p := e.plan("proxmox:i_p/node/pve-a")
	if len(p.Steps) != 1 || p.Steps[0].State != StepSkipped || !strings.Contains(p.Steps[0].Reason, "Proxmox or Unraid") {
		t.Fatalf("plan = %+v", p)
	}
}
