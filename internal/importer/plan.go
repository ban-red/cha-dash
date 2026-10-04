// Package importer turns another dashboard's configuration into Cha Dash
// state: a board with its layout, integration instances, and secrets moved
// into the vault. Each source has its own parser (importer/homepage today)
// that produces a Result; this package holds the shared plan shape and
// applies a plan in one transaction, idempotently.
//
// A plan never holds a secret value. Values travel next to it in Secrets,
// whose every formatting path (fmt, slog, JSON) redacts them.
package importer

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// Result is the dry-run answer: POST /api/import/<source>?dry_run=1 returns
// it as JSON.
type Result struct {
	Plan     Plan       `json:"plan"`
	Hints    []Hint     `json:"hints"`
	Unmapped []Unmapped `json:"unmapped"`
}

// Plan is everything an import would create or update.
type Plan struct {
	Source string `json:"source"`
	Board  Board  `json:"board"`
	// Tiles describe the board's items: what each one shows and where it
	// came from. The layout document only carries ids, types and sizes;
	// Apply stores the rest as the board's tiles (TileConfigs).
	Tiles     []Tile     `json:"tiles"`
	Instances []Instance `json:"instances"`
	// Secrets lists where each moved secret goes; never its value.
	Secrets      []SecretRef `json:"secrets"`
	SecretsMoved int         `json:"secrets_moved"`
	// Links are explicit runs-as relations (Homepage server + container).
	Links []Link `json:"links"`
	// Discovery is what the import expects discovery to fill in later.
	Discovery []Discovery `json:"discovery"`
	// Ignored lists settings and fields that aren't imported, and why.
	Ignored []Ignored `json:"ignored"`
	Summary Summary   `json:"summary"`
	// Changes compares the plan with what the hub already has. It's set by
	// Service.Diff and Service.Apply, not by parsers.
	Changes []Change `json:"changes,omitempty"`
}

// Board is the board the import creates, with its layout document.
type Board struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Slug          string         `json:"slug"`
	FormatVersion int            `json:"format_version"`
	Layout        LayoutDocument `json:"layout"`
	// Settings are board-level preferences carried over from the source,
	// such as {"locale": {"first_day_of_week": "sunday"}}.
	Settings map[string]any `json:"settings,omitempty"`
}

// Origin says where in the source something came from.
type Origin struct {
	File  string `json:"file"`
	Group string `json:"group,omitempty"`
	Entry string `json:"entry,omitempty"`
}

func (o Origin) String() string {
	switch {
	case o.Group != "" || o.Entry != "":
		return o.Group + " → " + o.Entry
	default:
		return o.File
	}
}

// Tile is one item on the imported board.
type Tile struct {
	ItemID string `json:"item_id"`
	// Section is the layout section id.
	Section string `json:"section"`
	// Widget is the widget type (the layout item's type).
	Widget      string `json:"widget"`
	Size        string `json:"size"`
	Name        string `json:"name"`
	Icon        string `json:"icon,omitempty"`
	Href        string `json:"href,omitempty"`
	Description string `json:"description,omitempty"`
	// Chromeless tiles have no header (a Homepage entry with no name).
	Chromeless bool `json:"chromeless,omitempty"`
	// Instance is the integration instance the tile shows.
	Instance string `json:"instance,omitempty"`
	// Check names the target in the checks instance that watches the tile.
	Check string `json:"check,omitempty"`
	// Sources are instances the tile reads by capability instead of
	// duplicating their config (the media calendar reads Sonarr and Radarr).
	Sources []Source `json:"sources,omitempty"`
	// Options are display options carried over from the source widget.
	Options map[string]any `json:"options,omitempty"`
	Origin  Origin         `json:"origin"`
}

// Source binds a tile to a capability of an instance.
type Source struct {
	Instance   string `json:"instance"`
	Capability string `json:"capability"`
	// Options are per-source display options, such as {"unmonitored": true}.
	Options map[string]any `json:"options,omitempty"`
}

// Instance is an integration_instances row. Config uses the keys in
// docs/plan/07-v0.1.md; credentials appear only as ${secret:<name>}.
type Instance struct {
	ID           string         `json:"id"`
	Integration  string         `json:"integration"`
	Name         string         `json:"name"`
	Config       map[string]any `json:"config"`
	SecretFields []string       `json:"secret_fields"`
	// MissingSecrets are secret fields the source referenced but didn't
	// hold (a {{HOMEPAGE_VAR_…}} placeholder): set them in Cha Dash.
	MissingSecrets []string `json:"missing_secrets,omitempty"`
	Origin         Origin   `json:"origin"`
}

