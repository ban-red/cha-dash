// Package export writes and reads Home files: a whole hub's configuration
// (boards with their layouts, integration instances, the settings worth
// carrying) as one YAML document, apiVersion chadash/v1alpha1, kind Home.
// The format is documented in README.md.
//
// Secrets are never in a Home file in plain text. Each secret field is a
// placeholder naming the vault entry it needs (`!secret instance/<id>/<field>`).
// A full backup can add an encrypted bundle: the values sealed with a key
// derived from a passphrase (scrypt, then XChaCha20-Poly1305). No type in
// this package can carry a plaintext secret into the encoder: placeholders
// are names, bundle values are importer.Secret (which redacts itself
// everywhere) until they are sealed, and Export refuses output that contains
// any stored secret value.
//
// Users, sessions, passkeys and the audit log are never exported, nor is
// the hub's own derived state: layout history, integration state, discovery
// dismissals, correlation decisions, onboarding progress.
//
// Importing compares a Home file with the hub (Plan, the dry run) and applies
// it in one transaction (Apply). Objects are matched by ID; importing an
// export of the same hub changes nothing.
package export

import (
	"encoding/json"
	"strings"
	"time"
)

// The document's identity. A file with another apiVersion or kind is refused.
const (
	APIVersion = "chadash/v1alpha1"
	Kind       = "Home"
)

// Document is a decoded Home file.
type Document struct {
	Meta      Meta
	Boards    []Board
	Instances []Instance
	// Settings are the carried settings rows, sorted by key (see carried).
	Settings []Setting
	// Bundle holds the encrypted secret values of a full backup; nil
	// otherwise.
	Bundle *Bundle
}

// Meta says where and when the file was made. It's informational: importing
// never compares it.
type Meta struct {
	ExportedAt time.Time
	HubVersion string
}

// Board is one board with its latest layout.
type Board struct {
	ID       string
	Name     string
	Slug     string
	Position int64
	// Layout is nil for a board that was never saved.
	Layout *Layout
	// Tiles is the board's tiles document (internal/boards/tiles.go), keyed
	// by layout item id; nil when it has none. See tiles.go.
	Tiles json.RawMessage
}

// Layout is a board's layout document (packages/layout) and its format
// version. The hub treats the document as opaque JSON.
type Layout struct {
	FormatVersion int64
	Document      json.RawMessage
}

// Instance is one integration instance.
type Instance struct {
	ID          string
	Integration string
	Name        string
	Enabled     bool
	// Config is the non-secret config, without tls: a JSON object.
	Config json.RawMessage
	// TLS is the instance's TLS policy ({"mode", "pin"}), or nil.
	TLS json.RawMessage
	// Secrets maps each set secret field to the vault entry it needs. Only
	// names: a value has nowhere to go.
	Secrets map[string]string
}

// Setting is one carried settings row.
type Setting struct {
	Key   string
	Value json.RawMessage
}

// carried reports whether a settings row belongs in a Home file: the import
// records, which keep what an import made beyond the board (explicit links,
// discovery hints, board settings, the instance ids for stale reports).
// Everything else in settings is this hub's own state (such as the client
// identity it signs in to integrations with) and stays.
func carried(key string) bool {
	return strings.HasPrefix(key, "import/") && len(key) > len("import/")
}
