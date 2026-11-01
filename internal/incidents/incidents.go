// Package incidents will keep the incident timeline (docs/plan/14-v0.4.md,
// "The incident timeline"): each incident's lifecycle as rows of
// incident_events (migration 00023), kept 90 days by default.
//
// Wave 25 fixes the shape only. The store that writes events as incidents
// change, its retention and the timeline's routes come in wave 28.
package incidents

import (
	"encoding/json"
	"time"
)

// EventKind is what happened to an incident.
type EventKind string

// The event kinds, as incident_events.kind allows them.
const (
	Opened       EventKind = "opened"
	Changed      EventKind = "changed" // its severity changed; data: {"from", "to"}
	Caused       EventKind = "caused"  // the cause engine named a cause; data: {"resource", "rule"}
	Acknowledged EventKind = "acknowledged"
	// Expected: it opened inside a maintenance window on its host, so it
	// didn't notify; data: {"window"}.
	Expected EventKind = "expected"
	Resolved EventKind = "resolved"
)

// Event is one row of incident_events.
type Event struct {
	ID         string    `json:"id"`          // iev_…
	IncidentID string    `json:"incident_id"` // as the inbox topic keys it
	At         time.Time `json:"at"`
	Kind       EventKind `json:"kind"`
	// Data is what the kind needs, a JSON object; {} when nothing.
	Data json.RawMessage `json:"data"`
}
