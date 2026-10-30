// Package stackproto is the wire contract between the hub's stacks API
// (internal/stacks) and the node's stacks module (internal/node/stacks):
// verb names, the local-policy permissions they need, and the request,
// response and progress shapes.
//
// It imports only the standard library, so the hub can speak it without
// linking the Compose SDK or go-git, which only the node may link.
package stackproto

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Verbs the stacks module serves.
const (
	VerbList     = "stack.list"
	VerbGet      = "stack.get"
	VerbValidate = "stack.validate"
	VerbPlan     = "stack.plan"
	VerbDeploy   = "stack.deploy"
	VerbRollback = "stack.rollback"
	VerbDetect   = "stack.detect"
	VerbAdopt    = "stack.adopt"
	VerbUnadopt  = "stack.unadopt"

	// Served from v0.3 (waves 12 and 14). Until then the node answers them
	// as it answers any verb it doesn't know.
	VerbRemoteCheck      = "stack.remote.check"
	VerbFetch            = "stack.fetch"
	VerbMove             = "stack.move"
	VerbSelfUpdate       = "stack.selfupdate"
	VerbSelfUpdateResult = "stack.selfupdate.result"
)

// Local policy permissions (policy.yaml's stacks.allow: read, deploy,
// adopt, sync, selfupdate). stack.move needs PermAdopt.
const (
	PermRead       = "stacks.read"
	PermDeploy     = "stacks.deploy"
	PermAdopt      = "stacks.adopt"
	PermSync       = "stacks.sync"       // stack.remote.check and stack.fetch, to the hosts in stacks.remotes
	PermSelfUpdate = "stacks.selfupdate" // stack.selfupdate and its result; off unless the host's policy turns it on
)

// Stack statuses, from the containers Docker reports for the project.
const (
	StatusRunning = "running" // every container runs
	StatusPartial = "partial" // some do
	StatusStopped = "stopped" // none does
	StatusDown    = "down"    // no containers at all
	StatusUnknown = "unknown"
)

// Locations of a managed stack.
const (
	LocationRoot     = "root"     // <root>/<name>/compose.yaml
	LocationExternal = "external" // adopted in place elsewhere; a shadow copy is versioned
)

// Commit kinds, from each commit's Chadash-Kind trailer.
const (
	KindDeploy     = "deploy"
	KindRollback   = "rollback"
	KindAdopt      = "adopt"
	KindUnadopt    = "unadopt"
	KindAccept     = "accept" // accepting edits made outside Cha Dash
	KindSelfUpdate = "self-update"
	KindOther      = "other"
)

// Deploy outcomes.
const (
	OutcomeDeployed   = "deployed"    // committed, up, and through the health gate
	OutcomeUnchanged  = "unchanged"   // the content equals the last commit; nothing done
	OutcomeBlocked    = "blocked"     // the preflight refused it; nothing was touched
	OutcomeRolledBack = "rolled_back" // the gate failed; the previous version is healthy again
	OutcomeStuck      = "stuck"       // the rollback failed too
)

// Error codes a verb fails with.
const (
	CodeNotFound    = "not_found"
	CodeInvalid     = "invalid"     // the request or the compose file is invalid
	CodeProtected   = "protected"   // Cha Dash's own stack (view and diff only), or a deploy that would touch a protected container
	CodeDrift       = "drift"       // the file on disk differs from the last commit
	CodeConflict    = "conflict"    // ExpectHead doesn't match: someone deployed meanwhile
	CodeBusy        = "busy"        // another operation holds the stack
	CodeNotManaged  = "not_managed" // the stack exists but isn't adopted yet
	CodeExists      = "exists"      // adopting a name that's taken
	CodeUnadoptable = "unadoptable"
	CodeForbidden   = "forbidden" // the node's policy refuses it, such as a remote outside stacks.remotes
	CodeInternal    = "internal"
)

// Error is a verb's failure: a stable code and a sentence the web can show
// as is. Error() is "<code>: <message>", so the code survives a transport
// that only carries the error's text (see ErrorFrom).
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf returns an *Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

var knownCodes = map[string]bool{
	CodeNotFound: true, CodeInvalid: true, CodeProtected: true, CodeDrift: true, CodeConflict: true,
	CodeBusy: true, CodeNotManaged: true, CodeExists: true, CodeUnadoptable: true, CodeForbidden: true,
	CodeInternal: true,
}

// ErrorFrom recovers an *Error from what a Caller returned: the error
// itself, or its text if the transport flattened it to "<code>: <message>"
// (possibly behind a prefix such as "node local: "). Anything else is nil.
func ErrorFrom(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	s := err.Error()
	for {
		code, msg, ok := strings.Cut(s, ": ")
		if !ok {
			return nil
		}
		if knownCodes[code] {
			return &Error{Code: code, Message: msg}
		}
		s = msg
	}
}

