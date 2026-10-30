// Package integration is the contract every integration implements: the
// Integration and Instance interfaces, the Manifest with its config JSON
// Schema, TestResult, the facts an instance emits, and the registry.
//
// An integration is one kind of source (Proxmox, Sonarr, the built-in
// checks). An instance is one configured connection to it; a home can have
// many. The framework (internal/runtime) owns the boilerplate around an
// instance: scheduling, backoff, the HTTP client and its TLS policy, secrets,
// repairs and status. An integration only connects, tests, and turns what it
// reads into facts.
//
// A Tier 0 integration in ten lines:
//
//	type sonarr struct{}
//
//	func init() { integration.Register(sonarr{}) }
//
//	func (sonarr) Manifest() integration.Manifest {
//		return integration.Manifest{ID: "sonarr", Name: "Sonarr", Icon: "sonarr", Version: "0.1.0",
//			Tier: integration.Bronze, Capabilities: []integration.Capability{"media.calendar"},
//			ConfigSchema: integration.Object(integration.URLProp(), integration.TLSProp(),
//				integration.Prop("api_key", integration.Secret("API key"))).Require("url", "api_key")}
//	}
//
//	func (sonarr) Connect(ctx context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
//		return &instance{url: cfg.String("url"), key: cfg.String("api_key"), d: d}, nil
//	}
//
// The instance's Sync fetches with d.HTTP, checks the response with
// CheckResponse (a 401 or 403 becomes a Repair) and publishes what it read
// with d.Emit.Replace.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"
)

// Integration is one kind of source. Implementations register themselves
// at init with Register and must be safe for concurrent use: the hub calls
// Connect once per instance, and again on every restart or test.
type Integration interface {
	Manifest() Manifest
	// Connect builds an instance from a validated config whose secrets are
	// resolved. It should not do network I/O: Test and Sync do that, so a
	// broken upstream never stops an instance from starting.
	Connect(ctx context.Context, cfg Config, deps Deps) (Instance, error)
}

// Instance is one configured connection. The runtime calls Sync from a
// single goroutine; Test may run concurrently with it on a separate
// instance from a separate Connect.
type Instance interface {
	// Test is the setup wizard's "Test connection": reach the source, detect
	// its version and read back what the credential can do. It must not emit
	// facts.
	Test(ctx context.Context) TestResult
	// Sync reads the source and emits facts through Deps.Emit. want says what
	// is on screen; an integration may skip work nobody is watching. Return an
	// error when the source couldn't be read: the runtime classifies it (see
	// Classify), backs off, marks the instance's series stale, and turns auth
	// failures into a Repair.
	Sync(ctx context.Context, want Interest) error
	// Close releases connections and sessions.
	Close() error
}

// Subscriber is an optional push channel (Docker events, Home Assistant's
// WebSocket, OPNsense SSE). The runtime runs Subscribe in its own goroutine
// while someone is watching and cancels ctx when nobody is. A returned error
// is retried with backoff; Sync keeps polling meanwhile.
type Subscriber interface {
	Subscribe(ctx context.Context, emit Emitter) error
}

// Intervaler lets an instance override Manifest.Poll.Interval from its
// config (the checks integration polls at its fastest target's interval).
type Intervaler interface {
	Interval() time.Duration
}

// InterestIntervaler is an optional Instance interface that picks the sync
// interval for what's watched; it wins over Intervaler. The runtime asks
// again whenever the interest changes, so a closely watched resource can
// get faster syncs at once.
type InterestIntervaler interface {
	IntervalFor(want Interest) time.Duration
}

// ConfigValidator is an optional check on Integration that runs after the
// JSON Schema, for rules the schema subset can't express ("http targets need
// a url"). The config's secrets are resolved.
type ConfigValidator interface {
	ValidateConfig(cfg Config) []FieldError
}

// Tier is the quality tier shown in the UI (03 · Architecture, quality tiers).
type Tier string

// Quality tiers.
const (
	Bronze Tier = "bronze" // reads basics; config schema; fixture tests
	Silver Tier = "silver" // repairs, version detection, diagnostics
	Gold   Tier = "gold"   // realtime where available, scoped actions, multi-version fixtures
)

// Capability is a contract widgets bind to: "checks.uptime",
// "compute.guests", "media.requests", … (03 · Architecture, capability
// contracts).
type Capability string

// Engine says how an integration is implemented.
type Engine string

// Engines.
const (
	EngineGo   Engine = "go"   // Tier 0: Go code in integrations/<id>
	EngineSpec Engine = "spec" // Tier 1: a YAML spec in specs/<id>.yaml
)

