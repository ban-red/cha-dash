// Package updates is the Update Center, minimal (docs/plan/09-v0.3.md): it
// lists the image updates the integrations publish on containers
// (attrs.update), keeps holds and snoozes (update_holds), names the action
// that updates each container, and keeps one quiet Inbox item counting the
// updates available.
//
// It checks nothing itself. A Docker instance through chadash-node asks the
// node (image.check) every 15 minutes, and the node asks each registry at
// most every 6 hours; Check now refreshes those instances, which ask the
// node to ask again.
package updates

import (
	"cmp"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
)

// SnoozeFor is how long a snooze hides an update, unless a newer digest
// appears first.
const SnoozeFor = 7 * 24 * time.Hour

// ItemID is the Inbox item counting the updates available.
const ItemID = "updates.available"

// ReviewAction is the item's suggestion: the web opens Settings → Updates.
const ReviewAction = "update.review"

// The kinds of hold.
const (
	KindHold   = "hold"
	KindSnooze = "snooze"
)

// Options configure a Service.
type Options struct {
	Store *store.Store
	Graph *graph.Graph
	// Inbox receives the one info item; nil leaves it out.
	Inbox *livedoc.Doc
	// Refresh asks every instance that publishes updates to sync now as a
	// Check now (Interest.Refresh), and says how many it asked.
	Refresh func() int
	Now     func() time.Time
	Log     *slog.Logger
}

// Service is the Update Center.
type Service struct {
	st      *store.Store
	g       *graph.Graph
	inbox   *livedoc.Doc
	refresh func() int
	now     func() time.Time
	log     *slog.Logger

	// Audit records holds and releases. Set by the server.
	Audit *authz.Recorder

	poke chan struct{} // a hold changed: recount the Inbox item now
	mu   sync.Mutex
	last string // the item as last written, to skip writing it unchanged
}

// New returns a Service.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Graph == nil {
		return nil, errors.New("updates: needs a store and a graph")
	}
	s := &Service{st: o.Store, g: o.Graph, inbox: o.Inbox, refresh: o.Refresh, now: o.Now, log: o.Log, poke: make(chan struct{}, 1)}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.refresh == nil {
		s.refresh = func() int { return 0 }
	}
	return s, nil
}

// Hold is a hold or a snooze on one container.
type Hold struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Digest    string    `json:"digest,omitempty"` // snooze: the newest digest when snoozed
	Until     time.Time `json:"until,omitzero"`   // snooze: when it ends
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Action is what updates a container: an engine action and its target.
type Action struct {
	ID     string `json:"id"`
	Target string `json:"target"`
}