// SecretRef says where one moved secret is stored.
type SecretRef struct {
	Name     string `json:"name"`
	Instance string `json:"instance"`
	Field    string `json:"field"`
}

// SecretRefValue is how config refers to a vault secret.
func SecretRefValue(name string) string { return "${secret:" + name + "}" }

// Link is an explicit relation between two imported things.
type Link struct {
	Type string `json:"type"` // runs_on
	// The app side: an instance, a tile, or both.
	Instance string `json:"instance,omitempty"`
	Tile     string `json:"tile,omitempty"`
	// The container side.
	Docker    string `json:"docker"` // the docker instance id
	Endpoint  string `json:"endpoint"`
	Container string `json:"container"`
	Origin    Origin `json:"origin"`
}

// Discovery is something the import expects to arrive through discovery.
type Discovery struct {
	Kind     string `json:"kind"` // label_section, label_endpoint
	Section  string `json:"section,omitempty"`
	Docker   string `json:"docker,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Message  string `json:"message"`
}

// Ignored is a setting or field that isn't imported.
type Ignored struct {
	File    string   `json:"file"`
	Key     string   `json:"key"`
	Entries []string `json:"entries,omitempty"`
	Reason  string   `json:"reason"`
}

// Hint is a credential-hygiene or setup hint. Inbox hints become quiet,
// dismissable Inbox items; the others are only shown in the import summary.
// Hints never contain a secret value.
type Hint struct {
	Code     string    `json:"code"`
	Severity string    `json:"severity"` // info, attention
	Inbox    bool      `json:"inbox"`
	Message  string    `json:"message"`
	Subjects []Subject `json:"subjects,omitempty"`
}

// Subject is what a hint is about.
type Subject struct {
	Instance string `json:"instance,omitempty"`
	Field    string `json:"field,omitempty"`
	Origin   Origin `json:"origin"`
}

// Unmapped is something the importer couldn't map. Fields lists field
// names only, never values.
type Unmapped struct {
	Kind   string   `json:"kind"` // entry, widget, container_link, docker_server, check
	Origin Origin   `json:"origin"`
	Type   string   `json:"type,omitempty"`
	Fields []string `json:"fields,omitempty"`
	Reason string   `json:"reason"`
}

// Summary counts what the plan holds.
type Summary struct {
	Sections      int            `json:"sections"`
	Tiles         int            `json:"tiles"`
	Instances     int            `json:"instances"`
	Integrations  map[string]int `json:"integrations"`
	Checks        int            `json:"checks"`
	Links         int            `json:"links"`
	SecretsMoved  int            `json:"secrets_moved"`
	Hints         int            `json:"hints"`
	InboxHints    int            `json:"inbox_hints"`
	Unmapped      int            `json:"unmapped"`
	Discovery     int            `json:"discovery"`
	IgnoredFields int            `json:"ignored"`
	// Notes are human-readable lines for the import summary.
	Notes []string `json:"notes,omitempty"`
}

// Change is one row of the diff between a plan and the hub's state.
type Change struct {
	Kind   string `json:"kind"` // board, layout, tiles, instance, secret, record
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Action string `json:"action"` // create, update, unchanged, stale
}

// Change actions.
const (
	ActionCreate    = "create"
	ActionUpdate    = "update"
	ActionUnchanged = "unchanged"
	// ActionStale marks an instance a previous import created that the
	// source no longer has. It's reported, never deleted.
	ActionStale = "stale"
)

// Secret is a secret value. Every way of printing it redacts it: fmt verbs,
// slog and JSON all see "[redacted]". Reveal is the only way out.
type Secret struct{ v string }

// NewSecret wraps a value.
func NewSecret(v string) Secret { return Secret{v: v} }

// Reveal returns the value, for the vault and nothing else.
func (s Secret) Reveal() string { return s.v }

const redacted = "[redacted]"

func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return redacted }
func (Secret) Format(f fmt.State, _ rune)   { fmt.Fprint(f, redacted) }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (s Secret) Equal(other string) bool    { return s.v == other }
func (s Secret) containedIn(haystack string) bool {
	return s.v != "" && strings.Contains(haystack, s.v)
}

// Secrets maps vault names to values.
type Secrets map[string]Secret

// Leaks reports the vault names whose values appear in text. Tests use it to
// prove a plan, a response or a log never carries a secret.
func (s Secrets) Leaks(text string) []string {
	var names []string
	for name, v := range s {
		if v.containedIn(text) {
			names = append(names, name)
		}
	}
	return names
}
