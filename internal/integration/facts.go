package integration

import (
	"math"
	"strconv"
	"time"
)

// Facts are what an instance knows about the home: resources and relations
// for the Home Graph, and metric samples for the metrics topic. The shapes
// follow the Demo Home's (sim/README.md), so the web app treats demo and real
// data the same way.
type Facts struct {
	Resources []Resource
	Relations []Relation
	Metrics   []Metric
}

// Emitter publishes an instance's facts. Each instance gets its own; it is
// safe for concurrent use (Sync and Subscribe may share it).
type Emitter interface {
	// Self is the ID of the instance's own resource,
	// "integration:<instance id>". The framework maintains that resource
	// (status, version, health); relate to it, e.g. Self monitors what it
	// reads.
	Self() string
	// Replace makes scope hold exactly f: resources, relations and series the
	// instance published under scope before and doesn't list now are
	// removed. Re-publishing identical facts costs nothing on the wire. Use
	// one scope ("") for everything, or several when parts of the source
	// refresh at different rates ("guests", "backups"). Relations whose
	// endpoints don't exist (yet) are skipped, not errors.
	Replace(scope string, f Facts) error
	// Raise opens or updates an inbox incident. The ID is local to the
	// instance; the framework namespaces it, fills the timestamps and keeps
	// OpenedAt while the incident stays open.
	Raise(in Incident) error
	// Resolve marks an open incident resolved ("auto") with a sentence saying
	// what happened. Resolving one that isn't open does nothing.
	Resolve(id, resolution string) error
	// SetVersion records the upstream version (status and diagnostics).
	SetVersion(v string)
	// SetWarnings replaces the instance's sync-time warnings. Any warning
	// makes the instance "degraded".
	SetWarnings(ws []Warning)
}

// Resource is one thing in the home. IDs are stable and namespaced
// ("check:<instance>/<slug>", "pve:<domain>/qemu/<vmid>"); kinds and
// relation types use the Home Graph vocabulary (sim/README.md).
type Resource struct {
	ID      string
	Kind    string
	Name    string
	State   string // actual state: running, stopped, online, error, …
	Desired string // desired state, when the source knows it
	Health  Health
	Labels  map[string]string
	Attrs   map[string]any // JSON-like values; facts that change at discrete moments only
	Links   []Link
	Caps    []string // capabilities widgets can bind to
}

// Health statuses. HealthRepair marks a source Cha Dash can't read, which
// isn't an outage.
const (
	HealthOK          = "ok"
	HealthAttention   = "attention"
	HealthDown        = "down"
	HealthUnknown     = "unknown"
	HealthMaintenance = "maintenance"
	HealthRepair      = "repair"
)

// Health is a resource's health with its reason. Since may be left zero: the
// emitter then keeps the time the status last changed.
type Health struct {
	Status string
	Reason string
	Since  time.Time
}

// Link is a deep link to the service or its native UI.
type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Relation is a typed edge, From <Type> To: runs_on, part_of, depends_on,
// stores_on, backed_up_by, provides_network_for, routes_to, exposes,
// monitors.
type Relation struct {
	From string
	Type string
	To   string
}

// Metric is one sample of one series, with the series' scale. Value is
// math.NaN() when unknown; it's shown as unknown, never as 0.
type Metric struct {
	Resource string     // resource ID
	Key      string     // metric key: "http.ms", "cpu.pct", "requests.pending"
	Unit     string     // "ms", "%", "count", "°C", "Mb/s"
	Min, Max float64    // the scale
	Band     [2]float64 // the normal range, drawn as a gray band
	Warn     float64    // optional thresholds (0: none)
	Crit     float64
	Value    float64
}

// Unknown is the Metric value for a missing sample.
func Unknown() float64 { return math.NaN() }

// Num is a metric value on the wire: NaN and ±Inf encode as null.
type Num float64

