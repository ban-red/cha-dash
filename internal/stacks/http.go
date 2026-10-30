package stacks

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/vault"
)

// maxBody bounds a request carrying a compose file (≤ 256 KiB, escaped).
const maxBody = 768 << 10

// Register mounts the stacks API. Who may call each route is decided by
// internal/authz/routes.go (the list is viewer+, everything else admin+);
// session only attaches the identity.
//
//	GET    /api/stacks                         every node's stacks and adoptable projects
//	GET    /api/stacks/secrets                 stack secrets: names and whether set
//	PUT    /api/stacks/secrets/{name}          {value} → 204; DELETE … → 204
//	GET    /api/stacks/{node}/{stack}          one stack: file, history, draft, secrets
//	PUT    /api/stacks/{node}/{stack}          {content, revision} → {draft}; 409 when stale
//	DELETE /api/stacks/{node}/{stack}/draft    discard the draft
//	POST   /api/stacks/{node}/{stack}/validate {content} → messages, plan, diff
//
//	GET    /api/stacks/{node}/{stack}/sync         the sync, or null
//	PUT    /api/stacks/{node}/{stack}/sync         follow git, or change how
//	DELETE /api/stacks/{node}/{stack}/sync         unfollow
//	POST   /api/stacks/{node}/{stack}/sync/check   test the remote: its tip and SSH host key
//	POST   /api/stacks/{node}/{stack}/sync/now     check now
//	POST   /api/stacks/{node}/{stack}/sync/resume  resume a held or paused sync
//	GET    /api/stacks/hooks                       deploy webhooks, never their secrets
//	POST   /api/stacks/hooks                       make one; its secret is answered once
//	POST   /api/stacks/hooks/{id}/rotate           a new secret, answered once
//	DELETE /api/stacks/hooks/{id}                  remove one
//	POST   /api/hooks/{id}                         the webhook itself (authz.Signature)
//
// Deploying, rolling back and adopting are actions (POST /api/actions/plan).
func (s *Service) Register(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	mux.Handle("GET /api/stacks", session(http.HandlerFunc(s.handleList)))
	mux.Handle("GET /api/stacks/secrets", session(http.HandlerFunc(s.handleSecrets)))
	mux.Handle("PUT /api/stacks/secrets/{name}", session(http.HandlerFunc(s.handlePutSecret)))
	mux.Handle("DELETE /api/stacks/secrets/{name}", session(http.HandlerFunc(s.handleDeleteSecret)))
	mux.Handle("GET /api/stacks/{node}/{stack}", session(http.HandlerFunc(s.handleGet)))
	mux.Handle("PUT /api/stacks/{node}/{stack}", session(http.HandlerFunc(s.handlePutDraft)))
	mux.Handle("DELETE /api/stacks/{node}/{stack}/draft", session(http.HandlerFunc(s.handleDeleteDraft)))
	mux.Handle("POST /api/stacks/{node}/{stack}/validate", session(http.HandlerFunc(s.handleValidate)))

	// Git sync and deploy webhooks (sync.go, synchttp.go, hooks.go).
	mux.Handle("GET /api/stacks/{node}/{stack}/sync", session(http.HandlerFunc(s.handleGetSync)))
	mux.Handle("PUT /api/stacks/{node}/{stack}/sync", session(http.HandlerFunc(s.handlePutSync)))
	mux.Handle("DELETE /api/stacks/{node}/{stack}/sync", session(http.HandlerFunc(s.handleDeleteSync)))
	mux.Handle("POST /api/stacks/{node}/{stack}/sync/check", session(http.HandlerFunc(s.handleCheckSync)))
	mux.Handle("POST /api/stacks/{node}/{stack}/sync/now", session(http.HandlerFunc(s.handleSyncNow)))
	mux.Handle("POST /api/stacks/{node}/{stack}/sync/resume", session(http.HandlerFunc(s.handleResumeSync)))
	mux.Handle("GET /api/stacks/hooks", session(http.HandlerFunc(s.handleListHooks)))
	mux.Handle("POST /api/stacks/hooks", session(http.HandlerFunc(s.handleCreateHook)))
	mux.Handle("POST /api/stacks/hooks/{id}/rotate", session(http.HandlerFunc(s.handleRotateHook)))
	mux.Handle("DELETE /api/stacks/hooks/{id}", session(http.HandlerFunc(s.handleDeleteHook)))
	// The webhook answers to its signature, not a session: Guard passes it
	// through (authz.Signature) and handleHook verifies it.
	mux.Handle("POST /api/hooks/{id}", http.HandlerFunc(s.handleHook))

	// Fleet stacks (wave 14): move preview, self-update status (fleet_http.go).
	s.registerFleet(mux, session)
}

