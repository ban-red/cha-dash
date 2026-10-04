package sim

import (
	"math"
	"strconv"
	"time"
)

// Kind is the type of a resource. The names follow the Home Graph vocabulary
// in docs/plan/03-architecture.md.
type Kind string

// Resource kinds used by the Demo Home.
const (
	KindCluster      Kind = "cluster"       // a Proxmox VE cluster (mars)
	KindHost         Kind = "host"          // a PVE host or the Unraid box
	KindVM           Kind = "vm"            // a QEMU guest
	KindLXC          Kind = "lxc"           // a container guest
	KindEngine       Kind = "engine"        // a Docker engine
	KindContainer    Kind = "container"     // a Docker container
	KindApp          Kind = "app"           // a logical service, what tiles bind to
	KindIntegration  Kind = "integration"   // an integration instance in the hub
	KindStorage      Kind = "storage"       // a PVE storage
	KindArray        Kind = "array"         // the Unraid array
	KindDisk         Kind = "disk"          // a physical disk
	KindPool         Kind = "pool"          // an Unraid pool
	KindShare        Kind = "share"         // an Unraid share
	KindBackupTarget Kind = "backup_target" // a PBS datastore
	KindBackupJob    Kind = "backup_job"    // a PVE backup job
	KindRouter       Kind = "router"        // OPNsense
	KindWAN          Kind = "wan"           // the internet uplink
	KindCamera       Kind = "camera"        // a Frigate camera
	KindRoute        Kind = "route"         // a Traefik router
	KindCheck        Kind = "check"         // a built-in uptime check
	KindWeather      Kind = "weather"       // the place a weather source reports on
)

// RelType is the type of a relation between two resources.
type RelType string

// Relation types. A relation reads "From <type> To".
const (
	RelRunsOn             RelType = "runs_on"              // guest → host, container → engine, app → container
	RelPartOf             RelType = "part_of"              // host → cluster, disk → array, route → traefik
	RelDependsOn          RelType = "depends_on"           // sonarr → qbittorrent
	RelStoresOn           RelType = "stores_on"            // datastore → pool, app → share
	RelBackedUpBy         RelType = "backed_up_by"         // guest → backup job
	RelProvidesNetworkFor RelType = "provides_network_for" // router → host, router → camera
	RelRoutesTo           RelType = "routes_to"            // route → app
	RelExposes            RelType = "exposes"              // frigate → camera
	RelMonitors           RelType = "monitors"             // integration → what it reads
)

// HealthStatus is a resource's health.
type HealthStatus string

// Health statuses. The first five are the Home Graph's; repair marks an
// integration whose upstream is fine but which Cha Dash can't read (an Inbox
// repair, not an outage).
const (
	HealthOK          HealthStatus = "ok"
	HealthAttention   HealthStatus = "attention"
	HealthDown        HealthStatus = "down"
	HealthUnknown     HealthStatus = "unknown"
	HealthMaintenance HealthStatus = "maintenance"
	HealthRepair      HealthStatus = "repair"
)

// Health is a resource's health with its reason and the time it began.
type Health struct {
	Status HealthStatus `json:"status"`
	Reason string       `json:"reason,omitempty"`
	Since  time.Time    `json:"since"`
}

// Resource states (actual and desired).
const (
	StateRunning    = "running"
	StateStopped    = "stopped"
	StateExited     = "exited"
	StateRestarting = "restarting"
	StateStarting   = "starting"
	StateOnline     = "online"
	StateIdle       = "idle"
	StateConnected  = "connected"
	StateError      = "error"
	StateEnabled    = "enabled"
)

// Link is a deep link to a native UI or the service itself.
type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Resource is one node of the Home Graph. Attrs hold only facts that change
// at discrete moments; anything that varies continuously is a metric series.
type Resource struct {
	ID      string            `json:"id"`
	Kind    Kind              `json:"kind"`
	Name    string            `json:"name"`
	Source  string            `json:"source"` // the integration resource that reports it
	State   string            `json:"state"`
	Desired string            `json:"desired,omitempty"`
	Health  Health            `json:"health"`
	Labels  map[string]string `json:"labels,omitempty"`
	Attrs   map[string]any    `json:"attrs,omitempty"`
	Links   []Link            `json:"links,omitempty"`
	Caps    []string          `json:"caps,omitempty"`
}

// Relation is a typed, directed edge: From <Type> To.
type Relation struct {
	From string  `json:"from"`
	Type RelType `json:"type"`
	To   string  `json:"to"`
}

// Num is a metric value. NaN means "unknown" and encodes as JSON null, so
// unknown is never shown as 0.
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

// Unknown is the Num for a missing value.
func Unknown() Num { return Num(math.NaN()) }

// Series is one metric of one resource, with its scale, normal band,
// thresholds, latest value and a short history for sparklines.
type Series struct {
	Resource string     `json:"resource"`
	Metric   string     `json:"metric"`
	Unit     string     `json:"unit"`
	Min      float64    `json:"min"`
	Max      float64    `json:"max"`
	Band     [2]float64 `json:"band"` // normal range, drawn as a gray band
	Warn     float64    `json:"warn,omitempty"`
	Crit     float64    `json:"crit,omitempty"`
	Value    Num        `json:"value"`
	AsOf     time.Time  `json:"as_of,omitzero"`
	Stale    bool       `json:"stale,omitempty"` // Value is last-known, older than the latest sample tick
	History  []Num      `json:"history"`         // aligned to State.History, oldest first
}