// Poll tunes an integration's coordinator. Zero fields take the defaults.
type Poll struct {
	// Interval between syncs while someone is watching. Default 30 s.
	Interval time.Duration
	// Idle is the interval while nobody is watching. Default 5 min, so health
	// and repairs stay current without polling at full rate.
	Idle time.Duration
	// Background keeps Interval even when nobody is watching: uptime checks,
	// whose job is to raise problems in the inbox.
	Background bool
	// Timeout bounds one Sync. Default 30 s.
	Timeout time.Duration
}

// Poll defaults and bounds.
const (
	DefaultInterval = 30 * time.Second
	DefaultIdle     = 5 * time.Minute
	DefaultTimeout  = 30 * time.Second
	MinInterval     = 5 * time.Second
)

// WithDefaults fills zero fields and clamps the interval to MinInterval.
func (p Poll) WithDefaults() Poll {
	if p.Interval <= 0 {
		p.Interval = DefaultInterval
	}
	p.Interval = max(p.Interval, MinInterval)
	if p.Idle <= 0 {
		p.Idle = DefaultIdle
	}
	p.Idle = max(p.Idle, p.Interval)
	if p.Timeout <= 0 {
		p.Timeout = DefaultTimeout
	}
	return p
}

// Manifest describes an integration. The catalog endpoint serves the fields
// with JSON names; the rest are for the runtime.
type Manifest struct {
	ID           string       `json:"id"`      // one of the IDs in docs/plan/07-v0.1.md
	Name         string       `json:"name"`    // display name
	Icon         string       `json:"icon"`    // icon slug, resolved by the web app
	Version      string       `json:"version"` // the integration's own version
	Tier         Tier         `json:"tier"`
	Capabilities []Capability `json:"capabilities"`
	// Aliases are other names the same upstream goes by (Seerr is also
	// Overseerr and Jellyseerr), for search and the add list. Optional.
	Aliases []string `json:"aliases,omitempty"`
	// ConfigSchema is the instance config as JSON Schema (the subset in
	// schema.go). Secret fields are top-level strings marked WriteOnly.
	ConfigSchema *Schema `json:"config_schema"`
	// Control says where an Actor's actions get their credential: a
	// control group of fields, or a mode (see Control). Optional.
	Control *Control `json:"control,omitempty"`

	Engine Engine `json:"-"` // EngineGo when empty
	Poll   Poll   `json:"-"`
}

// SecretFields lists the manifest's write-only fields, in schema order.
func (m Manifest) SecretFields() []string {
	if m.ConfigSchema == nil {
		return nil
	}
	var out []string
	for _, p := range m.ConfigSchema.Properties {
		if p.Schema.WriteOnly {
			out = append(out, p.Name)
		}
	}
	return out
}

// IsSecret reports whether field is one of the manifest's write-only fields.
func (m Manifest) IsSecret(field string) bool {
	return slices.Contains(m.SecretFields(), field)
}

// Interest is what someone is looking at. The runtime derives it from the
// realtime subscriptions (see runtime.Watch); today it's hub-wide, not per
// widget, so treat it as a hint.
type Interest struct {
	// Watched is true while any browser tab subscribes to the graph or the
	// metrics. Idle syncs (Watched false) should still report health.
	Watched bool
	// Kinds narrows what's on screen when every graph subscription is a
	// graph:kind/… filter; nil means every kind.
	Kinds []string
	// Metrics is true while someone subscribes to the metrics topic.
	Metrics bool
	// Resources lists the resources someone watches closely, sorted: a
	// sheet open on each (the metrics:resource/<id> topic). Publish their
	// costly series, such as a container's stats, only while listed.
	Resources []string
	// Refresh is true on a sync someone asked for (Check now): read
	// everything now, ignoring any schedule of the integration's own.
	Refresh bool
	// Record is true when nobody is watching but the hub keeps history
	// (internal/history): Watched and Metrics are then true too, and every
	// kind is wanted, so the integration reads what a board would show,
	// at most once a minute. Resources stays empty: what only a sheet
	// asks for is never recorded while nobody looks.
	Record bool
}

// WantsKind reports whether resources of kind are on screen.
func (i Interest) WantsKind(kind string) bool {
	return i.Kinds == nil || slices.Contains(i.Kinds, kind)
}

// WantsResource reports whether someone watches the resource id closely.
func (i Interest) WantsResource(id string) bool {
	_, ok := slices.BinarySearch(i.Resources, id)
	return ok
}

