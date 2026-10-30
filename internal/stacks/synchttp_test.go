package stacks

import (
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/stackproto"
)

const fakeHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"

// The sync's routes: follow, the step-ups, the read-only editor, test,
// check now, resume and take over, and unfollow.
func TestSyncRoutes(t *testing.T) {
	e := newSyncEnv(t)
	base := "/api/stacks/local/media/sync"
	follow := map[string]any{"url": remoteURL, "branch": "main", "auth": map[string]any{"kind": "none"}, "mode": "ask"}

	if v := decode[struct{ Sync *SyncView }](t, e.do("GET", base, nil)); v.Sync != nil {
		t.Fatalf("no sync yet: %+v", v.Sync)
	}
	for _, bad := range []map[string]any{
		{"url": "https://sam:FAKE-token@git.lab.example/ops/stacks.git", "branch": "main"},
		{"url": "git@git.lab.example:ops/stacks.git", "branch": "main"},
		{"url": "file:///srv/stacks.git", "branch": "main"},
		{"url": remoteURL, "branch": "-main"},
		{"url": remoteURL, "branch": "main", "path": "../etc/passwd"},
		{"url": remoteURL, "branch": "main", "poll_seconds": 5},
		{"url": remoteURL, "branch": "main", "auth": map[string]any{"kind": "ssh", "secret": "FAKE-KEY"}},
		{"url": "ssh://git.lab.example/ops/stacks.git", "branch": "main", "auth": map[string]any{"kind": "ssh", "secret": "FAKE-KEY"}},
		{"url": remoteURL, "branch": "main", "auth": map[string]any{"kind": "https"}},
	} {
		if w := e.do("PUT", base, bad); w.Code != 422 {
			t.Errorf("%v: %d %s", bad, w.Code, w.Body)
		}
	}

	// Follow, in draft-and-ask mode: no step-up.
	w := e.do("PUT", base, follow)
	if w.Code != 201 {
		t.Fatalf("follow: %d %s", w.Code, w.Body)
	}
	v := decode[struct{ Sync SyncView }](t, w).Sync
	if v.Path != "media/compose.yaml" || v.PollSeconds != DefaultPollSeconds || v.Mode != ModeAsk || v.Owner == nil || v.Owner.ID != "u_sam" || v.State != StateActive {
		t.Fatalf("sync %+v", v)
	}

	// The editor is read-only: saving or discarding a draft is refused with
	// where to edit instead.
	for _, method := range []string{"PUT", "DELETE"} {
		path := "/api/stacks/local/media"
		var body any = map[string]any{"content": good, "revision": 0}
		if method == "DELETE" {
			path, body = path+"/draft", nil
		}
		w := e.do(method, path, body)
		if w.Code != 409 || !strings.Contains(w.Body.String(), `"synced"`) ||
			!strings.Contains(w.Body.String(), "Edit media/compose.yaml on main in "+remoteURL+".") {
			t.Fatalf("%s draft of a synced stack: %d %s", method, w.Code, w.Body)
		}
	}

	// Deploy on push needs a step-up; so does another URL.
	follow["mode"] = "deploy"
	if w := e.do("PUT", base, follow); w.Code != 403 || !strings.Contains(w.Body.String(), "step_up_required") {
		t.Fatalf("deploy without a step-up: %d %s", w.Code, w.Body)
	}
	e.actor.StepUpUntil = time.Now().Add(time.Minute)
	if w := e.do("PUT", base, follow); w.Code != 200 {
		t.Fatalf("deploy with a step-up: %d %s", w.Code, w.Body)
	}
	e.actor.StepUpUntil = time.Time{}
	follow["url"] = "https://git.lab.example/ops/other.git"
	if w := e.do("PUT", base, follow); w.Code != 403 {
		t.Fatalf("a new URL without a step-up: %d %s", w.Code, w.Body)
	}
	// The same remote spelled another way isn't a new URL.
	follow["url"] = "HTTPS://Git.Lab.Example:443/ops/stacks/"
	if w := e.do("PUT", base, follow); w.Code != 200 {
		t.Fatalf("the same remote respelled: %d %s", w.Code, w.Body)
	}

	// Test: the node's answer, with the SSH host key to pin.
	e.remote.push("ssh://git.lab.example/ops/stacks.git", "main", sha1, "media/compose.yaml", good)
	e.remote.hostKey = fakeHostKey
	w = e.do("POST", base+"/check", map[string]any{"url": "ssh://git.lab.example/ops/stacks.git", "auth": map[string]any{"kind": "ssh", "secret": "FAKE-PRIVATE-KEY"}})
	chk := decode[struct {
		SHA     string  `json:"sha"`
		HostKey *string `json:"host_key"`
	}](t, w)
	if w.Code != 200 || chk.SHA != sha1 || chk.HostKey == nil || *chk.HostKey != fakeHostKey {
		t.Fatalf("check: %d %s", w.Code, w.Body)
	}
	if w := e.do("POST", base+"/check", map[string]any{"branch": "nope"}); w.Code != 404 {
		t.Fatalf("check a missing branch: %d %s", w.Code, w.Body)
	}

	// SSH with the key pinned and a credential saved; the secret never comes back.
	e.actor.StepUpUntil = time.Now().Add(time.Minute)
	ssh := map[string]any{"url": "ssh://git.lab.example/ops/stacks.git", "branch": "main", "host_key": fakeHostKey, "mode": "deploy",
		"auth": map[string]any{"kind": "ssh", "secret": "FAKE-PRIVATE-KEY-VALUE"}}
	w = e.do("PUT", base, ssh)
	if w.Code != 200 || strings.Contains(w.Body.String(), "FAKE-PRIVATE-KEY-VALUE") {
		t.Fatalf("ssh: %d %s", w.Code, w.Body)
	}
	v = decode[struct{ Sync SyncView }](t, w).Sync
	if !v.Auth.SecretSet || v.Auth.Kind != "ssh" || v.HostKey == nil {
		t.Fatalf("ssh sync %+v", v)
	}
	// Leaving the secret out keeps the saved one.
	delete(ssh["auth"].(map[string]any), "secret")
	if w := e.do("PUT", base, ssh); w.Code != 200 || !decode[struct{ Sync SyncView }](t, w).Sync.Auth.SecretSet {
		t.Fatalf("keep the secret: %d %s", w.Code, w.Body)
	}
	if c, _ := e.svc.credentialOf(t.Context(), v.ID); c.Secret != "FAKE-PRIVATE-KEY-VALUE" {
		t.Fatal("the saved credential was lost")
	}
	e.actor.StepUpUntil = time.Time{}

	// Check now isn't possible while the syncer isn't running.
	if w := e.do("POST", base+"/now", nil); w.Code != 503 {
		t.Fatalf("now: %d %s", w.Code, w.Body)
	}

	// kim takes it over by resuming it: a step-up, and it's hers.
	e.actor = authz.Actor{Kind: authz.KindUser, ID: "u_kim", Name: "kim", Role: auth.RoleAdmin, Session: "s_2"}
	if w := e.do("POST", base+"/resume", nil); w.Code != 403 {
		t.Fatalf("take over without a step-up: %d %s", w.Code, w.Body)
	}
	e.actor.StepUpUntil = time.Now().Add(time.Minute)
	w = e.do("POST", base+"/resume", nil)
	if v := decode[struct{ Sync SyncView }](t, w).Sync; w.Code != 200 || v.Owner == nil || v.Owner.Username != "kim" {
		t.Fatalf("take over: %d %s", w.Code, w.Body)
	}

	// Unfollow: the stack is a normal one again.
	if w := e.do("DELETE", base, nil); w.Code != 204 {
		t.Fatalf("unfollow: %d %s", w.Code, w.Body)
	}
	if c, _ := e.svc.credentialOf(t.Context(), v.ID); c.Secret != "" {
		t.Fatal("unfollowing kept the credential")
	}
	if w := e.do("PUT", "/api/stacks/local/media", map[string]any{"content": good, "revision": 0}); w.Code != 200 {
		t.Fatalf("draft after unfollowing: %d %s", w.Code, w.Body)
	}
	if w := e.do("DELETE", base, nil); w.Code != 404 {
		t.Fatalf("unfollow twice: %d %s", w.Code, w.Body)
	}
}

