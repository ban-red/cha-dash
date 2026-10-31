package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/stackproto"
)

// fakeRemote is a git remote as the node would report it: a tip per URL
// and branch, and files per commit. It answers stack.remote.check and
// stack.fetch in place of the node module (which TestRemote* in
// internal/node/stacks exercise against a real repository).
type fakeRemote struct {
	mu      sync.Mutex
	tips    map[string]string // url + " " + branch → sha
	files   map[string]string // sha + " " + path → content
	hostKey string
	checks  []string // node/url/branch of each check
	fetches int
	fail    error
}

func (f *fakeRemote) push(url, branch, sha, path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tips[url+" "+branch] = sha
	f.files[sha+" "+path] = content
}

func (f *fakeRemote) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.checks), f.fetches
}

func (f *fakeRemote) answer(node, verb string, raw json.RawMessage) (json.RawMessage, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch verb {
	case stackproto.VerbRemoteCheck:
		var req stackproto.RemoteCheckRequest
		_ = json.Unmarshal(raw, &req)
		f.checks = append(f.checks, node+" "+req.URL+" "+req.Branch)
		if f.fail != nil {
			return nil, true, f.fail
		}
		sha, ok := f.tips[req.URL+" "+req.Branch]
		if !ok {
			return nil, true, stackproto.Errorf(stackproto.CodeNotFound, "There's no branch %s.", req.Branch)
		}
		out, _ := json.Marshal(stackproto.RemoteCheckResponse{SHA: sha, HostKey: f.hostKey})
		return out, true, nil
	case stackproto.VerbFetch:
		var req stackproto.FetchRequest
		_ = json.Unmarshal(raw, &req)
		f.fetches++
		sha := f.tips[req.URL+" "+req.Branch]
		content, ok := f.files[sha+" "+req.Path]
		if !ok {
			return nil, true, stackproto.Errorf(stackproto.CodeNotFound, "There's no %s at %s.", req.Path, sha)
		}
		out, _ := json.Marshal(stackproto.FetchResponse{SHA: sha, Content: content, Subject: "Update " + req.Path, Author: "Dev"})
		return out, true, nil
	}
	return nil, false, nil
}

// syncEnv is a hub with a syncer, a fake remote and two admins.
type syncEnv struct {
	*hubEnv
	y      *Syncer
	remote *fakeRemote
	umu    sync.Mutex
	users  map[string]auth.UserInfo
	actor  authz.Actor
}

const (
	remoteURL = "https://git.lab.example/ops/stacks.git"
	sha1      = "1111111111111111111111111111111111111111"
	sha2      = "2222222222222222222222222222222222222222"
	sha3      = "3333333333333333333333333333333333333333"
)

func newSyncEnv(t *testing.T) *syncEnv {
	t.Helper()
	h := newHub(t)
	e := &syncEnv{hubEnv: h, remote: &fakeRemote{tips: map[string]string{}, files: map[string]string{}}, users: map[string]auth.UserInfo{
		"u_sam": {ID: "u_sam", Username: "sam", Role: auth.RoleAdmin},
		"u_kim": {ID: "u_kim", Username: "kim", Role: auth.RoleAdmin},
	}}
	for _, u := range e.users {
		if _, err := h.st.Writer().ExecContext(t.Context(), `INSERT INTO users (id, username, role, created_at, updated_at) VALUES (?, ?, ?, 0, 0)`, u.ID, u.Username, u.Role); err != nil {
			t.Fatal(err)
		}
	}
	h.caller.fake = e.remote.answer
	var err error
	e.y, err = NewSyncer(h.svc, SyncerOptions{Engine: h.eng, Users: e.user, Tick: time.Hour, Jitter: func() float64 { return 0.999 }})
	if err != nil {
		t.Fatal(err)
	}
	e.actor = authz.Actor{Kind: authz.KindUser, ID: "u_sam", Name: "sam", Role: auth.RoleAdmin, Session: "s_1"}
	h.svc.actorOf = func(*http.Request) (authz.Actor, bool) { return e.actor, true }
	h.svc.Audit = &authz.Recorder{Log: audit.New(h.st)}
	if w := h.do("PUT", "/api/stacks/secrets/db_password", map[string]string{"value": secretValue}); w.Code != 204 {
		t.Fatalf("set secret: %d %s", w.Code, w.Body)
	}
	return e
}