// pathStack reads and checks {node} and {stack}.
func pathStack(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	node, stack := r.PathValue("node"), r.PathValue("stack")
	if !nodeIDRE.MatchString(node) {
		httpx.Error(w, http.StatusNotFound, "not_found", "There's no such node.")
		return "", "", false
	}
	if !stackNameRE.MatchString(stack) {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "A stack name uses lowercase letters, digits, - and _, starting with a letter or digit.")
		return "", "", false
	}
	return node, stack, true
}

func (s *Service) fail(w http.ResponseWriter, err error) {
	if miss, ok := errors.AsType[errSecretMissing](err); ok {
		httpx.Error(w, http.StatusConflict, "secret_missing", miss.Error())
		return
	}
	status, code, msg := statusOf(err)
	if status == http.StatusBadGateway {
		s.log.Warn("stacks: node call failed", "err", err)
	}
	httpx.Error(w, status, code, msg)
}

// ── list ─────────────────────────────────────────────────────────────────

// NodeView is one node in GET /api/stacks.
type NodeView struct {
	Node     string                `json:"node"`
	Name     string                `json:"name"`
	Online   bool                  `json:"online"`
	Error    *httpError            `json:"error"`
	Root     string                `json:"root"`
	Stacks   []StackView           `json:"stacks"`
	Detected []stackproto.Detected `json:"detected"`
}