// Valid reports whether n holds a known value.
func (n Num) Valid() bool { return !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0) }

// MarshalJSON encodes n in its shortest form, or null when unknown.
func (n Num) MarshalJSON() ([]byte, error) {
	if !n.Valid() {
		return []byte("null"), nil
	}
	f := float64(n)
	if f == 0 {
		f = 0 // no "-0"
	}
	return strconv.AppendFloat(nil, f, 'f', -1, 64), nil
}

// UnmarshalJSON decodes a number or null.
func (n *Num) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*n = Num(math.NaN())
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return err
	}
	*n = Num(f)
	return nil
}

// Incident classes, severities and statuses (the simulator's vocabulary).
const (
	ClassProblem = "problem" // something in the home needs attention
	ClassRepair  = "repair"  // an integration needs the user; not an outage

	SeverityInfo      = "info"
	SeverityAttention = "attention"
	SeverityDown      = "down"

	StatusOpen     = "open"
	StatusResolved = "resolved"

	// CategorySecurity marks a security finding: an exposure or a
	// credential-hygiene hint. The integration still declares its severity
	// (attention for a real exposure, info for advice); the hub's security
	// posture decides the severity the inbox publishes (internal/security).
	CategorySecurity = "security"
)

// Incident is an inbox item, in exactly the shape of the simulator's
// incidents (sim/README.md, "What an incident carries").
type Incident struct {
	ID           string         `json:"id"`
	Scenario     string         `json:"scenario"` // what kind of incident: auth-rejected, check-failed, …
	Class        string         `json:"class"`
	Severity     string         `json:"severity"`
	Category     string         `json:"category,omitempty"` // CategorySecurity, or empty
	Status       string         `json:"status"`
	Phase        string         `json:"phase"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	Target       string         `json:"target"` // the primary resource
	OpenedAt     time.Time      `json:"opened_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ResolvedAt   time.Time      `json:"resolved_at,omitzero"`
	Facts        map[string]any `json:"facts,omitempty"`
	Causes       []Cause        `json:"causes"`
	BlastRadius  []Impact       `json:"blast_radius"`
	Suggested    Action         `json:"suggested"`
	Alternatives []Action       `json:"alternatives,omitempty"`
	ResolvesWhen string         `json:"resolves_when"`
	ResolvedBy   string         `json:"resolved_by,omitempty"` // "action" or "auto"
	Resolution   string         `json:"resolution,omitempty"`
}

// Cause is a root-cause hint.
type Cause struct {
	Text     string    `json:"text"`
	Resource string    `json:"resource"`
	At       time.Time `json:"at,omitzero"`
}

// Impact is one resource in a blast radius.
type Impact struct {
	Resource string `json:"resource"`
	Via      string `json:"via"`
	Depth    int    `json:"depth"`
}

// Action is something the user can do about an incident, with its safety
// tier (T0 instant … T4 critical).
type Action struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Tier   string   `json:"tier"`
	Target string   `json:"target"`
	Detail string   `json:"detail,omitempty"`
	Impact []Impact `json:"impact,omitempty"`
	// Params are the action's params, for the plan.
	Params map[string]any `json:"params,omitempty"`

	// The hub fills these for an action the engine runs (an Actor's), so
	// the web needn't decide availability itself; what an integration sets
	// is overwritten. PlanReady: the engine can plan it now. Unavailable:
	// why not, as a sentence, with Recipe naming the instance whose recipe
	// grants it. See runtime.Actions.Availability for the rule.
	PlanReady   *bool  `json:"plan_ready,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
	Recipe      string `json:"recipe,omitempty"`
}

// Attrs that carry the resource fields graph.Resource doesn't have yet, the
// same keys demo mode uses (internal/demo).
const (
	AttrDesired      = "desired"
	AttrHealthReason = "health.reason"
	AttrHealthSince  = "health.since"
	AttrLinks        = "links"
	AttrCaps         = "caps"
)