func (e *syncEnv) user(_ context.Context, id string) (auth.UserInfo, error) {
	e.umu.Lock()
	defer e.umu.Unlock()
	u, ok := e.users[id]
	if !ok {
		return auth.UserInfo{}, auth.ErrUserNotFound
	}
	return u, nil
}

// follow adds a sync of media on the local node, as sam.
func (e *syncEnv) follow(mode string, allow ...string) *Sync {
	e.t.Helper()
	y := &Sync{ID: newID("syn_"), Node: "local", Stack: "media", URL: remoteURL, Branch: "main", Path: "media/compose.yaml",
		AuthKind: stackproto.AuthNone, Mode: mode, PollSeconds: 300, SecretsAllow: append([]string{}, allow...), OwnerID: "u_sam", State: StateActive}
	if err := e.svc.insertSync(e.t.Context(), y); err != nil {
		e.t.Fatal(err)
	}
	return y
}

func (e *syncEnv) sync(id string) *Sync {
	e.t.Helper()
	y, err := e.svc.syncByID(e.t.Context(), id)
	if err != nil || y == nil {
		e.t.Fatalf("sync %s: %v", id, err)
	}
	return y
}

// waitSync waits until cond holds for the sync (a deploy runs in the
// background).
func (e *syncEnv) waitSync(id string, what string, cond func(*Sync) bool) *Sync {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if y := e.sync(id); cond(y) {
			return y
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.t.Fatalf("sync %s: %s never happened: %+v", id, what, e.sync(id))
	return nil
}

// approvals lists the approvals by state, oldest first.
func (e *syncEnv) approvals() []string {
	e.t.Helper()
	rows, err := e.st.Reader().QueryContext(e.t.Context(), `SELECT state, reason FROM approvals ORDER BY created_at, rowid`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var state, reason string
		if err := rows.Scan(&state, &reason); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, state+" "+reason)
	}
	return out
}

func (e *syncEnv) item(y *Sync) (incident, bool) {
	raw := e.inbox.Item(incidentsColl, syncItemID(y))
	var in incident
	return in, raw != nil && json.Unmarshal(raw, &in) == nil
}

func plainGood(image string) string {
	return strings.Replace(good, "nginx:1.27", image, 1)
}