// Secrets are the values of the ${secret:name} references a file uses, by
// name. The hub reads them from its vault and sends them with plan, deploy
// and rollback requests; the node substitutes them in memory and never
// writes them anywhere. Printing or logging Secrets shows only how many
// there are.
type Secrets map[string]string

func (s Secrets) String() string                { return fmt.Sprintf("[%d secrets redacted]", len(s)) }
func (s Secrets) GoString() string              { return s.String() }
func (s Secrets) LogValue() slog.Value          { return slog.StringValue(s.String()) }
func (s Secrets) Format(f fmt.State, verb rune) { _, _ = f.Write([]byte(s.String())) }

var _ interface{ LogValue() slog.Value } = Secrets(nil)

// ── stack.list ───────────────────────────────────────────────────────────

// ListRequest takes nothing.
type ListRequest struct{}

// ListResponse lists the managed stacks.
type ListResponse struct {
	Root   string    `json:"root"`
	Stacks []Summary `json:"stacks"`
}

// Summary is a managed stack in a list.
type Summary struct {
	Name      string  `json:"name"`
	Status    string  `json:"status"`
	Services  int     `json:"services"`
	Running   int     `json:"running"`
	Location  string  `json:"location"`
	Path      string  `json:"path"` // the stack's directory
	File      string  `json:"file"` // the compose file's name in it
	Protected bool    `json:"protected"`
	Drift     bool    `json:"drift"`
	Head      *Commit `json:"head"`
}

// Commit is one commit in a stack's history.
type Commit struct {
	Commit  string    `json:"commit"`
	Short   string    `json:"short"`
	Subject string    `json:"subject"`
	Author  string    `json:"author,omitempty"` // who asked (the Chadash-Actor trailer)
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
}

// ── stack.detect ─────────────────────────────────────────────────────────

// DetectResponse lists compose projects the node could adopt.
type DetectResponse struct {
	Projects []Detected `json:"projects"`
}

// Detected is a compose project that isn't managed yet: running ones found
// by their labels, and directories in the stacks root nobody adopted.
type Detected struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Services    int      `json:"services"`
	Running     int      `json:"running"`
	Path        string   `json:"path"`
	ConfigFiles []string `json:"config_files"`
	Adoptable   bool     `json:"adoptable"`
	Protected   bool     `json:"protected,omitempty"` // Cha Dash's own project: Reason says so
	Reason      string   `json:"reason,omitempty"`    // why not, when it isn't
	// Moveable says stack.move can bring it into the root; MoveRefusal
	// says why not, when it can't.
	Moveable    bool   `json:"moveable"`
	MoveRefusal string `json:"move_refusal,omitempty"`
}

// ── stack.get ────────────────────────────────────────────────────────────

// GetRequest names a stack. At, when set, asks for the file as it was in
// that commit (Content and Secrets then describe that version).
type GetRequest struct {
	Stack string `json:"stack"`
	At    string `json:"at,omitempty"`
}

// Stack is one stack in full. A name that isn't a stack yet answers
// not_found.
type Stack struct {
	Summary
	Managed         bool           `json:"managed"`
	ProtectedReason string         `json:"protected_reason,omitempty"`
	Content         string         `json:"content"` // the file at the last commit (on disk when not managed)
	Env             EnvInfo        `json:"env"`
	Meta            map[string]any `json:"meta"`
	Containers      []Service      `json:"containers"`
	History         []Commit       `json:"history"`
	Secrets         []string       `json:"secrets"` // ${secret:…} names the committed file references
}

// EnvInfo describes the .env next to the file: whether there is one and its
// keys. Values never leave the node.
type EnvInfo struct {
	Present bool     `json:"present"`
	Keys    []string `json:"keys"`
}

// Service is one running service's state.
type Service struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	State    string `json:"state"`
	Health   string `json:"health"` // healthy, unhealthy, starting, none (no healthcheck)
	Restarts int    `json:"restarts"`
}

// ── stack.validate ───────────────────────────────────────────────────────

// ValidateRequest is a proposed file for a stack (which may not exist yet).
type ValidateRequest struct {
	Stack   string `json:"stack"`
	Content string `json:"content"`
}

// Message severities.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Message is one validation finding.
type Message struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Line     int    `json:"line,omitempty"`
	Service  string `json:"service,omitempty"`
}

// ValidateResponse is what the loader thought of it. Secret references are
// checked for syntax, never resolved.
type ValidateResponse struct {
	OK       bool           `json:"ok"`
	Messages []Message      `json:"messages"`
	Secrets  []string       `json:"secrets"`
	Meta     map[string]any `json:"meta"`
}

