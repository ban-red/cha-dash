// Package firstpaint builds the snapshot the hub inlines into index.html for
// a signed-in user, whatever their role, so the front page renders with real values on its first
// paint and then goes live (03 · Architecture → Realtime, "Inline snapshot").
// The web app reads it from a non-executable data block, renders from it
// straight away, and lets the WebSocket's first snapshots supersede it.
//
// The snapshot is the first board plus the three realtime topics, in the
// shapes a tab receives them, trimmed to what the front page shows:
//
//	board    the first board's id, name, revision, format version, layout
//	         (null until it's saved) and tiles, and the board list
//	graph    whole: the Briefing, Needs attention and the widgets all read it
//	         (every board is the whole home until lenses arrive)
//	metrics  the series of resources in the graph, plus the shared history
//	         axis; a series whose resource the page can't name isn't shown
//	inbox    the clock, every open incident and the newest resolution (the
//	         Incidents tile's "Last: …"); older resolutions only appear in the
//	         Inbox, which the socket fills before anyone can open it. Seen
//	         the way the inbox topic shows it to the user's role (Inbox):
//	         whole, or the household's projection
//
// Encode escapes <, > and & (and U+2028/U+2029), so the JSON can sit in a
// <script type="application/json"> element: nothing in it can end the
// element or open a comment, whatever a resource is called. A snapshot over
// the size cap isn't inlined at all; the page then loads as it would
// without one, from the socket.
package firstpaint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/topics"
)

// Version is the snapshot format version the web app checks.
const Version = 1

// MaxBytes is the default cap on an encoded snapshot. The Demo Home is about
// a quarter of it; a home several times larger still fits, and a page this
// size is a few milliseconds on a LAN.
const MaxBytes = 512 << 10

// Builder makes snapshots from the hub's live state.
type Builder struct {
	Topics *topics.Set
	Boards *boards.Service
	// Demo marks the Demo Home, whose clock and time zone the page needs
	// before its first paint. DemoZone and DemoZoneLabel are the zone its
	// times show in (demo.Driver.TimeZone); empty means UTC.
	Demo          bool
	DemoZone      string
	DemoZoneLabel string
	// MaxBytes caps the encoded snapshot; 0 means MaxBytes.
	MaxBytes int
	// Inbox is how a role sees the inbox, the same view the inbox topic
	// gives it (topics.InboxView). Nil shows it whole, so a hub that
	// inlines snapshots for roles below viewer must set it.
	Inbox func(ctx context.Context, role string) topics.InboxView
	// WholeGraph says whether a role sees the whole graph, as the graph
	// topic gives it (authz.SeesWholeGraph); the others get
	// topics.HouseholdGraph. Nil shows it whole, so a hub that inlines
	// snapshots for roles below viewer must set it.
	WholeGraph func(role string) bool
}

// User is who the snapshot is for: the signed-in user, as GET
// /api/auth/session reports them.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// Board is the first board as GET /api/boards/{id}/layout reports it.
type Board struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Revision      int64           `json:"revision"`
	FormatVersion int64           `json:"format_version"`
	Layout        json.RawMessage `json:"layout"` // null until saved
	Tiles         json.RawMessage `json:"tiles"`  // each item's configuration; {} until set
}

// Snapshot is the document in the data block.
type Snapshot struct {
	V       int            `json:"v"`
	User    User           `json:"user"`
	Boards  []boards.Board `json:"boards"`
	Board   *Board         `json:"board"` // nil: no board yet
	Demo    bool           `json:"demo,omitempty"`
	Zone    string         `json:"time_zone,omitempty"`  // the Demo Home's display zone
	Label   string         `json:"zone_label,omitempty"` // and its label
	Graph   graph.Doc      `json:"graph"`
	Metrics map[string]any `json:"metrics"`
	Inbox   map[string]any `json:"inbox"`
}

// TooLargeError means the encoded snapshot is over the cap.
type TooLargeError struct{ Size, Max int }

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("firstpaint: snapshot is %d bytes, over the %d-byte cap", e.Size, e.Max)
}

// Build returns u's snapshot, encoded for a data block. It returns an
// *TooLargeError when the snapshot is over the cap.
func (b *Builder) Build(ctx context.Context, u User) ([]byte, error) {
	s, err := b.Snapshot(ctx, u)
	if err != nil {
		return nil, err
	}
	out, err := Encode(s)
	if err != nil {
		return nil, err
	}
	limit := b.MaxBytes
	if limit <= 0 {
		limit = MaxBytes
	}
	if len(out) > limit {
		return nil, &TooLargeError{Size: len(out), Max: limit}
	}
	return out, nil
}