// Row is one container whose image the integrations checked.
type Row struct {
	Resource string `json:"resource"`
	Name     string `json:"name"`
	// Engine is what the container runs on (a Docker instance's engine
	// resource), and Host its name: rows are grouped by it.
	Engine string `json:"engine"`
	Host   string `json:"host"`
	Stack  string `json:"stack,omitempty"` // its compose project, if any
	Image  string `json:"image,omitempty"`
	// State is the published one, except an update held or snoozed is held.
	State     string    `json:"state"`
	Current   string    `json:"current,omitempty"`
	Latest    string    `json:"latest,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Reason    string    `json:"reason,omitempty"`
	Hold      *Hold     `json:"hold,omitempty"`
	// Update is the action that updates it, or nil with Why saying what
	// would make it updatable.
	Update *Action `json:"update,omitempty"`
	Why    string  `json:"why,omitempty"`
}

// List is every container with an update attribute, available ones first,
// then by host and name.
func (s *Service) List(ctx context.Context) ([]Row, error) {
	holds, err := s.holds(ctx)
	if err != nil {
		return nil, err
	}
	_, doc := s.g.Snapshot(nil)
	runsOn, partOf := map[graph.RID]graph.RID{}, map[graph.RID]graph.RID{}
	for _, rel := range doc.Relations {
		switch rel.Type {
		case "runs_on":
			runsOn[rel.From] = rel.To
		case "part_of":
			partOf[rel.From] = rel.To
		}
	}
	now := s.now()
	rows := []Row{}
	for id, r := range doc.Resources {
		if r.Kind != "container" {
			continue
		}
		u, ok := attrUpdate(r.Attrs[integration.AttrUpdate])
		if !ok {
			continue
		}
		row := Row{
			Resource: string(id), Name: cmp.Or(r.Name, string(id)), Engine: string(runsOn[id]),
			Image: stringAttr(r.Attrs, "image"), State: u.State, Current: u.Current, Latest: u.Latest, CheckedAt: u.CheckedAt, Reason: u.Reason,
		}
		if e, ok := doc.Resources[runsOn[id]]; ok {
			row.Host = cmp.Or(e.Name, string(e.ID))
		}
		if st, ok := doc.Resources[partOf[id]]; ok {
			row.Stack = cmp.Or(st.Name, string(st.ID))
		}
		if h, ok := holds[holdKey(row.Engine, row.Name)]; ok {
			if h.active(now, row.Latest) {
				row.Hold = &h
				if row.State == integration.UpdateAvailable {
					row.State = integration.UpdateHeld
				}
			}
		}
		row.Update, row.Why = updater(row)
		rows = append(rows, row)
	}
	order := map[string]int{integration.UpdateAvailable: 0, integration.UpdateHeld: 1, integration.UpdateCantCheck: 2, integration.UpdateCurrent: 3}
	slices.SortFunc(rows, func(a, b Row) int {
		return cmp.Or(cmp.Compare(order[a.State], order[b.State]), strings.Compare(a.Host, b.Host), strings.Compare(a.Name, b.Name), strings.Compare(a.Resource, b.Resource))
	})
	return rows, nil
}

// active says whether a hold applies now: a hold until it's released, a
// snooze until it ends or a newer digest than the one it was snoozed at
// appears.
func (h Hold) active(now time.Time, latest string) bool {
	if h.Kind == KindHold {
		return true
	}
	return now.Before(h.Until) && (h.Digest == "" || latest == "" || latest == h.Digest)
}

// updater picks the action that updates a row's container, by how it's
// run, or says why there's none.
func updater(r Row) (*Action, string) {
	if r.State != integration.UpdateAvailable && r.State != integration.UpdateHeld {
		return nil, ""
	}
	switch {
	case strings.HasPrefix(r.Resource, "docker:"):
		// Only instances through chadash-node check for updates, and they
		// all offer docker.container.update: pull, recreate, the health
		// gate, and a rollback.
		return &Action{ID: "docker.container.update", Target: r.Resource}, ""
	case strings.HasPrefix(r.Resource, "unraid:"):
		return &Action{ID: "unraid.container.update", Target: r.Resource}, ""
	}
	return nil, "Cha Dash can update a container through chadash-node or Unraid. Add a node on its host to update it from here."
}

// attrUpdate reads attrs.update as the graph holds it (a map once it's
// been through JSON, the struct when an integration set it in-process).
func attrUpdate(v any) (integration.ImageUpdate, bool) {
	var u integration.ImageUpdate
	switch v := v.(type) {
	case nil:
		return u, false
	case integration.ImageUpdate:
		return v, v.State != ""
	default:
		b, err := json.Marshal(v)
		if err != nil || json.Unmarshal(b, &u) != nil {
			return u, false
		}
		return u, u.State != ""
	}
}

func stringAttr(attrs map[string]any, k string) string {
	s, _ := attrs[k].(string)
	return s
}

// ── holds ─────────────────────────────────────────────────────────────

// holdKey keys a hold by the engine and the container's name: an update
// recreates the container, and its ID changes with it.
func holdKey(engine, name string) string { return engine + "\x00" + name }

func (s *Service) holds(ctx context.Context) (map[string]Hold, error) {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT h.id, h.engine, h.container, h.kind, h.digest, h.until, COALESCE(u.username, ''), h.created_at
		FROM update_holds h LEFT JOIN users u ON u.id = h.created_by`)
	if err != nil {
		return nil, fmt.Errorf("updates: read holds: %w", err)
	}
	defer rows.Close()
	out := map[string]Hold{}
	for rows.Next() {
		var h Hold
		var engine, name string
		var until, at int64
		if err := rows.Scan(&h.ID, &engine, &name, &h.Kind, &h.Digest, &until, &h.CreatedBy, &at); err != nil {
			return nil, fmt.Errorf("updates: read holds: %w", err)
		}
		if until > 0 {
			h.Until = time.UnixMilli(until).UTC()
		}
		h.CreatedAt = time.UnixMilli(at).UTC()
		out[holdKey(engine, name)] = h
	}
	return out, rows.Err()
}

// ErrNotFound is a resource that isn't a container with an update.
var ErrNotFound = errors.New("updates: no such container")

// row finds one container's row.
func (s *Service) row(ctx context.Context, resource string) (Row, error) {
	rows, err := s.List(ctx)
	if err != nil {
		return Row{}, err
	}
	for _, r := range rows {
		if r.Resource == resource {
			return r, nil
		}
	}
	return Row{}, ErrNotFound
}

// Hold holds (kind hold) or snoozes (kind snooze) a container's updates,
// replacing any hold it had. userID may be empty (a token's actor is its
// user, passed by the caller).
func (s *Service) Hold(ctx context.Context, resource, kind, userID string) (Row, error) {
	r, err := s.row(ctx, resource)
	if err != nil {
		return Row{}, err
	}
	if r.Engine == "" {
		return Row{}, ErrNotFound
	}
	now := s.now()
	var until int64
	digest := ""
	if kind == KindSnooze {
		until, digest = now.Add(SnoozeFor).UnixMilli(), r.Latest
	}
	var by any
	if userID != "" {
		by = userID
	}
	_, err = s.st.Writer().ExecContext(ctx, `INSERT INTO update_holds (id, engine, container, kind, digest, until, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (engine, container) DO UPDATE SET kind = excluded.kind, digest = excluded.digest, until = excluded.until,
			created_by = excluded.created_by, created_at = excluded.created_at`,
		"uph_"+strings.ToLower(crand.Text()), r.Engine, r.Name, kind, digest, until, by, now.UnixMilli())
	if err != nil {
		return Row{}, fmt.Errorf("updates: hold: %w", err)
	}
	s.changed()
	return s.row(ctx, resource)
}