// Every change to a sync is audited, by name and host, never the credential.
func TestSyncRoutesAudit(t *testing.T) {
	e := newSyncEnv(t)
	base := "/api/stacks/local/media/sync"
	follow := map[string]any{"url": "https://git.lab.example/ops/stacks.git", "branch": "main", "mode": "ask",
		"auth": map[string]any{"kind": "https", "username": "sam", "secret": "FAKE-TOKEN-VALUE"}}
	steps := []struct {
		method, path string
		body         any
		status       int
		action       string
	}{
		{"PUT", base, follow, 201, "settings.stacks.sync.create"},
		{"PUT", base, follow, 200, "settings.stacks.sync.update"},
		{"POST", base + "/resume", nil, 200, "settings.stacks.sync.resume"},
	}
	for _, s := range steps {
		if w := e.do(s.method, s.path, s.body); w.Code != s.status {
			t.Fatalf("%s %s: %d %s", s.method, s.path, w.Code, w.Body)
		}
		ev, ok := lastEvent(t, e.hubEnv, s.action)
		if !ok || ev.Target != "stack:local/media" || strings.Contains(string(ev.Params), "FAKE-TOKEN-VALUE") || strings.Contains(string(ev.Params), "/ops/") {
			t.Fatalf("%s: %+v %s", s.action, ev, ev.Params)
		}
	}
	e.actor = authz.Actor{Kind: authz.KindUser, ID: "u_kim", Name: "kim", Role: auth.RoleAdmin, Session: "s_2", StepUpUntil: time.Now().Add(time.Minute)}
	if w := e.do("PUT", base, follow); w.Code != 200 {
		t.Fatalf("take over: %d %s", w.Code, w.Body)
	}
	if ev, ok := lastEvent(t, e.hubEnv, "settings.stacks.sync.takeover"); !ok || !strings.Contains(string(ev.Params), "u_sam") {
		t.Fatalf("takeover: %+v", ev)
	}
	// A refused step-up is audited too.
	e.actor.StepUpUntil = time.Time{}
	follow["mode"] = "deploy"
	_ = e.do("PUT", base, follow)
	if ev, _ := lastEvent(t, e.hubEnv, "settings.stacks.sync.update"); ev.Result != "refused" {
		t.Fatalf("refused step-up: %+v", ev)
	}
	if w := e.do("DELETE", base, nil); w.Code != 204 {
		t.Fatalf("unfollow: %d", w.Code)
	}
	if _, ok := lastEvent(t, e.hubEnv, "settings.stacks.sync.delete"); !ok {
		t.Fatal("no delete event")
	}
}

