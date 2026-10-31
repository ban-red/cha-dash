package stacks

import (
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/vault"
)

// Deploy webhooks (ADR-0021: a webhook is a poke, not a payload). A hook
// belongs to a remote URL (normalized) and a branch, not to a stack: a
// valid request triggers every active sync that follows them, and each
// sync then checks and fetches as it would on a poll. Nothing in the body
// is trusted except the push's ref, which can only turn the poke into
// "ignored".

// Senders, and the one header each is checked by.
const (
	SenderGitHub  = "github"
	SenderGitea   = "gitea"
	SenderForgejo = "forgejo"
	SenderGitLab  = "gitlab"
	SenderChadash = "chadash"
)

// Senders lists them, for validation.
var Senders = []string{SenderGitHub, SenderGitea, SenderForgejo, SenderGitLab, SenderChadash}

const (
	// maxHookBody bounds a webhook's body; larger answers 413 and the next
	// poll catches up.
	maxHookBody = 1 << 20
	// replayWindow is how long a delivery ID is remembered.
	replayWindow = 24 * time.Hour
	// timestampWindow is how far a Cha Dash signature's timestamp may be
	// from the hub's clock.
	timestampWindow = 5 * time.Minute
	// refusalWindow: the first refusal of a hook in each window is audited.
	refusalWindow = 10 * time.Minute
	// hookPathPrefix is where hooks answer.
	hookPathPrefix = "/api/hooks/"
)

var hookIDRE = regexp.MustCompile(`^whk_[a-z2-7]{26}$`)

// Hook is one row of stack_hooks.
type Hook struct {
	ID, URL, Branch, Sender string
	CreatedBy               string
	CreatedAt, LastUsedAt   time.Time
}

const hookColumns = `id, url, branch, sender, COALESCE(created_by, ''), created_at, last_used_at`

func scanHook(row interface{ Scan(...any) error }) (*Hook, error) {
	var h Hook
	var created, used int64
	if err := row.Scan(&h.ID, &h.URL, &h.Branch, &h.Sender, &h.CreatedBy, &created, &used); err != nil {
		return nil, err
	}
	h.CreatedAt = time.UnixMilli(created).UTC()
	if used > 0 {
		h.LastUsedAt = time.UnixMilli(used).UTC()
	}
	return &h, nil
}