// Deps are what the framework hands an instance.
type Deps struct {
	// HTTP is the instance's client: the config's tls policy (a TOFU pin is
	// recorded in the instance config on first use), timeouts, User-Agent,
	// a response size cap, and redirects only within the same origin.
	HTTP *http.Client
	// NewHTTPClient builds another client, e.g. with a Session or for a
	// second origin. A zero HTTPOptions.TLS inherits the instance's policy and
	// its TOFU persistence.
	NewHTTPClient func(HTTPOptions) (*http.Client, error)
	// Emit publishes facts into the Home Graph, the metrics and the inbox.
	Emit Emitter
	// Log is scoped to the instance. Secret values are scrubbed from it.
	Log *slog.Logger
	// Now is the clock; use it for facts and incident times.
	Now func() time.Time
	// UpdateConfig persists a change to the instance's non-secret config
	// without restarting it (TOFU pins for per-target TLS, discovered IDs).
	UpdateConfig func(ctx context.Context, mutate func(cfg map[string]any)) error
	// State keeps what the instance accumulates across restarts. Never nil:
	// an unsaved config under test gets one in memory.
	State State
	// Nodes reaches chadash-node's Docker verbs, for the Docker
	// integration's node transport (reads, and its actions). Nil when the
	// hub serves no nodes. See NodeCaller.
	Nodes NodeCaller
}

// NodeCaller runs a verb on a connected chadash-node: node is the node's
// name ("local") or ID, progress receives stream items (log lines) and may
// be nil. internal/nodes implements it with the same signature as its
// Caller; integrations see only this interface, since they import nothing
// but this package. The hub restricts it to the Docker verbs (engine.*,
// container.*), and the node's own policy decides the rest.
//
// A failure the node or the hub reports has a code: NodeErrorCode reads it
// ("forbidden" by the node's policy, "not_found", "conflict",
// "unavailable", "not_connected").
type NodeCaller interface {
	Call(ctx context.Context, node, verb string, req any, progress func(json.RawMessage)) (json.RawMessage, error)
}

// NodeErrorCode is the code of an error a NodeCaller returned, or "" for
// one without (a context error, say).
func NodeErrorCode(err error) string {
	if c, ok := errors.AsType[interface {
		error
		ErrorCode() string
	}](err); ok {
		return c.ErrorCode()
	}
	return ""
}

// MaxStateBytes caps one State value.
const MaxStateBytes = 64 << 10

// State is a small key-value store per instance that survives restarts and
// config changes, for what an integration accumulates over time (OPNsense's
// 30-day WAN totals), never as a cache of what it reads. Values are JSON, at
// most MaxStateBytes each, and are deleted with the instance.
type State interface {
	// Load returns the value stored under key, or nil when there is none.
	Load(ctx context.Context, key string) ([]byte, error)
	// Save stores value under key, replacing what was there.
	Save(ctx context.Context, key string, value []byte) error
}

// RedirectPolicy says which redirects a client follows.
type RedirectPolicy int

// Redirect policies.
const (
	// SameOrigin follows up to 5 redirects within the request's origin and
	// refuses the rest, so credentials never leave it.
	SameOrigin RedirectPolicy = iota
	// NoRedirects returns the redirect response itself (uptime checks: a 302
	// to a login page means the app is up).
	NoRedirects
)

// HTTPOptions configure Deps.NewHTTPClient.
type HTTPOptions struct {
	TLS       TLSPolicy         // zero: the instance's policy
	OnPin     func(pin string)  // TOFU: called when a pin is first recorded (explicit TLS only)
	Redirects RedirectPolicy    // default SameOrigin
	Session   Session           // optional login session
	Timeout   time.Duration     // per request; default 15 s
	Headers   map[string]string // added to every request that doesn't set them
	// UnixSocket, when set, is the path of a Unix socket every request is
	// dialled to instead of the URL's host (Docker's /var/run/docker.sock).
	// Use an http:// URL; its host only names the connection.
	UnixSocket string
}

// Session is the session cache hook: log in once, apply the session to each
// request, and log in again when a response says it expired.
type Session interface {
	// Login establishes a session using c, a client without the session hook.
	Login(ctx context.Context, c *http.Client) error
	// Apply adds the session (cookie, token header) to an outgoing request.
	Apply(r *http.Request)
}

// SessionLogout is an optional Session method, called when the instance stops.
type SessionLogout interface {
	Logout(ctx context.Context, c *http.Client) error
}

// SessionExpiry is an optional Session method. The default treats 401 and
// 403 as an expired session: log in again and retry once.
type SessionExpiry interface {
	Expired(resp *http.Response) bool
}