// The table of states the plan lists: a new tip deploys (deploy on push),
// the same tip does nothing, a rolled-back tip is rejected and never
// fetched again, and a newer one deploys.
func TestSyncDeploysOnPushAndRejectsARollback(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	y := e.follow(ModeDeploy, "db_password")

	// New tip: fetched, drafted from git, planned and run as the sync.
	e.remote.push(remoteURL, "main", sha1, "media/compose.yaml", good)
	e.y.runOnce(ctx, y.ID)
	got := e.waitSync(y.ID, "deploy c1", func(s *Sync) bool { return s.LastDeployed == sha1 })
	if got.LastSeen != sha1 || len(got.Rejected) != 0 || got.LastError != "" {
		t.Fatalf("after c1: %+v", got)
	}
	d := decode[StackDetail](t, e.do("GET", "/api/stacks/local/media", nil))
	if d.Draft != nil || len(d.History) != 1 || !strings.Contains(d.History[0].Subject, "by git sync for sam via sync") {
		t.Fatalf("history %+v draft %+v", d.History, d.Draft)
	}
	if d.Sync == nil || d.Sync.LastDeployed != sha1 || d.Sync.Owner == nil || d.Sync.Owner.Username != "sam" {
		t.Fatalf("detail's sync %+v", d.Sync)
	}
	if !slices.ContainsFunc(e.caller.sent, func(s string) bool {
		return strings.HasPrefix(s, stackproto.VerbDeploy+" ") && strings.Contains(s, `"source":"`+remoteURL+`@`+sha1+`"`)
	}) {
		t.Error("the deploy didn't carry its source")
	}
	if ev, ok := lastEvent(t, e.hubEnv, "stack.sync.fetch"); !ok || ev.Actor.Kind != "sync" || ev.Actor.OnBehalfOf != "u_sam" || !strings.Contains(string(ev.Params), sha1) {
		t.Fatalf("fetch audit %+v", ev)
	}

	// Same tip: checked, not fetched.
	_, fetches := e.remote.counts()
	e.y.runOnce(ctx, y.ID)
	if _, f := e.remote.counts(); f != fetches {
		t.Fatal("the same tip was fetched again")
	}

	// A tip whose deploy fails its gate: rejected, and the Inbox says so.
	e.remote.push(remoteURL, "main", sha2, "media/compose.yaml", plainGood("nginx-unhealthy:1.28"))
	e.y.runOnce(ctx, y.ID)
	got = e.waitSync(y.ID, "reject c2", func(s *Sync) bool { return s.isRejected(sha2) })
	if got.LastDeployed != sha1 {
		t.Fatalf("after c2: %+v", got)
	}
	in, ok := e.item(got)
	if !ok || in.Status != "open" || in.Severity != "attention" ||
		!strings.Contains(in.Summary, "Commit 2222222 failed its health gate; 1111111 is still running.") {
		t.Fatalf("inbox %+v", in)
	}

	// The rejected tip again: not fetched.
	_, fetches = e.remote.counts()
	e.y.runOnce(ctx, y.ID)
	if _, f := e.remote.counts(); f != fetches {
		t.Fatal("a rejected tip was fetched again")
	}

	// A newer commit deploys, and resolves the item.
	e.remote.push(remoteURL, "main", sha3, "media/compose.yaml", plainGood("nginx:1.29"))
	e.y.runOnce(ctx, y.ID)
	e.waitSync(y.ID, "deploy c3", func(s *Sync) bool { return s.LastDeployed == sha3 })
	if in, _ := e.item(got); in.Status != "resolved" {
		t.Fatalf("inbox after c3: %+v", in)
	}
}

