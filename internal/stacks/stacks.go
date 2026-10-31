// Package stacks is the hub's side of owned stacks: the stacks API over
// the nodes (nodes.Caller), drafts saved in the hub's database, secrets for
// stacks in the vault, and the stack actions the engine runs (stack.deploy,
// stack.rollback, stack.adopt, stack.unadopt).
//
// The node does the work (internal/node/stacks); this package never links
// the Compose SDK or go-git. It speaks the node's verbs through
// internal/stackproto. See README.md.
package stacks

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/nodes"
	"dash.cha.sh/internal/stackproto"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// Node is a node as the stacks API lists it.
type Node struct {
	ID     string
	Name   string
	Online bool
}

// Options configure the service.
type Options struct {
	Store *store.Store
	Vault *vault.Vault
	// Caller runs verbs on nodes; Nodes lists them. Both come from
	// internal/nodes.
	Caller nodes.Caller
	Nodes  func(ctx context.Context) ([]Node, error)
	// Graph gives deploys their blast radius (the stack's containers and
	// what depends on them). Nil leaves it empty.
	Graph *graph.Graph
	// Inbox receives a high-severity item when a stack is stuck (its
	// rollback failed too). Nil skips it.
	Inbox *livedoc.Doc
	Log   *slog.Logger
	Now   func() time.Time
	// CallTimeout bounds a read from a node. Default 15 s.
	CallTimeout time.Duration
	// Proxies find a webhook caller's address behind trusted reverse
	// proxies, for its rate limit. The zero value uses the peer address.
	Proxies httpx.Proxies
}

// Service is the stacks API and actions.
type Service struct {
	st      *store.Store
	vault   *vault.Vault
	caller  nodes.Caller
	nodes   func(ctx context.Context) ([]Node, error)
	graph   *graph.Graph
	inbox   *livedoc.Doc
	log     *slog.Logger
	now     func() time.Time
	timeout time.Duration
	// Audit records secret changes (names only, never values). Set by the
	// server; nil records nothing.
	Audit *authz.Recorder

	// Git sync and webhooks (sync.go, hooks.go).
	proxies httpx.Proxies
	syncer  *Syncer
	hooks   *hookGuard
	// actorOf replaces authz.ActorFrom, for tests.
	actorOf func(*http.Request) (authz.Actor, bool)
}

// New returns the service.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Caller == nil || o.Nodes == nil {
		return nil, errors.New("stacks: Store, Caller and Nodes are required")
	}
	s := &Service{st: o.Store, vault: o.Vault, caller: o.Caller, nodes: o.Nodes, graph: o.Graph, inbox: o.Inbox, log: o.Log, now: o.Now, timeout: o.CallTimeout,
		proxies: o.Proxies}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.timeout <= 0 {
		s.timeout = 15 * time.Second
	}
	s.hooks = newHookGuard(s.now)
	return s, nil
}

// ── targets ──────────────────────────────────────────────────────────────

// TargetPrefix starts a stack's action target: stack:<node>/<name>.
const TargetPrefix = "stack:"