// URLs are compared normalized: the same remote with or without .git, a
// trailing / or the default port.
func TestNormalizeURL(t *testing.T) {
	same := []string{
		"https://git.lab.example/ops/stacks",
		"https://git.lab.example/ops/stacks.git",
		"https://git.lab.example/ops/stacks/",
		"https://git.lab.example/ops/stacks.git/",
		"HTTPS://Git.Lab.Example:443/ops/stacks",
		" https://git.lab.example/ops/stacks.git ",
	}
	for _, u := range same {
		if got := normalizeURL(u); got != "https://git.lab.example/ops/stacks" {
			t.Errorf("%q → %q", u, got)
		}
	}
	for in, want := range map[string]string{
		"ssh://git.lab.example:22/ops/stacks.git":   "ssh://git.lab.example/ops/stacks",
		"ssh://git.lab.example:2222/ops/stacks.git": "ssh://git.lab.example:2222/ops/stacks",
		"http://git.lab.example:80/x":               "http://git.lab.example/x",
		"http://git.lab.example:8080/x":             "http://git.lab.example:8080/x",
		"https://[fd00::5]:443/x.git":               "https://[fd00::5]/x",
		"https://[fd00::5]:8443/x.git":              "https://[fd00::5]:8443/x",
		"https://git.lab.example/Ops/Stacks":        "https://git.lab.example/Ops/Stacks", // paths keep their case
	} {
		if got := normalizeURL(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	if normalizeURL("https://git.lab.example/ops/stacks") == normalizeURL("https://git.lab.example/ops/other") {
		t.Error("different repositories matched")
	}
}

// The vault names live outside stacks/, so a compose file can't reference
// them and the stack secrets list never shows them.
func TestSyncVaultNamesOutsideStackSecrets(t *testing.T) {
	for _, n := range []string{syncCredentialName("syn_x"), hookSecretName("whk_x")} {
		if strings.HasPrefix(n, secretPrefix) {
			t.Errorf("%s is in the stack secrets' namespace", n)
		}
	}
	if syncCredentialName("syn_x") != "stacksync/syn_x/credential" || hookSecretName("whk_x") != "stackhook/whk_x" {
		t.Error("vault names changed")
	}
	e := newSyncEnv(t)
	if w := e.do("PUT", "/api/stacks/local/media/sync", map[string]any{"url": remoteURL, "branch": "main",
		"auth": map[string]any{"kind": stackproto.AuthHTTPS, "secret": "FAKE-TOKEN-VALUE"}}); w.Code != 201 {
		t.Fatalf("follow: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/stacks/secrets", nil); strings.Contains(w.Body.String(), "stacksync") || strings.Contains(w.Body.String(), "credential") {
		t.Fatalf("secrets list: %s", w.Body)
	}
}
