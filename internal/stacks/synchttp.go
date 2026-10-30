package stacks

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/vault"
)

// The sync's routes (Settings → Stacks → Follow git). Admin+ by the authz
// table. A step-up (authz T3) is also needed to turn on deploy on push, to
// change the remote, to take over someone else's sync, and, while the sync
// deploys on push (before or after), to change its branch, path or
// credential, or add a name to secrets_allow.

// SyncView is a sync as the API shows it: never its credential, and the
// hook it's triggered by without the hook's secret.
type SyncView struct {
	ID           string        `json:"id"`
	Node         string        `json:"node"`
	Stack        string        `json:"stack"`
	URL          string        `json:"url"`
	Branch       string        `json:"branch"`
	Path         string        `json:"path"`
	Auth         SyncAuthView  `json:"auth"`
	HostKey      *string       `json:"host_key"`
	Mode         string        `json:"mode"`
	PollSeconds  int           `json:"poll_seconds"`
	SecretsAllow []string      `json:"secrets_allow"`
	Owner        *SyncOwner    `json:"owner"`
	State        string        `json:"state"`
	StateReason  string        `json:"state_reason"`
	LastSeen     string        `json:"last_seen_sha"`
	LastDeployed string        `json:"last_deployed_sha"`
	Rejected     string        `json:"rejected_sha"`  // the newest rejected commit
	RejectedSHAs []string      `json:"rejected_shas"` // every one remembered, newest first
	LastError    string        `json:"last_error"`
	Running      bool          `json:"running"`
	Hook         *SyncHookView `json:"hook"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

// SyncAuthView is how a sync signs in: the kind, the user, and whether a
// secret is saved. Never the secret.
type SyncAuthView struct {
	Kind      string `json:"kind"`
	Username  string `json:"username"`
	SecretSet bool   `json:"secret_set"`
}

// SyncOwner is the admin a sync acts for.
type SyncOwner struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// SyncHookView is the hook that triggers a sync.
type SyncHookView struct {
	ID         string     `json:"id"`
	Path       string     `json:"path"`
	Sender     string     `json:"sender"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (s *Service) syncView(ctx context.Context, y *Sync) (*SyncView, error) {
	v := &SyncView{ID: y.ID, Node: y.Node, Stack: y.Stack, URL: y.URL, Branch: y.Branch, Path: y.Path,
		Auth: SyncAuthView{Kind: y.AuthKind}, Mode: y.Mode, PollSeconds: y.PollSeconds, SecretsAllow: y.SecretsAllow,
		State: y.State, StateReason: y.StateReason, LastSeen: y.LastSeen, LastDeployed: y.LastDeployed, Rejected: y.latestRejected(),
		RejectedSHAs: append([]string{}, y.Rejected...), LastError: y.LastError, Running: s.syncer.Running(y.ID), CreatedAt: y.CreatedAt, UpdatedAt: y.UpdatedAt}
	if y.HostKey != "" {
		k := y.HostKey
		v.HostKey = &k
	}
	if y.AuthKind != stackproto.AuthNone {
		c, err := s.credentialOf(ctx, y.ID)
		if err != nil {
			return nil, err
		}
		v.Auth.Username, v.Auth.SecretSet = c.Username, c.Secret != ""
		c.Secret = ""
	}
	if y.OwnerID != "" {
		v.Owner = &SyncOwner{ID: y.OwnerID, Username: s.username(ctx, y.OwnerID)}
	}
	h, err := s.hookFor(ctx, normalizeURL(y.URL), y.Branch)
	if err != nil {
		return nil, err
	}
	if h != nil {
		v.Hook = &SyncHookView{ID: h.ID, Path: hookPath(h.ID), Sender: h.Sender}
		if !h.LastUsedAt.IsZero() {
			t := h.LastUsedAt
			v.Hook.LastUsedAt = &t
		}
	}
	return v, nil
}

// syncedSentence is what the editor and the draft routes say about a
// synced stack: where to edit it instead.
func syncedSentence(y *Sync) string {
	return "Edit " + y.Path + " on " + y.Branch + " in " + y.URL + "."
}

// refuseSynced answers 409 synced when the stack follows git.
func (s *Service) refuseSynced(w http.ResponseWriter, r *http.Request, node, stack string) bool {
	y, err := s.syncByStack(r.Context(), node, stack)
	if err != nil {
		s.internal(w, "stacks: read sync", err)
		return true
	}
	if y == nil {
		return false
	}
	httpx.Error(w, http.StatusConflict, "synced", syncedSentence(y))
	return true
}

// stepUp asks for a step-up (authz T3 on stack.sync), auditing a refusal
// under auditAction.
func (s *Service) stepUp(w http.ResponseWriter, r *http.Request, auditAction, target string, params any) bool {
	a, _ := s.actor(r)
	err := authz.Decide(a, authz.Request{Scope: "stack.sync", Target: target, Tier: authz.T3}, authz.Env{Now: s.now()})
	if err != nil {
		s.Audit.Change(r, auditAction, target, params, authz.T3, err)
		authz.WriteRefusal(w, err)
		return false
	}
	return true
}

func (s *Service) actor(r *http.Request) (authz.Actor, bool) {
	if s.actorOf != nil {
		return s.actorOf(r)
	}
	return authz.ActorFrom(r)
}

func (s *Service) handleGetSync(w http.ResponseWriter, r *http.Request) {
	node, stack, ok := pathStack(w, r)
	if !ok {
		return
	}
	y, err := s.syncByStack(r.Context(), node, stack)
	if err != nil {
		s.internal(w, "stacks: read sync", err)
		return
	}
	var v *SyncView
	if y != nil {
		if v, err = s.syncView(r.Context(), y); err != nil {
			s.internal(w, "stacks: read sync", err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sync": v})
}

// syncAuthReq is the auth part of a sync's PUT and check.
type syncAuthReq struct {
	Kind     string `json:"kind"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

func checkAuth(rawURL string, a syncAuthReq) error {
	scheme, _, _ := strings.Cut(strings.ToLower(rawURL), "://")
	switch a.Kind {
	case stackproto.AuthNone:
	case stackproto.AuthHTTPS:
		if scheme == "http" {
			return problem("A token is sent only over https://, never in the clear; use the remote's https:// URL.")
		}
		if scheme != "https" {
			return problem("A token signs in over HTTPS; this URL isn't an https:// one.")
		}
	case stackproto.AuthSSH:
		if scheme != "ssh" {
			return problem("An SSH key signs in over SSH; use an ssh:// URL.")
		}
	default:
		return problem("Pick how to sign in: none, https or ssh.")
	}
	if len(a.Username) > 256 || strings.ContainsAny(a.Username, "\r\n\x00@:/ ") {
		return problem("That user name can't be used.")
	}
	if len(a.Secret) > 64<<10 {
		return problem("The credential can be at most 64 KiB.")
	}
	return nil
}

func (s *Service) handlePutSync(w http.ResponseWriter, r *http.Request) {
	node, stack, ok := pathStack(w, r)
	if !ok {
		return
	}
	var req struct {
		URL          string      `json:"url"`
		Branch       string      `json:"branch"`
		Path         string      `json:"path"`
		Auth         syncAuthReq `json:"auth"`
		HostKey      string      `json:"host_key"`
		Mode         string      `json:"mode"`
		PollSeconds  int         `json:"poll_seconds"`
		SecretsAllow []string    `json:"secrets_allow"`
	}
	if !httpx.DecodeJSONLimit(w, r, &req, 128<<10) {
		return
	}
	defer func() { req.Auth.Secret = "" }()
	invalid := func(err error) { httpx.Error(w, http.StatusUnprocessableEntity, "invalid", err.Error()) }
	rawURL, err := checkRemoteURL(req.URL)
	if err != nil {
		invalid(err)
		return
	}
	if err := checkBranch(req.Branch); err != nil {
		invalid(err)
		return
	}
	if req.Path == "" {
		req.Path = stack + "/compose.yaml"
	}
	if err := checkPath(req.Path); err != nil {
		invalid(err)
		return
	}
	if req.Auth.Kind == "" {
		req.Auth.Kind = stackproto.AuthNone
	}
	if err := checkAuth(rawURL, req.Auth); err != nil {
		invalid(err)
		return
	}
	req.HostKey = strings.TrimSpace(req.HostKey)
	if req.Auth.Kind == stackproto.AuthSSH {
		if req.HostKey == "" || len(req.HostKey) > 8192 || strings.ContainsAny(req.HostKey, "\r\n") || !strings.Contains(req.HostKey, " ") {
			invalid(problem("Test the remote and pin its SSH host key first."))
			return
		}
	} else {
		req.HostKey = ""
	}
	if req.Mode == "" {
		req.Mode = ModeAsk
	}
	if req.Mode != ModeAsk && req.Mode != ModeDeploy {
		invalid(problem("The mode is ask (draft and ask) or deploy (deploy on push)."))
		return
	}
	if req.PollSeconds == 0 {
		req.PollSeconds = DefaultPollSeconds
	}
	if req.PollSeconds < MinPollSeconds || req.PollSeconds > MaxPollSeconds {
		invalid(problem("Poll every 1 minute at least, and every day at most."))
		return
	}
	for _, n := range req.SecretsAllow {
		if !secretNameRE.MatchString(n) {
			invalid(problem("The allowed secrets must be stack secret names."))
			return
		}
	}
	req.SecretsAllow = sortedUnique(req.SecretsAllow)
	if req.SecretsAllow == nil {
		req.SecretsAllow = []string{}
	}
	ctx := r.Context()
	cur, err := s.syncByStack(ctx, node, stack)
	if err != nil {
		s.internal(w, "stacks: read sync", err)
		return
	}
	// The saved credential stays with the remote it was given for: a new
	// URL needs it given again.
	sameRemote := cur != nil && normalizeURL(cur.URL) == normalizeURL(rawURL)
	if req.Auth.Kind != stackproto.AuthNone && req.Auth.Secret == "" && (!sameRemote || cur.AuthKind != req.Auth.Kind) {
		switch {
		case cur != nil && !sameRemote && cur.AuthKind == req.Auth.Kind:
			invalid(problem("The saved credential is only sent to " + remoteHost(cur.URL) + ". Give it again for the new remote."))
		case req.Auth.Kind == stackproto.AuthSSH:
			invalid(problem("Paste the SSH private key the node signs in with."))
		default:
			invalid(problem("Give the token the node signs in with."))
		}
		return
	}
	if s.vault == nil && req.Auth.Kind != stackproto.AuthNone {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "The vault isn't available.")
		return
	}
	actor, _ := s.actor(r)
	target := Target(node, stack)
	urlChanged := cur != nil && !sameRemote
	takeover := cur != nil && cur.OwnerID != actor.ID
	toDeploy := req.Mode == ModeDeploy && (cur == nil || cur.Mode != ModeDeploy)
	// While a sync deploys on push (before or after this change), pointing
	// it at other content is as strong as turning deploy on push on.
	var retarget []string
	if cur != nil && (cur.Mode == ModeDeploy || req.Mode == ModeDeploy) {
		changed, err := s.retargets(ctx, cur, req.Branch, req.Path, req.HostKey, req.Auth, req.SecretsAllow)
		if err != nil {
			s.internal(w, "stacks: read a sync's credential", err)
			return
		}
		retarget = changed
	}
	stepUp := urlChanged || takeover || toDeploy || len(retarget) > 0
	params := map[string]any{"host": remoteHost(rawURL), "branch": req.Branch, "path": req.Path, "auth_kind": req.Auth.Kind,
		"mode": req.Mode, "poll_seconds": req.PollSeconds, "secrets_allow": req.SecretsAllow}
	if cur != nil {
		params["sync"] = cur.ID
		params["url_changed"] = urlChanged
	}
	if len(retarget) > 0 {
		params["changed"] = retarget
	}
	action := "settings.stacks.sync.create"
	if cur != nil {
		action = "settings.stacks.sync.update"
	}
	if takeover {
		action = "settings.stacks.sync.takeover"
	}
	if stepUp && !s.stepUp(w, r, action, target, params) {
		return
	}
	// Cha Dash's own stack, and any the node protects, can't follow git.
	// Asked after the step-up, so nothing reaches the node without it.
	var st stackproto.Stack
	err = s.call(ctx, node, stackproto.VerbGet, stackproto.GetRequest{Stack: stack}, &st, s.timeout, nil)
	if e := stackproto.ErrorFrom(err); err != nil && (e == nil || e.Code != stackproto.CodeNotFound) {
		s.fail(w, err)
		return
	}
	if st.Protected {
		httpx.Error(w, http.StatusConflict, "protected", "Cha Dash's own stack, and any stack the node protects, can't follow git.")
		return
	}
	y := &Sync{ID: newID("syn_"), Node: node, Stack: stack, State: StateActive}
	if cur != nil {
		c := *cur // cur keeps what was, for the audit record
		y = &c
	}
	moved := cur != nil && (urlChanged || cur.URL != rawURL || cur.Branch != req.Branch || cur.Path != req.Path)
	y.URL, y.Branch, y.Path, y.AuthKind, y.HostKey = rawURL, req.Branch, req.Path, req.Auth.Kind, req.HostKey
	y.Mode, y.PollSeconds, y.SecretsAllow, y.OwnerID = req.Mode, req.PollSeconds, req.SecretsAllow, actor.ID
	y.LastError = ""
	if moved {
		// Another source: compare its tip afresh. Rejected commits stay
		// rejected, so moving away and back doesn't re-arm one.
		y.LastSeen = ""
	}
	if err := s.saveCredential(ctx, y.ID, rawURL, cur, req.Auth); err != nil {
		s.internal(w, "stacks: save a sync's credential", err)
		return
	}
	status := http.StatusOK
	if cur == nil {
		status = http.StatusCreated
		err = s.insertSync(ctx, y)
	} else {
		err = s.updateSync(ctx, y)
	}
	if err != nil {
		s.internal(w, "stacks: save sync", err)
		return
	}
	params["sync"] = y.ID
	tier := authz.T1
	if stepUp {
		tier = authz.T3
	}
	if takeover {
		s.Audit.Change(r, "settings.stacks.sync.takeover", target, map[string]any{"sync": y.ID, "from": cur.OwnerID}, authz.T3, nil)
		params["takeover"] = true
		action = "settings.stacks.sync.update"
	}
	s.Audit.Change(r, action, target, params, tier, nil)
	s.syncer.forget(y.ID)
	if y.State == StateActive {
		s.syncer.Trigger(y.ID)
	}
	v, err := s.syncView(ctx, y)
	if err != nil {
		s.internal(w, "stacks: read sync", err)
		return
	}
	httpx.WriteJSON(w, status, map[string]any{"sync": v})
}

// retargets lists what a change to a sync points it at, other than the
// remote: the branch, the path, the credential (a new secret, kind, user
// or pinned host key) and names newly allowed in secrets_allow.
func (s *Service) retargets(ctx context.Context, cur *Sync, branch, path, hostKey string, a syncAuthReq, allow []string) ([]string, error) {
	var out []string
	if cur.Branch != branch {
		out = append(out, "branch")
	}
	if cur.Path != path {
		out = append(out, "path")
	}
	cred := a.Secret != "" || a.Kind != cur.AuthKind || hostKey != cur.HostKey
	if !cred && a.Kind != stackproto.AuthNone {
		c, err := s.credentialOf(ctx, cur.ID)
		if err != nil {
			return nil, err
		}
		cred = c.Username != a.Username
		c.Secret = ""
	}
	if cred {
		out = append(out, "credential")
	}
	for _, n := range allow {
		if !slices.Contains(cur.SecretsAllow, n) {
			out = append(out, "secrets_allow")
			break
		}
	}
	return out, nil
}

// saveCredential writes a sync's credential to the vault: a new secret,
// or the saved one under a new user name; none deletes it.
func (s *Service) saveCredential(ctx context.Context, id, url string, cur *Sync, a syncAuthReq) error {
	if a.Kind == stackproto.AuthNone {
		if s.vault == nil {
			return nil
		}
		if err := s.vault.Delete(ctx, syncCredentialName(id)); err != nil && !errors.Is(err, vault.ErrNotFound) {
			return err
		}
		return nil
	}
	c := credential{Username: a.Username, Secret: a.Secret}
	// Only for the same remote: handlePutSync refuses an empty secret for
	// another one, and this holds whatever the caller.
	if c.Secret == "" && cur != nil && normalizeURL(cur.URL) == normalizeURL(url) {
		old, err := s.credentialOf(ctx, id)
		if err != nil {
			return err
		}
		c.Secret = old.Secret
	}
	raw, err := json.Marshal(c) //nolint:gosec // G117: marshaled only to be sealed in the vault, never answered or logged

	c.Secret = ""
	if err != nil {
		return err
	}
	defer clear(raw)
	return s.vault.Put(ctx, syncCredentialName(id), raw)
}

func (s *Service) pathSync(w http.ResponseWriter, r *http.Request) (*Sync, bool) {
	node, stack, ok := pathStack(w, r)
	if !ok {
		return nil, false
	}
	y, err := s.syncByStack(r.Context(), node, stack)
	if err != nil {
		s.internal(w, "stacks: read sync", err)
		return nil, false
	}
	if y == nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "This stack doesn't follow git.")
		return nil, false
	}
	return y, true
}

func (s *Service) handleDeleteSync(w http.ResponseWriter, r *http.Request) {
	y, ok := s.pathSync(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, err := s.st.Writer().ExecContext(ctx, `DELETE FROM stack_syncs WHERE id = ?`, y.ID); err != nil {
		s.internal(w, "stacks: delete sync", err)
		return
	}
	if s.vault != nil {
		if err := s.vault.Delete(context.WithoutCancel(ctx), syncCredentialName(y.ID)); err != nil && !errors.Is(err, vault.ErrNotFound) {
			s.log.Warn("stacks: delete a sync's credential", "sync", y.ID, "err", err)
		}
	}
	s.syncer.withdraw(ctx, y.ID, "The stack stopped following git.")
	s.syncer.forget(y.ID)
	s.clearSyncItem(y)
	s.Audit.Change(r, "settings.stacks.sync.delete", Target(y.Node, y.Stack), map[string]any{"sync": y.ID, "host": remoteHost(y.URL), "branch": y.Branch}, authz.T1, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleCheckSync(w http.ResponseWriter, r *http.Request) {
	node, stack, ok := pathStack(w, r)
	if !ok {
		return
	}
	var req struct {
		URL    string       `json:"url"`
		Branch string       `json:"branch"`
		Auth   *syncAuthReq `json:"auth"`
	}
	if !httpx.DecodeJSONLimit(w, r, &req, 128<<10) {
		return
	}
	ctx := r.Context()
	cur, err := s.syncByStack(ctx, node, stack)
	if err != nil {
		s.internal(w, "stacks: read sync", err)
		return
	}
	ra := stackproto.RemoteAuth{Kind: stackproto.AuthNone}
	if cur != nil {
		req.URL, req.Branch = cmp.Or(req.URL, cur.URL), cmp.Or(req.Branch, cur.Branch)
		ra.Kind = cur.AuthKind
	}
	if req.Auth != nil {
		ra = stackproto.RemoteAuth{Kind: cmp.Or(req.Auth.Kind, stackproto.AuthNone), Username: req.Auth.Username, Secret: req.Auth.Secret}
		req.Auth.Secret = ""
	}
	defer func() { ra.Secret = "" }()
	rawURL, err := checkRemoteURL(req.URL)
	if err == nil {
		err = checkBranch(req.Branch)
	}
	if err == nil {
		err = checkAuth(rawURL, syncAuthReq{Kind: ra.Kind, Username: ra.Username, Secret: ra.Secret})
	}
	if err != nil {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	// The saved credential goes only to the remote it was saved for: testing
	// another URL takes its own.
	if ra.Kind != stackproto.AuthNone && ra.Secret == "" && cur != nil && cur.AuthKind == ra.Kind && normalizeURL(rawURL) != normalizeURL(cur.URL) {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "The saved credential is only sent to "+remoteHost(cur.URL)+". Give it again to test another remote.")
		return
	}
	if ra.Kind != stackproto.AuthNone && ra.Secret == "" && cur != nil && cur.AuthKind == ra.Kind {
		c, err := s.credentialOf(ctx, cur.ID)
		if err != nil {
			s.internal(w, "stacks: read a sync's credential", err)
			return
		}
		ra.Secret = c.Secret
		if ra.Username == "" {
			ra.Username = c.Username
		}
	}
	var res stackproto.RemoteCheckResponse
	if err := s.call(ctx, node, stackproto.VerbRemoteCheck, stackproto.RemoteCheckRequest{URL: rawURL, Branch: req.Branch, Auth: ra}, &res, fetchTimeout, nil); err != nil {
		s.fail(w, err)
		return
	}
	out := map[string]any{"sha": res.SHA, "host_key": nil}
	if res.HostKey != "" {
		out["host_key"] = res.HostKey
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	y, ok := s.pathSync(w, r)
	if !ok {
		return
	}
	if y.State != StateActive {
		httpx.Error(w, http.StatusConflict, "not_active", "The sync is "+y.State+"; resume it first.")
		return
	}
	if !s.syncer.Trigger(y.ID) {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "The syncer isn't running.")
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (s *Service) handleResumeSync(w http.ResponseWriter, r *http.Request) {
	y, ok := s.pathSync(w, r)
	if !ok {
		return
	}
	actor, _ := s.actor(r)
	target := Target(y.Node, y.Stack)
	takeover := y.OwnerID != actor.ID
	if takeover && !s.stepUp(w, r, "settings.stacks.sync.takeover", target, map[string]any{"sync": y.ID}) {
		return
	}
	from := y.OwnerID
	y.State, y.StateReason, y.LastError, y.OwnerID = StateActive, "", "", actor.ID
	// Follow the branch again: its tip is compared afresh (a rejected
	// commit stays rejected).
	y.LastSeen = ""
	if err := s.updateSync(r.Context(), y); err != nil {
		s.internal(w, "stacks: resume sync", err)
		return
	}
	if takeover {
		s.Audit.Change(r, "settings.stacks.sync.takeover", target, map[string]any{"sync": y.ID, "from": from}, authz.T3, nil)
	}
	s.Audit.Change(r, "settings.stacks.sync.resume", target, map[string]any{"sync": y.ID}, authz.T1, nil)
	s.clearSyncItem(y, itemHeld, itemPaused)
	s.syncer.forget(y.ID)
	s.syncer.Trigger(y.ID)
	v, err := s.syncView(r.Context(), y)
	if err != nil {
		s.internal(w, "stacks: read sync", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sync": v})
}