// HistoryAxis is the shared time axis of every Series.History in a State.
type HistoryAxis struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	StepSeconds int       `json:"step_s"`
	Points      int       `json:"points"`
}

// Sample is one metric value in a metrics event. Value is null when the
// source reads the series and reports it as unknown (a spun-down disk's
// temperature), never 0.
type Sample struct {
	Resource string `json:"resource"`
	Metric   string `json:"metric"`
	Value    Num    `json:"value"`
}

// Severity is how loudly an incident should present.
type Severity string

// Severities, matching the status vocabulary in 02-experience.
const (
	SeverityInfo      Severity = "info"
	SeverityAttention Severity = "attention"
	SeverityDown      Severity = "down"
)

// Class separates real problems from integration repairs. Repairs go to the
// Inbox and never shout in the status sentence.
type Class string

// Incident classes.
const (
	ClassProblem Class = "problem"
	ClassRepair  Class = "repair"
)

// Tier is an action's safety tier from the action safety ladder (T0–T4).
type Tier string

// Safety tiers.
const (
	T0 Tier = "T0" // instant: open, copy, view logs
	T1 Tier = "T1" // reversible: act now, undo toast
	T2 Tier = "T2" // disruptive: hold to confirm, blast radius
	T3 Tier = "T3" // destructive: type the name, step-up auth
	T4 Tier = "T4" // critical: disabled by default
)

// Incident status values.
const (
	StatusOpen     = "open"
	StatusResolved = "resolved"
)

// CategorySecurity marks a security finding, as the hub's integrations do.
// The simulator writes each finding's declared severity; the hub's security
// posture decides what the inbox shows (internal/demo applies it).
const CategorySecurity = "security"

// Cause is a root-cause hint: a recent change on a related resource.
type Cause struct {
	Text     string    `json:"text"`
	Resource string    `json:"resource"`
	At       time.Time `json:"at,omitzero"`
}

// Impact is one resource inside a blast radius. Via is the relation that
// pulled it in; Depth is its distance from the origin.
type Impact struct {
	Resource string  `json:"resource"`
	Via      RelType `json:"via"`
	Depth    int     `json:"depth"`
}

// Action is something the user can do about an incident.
type Action struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Tier   Tier     `json:"tier"`
	Target string   `json:"target"`
	Detail string   `json:"detail,omitempty"`
	Impact []Impact `json:"impact,omitempty"` // blast radius of doing it (T2 and up)
}

// Incident is what the Inbox and the Needs-attention section show.
type Incident struct {
	ID           string         `json:"id"`
	Scenario     Scenario       `json:"scenario"`
	Class        Class          `json:"class"`
	Severity     Severity       `json:"severity"`
	Category     string         `json:"category,omitempty"` // CategorySecurity, or empty
	Status       string         `json:"status"`
	Phase        string         `json:"phase"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	Target       string         `json:"target"`
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

// EventType names an event in the stream returned by Advance.
type EventType string

// Event types. See the README for the full event model.
const (
	EventResourceUpsert  EventType = "resource.upsert"
	EventResourceRemove  EventType = "resource.remove"
	EventRelationUpsert  EventType = "relation.upsert"
	EventRelationRemove  EventType = "relation.remove"
	EventIncidentOpen    EventType = "incident.open"
	EventIncidentUpdate  EventType = "incident.update"
	EventIncidentResolve EventType = "incident.resolve"
	EventMetrics         EventType = "metrics"
)

// Event is one change. Exactly one payload field is set, according to Type.
type Event struct {
	Seq      uint64    `json:"seq"`
	At       time.Time `json:"at"`
	Type     EventType `json:"type"`
	ID       string    `json:"id,omitempty"`       // resource.remove
	Resource *Resource `json:"resource,omitempty"` // resource.upsert (the full resource)
	Relation *Relation `json:"relation,omitempty"` // relation.upsert, relation.remove
	Incident *Incident `json:"incident,omitempty"` // incident.* (the full incident)
	Samples  []Sample  `json:"samples,omitempty"`  // metrics
}

// State is a full snapshot of the Demo Home at one moment.
type State struct {
	Seed      int64       `json:"seed"`
	Start     time.Time   `json:"start"`
	Now       time.Time   `json:"now"`
	Seq       uint64      `json:"seq"` // the last event emitted before this snapshot
	History   HistoryAxis `json:"history"`
	Resources []Resource  `json:"resources"` // sorted by ID
	Relations []Relation  `json:"relations"` // sorted by From, Type, To
	Series    []Series    `json:"series"`    // sorted by Resource, Metric
	Incidents []Incident  `json:"incidents"` // every incident opened so far, by OpenedAt then ID
}

// Resource returns the resource with the given ID.
func (st State) Resource(id string) (Resource, bool) {
	for _, r := range st.Resources {
		if r.ID == id {
			return r, true
		}
	}
	return Resource{}, false
}

// Incident returns the incident with the given ID.
func (st State) Incident(id string) (Incident, bool) {
	for _, in := range st.Incidents {
		if in.ID == id {
			return in, true
		}
	}
	return Incident{}, false
}

// Open returns the incidents that are still open.
func (st State) Open() []Incident {
	var out []Incident
	for _, in := range st.Incidents {
		if in.Status == StatusOpen {
			out = append(out, in)
		}
	}
	return out
}
