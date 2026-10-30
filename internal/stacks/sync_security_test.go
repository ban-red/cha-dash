package stacks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/stackproto"
)

// "Test remote" sends the saved credential only to the remote it was saved
// for; a token never goes over http://, in a test or a save; and a new URL
// can't carry the old credential with it.
func TestSyncCredentialStaysWithItsRemote(t *testing.T) {
	e := newSyncEnv(t)
	base := "/api/stacks/local/media/sync"
	const token = "FAKE-TOKEN-SAVED-0001"
	follow := map[string]any{"url": remoteURL, "branch": "main", "mode": "ask",
		"auth": map[string]any{"kind": "https", "username": "sam", "secret": token}}
	if w := e.do("PUT", base, follow); w.Code != 201 {
		t.Fatalf("follow: %d %s", w.Code, w.Body)
	}
	e.remote.push(remoteURL, "main", sha1, "media/compose.yaml", good)

	// Another URL, with or without the kind: refused before the node.
	for _, body := range []map[string]any{
		{"url": "https://elsewhere.example/ops/stacks.git"},
		{"url": "https://elsewhere.example/ops/stacks.git", "auth": map[string]any{"kind": "https"}},
		{"url": "https://elsewhere.example/ops/stacks.git", "auth": map[string]any{"kind": "https", "username": "sam"}},
	} {
		if w := e.do("POST", base+"/check", body); w.Code != 422 || !strings.Contains(w.Body.String(), "only sent to git.lab.example") {
			t.Fatalf("check %v: %d %s", body, w.Code, w.Body)
		}
	}
	if len(e.sentWith("elsewhere.example")) > 0 || len(e.sentWith(token)) > 0 {
		t.Fatal("the saved credential went to the node for another remote")
	}
	// The same remote, respelled: the saved credential is used.
	if w := e.do("POST", base+"/check", map[string]any{"url": "HTTPS://Git.Lab.Example:443/ops/stacks/"}); w.Code != 404 && w.Code != 200 {
		t.Fatalf("check the same remote: %d %s", w.Code, w.Body)
	}
	if len(e.sentWith(token)) == 0 {
		t.Fatal("the same remote didn't get the saved credential")
	}
	// Another URL with its own token is fine.
	if w := e.do("POST", base+"/check", map[string]any{"url": "https://elsewhere.example/ops/stacks.git",
		"auth": map[string]any{"kind": "https", "secret": "FAKE-TOKEN-OTHER-0002"}}); w.Code == 422 {
		t.Fatalf("check another remote with its own token: %d %s", w.Code, w.Body)
	}

	// A token over http://: refused in a check and in a save.
	plain := "http://git.lab.example/ops/stacks.git"
	if w := e.do("POST", base+"/check", map[string]any{"url": plain, "auth": map[string]any{"kind": "https", "secret": "FAKE-TOKEN-PLAIN-03"}}); w.Code != 422 || !strings.Contains(w.Body.String(), "https://") {
		t.Fatalf("check a token over http: %d %s", w.Code, w.Body)
	}
	e.actor.StepUpUntil = time.Now().Add(time.Minute)
	if w := e.do("PUT", base, map[string]any{"url": plain, "branch": "main", "auth": map[string]any{"kind": "https", "secret": "FAKE-TOKEN-PLAIN-03"}}); w.Code != 422 {
		t.Fatalf("save a token over http: %d %s", w.Code, w.Body)
	}
	if len(e.sentWith("FAKE-TOKEN-PLAIN-03")) > 0 {
		t.Fatal("a token went out for an http:// remote")
	}

	// A new URL with the secret left out: refused, even with a step-up.
	moved := map[string]any{"url": "https://elsewhere.example/ops/stacks.git", "branch": "main", "mode": "ask",
		"auth": map[string]any{"kind": "https", "username": "sam"}}
	if w := e.do("PUT", base, moved); w.Code != 422 || !strings.Contains(w.Body.String(), "Give it again") {
		t.Fatalf("a new URL with the old credential: %d %s", w.Code, w.Body)
	}
	y, _ := e.svc.syncByStack(t.Context(), "local", "media")
	if y.URL != remoteURL {
		t.Fatalf("the refused save moved the sync: %+v", y)
	}
	moved["auth"] = map[string]any{"kind": "https", "username": "sam", "secret": "FAKE-TOKEN-NEW-0004"}
	if w := e.do("PUT", base, moved); w.Code != 200 {
		t.Fatalf("a new URL with its own token: %d %s", w.Code, w.Body)
	}
	if c, _ := e.svc.credentialOf(t.Context(), y.ID); c.Secret != "FAKE-TOKEN-NEW-0004" {
		t.Fatal("the new token wasn't saved")
	}
	// Directly, too: saveCredential never carries a secret to another URL.
	if err := e.svc.saveCredential(t.Context(), y.ID, "https://third.example/x.git", e.sync(y.ID), syncAuthReq{Kind: "https", Username: "sam"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.svc.credentialOf(t.Context(), y.ID); c.Secret != "" {
		t.Fatal("saveCredential carried the secret to another URL")
	}
}

// While a sync deploys on push, pointing it at other content needs a
// step-up: the branch, the path, the credential, a newly allowed secret.
// Turning it into or out of deploy mode counts both ways. Rejected commits
// survive a move.
func TestSyncRetargetNeedsAStepUp(t *testing.T) {
	e := newSyncEnv(t)
	base := "/api/stacks/local/media/sync"
	body := func(mod func(m map[string]any)) map[string]any {
		m := map[string]any{"url": remoteURL, "branch": "main", "path": "media/compose.yaml", "mode": "deploy",
			"secrets_allow": []string{"db_password"}, "auth": map[string]any{"kind": "https", "username": "sam", "secret": "FAKE-TOKEN-A-0001"}}
		if mod != nil {
			mod(m)
		}
		return m
	}
	e.actor.StepUpUntil = time.Now().Add(time.Minute)
	if w := e.do("PUT", base, body(nil)); w.Code != 201 {
		t.Fatalf("follow: %d %s", w.Code, w.Body)
	}
	e.actor.StepUpUntil = time.Time{}
	keep := func(m map[string]any) { m["auth"] = map[string]any{"kind": "https", "username": "sam"} }

	for name, mod := range map[string]func(m map[string]any){
		"branch": func(m map[string]any) { keep(m); m["branch"] = "release" },
		"path":   func(m map[string]any) { keep(m); m["path"] = "other/compose.yaml" },
		"secret": func(m map[string]any) {
			m["auth"] = map[string]any{"kind": "https", "username": "sam", "secret": "FAKE-TOKEN-B-0002"}
		},
		"username":      func(m map[string]any) { m["auth"] = map[string]any{"kind": "https", "username": "kim"} },
		"no credential": func(m map[string]any) { m["auth"] = map[string]any{"kind": "none"} },
		"secrets_allow": func(m map[string]any) { keep(m); m["secrets_allow"] = []string{"db_password", "api_key"} },
		"to ask, moved": func(m map[string]any) { keep(m); m["mode"] = "ask"; m["branch"] = "release" },
	} {
		w := e.do("PUT", base, body(mod))
		if w.Code != 403 || !strings.Contains(w.Body.String(), "step_up_required") {
			t.Errorf("%s without a step-up: %d %s", name, w.Code, w.Body)
		}
	}
	if ev, _ := lastEvent(t, e.hubEnv, "settings.stacks.sync.update"); ev.Result != "refused" {
		t.Fatalf("a refused retarget isn't audited: %+v", ev)
	}
	// What doesn't point it elsewhere needs none.
	for _, c := range []struct {
		name string
		mod  func(m map[string]any)
	}{
		{"same", keep},
		{"poll", func(m map[string]any) { keep(m); m["poll_seconds"] = 600 }},
		{"drop an allowed", func(m map[string]any) { keep(m); m["secrets_allow"] = []string{} }},
		{"to ask", func(m map[string]any) { keep(m); m["secrets_allow"] = []string{}; m["mode"] = "ask" }},
	} {
		if w := e.do("PUT", base, body(c.mod)); w.Code != 200 {
			t.Fatalf("%s: %d %s", c.name, w.Code, w.Body)
		}
	}
	// In ask mode, the branch moves freely.
	if w := e.do("PUT", base, body(func(m map[string]any) { keep(m); m["mode"] = "ask"; m["branch"] = "release" })); w.Code != 200 {
		t.Fatalf("ask mode, another branch: %d %s", w.Code, w.Body)
	}

	// Rejected commits stay rejected across a move.
	y, _ := e.svc.syncByStack(t.Context(), "local", "media")
	if _, err := e.svc.patchSync(t.Context(), y.ID, func(s *Sync) { s.reject(sha2); s.LastSeen = sha2 }); err != nil {
		t.Fatal(err)
	}
	if w := e.do("PUT", base, body(func(m map[string]any) { keep(m); m["mode"] = "ask"; m["branch"] = "main" })); w.Code != 200 {
		t.Fatalf("back to main: %d %s", w.Code, w.Body)
	}
	if got := e.sync(y.ID); !got.isRejected(sha2) || got.LastSeen != "" {
		t.Fatalf("after a move: %+v", got)
	}
}

// Deploy on push asks first when a commit amounts to root on the node's
// host, and the Inbox says why.
func TestSyncAsksBeforeRootEquivalent(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	y := e.follow(ModeDeploy, "db_password")
	root := strings.Replace(good, "    image: nginx:1.27\n", "    image: nginx:1.27\n    privileged: true\n", 1)
	if root == good {
		t.Fatal("the test's file didn't change")
	}
	e.remote.push(remoteURL, "main", sha1, "media/compose.yaml", root)
	e.y.runOnce(ctx, y.ID)
	if got := e.approvals(); len(got) != 1 || !strings.HasPrefix(got[0], action.ApprovalAwaiting) {
		t.Fatalf("a privileged commit must ask: %q", got)
	}
	got := e.sync(y.ID)
	if got.LastDeployed != "" || got.LastSeen != sha1 {
		t.Fatalf("sync %+v", got)
	}
	in, ok := e.item(got)
	if !ok || in.Status != "open" || !strings.Contains(in.Summary, "root on local") || !strings.Contains(strings.ToLower(in.Summary), "privileged") {
		t.Fatalf("inbox %+v", in)
	}

	// A plain commit still deploys on its own.
	e.remote.push(remoteURL, "main", sha2, "media/compose.yaml", plainGood("nginx:1.28"))
	e.y.runOnce(ctx, y.ID)
	e.waitSync(y.ID, "deploy c2", func(s *Sync) bool { return s.LastDeployed == sha2 })
}

// The gate checks the commit fetched, not only the one checked, and
// remembers the last 20 rejected commits.
func TestSyncRejectsTheFetchedCommit(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	y := e.follow(ModeDeploy, "db_password")
	for i := range 25 {
		y.reject(fmt.Sprintf("%040d", i+100))
	}
	y.reject(sha2)
	if len(y.Rejected) != maxRejected || y.Rejected[0] != sha2 {
		t.Fatalf("rejected %v", y.Rejected)
	}
	y.reject(sha3)
	y.reject(sha2) // again: moves to the front, not twice
	if y.Rejected[0] != sha2 || y.Rejected[1] != sha3 || len(y.Rejected) != maxRejected {
		t.Fatalf("rejected %v", y.Rejected)
	}
	if err := e.svc.updateSync(ctx, y); err != nil {
		t.Fatal(err)
	}
	var col string
	if err := e.st.Reader().QueryRowContext(ctx, `SELECT rejected_sha FROM stack_syncs WHERE id = ?`, y.ID).Scan(&col); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(col, sha2+" "+sha3+" ") || len(strings.Fields(col)) != maxRejected {
		t.Fatalf("rejected_sha %q", col)
	}
	v := decode[struct{ Sync SyncView }](t, e.do("GET", "/api/stacks/local/media/sync", nil)).Sync
	if v.Rejected != sha2 || len(v.RejectedSHAs) != maxRejected {
		t.Fatalf("view %+v", v)
	}

	// The check said sha1, but the fetch got sha2 (a push in between):
	// refused, no draft, nothing planned.
	owner, _ := e.user(ctx, "u_sam")
	e.y.process(ctx, e.sync(y.ID), owner, stackproto.FetchResponse{SHA: sha2, Content: good})
	if d, _ := e.svc.draft(ctx, "local", "media"); d != nil {
		t.Fatalf("a rejected commit became a draft: %+v", d)
	}
	if got := e.approvals(); len(got) != 0 {
		t.Fatalf("approvals %q", got)
	}
	if got := e.sync(y.ID); got.LastSeen != sha2 {
		t.Fatalf("sync %+v", got)
	}
}

// lockedEngine refuses as the engine does when something else runs on the
// stack.
type lockedEngine struct {
	SyncEngine
	code string
}

func (l lockedEngine) ApproveOrSubmit(context.Context, action.Actor, string, action.ApproveRequest, string) (action.Task, *action.Approval, error) {
	return action.Task{}, nil, &action.Refusal{Status: 409, Code: l.code, Message: "Something else is already running on this. Try again when it's done."}
}

// A refusal that passes (locked, unavailable) doesn't mark the tip seen:
// the next poll deploys it.
func TestSyncRetriesATransientRefusal(t *testing.T) {
	for _, code := range []string{"locked", "unavailable"} {
		t.Run(code, func(t *testing.T) {
			e := newSyncEnv(t)
			ctx := t.Context()
			y := e.follow(ModeDeploy, "db_password")
			e.remote.push(remoteURL, "main", sha1, "media/compose.yaml", good)
			e.y.eng = lockedEngine{SyncEngine: e.eng, code: code}
			e.y.runOnce(ctx, y.ID)
			got := e.sync(y.ID)
			if got.LastSeen != "" || !strings.Contains(got.LastError, "already running") {
				t.Fatalf("after a %s refusal: %+v", code, got)
			}
			e.y.eng = e.eng
			e.y.runOnce(ctx, y.ID)
			e.waitSync(y.ID, "deploy c1", func(s *Sync) bool { return s.LastDeployed == sha1 && s.LastError == "" })
		})
	}
}

// A hook made with deploy on push switches only the caller's own syncs and
// lists the others.
func TestHookSwitchesOnlyTheCallersSyncs(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	mine := e.follow(ModeAsk)
	theirs := &Sync{ID: newID("syn_"), Node: "local", Stack: "other", URL: remoteURL, Branch: "main", Path: "other/compose.yaml",
		AuthKind: "none", Mode: ModeAsk, PollSeconds: 300, SecretsAllow: []string{}, OwnerID: "u_kim", State: StateActive}
	if err := e.svc.insertSync(ctx, theirs); err != nil {
		t.Fatal(err)
	}
	e.actor.StepUpUntil = time.Now().Add(time.Minute)
	w := e.do("POST", "/api/stacks/hooks", map[string]any{"url": remoteURL, "branch": "main", "sender": SenderGitHub, "deploy_on_push": true})
	v := decode[struct {
		Switched    int        `json:"switched"`
		NotSwitched []HookSync `json:"not_switched"`
	}](t, w)
	if w.Code != 201 || v.Switched != 1 || len(v.NotSwitched) != 1 || v.NotSwitched[0].Stack != "other" || v.NotSwitched[0].Mode != ModeAsk {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if e.sync(mine.ID).Mode != ModeDeploy || e.sync(theirs.ID).Mode != ModeAsk {
		t.Fatal("switched the wrong syncs")
	}
}

// The per-hook limiter counts only hooks that exist, after the lookup, and
// refusals are audited as anonymous.
func TestHookLimiterAndRefusalActor(t *testing.T) {
	e := newSyncEnv(t)
	id, secret := e.makeHook(remoteURL, "main", SenderGitea)
	body := []byte(`{"ref":"refs/heads/main"}`)
	unknown := "whk_" + strings.Repeat("b", 26)
	for i := range 25 {
		if w := e.doHook(unknown, body, nil); w.Code != 401 {
			t.Fatalf("unknown hook, request %d: %d %s", i, w.Code, w.Body)
		}
	}
	if e.svc.hooks.perHok.Wait(unknown) != 0 {
		t.Fatal("a made-up hook ID has a bucket")
	}
	_ = e.doHook(id, body, signed(SenderGitea, "FAKE-wrong-secret-00000000000000", string(body), "d-0", time.Now()))
	ev, ok := lastEvent(t, e.hubEnv, "stack.hook.refused")
	if !ok || ev.Actor.Kind != authz.KindAnonymous || ev.Actor.Name != "webhook" {
		t.Fatalf("refusal actor %+v", ev.Actor)
	}
	if w := e.doHook(id, body, signed(SenderGitea, secret, string(body), "d-1", time.Now())); w.Code != 202 {
		t.Fatalf("valid: %d %s", w.Code, w.Body)
	}
}

// OwnerChangedAsync runs on the syncer's WaitGroup, so Run waits for it.
func TestOwnerChangedIsTracked(t *testing.T) {
	e := newSyncEnv(t)
	y := e.follow(ModeAsk)
	if e.y.OwnerChangedAsync("u_sam") {
		t.Fatal("ran before the syncer started")
	}
	e.umu.Lock()
	e.users["u_sam"] = auth.UserInfo{ID: "u_sam", Username: "sam", Role: auth.RoleViewer}
	e.umu.Unlock()
	run, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { e.y.Run(run); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !e.y.OwnerChangedAsync("u_sam") {
		if time.Now().After(deadline) {
			t.Fatal("the syncer never started")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	<-done
	// Run returned only after the goroutine finished: the pause is written.
	if got := e.sync(y.ID); got.State != StatePaused {
		t.Fatalf("after shutdown: %+v", got)
	}
}