// ── stack.plan ───────────────────────────────────────────────────────────

// PlanRequest asks what deploying Content would do. Secrets must hold
// every reference, since they're part of each service's config hash.
type PlanRequest struct {
	Stack   string  `json:"stack"`
	Content string  `json:"content"`
	Secrets Secrets `json:"secrets"`
}

// Service plan actions.
const (
	ActionCreate    = "create"
	ActionRecreate  = "recreate"
	ActionRemove    = "remove"
	ActionUnchanged = "unchanged"
)

// Plan is what a deploy would do, predicted from compose config hashes
// against the labels on the running containers.
type Plan struct {
	Head      string        `json:"head"`    // the commit it was planned against ("" for none)
	Changed   bool          `json:"changed"` // the content differs from the last commit
	Services  []ServicePlan `json:"services"`
	DowntimeS float64       `json:"downtime_s"` // rough: ~1.5 s per recreated service
	// RecreateAll is set when no running container's hash matches although
	// the file is unchanged: a different Compose version stamped them, so
	// the first deploy recreates everything once.
	RecreateAll bool `json:"recreate_all,omitempty"`
	// RootEquivalent lists what the file does that amounts to root on the
	// node's host (privileged, host namespaces, devices, cap_add, host
	// paths), one sentence each, for the deploy confirmation. The node's
	// policy (stacks.deny) can refuse these; it allowed them here.
	RootEquivalent []string `json:"root_equivalent,omitempty"`
}

// ServicePlan is one service's fate.
type ServicePlan struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Pull   bool   `json:"pull,omitempty"` // its image will be pulled first
	Image  string `json:"image,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ── stack.deploy and stack.rollback ──────────────────────────────────────

// DeployRequest deploys Content through the seven steps. ExpectHead, when
// set, must be the stack's last commit (the one the plan was made
// against). By, Via and Ref end up in the commit message.
type DeployRequest struct {
	Stack      string  `json:"stack"`
	Content    string  `json:"content"`
	Secrets    Secrets `json:"secrets"`
	ExpectHead string  `json:"expect_head,omitempty"`
	By         string  `json:"by,omitempty"`  // who asked: "sam"
	Via        string  `json:"via,omitempty"` // web, api
	Ref        string  `json:"ref,omitempty"` // the hub's task ID
	// Source is the git commit the content came from, "<url>@<sha>", for a
	// deploy a sync started. It becomes a Chadash-Source trailer.
	Source string `json:"source,omitempty"`
}

// RollbackRequest restores the file of an earlier commit as a new commit,
// pinned to the image digests it last ran, through the same gate.
type RollbackRequest struct {
	Stack      string  `json:"stack"`
	Commit     string  `json:"commit"`
	Secrets    Secrets `json:"secrets"`
	ExpectHead string  `json:"expect_head,omitempty"`
	By         string  `json:"by,omitempty"`
	Via        string  `json:"via,omitempty"`
	Ref        string  `json:"ref,omitempty"`
}

// DeployResult says how a deploy (or a rollback) ended. Only setup
// failures are errors; blocked, rolled_back and stuck are outcomes.
type DeployResult struct {
	Outcome        string `json:"outcome"`
	Commit         string `json:"commit,omitempty"`          // the attempt's commit
	RollbackCommit string `json:"rollback_commit,omitempty"` // the automatic rollback's, if any
	Reason         string `json:"reason,omitempty"`          // why it was blocked or rolled back, a sentence
	Plan           Plan   `json:"plan"`
	DurationMS     int64  `json:"duration_ms"`
}

// ── stack.adopt and stack.unadopt ────────────────────────────────────────

// AdoptRequest adopts a project in place, or accepts the edits made outside
// Cha Dash to a managed stack.
type AdoptRequest struct {
	Stack string `json:"stack"`
	By    string `json:"by,omitempty"`
	Via   string `json:"via,omitempty"`
}

// AdoptResult says what adopting did. Nothing restarts and nothing moves.
type AdoptResult struct {
	Commit   string `json:"commit"`
	Kind     string `json:"kind"` // adopt or accept
	Location string `json:"location"`
	Path     string `json:"path"`
}

// UnadoptRequest stops managing a stack. Its files stay where they are and
// its containers keep running; only the stacks repo forgets it.
type UnadoptRequest struct {
	Stack string `json:"stack"`
	By    string `json:"by,omitempty"`
	Via   string `json:"via,omitempty"`
}

// UnadoptResult is the commit that recorded it.
type UnadoptResult struct {
	Commit string `json:"commit"`
}

// ── stack.remote.check and stack.fetch ───────────────────────────────────

// Remote auth kinds.
const (
	AuthNone  = "none"
	AuthHTTPS = "https"
	AuthSSH   = "ssh"
)

// RemoteAuth is how the node signs in to a git remote: nothing, an HTTPS
// user and token, or an SSH key (Secret holds the private key). The hub
// fills it in from its vault for each call, and the node never writes it
// down. Printing or logging it shows the kind and the user, never the
// secret.
type RemoteAuth struct {
	Kind     string `json:"kind"`
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret,omitempty"`
}