func lastEvent(t *testing.T, h *hubEnv, action string) (audit.Event, bool) {
	t.Helper()
	page, err := audit.New(h.st).Query(t.Context(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range page.Events {
		if ev.Action == action {
			return ev, true
		}
	}
	return audit.Event{}, false
}

// Draft and ask: the tip becomes a git draft and waits for a person; a
// newer tip withdraws the older approval. Deploy on push still asks when the
// file references a secret outside the allowlist.
func TestSyncAsks(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	y := e.follow(ModeAsk, "db_password")
	e.remote.push(remoteURL, "main", sha1, "media/compose.yaml", good)
	e.y.runOnce(ctx, y.ID)
	if got := e.approvals(); len(got) != 1 || !strings.HasPrefix(got[0], action.ApprovalAwaiting) {
		t.Fatalf("approvals %q", got)
	}
	// The approval says which commit it is, by its subject.
	var note string
	if err := e.st.Reader().QueryRowContext(ctx, `SELECT json_extract(p.document, '$.note') FROM approvals a JOIN plans p ON p.id = a.plan_id`).Scan(&note); err != nil ||
		note != "Commit "+short(sha1)+": Update media/compose.yaml" {
		t.Fatalf("note %q %v", note, err)
	}
	d, _ := e.svc.draft(ctx, "local", "media")
	if d == nil || d.Source != "git:"+sha1 || d.UpdatedBy != "git sync" {
		t.Fatalf("draft %+v", d)
	}
	if s := e.sync(y.ID); s.LastSeen != sha1 || s.LastDeployed != "" {
		t.Fatalf("sync %+v", s)
	}

	e.remote.push(remoteURL, "main", sha2, "media/compose.yaml", plainGood("nginx:1.28"))
	e.y.runOnce(ctx, y.ID)
	got := e.approvals()
	if len(got) != 2 || !strings.HasPrefix(got[0], action.ApprovalExpired+" A newer commit, 2222222, replaced it.") || !strings.HasPrefix(got[1], action.ApprovalAwaiting) {
		t.Fatalf("approvals %q", got)
	}

	// Deploy on push with db_password outside the allowlist: it asks too.
	e2 := newSyncEnv(t)
	y2 := e2.follow(ModeDeploy)
	e2.remote.push(remoteURL, "main", sha1, "media/compose.yaml", good)
	e2.y.runOnce(ctx, y2.ID)
	if got := e2.approvals(); len(got) != 1 || !strings.HasPrefix(got[0], action.ApprovalAwaiting) {
		t.Fatalf("a secret outside the allowlist must ask: %q", got)
	}
	s2 := e2.sync(y2.ID)
	if in, ok := e2.item(s2); !ok || in.Status != "open" || !strings.Contains(in.Title, "waits for approval") {
		t.Fatalf("asks item %+v", in)
	}
	// Denying it closes the item: nothing waits any more.
	var id string
	if err := e2.st.Reader().QueryRowContext(ctx, `SELECT id FROM approvals`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.eng.Deny(ctx, action.Actor{Kind: action.KindUser, ID: "u_sam", Name: "sam"}, id, action.DenyRequest{}); err != nil {
		t.Fatal(err)
	}
	if in, _ := e2.item(s2); in.Status != "resolved" {
		t.Fatalf("after a denial the asks item is %q", in.Status)
	}
}

// The gate: drift waits (and retries), a held sync doesn't check, and an
// owner who can't act any more pauses it.
func TestSyncGate(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	y := e.follow(ModeDeploy, "db_password")
	e.remote.push(remoteURL, "main", sha1, "media/compose.yaml", good)
	e.y.runOnce(ctx, y.ID)
	e.waitSync(y.ID, "deploy c1", func(s *Sync) bool { return s.LastDeployed == sha1 })

	// Drift: the file on disk was edited.
	if err := os.WriteFile(filepath.Join(e.root, "media", "compose.yaml"), []byte(good+"# edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.remote.push(remoteURL, "main", sha2, "media/compose.yaml", plainGood("nginx:1.28"))
	e.y.runOnce(ctx, y.ID)
	got := e.sync(y.ID)
	if got.LastSeen != sha1 || !strings.Contains(got.LastError, "edited on disk") {
		t.Fatalf("drift: %+v", got)
	}
	if d, _ := e.svc.draft(ctx, "local", "media"); d != nil {
		t.Fatalf("drift made a draft: %+v", d)
	}
	if in, ok := e.item(got); !ok || in.Status != "open" || !strings.Contains(in.Title, "edited on disk") {
		t.Fatalf("inbox %+v", in)
	}
	if err := os.WriteFile(filepath.Join(e.root, "media", "compose.yaml"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}

	// Held: nothing is checked.
	if _, err := e.svc.patchSync(ctx, y.ID, func(s *Sync) { s.State, s.StateReason = StateHeld, "Held for the test." }); err != nil {
		t.Fatal(err)
	}
	checks, _ := e.remote.counts()
	e.y.runOnce(ctx, y.ID)
	if c, _ := e.remote.counts(); c != checks {
		t.Fatal("a held sync checked its remote")
	}

	// Active again, but the owner was demoted: paused, and the Inbox says so.
	if _, err := e.svc.patchSync(ctx, y.ID, func(s *Sync) { s.State, s.StateReason = StateActive, "" }); err != nil {
		t.Fatal(err)
	}
	e.umu.Lock()
	e.users["u_sam"] = auth.UserInfo{ID: "u_sam", Username: "sam", Role: auth.RoleOperator}
	e.umu.Unlock()
	e.y.runOnce(ctx, y.ID)
	got = e.sync(y.ID)
	if got.State != StatePaused || !strings.Contains(got.StateReason, "no longer an admin") {
		t.Fatalf("demoted owner: %+v", got)
	}
	if in, _ := e.item(got); in.Status != "open" || !strings.Contains(in.Title, "paused") {
		t.Fatalf("inbox %+v", in)
	}

	// OwnerChanged pauses another of their syncs at once; a deleted owner
	// pauses too.
	other := &Sync{ID: newID("syn_"), Node: "local", Stack: "other", URL: remoteURL, Branch: "main", Path: "other/compose.yaml",
		AuthKind: stackproto.AuthNone, Mode: ModeAsk, PollSeconds: 300, SecretsAllow: []string{}, OwnerID: "u_sam", State: StateActive}
	if err := e.svc.insertSync(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer().ExecContext(ctx, `UPDATE stack_syncs SET stack = 'other' WHERE id = ?`, other.ID); err != nil {
		t.Fatal(err)
	}
	e.y.OwnerChanged(ctx, "u_sam")
	if s := e.sync(other.ID); s.State != StatePaused {
		t.Fatalf("OwnerChanged: %+v", s)
	}
	_, _ = e.svc.patchSync(ctx, other.ID, func(s *Sync) { s.State, s.OwnerID = StateActive, "" })
	e.y.runOnce(ctx, other.ID)
	if s := e.sync(other.ID); s.State != StatePaused || !strings.Contains(s.StateReason, "deleted") {
		t.Fatalf("deleted owner: %+v", s)
	}
}

// A rollback from the web holds the sync at that commit until it's resumed.
func TestRollbackHoldsTheSync(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	y := e.follow(ModeDeploy, "db_password")
	e.remote.push(remoteURL, "main", sha1, "media/compose.yaml", good)
	e.y.runOnce(ctx, y.ID)
	e.waitSync(y.ID, "deploy c1", func(s *Sync) bool { return s.LastDeployed == sha1 })
	e.remote.push(remoteURL, "main", sha2, "media/compose.yaml", plainGood("nginx:1.28"))
	e.y.runOnce(ctx, y.ID)
	e.waitSync(y.ID, "deploy c2", func(s *Sync) bool { return s.LastDeployed == sha2 })

	d := decode[StackDetail](t, e.do("GET", "/api/stacks/local/media", nil))
	first := d.History[len(d.History)-1].Commit
	_, task := e.run(ActionRollback, Target("local", "media"), `{"commit":"`+first+`"}`)
	if task.State != action.StateSucceeded {
		t.Fatalf("rollback: %+v", task.Error)
	}
	got := e.sync(y.ID)
	if got.State != StateHeld || !strings.Contains(got.StateReason, "Held at "+first[:7]+" after sam rolled media back") {
		t.Fatalf("after a rollback: %+v", got)
	}
	// Resuming follows the branch again (its tip is compared afresh).
	w := e.do("POST", "/api/stacks/local/media/sync/resume", nil)
	v := decode[struct{ Sync SyncView }](t, w)
	if w.Code != 200 || v.Sync.State != StateActive || v.Sync.LastSeen != "" {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
}

// A check that keeps failing records the error, and after three polls in a
// row opens a hint; one that works clears it.
func TestSyncFetchFailing(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	y := e.follow(ModeAsk)
	e.remote.fail = stackproto.Errorf(stackproto.CodeInternal, "The remote didn't answer.")
	for range failingAfter + 1 {
		e.y.runOnce(ctx, y.ID)
	}
	got := e.sync(y.ID)
	if got.LastError != "The remote didn't answer." {
		t.Fatalf("%+v", got)
	}
	if in, ok := e.item(got); !ok || in.Severity != "info" || !strings.Contains(in.Title, "can't reach") {
		t.Fatalf("inbox %+v", in)
	}
	e.remote.fail = nil
	e.remote.push(remoteURL, "main", sha1, "media/compose.yaml", good)
	e.y.runOnce(ctx, y.ID)
	if got := e.sync(y.ID); got.LastError != "" {
		t.Fatalf("%+v", got)
	}
}

func TestCommitNote(t *testing.T) {
	long := strings.Repeat("é", maxSubject+5)
	for _, c := range []struct{ subject, want string }{
		{"Raise the retries", "Commit 1234567: Raise the retries"},
		{"  spaced\tout\r\n  ", "Commit 1234567: spaced out"},
		{"", "Commit 1234567"},
		{long, "Commit 1234567: " + strings.Repeat("é", maxSubject-1) + "…"},
	} {
		if got := commitNote("1234567890abcdef", c.subject); got != c.want {
			t.Errorf("commitNote(%q) = %q, want %q", c.subject, got, c.want)
		}
	}
}