// Snapshot assembles u's snapshot from the live state.
func (b *Builder) Snapshot(ctx context.Context, u User) (Snapshot, error) {
	s := Snapshot{V: Version, User: u, Demo: b.Demo}
	if b.Demo {
		s.Zone, s.Label = b.DemoZone, b.DemoZoneLabel
	}
	list, err := b.Boards.List(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("firstpaint: boards: %w", err)
	}
	s.Boards = list
	if len(list) > 0 {
		l, err := b.Boards.Layout(ctx, list[0].ID)
		if err != nil {
			return Snapshot{}, fmt.Errorf("firstpaint: layout: %w", err)
		}
		doc, tiles := l.Document, l.Tiles
		if doc == nil {
			doc = json.RawMessage("null")
		}
		if tiles == nil {
			tiles = json.RawMessage(boards.EmptyTiles)
		}
		s.Board = &Board{ID: list[0].ID, Name: list[0].Name, Revision: l.Revision, FormatVersion: l.FormatVersion, Layout: doc, Tiles: tiles}
	}

	_, s.Graph = b.Topics.Graph.Snapshot(nil)
	if b.WholeGraph != nil && !b.WholeGraph(u.Role) {
		s.Graph = topics.HouseholdGraph(s.Graph)
	}
	_, metrics := b.Topics.Metrics.Snapshot()
	s.Metrics = trimMetrics(metrics, s.Graph)
	_, inbox := b.Topics.Inbox.Snapshot()
	view := topics.InboxView{Detail: true}
	if b.Inbox != nil {
		view = b.Inbox(ctx, u.Role)
	}
	s.Inbox = trimInbox(topics.ProjectInbox(inbox, view))
	return s, nil
}

// Encode marshals s for a <script type="application/json"> element: <, >
// and &, and the JavaScript line terminators, are escaped wherever they
// appear, so the result never contains "</script" or "<!--".
func Encode(s Snapshot) ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("firstpaint: %w", err)
	}
	// json.Marshal escapes these already; doing it again explicitly keeps
	// the guarantee independent of the encoder's options.
	var out bytes.Buffer
	out.Grow(len(raw))
	json.HTMLEscape(&out, raw)
	return out.Bytes(), nil
}

// trimMetrics keeps the series whose resource is in g. The document's other
// members (the shared history axis) are kept whole.
func trimMetrics(doc map[string]any, g graph.Doc) map[string]any {
	series, ok := doc[topics.Series].(map[string]json.RawMessage)
	if !ok {
		return doc
	}
	kept := make(map[string]json.RawMessage, len(series))
	for key, v := range series {
		i := strings.LastIndexByte(key, '#')
		if i <= 0 {
			continue
		}
		if _, ok := g.Resources[graph.RID(key[:i])]; ok {
			kept[key] = v
		}
	}
	doc[topics.Series] = kept
	return doc
}

// incidentTimes is what trimInbox reads from an incident.
type incidentTimes struct {
	Status     string `json:"status"`
	OpenedAt   string `json:"opened_at"`
	UpdatedAt  string `json:"updated_at"`
	ResolvedAt string `json:"resolved_at"`
}

// resolvedAt is when an incident resolved, the way the web app sorts them:
// resolved_at, else updated_at, else opened_at.
func (in incidentTimes) resolvedAt() time.Time {
	for _, s := range []string{in.ResolvedAt, in.UpdatedAt, in.OpenedAt} {
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// trimInbox keeps every open incident and the newest resolved one (all of
// them, if several resolved at that same moment).
func trimInbox(doc map[string]any) map[string]any {
	incidents, ok := doc[topics.Incidents].(map[string]json.RawMessage)
	if !ok {
		return doc
	}
	kept := make(map[string]json.RawMessage, len(incidents))
	var newest time.Time
	var latest []string
	for id, raw := range incidents {
		var in incidentTimes
		if err := json.Unmarshal(raw, &in); err != nil {
			continue
		}
		if in.Status != "resolved" {
			kept[id] = raw
			continue
		}
		switch at := in.resolvedAt(); {
		case latest == nil || at.After(newest):
			newest, latest = at, []string{id}
		case at.Equal(newest):
			latest = append(latest, id)
		}
	}
	for _, id := range latest {
		kept[id] = incidents[id]
	}
	doc[topics.Incidents] = kept
	return doc
}