func (a RemoteAuth) String() string {
	s := "{kind: " + a.Kind
	if a.Username != "" {
		s += ", username: " + a.Username
	}
	if a.Secret != "" {
		s += ", secret: [redacted]"
	}
	return s + "}"
}
func (a RemoteAuth) GoString() string              { return a.String() }
func (a RemoteAuth) LogValue() slog.Value          { return slog.StringValue(a.String()) }
func (a RemoteAuth) Format(f fmt.State, verb rune) { _, _ = f.Write([]byte(a.String())) }

var _ interface{ LogValue() slog.Value } = RemoteAuth{}

// RemoteCheckRequest asks for a branch's tip without fetching. A URL with
// a user in it fails invalid, and a host outside the node's stacks.remotes
// fails forbidden.
type RemoteCheckRequest struct {
	URL    string     `json:"url"`
	Branch string     `json:"branch"`
	Auth   RemoteAuth `json:"auth"`
}

// RemoteCheckResponse is the branch's tip. HostKey is the SSH host key the
// remote presented, for the hub to pin on first use.
type RemoteCheckResponse struct {
	SHA     string `json:"sha"`
	HostKey string `json:"host_key,omitempty"`
}

// FetchRequest fetches a branch's tip (shallow, into the node's cache) and
// reads one file from it. HostKey, when set, is the pinned SSH host key
// the remote must present. It fails like stack.remote.check.
type FetchRequest struct {
	URL     string     `json:"url"`
	Branch  string     `json:"branch"`
	Path    string     `json:"path"`
	Auth    RemoteAuth `json:"auth"`
	HostKey string     `json:"host_key,omitempty"`
}

// FetchResponse is the file at the tip and the commit it came from.
type FetchResponse struct {
	SHA     string `json:"sha"`
	Content string `json:"content"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
}

// ── stack.move ───────────────────────────────────────────────────────────

// MoveRequest brings a project into the root, keeping its name. Exactly
// one of FromPath (the node reads the file there) and Content (the hub
// supplies it, from an importer) is set; Env is the .env to write beside
// supplied content. Nothing restarts.
type MoveRequest struct {
	Stack    string `json:"stack"`
	FromPath string `json:"from_path,omitempty"`
	Content  string `json:"content,omitempty"`
	Env      string `json:"env,omitempty"`
	By       string `json:"by,omitempty"`
	Via      string `json:"via,omitempty"`
}

// MoveResult is the commit that recorded the move, each relative path it
// made absolute, and what the first deploy will do.
type MoveResult struct {
	Commit   string    `json:"commit"`
	Rewrites []Rewrite `json:"rewrites"`
	Plan     Plan      `json:"plan"`
}

// Rewrite is one line a move changed: a relative path made absolute.
type Rewrite struct {
	Line int    `json:"line"`
	From string `json:"from"`
	To   string `json:"to"`
}

// ── stack.selfupdate and stack.selfupdate.result ─────────────────────────

// SelfUpdateRequest updates the hub's own container to Image (a tag; the
// node pins its digest). It streams Progress like stack.deploy, then
// answers a SelfUpdateResult.
type SelfUpdateRequest struct {
	Image string `json:"image"`
	By    string `json:"by,omitempty"`
	Via   string `json:"via,omitempty"`
	Ref   string `json:"ref,omitempty"`
}

// SelfUpdateResultRequest asks for the last self-update's result.
type SelfUpdateResultRequest struct{}

// SelfUpdateResult says how a self-update ended (OutcomeDeployed,
// OutcomeRolledBack or OutcomeStuck), with the image digests before and
// after.
type SelfUpdateResult struct {
	Outcome  string `json:"outcome"`
	Previous string `json:"previous"`
	Current  string `json:"current"`
}

// ── progress ─────────────────────────────────────────────────────────────

// Progress is one streamed update from a long verb: a new step, a log
// line, or how far along it is. Never a secret.
type Progress struct {
	Step     string   `json:"step,omitempty"`
	Line     string   `json:"line,omitempty"`
	Fraction *float64 `json:"fraction,omitempty"`
}
