package stacks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/notify"
)

const hookSecret = "FAKE-hook-secret-0123456789abcdef"

func hexMAC(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// signed is a request each sender would make for body.
func signed(sender, secret, body, delivery string, at time.Time) http.Header {
	h := http.Header{}
	switch sender {
	case SenderGitHub:
		h.Set("X-Hub-Signature-256", "sha256="+hexMAC(secret, body))
		h.Set("X-GitHub-Delivery", delivery)
	case SenderGitea:
		h.Set("X-Gitea-Signature", hexMAC(secret, body))
		h.Set("X-Gitea-Delivery", delivery)
	case SenderForgejo:
		h.Set("X-Forgejo-Signature", hexMAC(secret, body))
		h.Set("X-Forgejo-Delivery", delivery)
	case SenderChadash:
		ts := at.Unix()
		h.Set("X-Chadash-Timestamp", strconv.FormatInt(ts, 10))
		h.Set("X-Chadash-Signature", notify.Sign([]byte(secret), ts, []byte(body)))
	case SenderGitLab:
		h.Set("X-Gitlab-Token", secret)
	}
	return h
}

// Signature vectors for all five senders, and what each refuses: the wrong
// secret, another sender's header, a missing delivery ID and a stale
// timestamp.
func TestHookSignatures(t *testing.T) {
	// GitHub's documented example, so the HMAC isn't only self-consistent.
	gh := http.Header{}
	gh.Set("X-Hub-Signature-256", "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17")
	gh.Set("X-GitHub-Delivery", "d-1")
	if id, err := verifyHook(SenderGitHub, []byte("It's a Secret to Everybody"), gh, []byte("Hello, World!"), time.Now()); err != nil || id != "d-1" {
		t.Fatalf("GitHub's example: %q %v", id, err)
	}

	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	body := `{"ref":"refs/heads/main"}`
	for _, sender := range Senders {
		h := signed(sender, hookSecret, body, "d-"+sender, now)
		if _, err := verifyHook(sender, []byte(hookSecret), h, []byte(body), now); err != nil {
			t.Errorf("%s: a valid request refused: %v", sender, err)
		}
		if _, err := verifyHook(sender, []byte("FAKE-other-secret-000000000000000"), h, []byte(body), now); err == nil {
			t.Errorf("%s: the wrong secret accepted", sender)
		}
		if sender != SenderGitLab {
			if _, err := verifyHook(sender, []byte(hookSecret), h, []byte(body+" "), now); err == nil {
				t.Errorf("%s: a changed body accepted", sender)
			}
		}
		// Every other sender's valid headers are refused: a hook checks one.
		for _, other := range Senders {
			if other == sender {
				continue
			}
			if _, err := verifyHook(sender, []byte(hookSecret), signed(other, hookSecret, body, "d-x", now), []byte(body), now); err == nil {
				t.Errorf("a %s hook accepted %s's header", sender, other)
			}
		}
	}
	for _, sender := range []string{SenderGitHub, SenderGitea, SenderForgejo} {
		h := signed(sender, hookSecret, body, "", now)
		if _, err := verifyHook(sender, []byte(hookSecret), h, []byte(body), now); err == nil {
			t.Errorf("%s without a delivery ID accepted", sender)
		}
	}
	stale := signed(SenderChadash, hookSecret, body, "", now.Add(-6*time.Minute))
	if _, err := verifyHook(SenderChadash, []byte(hookSecret), stale, []byte(body), now); err == nil {
		t.Error("a stale timestamp accepted")
	}
	future := signed(SenderChadash, hookSecret, body, "", now.Add(6*time.Minute))
	if _, err := verifyHook(SenderChadash, []byte(hookSecret), future, []byte(body), now); err == nil {
		t.Error("a timestamp from the future accepted")
	}
	if _, err := verifyHook(SenderGitHub, nil, signed(SenderGitHub, "", body, "d", now), []byte(body), now); err == nil {
		t.Error("an empty secret accepted")
	}
}

// doHook posts a raw body to a hook with headers.
func (h *hubEnv) doHook(id string, body []byte, hdr http.Header) *httptest.ResponseRecorder {
	h.t.Helper()
	mux := http.NewServeMux()
	h.svc.Register(mux, func(next http.Handler) http.Handler { return next })
	req := httptest.NewRequest("POST", "/api/hooks/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	maps.Copy(req.Header, hdr)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// makeHook creates a hook through the route and returns its ID and secret.
func (e *syncEnv) makeHook(url, branch, sender string) (string, string) {
	e.t.Helper()
	w := e.do("POST", "/api/stacks/hooks", map[string]any{"url": url, "branch": branch, "sender": sender})
	if w.Code != 201 {
		e.t.Fatalf("make hook: %d %s", w.Code, w.Body)
	}
	v := decode[struct {
		Hook   HookView
		Secret string
	}](e.t, w)
	return v.Hook.ID, v.Secret
}

// One hook triggers every active sync on its remote and branch, across two
// nodes and however the URL is spelled, and none on another branch. A ref
// that isn't the branch is ignored.
func TestHookTriggersEverySyncOnItsRemote(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	e.caller.modules["pve-b"] = e.caller.modules["local"]
	e.svc.nodes = func(context.Context) ([]Node, error) {
		return []Node{{ID: "local", Name: "jupiter", Online: true}, {ID: "pve-b", Name: "saturn", Online: true}}, nil
	}
	add := func(node, stack, url, branch string) {
		y := &Sync{ID: newID("syn_"), Node: node, Stack: stack, URL: url, Branch: branch, Path: stack + "/compose.yaml",
			AuthKind: "none", Mode: ModeAsk, PollSeconds: 300, SecretsAllow: []string{}, OwnerID: "u_sam", State: StateActive}
		if err := e.svc.insertSync(ctx, y); err != nil {
			t.Fatal(err)
		}
	}
	add("local", "media", remoteURL, "main")
	add("pve-b", "media", "HTTPS://Git.Lab.Example:443/ops/stacks/", "main")
	add("local", "other", remoteURL, "dev")
	id, secret := e.makeHook("https://git.lab.example/ops/stacks", "main", SenderGitHub)

	run, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { e.y.Run(run); close(done) }()
	defer func() { stop(); <-done }()
	waitRunning := time.Now().Add(5 * time.Second)
	for !e.y.Trigger("syn_probe") && time.Now().Before(waitRunning) {
		time.Sleep(time.Millisecond)
	}

	// A push to another branch: ignored.
	body := `{"ref":"refs/heads/dev"}`
	if w := e.doHook(id, []byte(body), signed(SenderGitHub, secret, body, "d-1", time.Now())); w.Code != 202 || !strings.Contains(w.Body.String(), "ignored") {
		t.Fatalf("other branch: %d %s", w.Code, w.Body)
	}
	body = `{"ref":"refs/heads/main","after":"` + sha1 + `"}`
	if w := e.doHook(id, []byte(body), signed(SenderGitHub, secret, body, "d-2", time.Now())); w.Code != 202 || !strings.Contains(w.Body.String(), "queued") {
		t.Fatalf("push: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	want := []string{"local " + remoteURL + " main", "pve-b HTTPS://Git.Lab.Example:443/ops/stacks/ main"}
	for {
		e.remote.mu.Lock()
		got := slices.Clone(e.remote.checks)
		e.remote.mu.Unlock()
		slices.Sort(got)
		if slices.Equal(got, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("checks %q, want %q", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if v := decode[struct{ Hooks []HookView }](t, e.do("GET", "/api/stacks/hooks", nil)); len(v.Hooks) != 1 || v.Hooks[0].LastUsedAt == nil || len(v.Hooks[0].Syncs) != 2 || v.Hooks[0].Syncs[0].NodeName != "jupiter" {
		t.Fatalf("hooks %+v", v.Hooks)
	}
}

// The webhook route: one 401 for an unknown hook and a bad signature, a
// replay refused, 413 past 1 MiB, rate limits, and refusals audited once
// per window.
func TestHookRoute(t *testing.T) {
	e := newSyncEnv(t)
	id, secret := e.makeHook(remoteURL, "main", SenderGitea)
	body := []byte(`{"ref":"refs/heads/main"}`)

	unknown := e.doHook("whk_aaaaaaaaaaaaaaaaaaaaaaaaaa", body, signed(SenderGitea, secret, string(body), "d-0", time.Now()))
	badSig := e.doHook(id, body, signed(SenderGitea, "FAKE-wrong-secret-00000000000000", string(body), "d-0", time.Now()))
	garbage := e.doHook("not-a-hook", body, nil)
	for _, w := range []*httptest.ResponseRecorder{badSig, garbage} {
		if w.Code != unknown.Code || w.Body.String() != unknown.Body.String() {
			t.Fatalf("answers differ: %d %s vs %d %s", w.Code, w.Body, unknown.Code, unknown.Body)
		}
	}
	if unknown.Code != 401 || !strings.Contains(unknown.Body.String(), `"unauthorized"`) {
		t.Fatalf("unknown: %d %s", unknown.Code, unknown.Body)
	}
	ev, ok := lastEvent(t, e.hubEnv, "stack.hook.refused")
	if !ok || ev.Target != "hook:"+id || ev.Result != "refused" || strings.Contains(string(ev.Params), "FAKE") {
		t.Fatalf("refusal audit %+v", ev)
	}

	// Valid, then the same delivery again: refused as a replay.
	h := signed(SenderGitea, secret, string(body), "d-1", time.Now())
	if w := e.doHook(id, body, h); w.Code != 202 {
		t.Fatalf("valid: %d %s", w.Code, w.Body)
	}
	if w := e.doHook(id, body, h); w.Code != 401 {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	// The replay is the third refusal within ten minutes: counted, not audited again.
	if n := countEvents(t, e.hubEnv, "stack.hook.refused"); n != 1 {
		t.Fatalf("refusals audited %d times", n)
	}

	big := bytes.Repeat([]byte("x"), maxHookBody+1)
	if w := e.doHook(id, big, signed(SenderGitea, secret, string(big), "d-2", time.Now())); w.Code != 413 {
		t.Fatalf("too large: %d %s", w.Code, w.Body)
	}

	// The per-hook limit.
	limited := false
	for i := range 40 {
		if w := e.doHook(id, body, signed(SenderGitea, "FAKE-wrong", string(body), "r-"+strconv.Itoa(i), time.Now())); w.Code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("never rate-limited")
	}
}

func countEvents(t *testing.T, h *hubEnv, action string) int {
	t.Helper()
	page, err := audit.New(h.st).Query(t.Context(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range page.Events {
		if ev.Action == action {
			n++
		}
	}
	return n
}

// Making, listing, rotating and deleting hooks: the secret is answered once,
// never listed, and rotating it invalidates the old one at once. Switching
// the syncs to deploy on push needs a step-up.
func TestHookRoutes(t *testing.T) {
	e := newSyncEnv(t)
	y := e.follow(ModeAsk)
	req := map[string]any{"url": "https://git.lab.example/ops/stacks/", "branch": "main", "sender": SenderGitHub, "deploy_on_push": true}
	if w := e.do("POST", "/api/stacks/hooks", req); w.Code != 403 || !strings.Contains(w.Body.String(), "step_up_required") {
		t.Fatalf("deploy on push without a step-up: %d %s", w.Code, w.Body)
	}
	for _, bad := range []map[string]any{
		{"url": "https://sam:FAKE@git.lab.example/x", "branch": "main", "sender": "github"},
		{"url": remoteURL, "branch": "main", "sender": "bitbucket"},
	} {
		if w := e.do("POST", "/api/stacks/hooks", bad); w.Code != 422 {
			t.Fatalf("%v: %d %s", bad, w.Code, w.Body)
		}
	}
	e.actor.StepUpUntil = time.Now().Add(time.Minute)
	w := e.do("POST", "/api/stacks/hooks", req)
	created := decode[struct {
		Hook     HookView
		Secret   string
		Switched int
	}](t, w)
	if w.Code != 201 || created.Switched != 1 || len(created.Secret) != 64 || !strings.HasPrefix(created.Hook.Path, "/api/hooks/whk_") ||
		created.Hook.URL != "https://git.lab.example/ops/stacks" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if e.sync(y.ID).Mode != ModeDeploy {
		t.Fatal("the sync wasn't switched to deploy on push")
	}
	if _, ok := lastEvent(t, e.hubEnv, "settings.stacks.hook.create"); !ok {
		t.Fatal("create wasn't audited")
	}
	id := created.Hook.ID
	if w := e.do("POST", "/api/stacks/hooks", map[string]any{"url": remoteURL, "branch": "main", "sender": "gitea"}); w.Code != 409 {
		t.Fatalf("a second hook for the same remote: %d %s", w.Code, w.Body)
	}
	list := e.do("GET", "/api/stacks/hooks", nil)
	if strings.Contains(list.Body.String(), created.Secret) || !strings.Contains(list.Body.String(), id) {
		t.Fatalf("list: %s", list.Body)
	}
	got := e.do("GET", "/api/stacks/local/media/sync", nil)
	if v := decode[struct{ Sync *SyncView }](t, got); v.Sync == nil || v.Sync.Hook == nil || v.Sync.Hook.ID != id || strings.Contains(got.Body.String(), created.Secret) {
		t.Fatalf("the sync's hook: %s", got.Body)
	}

	// Rotate: the old secret stops working at once.
	w = e.do("POST", "/api/stacks/hooks/"+id+"/rotate", nil)
	rotated := decode[struct{ Secret string }](t, w)
	if w.Code != 200 || rotated.Secret == "" || rotated.Secret == created.Secret {
		t.Fatalf("rotate: %d %s", w.Code, w.Body)
	}
	body := `{"ref":"refs/heads/main"}`
	if w := e.doHook(id, []byte(body), signed(SenderGitHub, created.Secret, body, "d-1", time.Now())); w.Code != 401 {
		t.Fatalf("the old secret: %d", w.Code)
	}
	if w := e.doHook(id, []byte(body), signed(SenderGitHub, rotated.Secret, body, "d-2", time.Now())); w.Code != 202 {
		t.Fatalf("the new secret: %d %s", w.Code, w.Body)
	}
	if _, ok := lastEvent(t, e.hubEnv, "settings.stacks.hook.rotate"); !ok {
		t.Fatal("rotate wasn't audited")
	}

	// Delete: the hook answers 401 like any unknown one; its sync stays.
	if w := e.do("DELETE", "/api/stacks/hooks/"+id, nil); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := e.doHook(id, []byte(body), signed(SenderGitHub, rotated.Secret, body, "d-3", time.Now())); w.Code != 401 {
		t.Fatalf("a deleted hook: %d", w.Code)
	}
	if _, ok := lastEvent(t, e.hubEnv, "settings.stacks.hook.delete"); !ok {
		t.Fatal("delete wasn't audited")
	}
	if e.sync(y.ID).State != StateActive {
		t.Fatal("deleting the hook touched its sync")
	}
	if w := e.do("POST", "/api/stacks/hooks/"+id+"/rotate", nil); w.Code != 404 {
		t.Fatalf("rotate a deleted hook: %d", w.Code)
	}
}

// pushRef reads only the ref, from JSON or GitHub's form encoding.
func TestPushRef(t *testing.T) {
	for _, c := range []struct{ ct, body, want string }{
		{"application/json", `{"ref":"refs/heads/main","commits":[]}`, "refs/heads/main"},
		{"application/json; charset=utf-8", `{"zen":"ping"}`, ""},
		{"application/x-www-form-urlencoded", "payload=" + `%7B%22ref%22%3A%22refs%2Fheads%2Fdev%22%7D`, "refs/heads/dev"},
		{"application/json", `not json`, ""},
	} {
		if got := pushRef(c.ct, []byte(c.body)); got != c.want {
			t.Errorf("%s %q → %q", c.ct, c.body, got)
		}
	}
}

// FuzzHookVerify: no header or body makes verification panic, a request
// signed with the secret verifies, and nothing verifies without it.
func FuzzHookVerify(f *testing.F) {
	f.Add(uint8(0), "sha256=00", "d-1", "1793361600", "tok", []byte(`{"ref":"refs/heads/main"}`))
	f.Add(uint8(3), "", "", "", "FAKE-hook-secret-0123456789abcdef", []byte(""))
	f.Add(uint8(4), "sha256=zz", "", "-1", "", []byte("payload=%7B"))
	f.Add(uint8(1), strings.Repeat("a", 64), strings.Repeat("d", 200), "99999999999999999999", "", []byte{0xff, 0xfe})
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, which uint8, sig, delivery, ts, token string, body []byte) {
		sender := Senders[int(which)%len(Senders)]
		hdr := http.Header{}
		for _, k := range []string{"X-Hub-Signature-256", "X-Gitea-Signature", "X-Forgejo-Signature", "X-Chadash-Signature"} {
			hdr.Set(k, sig)
		}
		for _, k := range []string{"X-GitHub-Delivery", "X-Gitea-Delivery", "X-Forgejo-Delivery"} {
			hdr.Set(k, delivery)
		}
		hdr.Set("X-Chadash-Timestamp", ts)
		hdr.Set("X-Gitlab-Token", token)
		if _, err := verifyHook(sender, []byte(hookSecret), hdr, body, now); err == nil && token != hookSecret {
			// Without the secret, only a forged HMAC could pass; the fuzzer
			// can't make one.
			if sender == SenderGitLab || !strings.Contains(sig, hexMAC(hookSecret, string(body))) && !strings.Contains(sig, hexMAC(hookSecret, ts+"."+string(body))) {
				t.Fatalf("%s verified without the secret: %q", sender, sig)
			}
		}
		good := signed(sender, hookSecret, string(body), "d-fuzz", now)
		if _, err := verifyHook(sender, []byte(hookSecret), good, body, now); err != nil {
			t.Fatalf("%s: a signed request refused: %v", sender, err)
		}
		_ = pushRef("application/json", body)
		_ = pushRef("application/x-www-form-urlencoded", body)
	})
}