var (
	nodeIDRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	stackNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

// Target is the action target for a stack.
func Target(node, stack string) string { return TargetPrefix + node + "/" + stack }

// parseTarget splits stack:<node>/<name>.
func parseTarget(t string) (node, stack string, ok bool) {
	rest, ok := strings.CutPrefix(t, TargetPrefix)
	if !ok {
		return "", "", false
	}
	node, stack, ok = strings.Cut(rest, "/")
	if !ok || !nodeIDRE.MatchString(node) || !stackNameRE.MatchString(stack) {
		return "", "", false
	}
	return node, stack, true
}

// ── calling nodes ────────────────────────────────────────────────────────

// errNodeOffline is a node that isn't connected; node is its name.
type errNodeOffline struct{ node string }

func (e errNodeOffline) Error() string {
	return fmt.Sprintf("The node %s isn't connected, so its stacks can't be read or changed right now.", e.node)
}

// nodeName is what people call a node: its name, or its ID when there's
// none or the hub can't say.
func (s *Service) nodeName(ctx context.Context, id string) string {
	if ns, err := s.nodes(ctx); err == nil {
		for _, n := range ns {
			if n.ID == id && n.Name != "" {
				return n.Name
			}
		}
	}
	return id
}

// node finds a node by ID.
func (s *Service) node(ctx context.Context, id string) (Node, error) {
	ns, err := s.nodes(ctx)
	if err != nil {
		return Node{}, err
	}
	for _, n := range ns {
		if n.ID == id {
			if !n.Online {
				return n, errNodeOffline{cmp.Or(n.Name, n.ID)}
			}
			return n, nil
		}
	}
	return Node{}, errNodeUnknown{id}
}

// errNodeUnknown is a node ID the hub doesn't know (a name isn't an ID). It
// isn't stackproto's not_found, which means a stack that doesn't exist yet.
type errNodeUnknown struct{ id string }

func (e errNodeUnknown) Error() string { return "There's no node with the ID " + e.id + "." }

// call runs a verb and decodes its answer. A read gets the call timeout;
// pass timeout 0 for a deploy, which the engine's task bounds instead.
func (s *Service) call(ctx context.Context, node, verb string, req, resp any, timeout time.Duration, progress func(json.RawMessage)) error {
	if _, err := s.node(ctx, node); err != nil {
		return err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if progress == nil {
		progress = func(json.RawMessage) {}
	}
	raw, err := s.caller.Call(ctx, node, verb, req, progress)
	if err != nil {
		if e := stackproto.ErrorFrom(err); e != nil {
			return e
		}
		return fmt.Errorf("%s on node %s: %w", verb, node, err)
	}
	if resp != nil {
		if err := json.Unmarshal(raw, resp); err != nil {
			return fmt.Errorf("%s on node %s: bad answer: %w", verb, node, err)
		}
	}
	return nil
}

// statusOf maps an error from a node to an HTTP status and code.
func statusOf(err error) (int, string, string) {
	if off, ok := errors.AsType[errNodeOffline](err); ok {
		return http.StatusServiceUnavailable, "node_unavailable", off.Error()
	}
	if unk, ok := errors.AsType[errNodeUnknown](err); ok {
		return http.StatusNotFound, "not_found", unk.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "node_timeout", "The node didn't answer in time."
	}
	e := stackproto.ErrorFrom(err)
	if e == nil {
		return http.StatusBadGateway, "node_error", "The node couldn't do that: " + err.Error()
	}
	switch e.Code {
	case stackproto.CodeNotFound:
		return http.StatusNotFound, "not_found", e.Message
	case stackproto.CodeInvalid:
		return http.StatusUnprocessableEntity, "invalid", e.Message
	case stackproto.CodeForbidden:
		return http.StatusForbidden, "forbidden", e.Message
	case stackproto.CodeInternal:
		return http.StatusBadGateway, "node_error", e.Message
	}
	return http.StatusConflict, e.Code, e.Message
}

// ── drafts ───────────────────────────────────────────────────────────────

// SourceWeb is the source of a draft saved in the editor. A git sync
// writes SourceGit followed by the commit's SHA.
const (
	SourceWeb = "web"
	SourceGit = "git:"
)

// Draft is a saved, undeployed compose file.
type Draft struct {
	Content   string    `json:"content"`
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
	Source    string    `json:"source"` // SourceWeb, or "git:<sha>"
}

func (d *Draft) sha() string {
	if d == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(d.Content))
	return hex.EncodeToString(sum[:])
}

func (s *Service) draft(ctx context.Context, node, stack string) (*Draft, error) {
	var d Draft
	var at int64
	err := s.st.Reader().QueryRowContext(ctx,
		`SELECT content, revision, updated_at, updated_by, source FROM stack_drafts WHERE node = ? AND stack = ?`, node, stack).
		Scan(&d.Content, &d.Revision, &at, &d.UpdatedBy, &d.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stacks: read draft: %w", err)
	}
	d.UpdatedAt = time.UnixMilli(at).UTC()
	return &d, nil
}

// drafts lists a node's drafts by stack.
func (s *Service) drafts(ctx context.Context, node string) (map[string]Draft, error) {
	rows, err := s.st.Reader().QueryContext(ctx,
		`SELECT stack, content, revision, updated_at, updated_by, source FROM stack_drafts WHERE node = ?`, node)
	if err != nil {
		return nil, fmt.Errorf("stacks: list drafts: %w", err)
	}
	defer rows.Close()
	out := map[string]Draft{}
	for rows.Next() {
		var name string
		var d Draft
		var at int64
		if err := rows.Scan(&name, &d.Content, &d.Revision, &at, &d.UpdatedBy, &d.Source); err != nil {
			return nil, err
		}
		d.UpdatedAt = time.UnixMilli(at).UTC()
		out[name] = d
	}
	return out, rows.Err()
}

// errStale is a draft saved from an older revision.
type errStale struct{ current int64 }

func (e errStale) Error() string { return "the draft changed since this edit started" }

// saveDraft saves content at the next revision, if from is the current one
// (0 for none). source is SourceWeb for the editor's save, or "git:<sha>"
// for a sync's.
func (s *Service) saveDraft(ctx context.Context, node, stack, content, by, source string, from int64) (*Draft, error) {
	tx, err := s.st.Writer().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var cur int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM stack_drafts WHERE node = ? AND stack = ?`, node, stack).Scan(&cur)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if cur != from {
		return nil, errStale{cur}
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	d := &Draft{Content: content, Revision: cur + 1, UpdatedAt: now, UpdatedBy: by, Source: source}
	_, err = tx.ExecContext(ctx, `INSERT INTO stack_drafts (node, stack, content, revision, updated_at, updated_by, source) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (node, stack) DO UPDATE SET content = excluded.content, revision = excluded.revision,
		updated_at = excluded.updated_at, updated_by = excluded.updated_by, source = excluded.source`,
		node, stack, content, d.Revision, now.UnixMilli(), by, d.Source)
	if err != nil {
		return nil, err
	}
	return d, tx.Commit()
}

// deleteDraft removes a draft; with revision > 0 only if it's still that
// revision (a deploy clears the draft it deployed, not a newer one).
func (s *Service) deleteDraft(ctx context.Context, node, stack string, revision int64) error {
	q, args := `DELETE FROM stack_drafts WHERE node = ? AND stack = ?`, []any{node, stack}
	if revision > 0 {
		q += ` AND revision = ?`
		args = append(args, revision)
	}
	_, err := s.st.Writer().ExecContext(ctx, q, args...)
	return err
}

// ── secrets ──────────────────────────────────────────────────────────────

// Stack secrets live in the vault as stacks/<name>, a namespace of their
// own: a compose file can never reach an integration's credentials
// (instance/…), whatever it references.
const secretPrefix = "stacks/"

var secretNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SecretState is a referenced secret and whether the vault has it.
type SecretState struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

func (s *Service) secretNames(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	if s.vault == nil {
		return out, nil
	}
	names, err := s.vault.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if rest, ok := strings.CutPrefix(n, secretPrefix); ok {
			out[rest] = true
		}
	}
	return out, nil
}

// secretStates says which of names the vault has.
func (s *Service) secretStates(ctx context.Context, names []string) ([]SecretState, error) {
	have, err := s.secretNames(ctx)
	if err != nil {
		return nil, err
	}
	out := []SecretState{}
	for _, n := range sortedUnique(names) {
		out = append(out, SecretState{Name: n, Set: have[n]})
	}
	return out, nil
}

// errSecretMissing names a referenced secret the vault doesn't have.
type errSecretMissing struct{ name string }

func (e errSecretMissing) Error() string {
	return fmt.Sprintf("The secret %q isn't set. Set it under Settings → Stacks → Secrets first.", e.name)
}

// resolveSecrets reads names from the vault, for one request to a node.
// The values live only in the returned map, which the caller drops after
// the call.
func (s *Service) resolveSecrets(ctx context.Context, names []string) (stackproto.Secrets, error) {
	out := stackproto.Secrets{}
	for _, n := range sortedUnique(names) {
		if s.vault == nil {
			return nil, errSecretMissing{n}
		}
		v, err := s.vault.Get(ctx, secretPrefix+n)
		if errors.Is(err, vault.ErrNotFound) {
			return nil, errSecretMissing{n}
		}
		if err != nil {
			return nil, fmt.Errorf("stacks: read secret %q: %w", n, err)
		}
		out[n] = string(v)
	}
	return out, nil
}

// grantSecrets records that the hub sends names to a stack on a node. A
// deploy sends what its draft (the hub's own content) references; a
// rollback may only send names granted this way (stack_secret_grants).
func (s *Service) grantSecrets(ctx context.Context, node, stack string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	now := s.now().UnixMilli()
	return s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		for _, n := range sortedUnique(names) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO stack_secret_grants (node, stack, name, granted_at) VALUES (?, ?, ?, ?)
				ON CONFLICT (node, stack, name) DO NOTHING`, node, stack, n, now); err != nil {
				return fmt.Errorf("stacks: record secret grant: %w", err)
			}
		}
		return nil
	})
}