func (s *Service) hookByID(ctx context.Context, id string) (*Hook, error) {
	h, err := scanHook(s.st.Reader().QueryRowContext(ctx, `SELECT `+hookColumns+` FROM stack_hooks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

// hookFor is the hook on a remote (normalized) and branch, or nil.
func (s *Service) hookFor(ctx context.Context, url, branch string) (*Hook, error) {
	h, err := scanHook(s.st.Reader().QueryRowContext(ctx, `SELECT `+hookColumns+` FROM stack_hooks WHERE url = ? AND branch = ?`, url, branch))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

func (s *Service) listHooks(ctx context.Context) ([]*Hook, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT `+hookColumns+` FROM stack_hooks ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Hook
	for rows.Next() {
		h, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// newHookSecret is 256 random bits, as hex.
func newHookSecret() string {
	b := make([]byte, 32)
	_, _ = io.ReadFull(crand.Reader, b)
	return hex.EncodeToString(b)
}

// ── verification ─────────────────────────────────────────────────────────

// errBadSignature is every way a request can fail to prove itself; the
// reason is for the audit log, never the answer.
type errBadSignature struct{ reason string }

func (e errBadSignature) Error() string { return e.reason }

func bad(reason string) error { return errBadSignature{reason} }

// verifyHook checks a request against a hook's secret, by its sender's one
// header, and returns the delivery ID to remember for replay ("" when the
// sender has none). It reads nothing but those headers and the raw body.
func verifyHook(sender string, secret []byte, hdr http.Header, body []byte, now time.Time) (string, error) {
	if len(secret) == 0 {
		return "", bad("the hook has no secret")
	}
	switch sender {
	case SenderGitHub:
		sig, ok := strings.CutPrefix(hdr.Get("X-Hub-Signature-256"), "sha256=")
		if !ok {
			return "", bad("no X-Hub-Signature-256")
		}
		if !macEqual(secret, sig, body) {
			return "", bad("signature mismatch")
		}
		return delivery(hdr.Get("X-GitHub-Delivery"))
	case SenderGitea, SenderForgejo:
		name := "Gitea"
		if sender == SenderForgejo {
			name = "Forgejo"
		}
		sig := hdr.Get("X-" + name + "-Signature")
		if sig == "" {
			return "", bad("no X-" + name + "-Signature")
		}
		if !macEqual(secret, sig, body) {
			return "", bad("signature mismatch")
		}
		return delivery(hdr.Get("X-" + name + "-Delivery"))
	case SenderChadash:
		ts := hdr.Get("X-Chadash-Timestamp")
		n, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || len(ts) > 12 {
			return "", bad("no X-Chadash-Timestamp")
		}
		sig, ok := strings.CutPrefix(hdr.Get("X-Chadash-Signature"), "sha256=")
		if !ok {
			return "", bad("no X-Chadash-Signature")
		}
		signed := make([]byte, 0, len(ts)+1+len(body))
		signed = append(append(append(signed, ts...), '.'), body...)
		if !macEqual(secret, sig, signed) {
			return "", bad("signature mismatch")
		}
		if d := now.Sub(time.Unix(n, 0)); d > timestampWindow || d < -timestampWindow {
			return "", bad("stale timestamp")
		}
		return "", nil
	case SenderGitLab:
		tok := hdr.Get("X-Gitlab-Token")
		if tok == "" {
			return "", bad("no X-Gitlab-Token")
		}
		if subtle.ConstantTimeCompare([]byte(tok), secret) != 1 {
			return "", bad("token mismatch")
		}
		return "", nil
	}
	return "", bad("unknown sender")
}

// macEqual compares a hex HMAC-SHA256 of msg under secret with hmac.Equal.
func macEqual(secret []byte, sigHex string, msg []byte) bool {
	got, err := hex.DecodeString(sigHex)
	if err != nil || len(got) != sha256.Size {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(msg)
	return hmac.Equal(got, m.Sum(nil))
}

func delivery(id string) (string, error) {
	if id == "" || len(id) > 128 {
		return "", bad("no delivery ID")
	}
	return id, nil
}

// pushRef reads the push's ref from a body ("" when there's none, or the
// body isn't a push). GitHub may send a form with the JSON in payload.
func pushRef(contentType string, body []byte) string {
	if mt, _, _ := mime.ParseMediaType(contentType); mt == "application/x-www-form-urlencoded" {
		v, err := url.ParseQuery(string(body))
		if err != nil {
			return ""
		}
		body = []byte(v.Get("payload"))
	}
	var p struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(body, &p) != nil {
		return ""
	}
	return p.Ref
}

// ── replay, limits, failures ─────────────────────────────────────────────

// hookGuard holds what the webhook route remembers between requests: the
// delivery IDs seen in the last day, the rate limits, and each hook's
// failures.
type hookGuard struct {
	now    func() time.Time
	perIP  *httpx.Limiter
	perHok *httpx.Limiter

	mu       sync.Mutex
	seen     map[string]time.Time // hook ID + "\x00" + delivery → when
	failures map[string]*hookFailures
}

type hookFailures struct {
	count       int
	windowStart time.Time
}

// maxDeliveries bounds the replay memory; a full map is pruned of expired
// IDs, and if every one is still fresh the oldest go.
const maxDeliveries = 20000

func newHookGuard(now func() time.Time) *hookGuard {
	return &hookGuard{
		now: now,
		// A forge sends a push or two at a time; a CI job a few. Anything
		// faster is a client in a loop or someone guessing.
		perIP:    httpx.NewLimiter(30, 2*time.Second),
		perHok:   httpx.NewLimiter(20, 3*time.Second),
		seen:     map[string]time.Time{},
		failures: map[string]*hookFailures{},
	}
}

// replayed records a delivery and reports whether it was seen before.
func (g *hookGuard) replayed(hook, id string) bool {
	if id == "" {
		return false
	}
	now := g.now()
	key := hook + "\x00" + id
	g.mu.Lock()
	defer g.mu.Unlock()
	if at, ok := g.seen[key]; ok && now.Sub(at) < replayWindow {
		return true
	}
	if len(g.seen) >= maxDeliveries {
		var oldestKey string
		var oldest time.Time
		for k, at := range g.seen {
			if now.Sub(at) >= replayWindow {
				delete(g.seen, k)
			} else if oldestKey == "" || at.Before(oldest) {
				oldestKey, oldest = k, at
			}
		}
		if len(g.seen) >= maxDeliveries {
			delete(g.seen, oldestKey)
		}
	}
	g.seen[key] = now
	return false
}

// failed counts a refusal of a hook and reports whether it's the first in
// its window (the one to audit), and how many there were.
func (g *hookGuard) failed(hook string) (bool, int) {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.failures[hook]
	if f == nil {
		f = &hookFailures{}
		g.failures[hook] = f
	}
	f.count++
	if f.windowStart.IsZero() || now.Sub(f.windowStart) >= refusalWindow {
		f.windowStart = now
		return true, f.count
	}
	return false, f.count
}

func (g *hookGuard) failureCount(hook string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if f := g.failures[hook]; f != nil {
		return f.count
	}
	return 0
}

func (g *hookGuard) forget(hook string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, hook)
	for k := range g.seen {
		if strings.HasPrefix(k, hook+"\x00") {
			delete(g.seen, k)
		}
	}
}

// ── POST /api/hooks/{id} ─────────────────────────────────────────────────

// unauthorized is the one answer to an unknown hook and a bad signature.
func unauthorized(w http.ResponseWriter) {
	httpx.Error(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
}

func (s *Service) handleHook(w http.ResponseWriter, r *http.Request) {
	ip := s.proxies.ClientIP(r)
	if ok, wait := s.hooks.perIP.Allow(httpx.ClientKey(ip)); !ok {
		tooMany(w, wait)
		return
	}
	id := r.PathValue("id")
	if !hookIDRE.MatchString(id) {
		unauthorized(w)
		return
	}
	ctx := r.Context()
	h, err := s.hookByID(ctx, id)
	if err != nil {
		s.log.Error("stacks: read hook", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if h == nil || s.vault == nil {
		unauthorized(w)
		return
	}
	// Per hook only once it exists: made-up IDs would otherwise fill the
	// limiter's table (the per-address limit above holds them back).
	if ok, wait := s.hooks.perHok.Allow(id); !ok {
		tooMany(w, wait)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHookBody))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "too_large", "A webhook's body can be at most 1 MiB; the next poll will catch up.")
			return
		}
		httpx.Error(w, http.StatusBadRequest, "bad_request", "The body couldn't be read.")
		return
	}
	secret, err := s.vault.Get(ctx, hookSecretName(id))
	if err != nil {
		s.log.Error("stacks: read a hook's secret", "hook", id, "err", err)
		unauthorized(w)
		return
	}
	deliveryID, err := verifyHook(h.Sender, secret, r.Header, body, s.now())
	clear(secret)
	if err == nil && s.hooks.replayed(id, deliveryID) {
		err = bad("replayed delivery")
	}
	if err != nil {
		s.hookRefused(r, h, ip.String(), err.Error())
		unauthorized(w)
		return
	}
	if _, err := s.st.Writer().ExecContext(context.WithoutCancel(ctx), `UPDATE stack_hooks SET last_used_at = ? WHERE id = ?`, s.now().UnixMilli(), id); err != nil {
		s.log.Warn("stacks: record a hook's use", "hook", id, "err", err)
	}
	if ref := pushRef(r.Header.Get("Content-Type"), body); ref != "" && ref != "refs/heads/"+h.Branch {
		httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
		return
	}
	s.syncer.TriggerRemote(context.WithoutCancel(ctx), h.URL, h.Branch)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many requests; try again shortly.")
}

// hookRefused counts a refused request and audits the first in each
// window as stack.hook.refused, with the reason and never the headers.
func (s *Service) hookRefused(r *http.Request, h *Hook, ip, reason string) {
	first, n := s.hooks.failed(h.ID)
	if !first || s.Audit == nil || s.Audit.Log == nil {
		return
	}
	params, _ := json.Marshal(map[string]any{"hook": h.ID, "sender": h.Sender, "reason": reason, "failures": n})
	if _, err := s.Audit.Log.Append(context.WithoutCancel(r.Context()), audit.Entry{
		Actor:  audit.Actor{Kind: authz.KindAnonymous, Name: "webhook", Via: "hook", IP: ip},
		Action: "stack.hook.refused", Target: "hook:" + h.ID, Params: params, Result: audit.Refused,
		Error: &audit.Error{Code: "unauthorized", Message: "The webhook's request didn't prove itself: " + reason + "."},
	}); err != nil {
		s.log.Error("stacks: audit a refused hook", "hook", h.ID, "err", err)
	}
}

// hookPath is where a hook answers.
func hookPath(id string) string { return hookPathPrefix + id }

// ── hooks as settings ────────────────────────────────────────────────────

// HookView is a hook in GET /api/stacks/hooks. Never its secret.
type HookView struct {
	ID         string     `json:"id"`
	URL        string     `json:"url"`
	Branch     string     `json:"branch"`
	Sender     string     `json:"sender"`
	Path       string     `json:"path"`
	CreatedAt  time.Time  `json:"created_at"`
	CreatedBy  *string    `json:"created_by"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Failures   int        `json:"failures"`
	Syncs      []HookSync `json:"syncs"`
}

// HookSync is a sync a hook triggers.
type HookSync struct {
	Node     string `json:"node"`
	NodeName string `json:"node_name"`
	Stack    string `json:"stack"`
	Mode     string `json:"mode"`
	State    string `json:"state"`
}

func (s *Service) hookView(ctx context.Context, h *Hook, all []*Sync) HookView {
	v := HookView{ID: h.ID, URL: h.URL, Branch: h.Branch, Sender: h.Sender, Path: hookPath(h.ID), CreatedAt: h.CreatedAt,
		Failures: s.hooks.failureCount(h.ID), Syncs: []HookSync{}}
	if h.CreatedBy != "" {
		if name := s.username(ctx, h.CreatedBy); name != "" {
			v.CreatedBy = &name
		}
	}
	if !h.LastUsedAt.IsZero() {
		t := h.LastUsedAt
		v.LastUsedAt = &t
	}
	for _, y := range all {
		if y.Branch == h.Branch && normalizeURL(y.URL) == h.URL {
			v.Syncs = append(v.Syncs, HookSync{Node: y.Node, NodeName: s.nodeName(ctx, y.Node), Stack: y.Stack, Mode: y.Mode, State: y.State})
		}
	}
	// By node, then stack, so the list reads the same every time.
	slices.SortFunc(v.Syncs, func(a, b HookSync) int {
		if c := strings.Compare(a.NodeName, b.NodeName); c != 0 {
			return c
		}
		return strings.Compare(a.Stack, b.Stack)
	})
	return v
}

// username is a user's name, or "" when they're gone.
func (s *Service) username(ctx context.Context, id string) string {
	var name string
	_ = s.st.Reader().QueryRowContext(ctx, `SELECT username FROM users WHERE id = ?`, id).Scan(&name)
	return name
}

func (s *Service) handleListHooks(w http.ResponseWriter, r *http.Request) {
	hooks, err := s.listHooks(r.Context())
	if err != nil {
		s.internal(w, "stacks: list hooks", err)
		return
	}
	all, err := s.syncs(r.Context())
	if err != nil {
		s.internal(w, "stacks: list syncs", err)
		return
	}
	out := []HookView{}
	for _, h := range hooks {
		out = append(out, s.hookView(r.Context(), h, all))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"hooks": out})
}

func (s *Service) internal(w http.ResponseWriter, msg string, err error) {
	s.log.Error(msg, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
}

func (s *Service) handleCreateHook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL          string `json:"url"`
		Branch       string `json:"branch"`
		Sender       string `json:"sender"`
		DeployOnPush bool   `json:"deploy_on_push"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	raw, err := checkRemoteURL(req.URL)
	if err == nil {
		err = checkBranch(req.Branch)
	}
	if err == nil && !slices.Contains(Senders, req.Sender) {
		err = problem("Pick who sends it: github, gitea, forgejo, gitlab or chadash.")
	}
	if err != nil {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	if s.vault == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "The vault isn't available.")
		return
	}
	url := normalizeURL(raw)
	params := map[string]any{"host": remoteHost(raw), "branch": req.Branch, "sender": req.Sender, "deploy_on_push": req.DeployOnPush}
	if req.DeployOnPush && !s.stepUp(w, r, "settings.stacks.hook.create", "hook:new", params) {
		return
	}
	if h, err := s.hookFor(r.Context(), url, req.Branch); err != nil {
		s.internal(w, "stacks: read hook", err)
		return
	} else if h != nil {
		httpx.Error(w, http.StatusConflict, "exists", "There's already a hook for "+req.Branch+" on that remote. Rotate its secret instead.")
		return
	}
	h := &Hook{ID: newID("whk_"), URL: url, Branch: req.Branch, Sender: req.Sender, CreatedAt: s.now().UTC().Truncate(time.Millisecond)}
	if a, ok := s.actor(r); ok {
		h.CreatedBy = a.ID
	}
	secret := newHookSecret()
	if err := s.vault.Put(r.Context(), hookSecretName(h.ID), []byte(secret)); err != nil {
		s.internal(w, "stacks: save a hook's secret", err)
		return
	}
	if _, err := s.st.Writer().ExecContext(r.Context(), `INSERT INTO stack_hooks (id, url, branch, sender, created_by, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?, 0)`,
		h.ID, h.URL, h.Branch, h.Sender, nullable(h.CreatedBy), h.CreatedAt.UnixMilli()); err != nil {
		_ = s.vault.Delete(context.WithoutCancel(r.Context()), hookSecretName(h.ID))
		if strings.Contains(err.Error(), "UNIQUE") {
			httpx.Error(w, http.StatusConflict, "exists", "There's already a hook for "+req.Branch+" on that remote. Rotate its secret instead.")
			return
		}
		s.internal(w, "stacks: save hook", err)
		return
	}
	switched := 0
	notSwitched := []HookSync{}
	if req.DeployOnPush {
		following, err := s.syncsFollowing(r.Context(), url, req.Branch)
		if err != nil {
			s.internal(w, "stacks: list syncs", err)
			return
		}
		caller, _ := s.actor(r)
		for _, y := range following {
			if y.Mode == ModeDeploy {
				continue
			}
			// Only the caller's own syncs: another admin's runs on their
			// say-so, and switching it would deploy as them without asking.
			if y.OwnerID != caller.ID {
				notSwitched = append(notSwitched, HookSync{Node: y.Node, Stack: y.Stack, Mode: y.Mode, State: y.State})
				continue
			}
			y.Mode = ModeDeploy
			if err := s.updateSync(r.Context(), y); err != nil {
				s.internal(w, "stacks: switch a sync to deploy on push", err)
				return
			}
			switched++
			s.Audit.Change(r, "settings.stacks.sync.update", Target(y.Node, y.Stack), map[string]any{"sync": y.ID, "mode": ModeDeploy, "via": "hook:" + h.ID}, authz.T3, nil)
		}
	}
	params["hook"], params["switched"], params["not_switched"] = h.ID, switched, len(notSwitched)
	tier := authz.T1
	if req.DeployOnPush {
		tier = authz.T3
	}
	s.Audit.Change(r, "settings.stacks.hook.create", "hook:"+h.ID, params, tier, nil)
	all, _ := s.syncs(r.Context())
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"hook": s.hookView(r.Context(), h, all), "secret": secret, "switched": switched,
		"not_switched": notSwitched})
}

func (s *Service) pathHook(w http.ResponseWriter, r *http.Request) (*Hook, bool) {
	id := r.PathValue("id")
	var h *Hook
	var err error
	if hookIDRE.MatchString(id) {
		h, err = s.hookByID(r.Context(), id)
	}
	if err != nil {
		s.internal(w, "stacks: read hook", err)
		return nil, false
	}
	if h == nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "There's no such hook.")
		return nil, false
	}
	return h, true
}

func (s *Service) handleRotateHook(w http.ResponseWriter, r *http.Request) {
	h, ok := s.pathHook(w, r)
	if !ok {
		return
	}
	if s.vault == nil {
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable", "The vault isn't available.")
		return
	}
	secret := newHookSecret()
	if err := s.vault.Put(r.Context(), hookSecretName(h.ID), []byte(secret)); err != nil {
		s.internal(w, "stacks: rotate a hook's secret", err)
		return
	}
	s.hooks.forget(h.ID)
	s.Audit.Change(r, "settings.stacks.hook.rotate", "hook:"+h.ID, map[string]any{"hook": h.ID}, authz.T1, nil)
	all, _ := s.syncs(r.Context())
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"hook": s.hookView(r.Context(), h, all), "secret": secret})
}

func (s *Service) handleDeleteHook(w http.ResponseWriter, r *http.Request) {
	h, ok := s.pathHook(w, r)
	if !ok {
		return
	}
	if _, err := s.st.Writer().ExecContext(r.Context(), `DELETE FROM stack_hooks WHERE id = ?`, h.ID); err != nil {
		s.internal(w, "stacks: delete hook", err)
		return
	}
	if s.vault != nil {
		if err := s.vault.Delete(context.WithoutCancel(r.Context()), hookSecretName(h.ID)); err != nil && !errors.Is(err, vault.ErrNotFound) {
			s.log.Warn("stacks: delete a hook's secret", "hook", h.ID, "err", err)
		}
	}
	s.hooks.forget(h.ID)
	s.Audit.Change(r, "settings.stacks.hook.delete", "hook:"+h.ID, map[string]any{"hook": h.ID, "host": remoteHost(h.URL), "branch": h.Branch}, authz.T1, nil)
	w.WriteHeader(http.StatusNoContent)
}