// Release ends a container's hold or snooze. It answers false when there
// was none.
func (s *Service) Release(ctx context.Context, resource string) (Row, bool, error) {
	r, err := s.row(ctx, resource)
	if err != nil {
		return Row{}, false, err
	}
	res, err := s.st.Writer().ExecContext(ctx, `DELETE FROM update_holds WHERE engine = ? AND container = ?`, r.Engine, r.Name)
	if err != nil {
		return Row{}, false, fmt.Errorf("updates: release: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return r, false, nil
	}
	s.changed()
	r, err = s.row(ctx, resource)
	return r, true, err
}

// Check asks every instance that publishes updates to check now, and says
// how many it asked.
func (s *Service) Check() int { return s.refresh() }

// ── the Inbox item ────────────────────────────────────────────────────

// Run keeps the Inbox item current until ctx ends: every 30 seconds, and
// at once when a hold changes.
func (s *Service) Run(ctx context.Context) {
	if s.inbox == nil {
		return
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		s.Recount(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.poke:
		}
	}
}

func (s *Service) changed() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// item is the Inbox item, the inbox topic's incident shape.
type item struct {
	ID           string         `json:"id"`
	Scenario     string         `json:"scenario"`
	Class        string         `json:"class"`
	Severity     string         `json:"severity"`
	Status       string         `json:"status"`
	Phase        string         `json:"phase"`
	Title        string         `json:"title"`
	Summary      string         `json:"summary"`
	Target       string         `json:"target"`
	OpenedAt     time.Time      `json:"opened_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ResolvedAt   *time.Time     `json:"resolved_at,omitempty"`
	Facts        map[string]any `json:"facts,omitempty"`
	Causes       []any          `json:"causes"`
	BlastRadius  []any          `json:"blast_radius"`
	Suggested    map[string]any `json:"suggested"`
	ResolvesWhen string         `json:"resolves_when"`
	ResolvedBy   string         `json:"resolved_by,omitempty"`
}

// incidentsColl is the inbox topic's collection (topics.Incidents).
const incidentsColl = "incidents"

// Recount writes the Inbox item: open, info severity, while any update is
// available and not held; resolved when none is. Never in Needs attention.
func (s *Service) Recount(ctx context.Context) {
	if s.inbox == nil {
		return
	}
	rows, err := s.List(ctx)
	if err != nil {
		s.log.Warn("updates: count", "err", err)
		return
	}
	var names []string
	for _, r := range rows {
		if r.State == integration.UpdateAvailable {
			names = append(names, r.Name)
		}
	}
	key := strings.Join(names, ",")
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == s.last {
		return
	}
	s.last = key
	now := s.now().UTC()
	_, _ = s.inbox.Update(func(tx *livedoc.Tx) error {
		var cur item
		open := false
		if raw := tx.Item(incidentsColl, ItemID); raw != nil && json.Unmarshal(raw, &cur) == nil && cur.Status == "open" {
			open = true
		}
		if len(names) == 0 {
			if open {
				cur.Status, cur.Phase, cur.UpdatedAt, cur.ResolvedAt, cur.ResolvedBy = "resolved", "resolved", now, &now, "system"
				tx.Put(incidentsColl, ItemID, cur)
			}
			return nil
		}
		title := "1 update available"
		if len(names) > 1 {
			title = fmt.Sprintf("%d updates available", len(names))
		}
		shown := names
		if len(shown) > 5 {
			shown = append(slices.Clone(shown[:5]), fmt.Sprintf("%d more", len(names)-5))
		}
		in := item{
			ID: ItemID, Scenario: "updates-available", Class: "problem", Severity: "info", Status: "open", Phase: "open",
			Title:   title,
			Summary: "New images are out for " + joinNames(shown) + ". Update, hold or snooze them in Settings → Updates.",
			Target:  integration.HubResource, OpenedAt: now, UpdatedAt: now,
			Facts:  map[string]any{"count": len(names), "containers": names},
			Causes: []any{}, BlastRadius: []any{},
			Suggested:    map[string]any{"id": ReviewAction, "label": "Review in Settings → Updates", "tier": "T0", "target": integration.HubResource},
			ResolvesWhen: "Resolves when every update is applied, held or snoozed.",
		}
		if open {
			in.OpenedAt = cur.OpenedAt
		}
		tx.Put(incidentsColl, ItemID, in)
		return nil
	})
}

// joinNames is "a", "a and b", "a, b and c".
func joinNames(n []string) string {
	if len(n) <= 1 {
		return strings.Join(n, "")
	}
	return strings.Join(n[:len(n)-1], ", ") + " and " + n[len(n)-1]
}