type httpError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// StackView is a stack in the list: the node's summary plus the hub's
// draft flag.
type StackView struct {
	stackproto.Summary
	Draft bool `json:"draft"`
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	ns, err := s.nodes(r.Context())
	if err != nil {
		s.log.Error("stacks: list nodes", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := make([]NodeView, len(ns))
	var wg sync.WaitGroup
	for i, n := range ns {
		wg.Go(func() { out[i] = s.nodeView(r.Context(), n) })
	}
	wg.Wait()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

func (s *Service) nodeView(ctx context.Context, n Node) NodeView {
	v := NodeView{Node: n.ID, Name: n.Name, Online: n.Online, Stacks: []StackView{}, Detected: []stackproto.Detected{}}
	if v.Name == "" {
		v.Name = n.ID
	}
	drafts, err := s.drafts(ctx, n.ID)
	if err != nil {
		s.log.Error("stacks: list drafts", "err", err)
	}
	setErr := func(err error) {
		_, code, msg := statusOf(err)
		v.Error = &httpError{Code: code, Message: msg}
	}
	if !n.Online {
		setErr(errNodeOffline{cmp.Or(n.Name, n.ID)})
	} else {
		var list stackproto.ListResponse
		var det stackproto.DetectResponse
		var lerr, derr error
		var wg sync.WaitGroup
		wg.Go(func() { lerr = s.call(ctx, n.ID, stackproto.VerbList, stackproto.ListRequest{}, &list, s.timeout, nil) })
		wg.Go(func() {
			derr = s.call(ctx, n.ID, stackproto.VerbDetect, stackproto.ListRequest{}, &det, s.timeout, nil)
		})
		wg.Wait()
		switch {
		case lerr != nil:
			setErr(lerr)
		case derr != nil:
			setErr(derr)
		}
		v.Root = list.Root
		for _, st := range list.Stacks {
			_, has := drafts[st.Name]
			v.Stacks = append(v.Stacks, StackView{Summary: st, Draft: has})
			delete(drafts, st.Name)
		}
		if det.Projects != nil {
			v.Detected = det.Projects
		}
		for _, d := range v.Detected {
			delete(drafts, d.Name) // a draft for a detected project shows there
		}
	}
	// Drafts for stacks that don't exist yet: new stacks.
	for _, name := range sortedKeys(drafts) {
		sum := stackproto.Summary{Name: name, Status: stackproto.StatusDown, Location: stackproto.LocationRoot, File: "compose.yaml"}
		if v.Root != "" {
			sum.Path = strings.TrimSuffix(v.Root, "/") + "/" + name
		}
		v.Stacks = append(v.Stacks, StackView{Summary: sum, Draft: true})
	}
	slices.SortFunc(v.Stacks, func(a, b StackView) int { return strings.Compare(a.Name, b.Name) })
	return v
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ── one stack ────────────────────────────────────────────────────────────

// StackDetail is GET /api/stacks/{node}/{stack}.
type StackDetail struct {
	Node            string               `json:"node"`
	Name            string               `json:"name"`
	Path            string               `json:"path"`
	File            string               `json:"file"`
	Location        string               `json:"location"`
	Managed         bool                 `json:"managed"`
	Exists          bool                 `json:"exists"` // false for a stack that's only a draft
	Protected       bool                 `json:"protected"`
	ProtectedReason *string              `json:"protected_reason"`
	Drift           bool                 `json:"drift"`
	Status          string               `json:"status"`
	Content         string               `json:"content"`
	Env             stackproto.EnvInfo   `json:"env"`
	Meta            map[string]any       `json:"meta"`
	Services        []stackproto.Service `json:"services"`
	History         []stackproto.Commit  `json:"history"`
	Head            *stackproto.Commit   `json:"head"`
	Draft           *Draft               `json:"draft"`
	Secrets         []SecretState        `json:"secrets"`
	// Sync is set when the stack follows git: its editor is read-only.
	Sync *SyncView `json:"sync"`
	// MovedFrom is where stack.move copied the file from, while that copy
	// is still there; null otherwise.
	MovedFrom *string `json:"moved_from"`
}

func (s *Service) detail(ctx context.Context, node, name string) (*StackDetail, error) {
	draft, err := s.draft(ctx, node, name)
	if err != nil {
		return nil, err
	}
	var st stackproto.Stack
	err = s.call(ctx, node, stackproto.VerbGet, stackproto.GetRequest{Stack: name}, &st, s.timeout, nil)
	exists := true
	if e := stackproto.ErrorFrom(err); e != nil && e.Code == stackproto.CodeNotFound && draft != nil {
		exists, err = false, nil
		st = stackproto.Stack{Name: name, Status: stackproto.StatusDown, Location: stackproto.LocationRoot, File: "compose.yaml"}
	}
	if err != nil {
		return nil, err
	}
	d := &StackDetail{
		Node: node, Name: name, Path: st.Path, File: st.File, Location: st.Location, Managed: st.Managed, Exists: exists,
		Protected: st.Protected, Drift: st.Drift, Status: st.Status, Content: st.Content, Env: st.Env, Meta: st.Meta,
		Services: st.Containers, History: st.History, Head: st.Head, Draft: draft,
	}
	if st.ProtectedReason != "" {
		d.ProtectedReason = &st.ProtectedReason
	}
	if st.MovedFrom != "" {
		d.MovedFrom = &st.MovedFrom
	}
	if d.Meta == nil {
		d.Meta = map[string]any{}
	}
	if d.Services == nil {
		d.Services = []stackproto.Service{}
	}
	if d.History == nil {
		d.History = []stackproto.Commit{}
	}
	if d.Env.Keys == nil {
		d.Env.Keys = []string{}
	}
	refs := slices.Concat(st.Secrets, secretRefs(st.Content))
	if draft != nil {
		refs = append(refs, secretRefs(draft.Content)...)
	}
	if d.Secrets, err = s.secretStates(ctx, refs); err != nil {
		return nil, err
	}
	y, err := s.syncByStack(ctx, node, name)
	if err != nil {
		return nil, err
	}
	if y != nil {
		if d.Sync, err = s.syncView(ctx, y); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	node, name, ok := pathStack(w, r)
	if !ok {
		return
	}
	d, err := s.detail(r.Context(), node, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// ── drafts ───────────────────────────────────────────────────────────────

func (s *Service) handlePutDraft(w http.ResponseWriter, r *http.Request) {
	node, name, ok := pathStack(w, r)
	if !ok {
		return
	}
	var req struct {
		Content  string `json:"content"`
		Revision int64  `json:"revision"`
	}
	if !httpx.DecodeJSONLimit(w, r, &req, maxBody) {
		return
	}
	if len(req.Content) > 256<<10 {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "too_large", "A compose file can be at most 256 KiB.")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "The draft is empty.")
		return
	}
	// A synced stack is edited in git.
	if s.refuseSynced(w, r, node, name) {
		return
	}
	// A protected stack can't be deployed, so a draft of it is pointless.
	var st stackproto.Stack
	err := s.call(r.Context(), node, stackproto.VerbGet, stackproto.GetRequest{Stack: name}, &st, s.timeout, nil)
	if e := stackproto.ErrorFrom(err); err != nil && (e == nil || e.Code != stackproto.CodeNotFound) {
		s.fail(w, err)
		return
	}
	if st.Protected {
		httpx.Error(w, http.StatusConflict, "protected", st.ProtectedReason)
		return
	}
	by := ""
	if id, ok := auth.FromContext(r.Context()); ok {
		by = id.User.Username
	}
	d, err := s.saveDraft(r.Context(), node, name, req.Content, by, SourceWeb, req.Revision)
	if stale, ok := errors.AsType[errStale](err); ok {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":    map[string]string{"code": "conflict", "message": "Someone saved this draft since you started editing. Reload it to see their changes."},
			"revision": stale.current,
		})
		return
	}
	if err != nil {
		s.log.Error("stacks: save draft", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	// Never the content: compose files often hold credentials.
	s.Audit.Change(r, "settings.stacks.draft.update", "stack:"+node+"/"+name, map[string]any{"revision": d.Revision, "bytes": len(req.Content)}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"draft": d})
}

func (s *Service) handleDeleteDraft(w http.ResponseWriter, r *http.Request) {
	node, name, ok := pathStack(w, r)
	if !ok {
		return
	}
	if s.refuseSynced(w, r, node, name) {
		return
	}
	if err := s.deleteDraft(r.Context(), node, name, 0); err != nil {
		s.log.Error("stacks: delete draft", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.Audit.Change(r, "settings.stacks.draft.delete", "stack:"+node+"/"+name, nil, authz.T1, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── validate ─────────────────────────────────────────────────────────────

// Validation is POST …/validate.
type Validation struct {
	OK       bool                 `json:"ok"`
	Messages []stackproto.Message `json:"messages"`
	Secrets  []SecretState        `json:"secrets"`
	Plan     *stackproto.Plan     `json:"plan"`
	Diff     []DiffLine           `json:"diff"`
}

func (s *Service) handleValidate(w http.ResponseWriter, r *http.Request) {
	node, name, ok := pathStack(w, r)
	if !ok {
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if !httpx.DecodeJSONLimit(w, r, &req, maxBody) {
		return
	}
	if len(req.Content) > 256<<10 {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "too_large", "A compose file can be at most 256 KiB.")
		return
	}
	v, err := s.validate(r.Context(), node, name, req.Content)
	if err != nil {
		s.fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

// validate asks the node, checks the vault has every referenced secret,
// and, when both pass, plans the deploy.
func (s *Service) validate(ctx context.Context, node, name, content string) (*Validation, error) {
	var res stackproto.ValidateResponse
	if err := s.call(ctx, node, stackproto.VerbValidate, stackproto.ValidateRequest{Stack: name, Content: content}, &res, s.timeout, nil); err != nil {
		return nil, err
	}
	var committed string
	var st stackproto.Stack
	err := s.call(ctx, node, stackproto.VerbGet, stackproto.GetRequest{Stack: name}, &st, s.timeout, nil)
	if e := stackproto.ErrorFrom(err); err != nil && (e == nil || e.Code != stackproto.CodeNotFound) {
		return nil, err
	} else if err == nil {
		committed = st.Content
	}
	v := &Validation{OK: res.OK, Messages: res.Messages, Diff: lineDiff(committed, content)}
	if v.Messages == nil {
		v.Messages = []stackproto.Message{}
	}
	// The names come from the hub's own reading of the content, never the
	// node's list: the node mustn't pick which secrets are sent to it.
	refs := secretRefs(content)
	if v.Secrets, err = s.secretStates(ctx, refs); err != nil {
		return nil, err
	}
	for _, sec := range v.Secrets {
		if !sec.Set {
			v.OK = false
			v.Messages = append(v.Messages, stackproto.Message{Severity: stackproto.SeverityError, Message: errSecretMissing{sec.Name}.Error()})
		}
	}
	if st.Protected {
		v.Messages = append(v.Messages, stackproto.Message{Severity: stackproto.SeverityInfo, Message: st.ProtectedReason})
	}
	if !v.OK {
		return v, nil
	}
	secrets, err := s.resolveSecrets(ctx, refs)
	if err != nil {
		return nil, err
	}
	var plan stackproto.Plan
	err = s.call(ctx, node, stackproto.VerbPlan, stackproto.PlanRequest{Stack: name, Content: content, Secrets: secrets}, &plan, s.timeout, nil)
	clear(secrets)
	if e := stackproto.ErrorFrom(err); e != nil && (e.Code == stackproto.CodeInvalid || e.Code == stackproto.CodeProtected) {
		v.OK = false
		v.Messages = append(v.Messages, stackproto.Message{Severity: stackproto.SeverityError, Message: e.Message})
		return v, nil
	}
	if err != nil {
		return nil, err
	}
	v.Plan = &plan
	return v, nil
}

// ── secrets ──────────────────────────────────────────────────────────────

func (s *Service) handleSecrets(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Name string `json:"name"`
		Set  bool   `json:"set"`
	}
	have, err := s.secretNames(r.Context())
	if err != nil {
		s.log.Error("stacks: list secrets", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := []item{}
	for _, n := range sortedKeys(have) {
		out = append(out, item{Name: n, Set: true})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"secrets": out})
}

func (s *Service) secretName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if !secretNameRE.MatchString(name) {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "A secret name uses letters, digits, '.', '_' and '-', up to 64, starting with a letter or digit.")
		return "", false
	}
	if s.vault == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "The vault isn't available.")
		return "", false
	}
	return name, true
}

func (s *Service) handlePutSecret(w http.ResponseWriter, r *http.Request) {
	name, ok := s.secretName(w, r)
	if !ok {
		return
	}
	var req struct {
		Value *string `json:"value"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Value == nil || *req.Value == "" {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "Send the secret's value.")
		return
	}
	if err := s.vault.Put(r.Context(), secretPrefix+name, []byte(*req.Value)); err != nil {
		if errors.Is(err, vault.ErrTooLarge) {
			httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "A secret can be at most 64 KiB.")
			return
		}
		s.log.Error("stacks: save secret", "name", name, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.Audit.Change(r, "settings.stacks.secret.update", "secret:"+secretPrefix+name, map[string]string{"name": name}, authz.T1, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	name, ok := s.secretName(w, r)
	if !ok {
		return
	}
	if err := s.vault.Delete(r.Context(), secretPrefix+name); err != nil && !errors.Is(err, vault.ErrNotFound) {
		s.log.Error("stacks: delete secret", "name", name, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.Audit.Change(r, "settings.stacks.secret.delete", "secret:"+secretPrefix+name, map[string]string{"name": name}, authz.T1, nil)
	w.WriteHeader(http.StatusNoContent)
}