// errSecretNotGranted is a rollback naming a secret no deploy ever sent to
// that stack on that node.
type errSecretNotGranted struct{ name, stack string }

func (e errSecretNotGranted) Error() string {
	return fmt.Sprintf("The version to restore references the secret %q, which Cha Dash never deployed to %s, so it isn't sent. Deploy that version from a draft instead.", e.name, e.stack)
}

// grantedOnly keeps the rollback from sending any secret the stack wasn't
// granted: the node says which names the old version references, and a
// node can't be trusted to ask only for its own.
func (s *Service) grantedOnly(ctx context.Context, node, stack string, names []string) error {
	granted := map[string]bool{}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT name FROM stack_secret_grants WHERE node = ? AND stack = ?`, node, stack)
	if err != nil {
		return fmt.Errorf("stacks: read secret grants: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		granted[n] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, n := range sortedUnique(names) {
		if !granted[n] {
			return errSecretNotGranted{name: n, stack: stack}
		}
	}
	return nil
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// secretRefs lists ${secret:name} references in a file's text, as the node
// reads them ($${secret:…} is a literal).
func secretRefs(content string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(content[i:], "${secret:")
		if j < 0 {
			break
		}
		at := i + j
		i = at + len("${secret:")
		if at > 0 && content[at-1] == '$' && (at < 2 || content[at-2] != '$') {
			continue
		}
		end := strings.IndexByte(content[i:], '}')
		if end < 0 {
			break
		}
		if name := content[i : i+end]; secretNameRE.MatchString(name) {
			out = append(out, name)
		}
	}
	return sortedUnique(out)
}
