package importer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// RecordKey is the settings row where an import keeps what the board and
// instances don't: tile metadata, explicit links, discovery hints, and the
// instance ids it created (so a re-import can report stale ones).
func RecordKey(source string) string { return "import/" + source }

// Record is the stored import record (the settings row's JSON value).
type Record struct {
	Source    string         `json:"source"`
	BoardID   string         `json:"board_id"`
	Instances []string       `json:"instances"`
	Tiles     []Tile         `json:"tiles"`
	Links     []Link         `json:"links"`
	Discovery []Discovery    `json:"discovery"`
	Settings  map[string]any `json:"settings,omitempty"`
}

// Applied is what Apply returns (and POST /api/import/<source> answers).
type Applied struct {
	BoardID     string   `json:"board_id"`
	InstanceIDs []string `json:"instance_ids"`
	Changes     []Change `json:"changes"`
}

// Changed reports whether applying changed anything.
func (a Applied) Changed() bool { return changed(a.Changes) }

func changed(cs []Change) bool {
	for _, c := range cs {
		if c.Action == ActionCreate || c.Action == ActionUpdate {
			return true
		}
	}
	return false
}

// Service applies plans to the hub's database and vault.
type Service struct {
	st    *store.Store
	vault *vault.Vault
}

// New returns a Service.
func New(st *store.Store, v *vault.Vault) *Service { return &Service{st: st, vault: v} }

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// work is a plan compared with the database: what to write, and the changes
// to report.
type work struct {
	changes []Change

	board struct {
		action   string
		slug     string
		position int64
	}
	layout struct {
		action string
		doc    []byte
		next   int64
	}
	instances []instanceWork
	secrets   []secretWork
	record    struct {
		action string
		value  []byte
	}
}

type instanceWork struct {
	action  string
	inst    *Instance
	config  []byte
	oldName string // the stored name, for updates
}

type secretWork struct {
	action string
	ref    SecretRef
}

// Diff sets res.Plan.Changes to what applying the plan would do, without
// writing anything. Instance names that collide with instances the import
// didn't create get a " (imported)" suffix, in the plan as in the diff.
func (s *Service) Diff(ctx context.Context, res *Result, secrets Secrets) error {
	w, err := s.compare(ctx, s.st.Reader(), res, secrets)
	if err != nil {
		return err
	}
	res.Plan.Changes = w.changes
	return nil
}

// Apply applies the plan in one write transaction: the board and its layout,
// the instances, the secrets (sealed inside the same transaction) and the
// import record. Anything already equal is left alone, so applying the same
// plan twice changes nothing the second time. userID may be empty.
func (s *Service) Apply(ctx context.Context, res *Result, secrets Secrets, userID string) (Applied, error) {
	var w *work
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		if w, err = s.compare(ctx, tx, res, secrets); err != nil {
			return err
		}
		return s.write(ctx, tx, &res.Plan, w, secrets, userID)
	})
	if err != nil {
		return Applied{}, err
	}
	res.Plan.Changes = w.changes
	ids := make([]string, 0, len(res.Plan.Instances))
	for _, in := range res.Plan.Instances {
		ids = append(ids, in.ID)
	}
	return Applied{BoardID: res.Plan.Board.ID, InstanceIDs: ids, Changes: w.changes}, nil
}

// LoadRecord returns the stored record for source, or nil if the source was
// never imported.
func (s *Service) LoadRecord(ctx context.Context, source string) (*Record, error) {
	var raw string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, RecordKey(source)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, fmt.Errorf("importer: record %s: %w", RecordKey(source), err)
	}
	return &r, nil
}

func (s *Service) compare(ctx context.Context, q querier, res *Result, secrets Secrets) (*work, error) {
	p := &res.Plan
	w := &work{}
	add := func(kind, id, name, action string) {
		w.changes = append(w.changes, Change{Kind: kind, ID: id, Name: name, Action: action})
	}

	// Board.
	var title, slug string
	err := q.QueryRowContext(ctx, `SELECT title, slug FROM boards WHERE id = ?`, p.Board.ID).Scan(&title, &slug)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		w.board.action = ActionCreate
		if w.board.slug, err = freeSlug(ctx, q, p.Board.Slug); err != nil {
			return nil, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(position) + 1, 0) FROM boards`).Scan(&w.board.position); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case title != p.Board.Name:
		w.board.action, w.board.slug = ActionUpdate, slug
	default:
		w.board.action, w.board.slug = ActionUnchanged, slug
	}
	p.Board.Slug = w.board.slug
	add("board", p.Board.ID, p.Board.Name, w.board.action)

	// Layout: compared as JSON values, so key order and whitespace don't count.
	doc, err := json.Marshal(p.Board.Layout)
	if err != nil {
		return nil, err
	}
	w.layout.doc = doc
	var rev sql.NullInt64
	var cur sql.NullString
	if w.board.action != ActionCreate {
		err := q.QueryRowContext(ctx, `SELECT revision, document FROM layouts WHERE board_id = ? ORDER BY revision DESC LIMIT 1`, p.Board.ID).Scan(&rev, &cur)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	switch {
	case !cur.Valid:
		w.layout.action = ActionCreate
	case sameJSON([]byte(cur.String), doc):
		w.layout.action = ActionUnchanged
	default:
		w.layout.action = ActionUpdate
	}
	w.layout.next = rev.Int64 + 1
	add("layout", p.Board.ID, p.Board.Name, w.layout.action)

	// Instances.
	planned := make(map[string]bool, len(p.Instances))
	for _, in := range p.Instances {
		planned[in.ID] = true
	}
	for i := range p.Instances {
		in := &p.Instances[i]
		cfg, err := json.Marshal(in.Config)
		if err != nil {
			return nil, err
		}
		var integ, name, stored string
		err = q.QueryRowContext(ctx, `SELECT integration, name, config FROM integration_instances WHERE id = ?`, in.ID).Scan(&integ, &name, &stored)
		action := ActionUnchanged
		switch {
		case errors.Is(err, sql.ErrNoRows):
			action = ActionCreate
		case err != nil:
			return nil, err
		case integ != in.Integration || name != in.Name || !sameJSON([]byte(stored), cfg):
			action = ActionUpdate
		}
		if action != ActionUnchanged {
			if in.Name, err = freeName(ctx, q, in.Integration, in.Name, planned); err != nil {
				return nil, err
			}
			if action == ActionUpdate && integ == in.Integration && name == in.Name && sameJSON([]byte(stored), cfg) {
				action = ActionUnchanged // the only difference was a name we had to change anyway
			}
		}
		w.instances = append(w.instances, instanceWork{action: action, inst: in, config: cfg, oldName: name})
		add("instance", in.ID, in.Name, action)
	}

	// Secrets: equal values are left alone, so a re-import doesn't reseal.
	for _, ref := range p.Secrets {
		val, ok := secrets[ref.Name]
		if !ok {
			return nil, fmt.Errorf("importer: plan names secret %q but carries no value for it", ref.Name)
		}
		action := ActionUnchanged
		old, err := s.vault.Get(ctx, ref.Name)
		switch {
		case errors.Is(err, vault.ErrNotFound):
			action = ActionCreate
		case errors.Is(err, vault.ErrCorrupt), errors.Is(err, vault.ErrWrongKey):
			action = ActionUpdate
		case err != nil:
			return nil, err
		case !val.Equal(string(old)):
			action = ActionUpdate
		}
		clear(old)
		w.secrets = append(w.secrets, secretWork{action: action, ref: ref})
		add("secret", ref.Name, ref.Field, action)
	}

	// The import record, and instances a previous import made that the
	// source no longer has.
	rec := Record{
		Source: p.Source, BoardID: p.Board.ID, Tiles: p.Tiles, Links: p.Links,
		Discovery: p.Discovery, Settings: p.Board.Settings,
		Instances: make([]string, 0, len(p.Instances)),
	}
	for _, in := range p.Instances {
		rec.Instances = append(rec.Instances, in.ID)
	}
	if w.record.value, err = json.Marshal(rec); err != nil {
		return nil, err
	}
	var prevRaw string
	err = q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, RecordKey(p.Source)).Scan(&prevRaw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		w.record.action = ActionCreate
	case err != nil:
		return nil, err
	default:
		w.record.action = ActionUnchanged
		if !sameJSON([]byte(prevRaw), w.record.value) {
			w.record.action = ActionUpdate
		}
		var prev Record
		if json.Unmarshal([]byte(prevRaw), &prev) == nil {
			for _, id := range prev.Instances {
				if planned[id] {
					continue
				}
				var name string
				err := q.QueryRowContext(ctx, `SELECT name FROM integration_instances WHERE id = ?`, id).Scan(&name)
				if err == nil {
					add("instance", id, name, ActionStale)
				} else if !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
			}
		}
	}
	add("record", RecordKey(p.Source), "", w.record.action)
	return w, nil
}

func (s *Service) write(ctx context.Context, tx *sql.Tx, p *Plan, w *work, secrets Secrets, userID string) error {
	now := store.NowMillis()
	switch w.board.action {
	case ActionCreate:
		if _, err := tx.ExecContext(ctx, `INSERT INTO boards (id, slug, title, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			p.Board.ID, w.board.slug, p.Board.Name, w.board.position, now, now); err != nil {
			return fmt.Errorf("importer: board: %w", err)
		}
	case ActionUpdate:
		if _, err := tx.ExecContext(ctx, `UPDATE boards SET title = ?, updated_at = ? WHERE id = ?`, p.Board.Name, now, p.Board.ID); err != nil {
			return fmt.Errorf("importer: board: %w", err)
		}
	}

	if w.layout.action != ActionUnchanged {
		createdBy := sql.NullString{String: userID, Valid: userID != ""}
		if _, err := tx.ExecContext(ctx, `INSERT INTO layouts (board_id, revision, format_version, document, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
			p.Board.ID, w.layout.next, p.Board.FormatVersion, string(w.layout.doc), now, createdBy); err != nil {
			return fmt.Errorf("importer: layout: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE boards SET updated_at = ? WHERE id = ?`, now, p.Board.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM layouts WHERE board_id = ? AND revision <= ?`, p.Board.ID, w.layout.next-boards.KeepRevisions); err != nil {
			return err
		}
	}

	// Renames go through a placeholder first, so two imported instances can
	// trade names (or a new one can take a name an old one gives up) without
	// tripping UNIQUE (integration, name) halfway through.
	for _, iw := range w.instances {
		if iw.action == ActionUpdate && iw.oldName != iw.inst.Name {
			if _, err := tx.ExecContext(ctx, `UPDATE integration_instances SET name = ? WHERE id = ?`, "~renaming~"+iw.inst.ID, iw.inst.ID); err != nil {
				return fmt.Errorf("importer: instance %s: %w", iw.inst.ID, err)
			}
		}
	}
	for _, iw := range w.instances {
		in := iw.inst
		switch iw.action {
		case ActionCreate:
			if _, err := tx.ExecContext(ctx, `INSERT INTO integration_instances (id, integration, name, config, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, ?)`,
				in.ID, in.Integration, in.Name, string(iw.config), now, now); err != nil {
				return fmt.Errorf("importer: instance %s (%s): %w", in.Name, in.Integration, err)
			}
		case ActionUpdate:
			if _, err := tx.ExecContext(ctx, `UPDATE integration_instances SET integration = ?, name = ?, config = ?, updated_at = ? WHERE id = ?`,
				in.Integration, in.Name, string(iw.config), now, in.ID); err != nil {
				return fmt.Errorf("importer: instance %s (%s): %w", in.Name, in.Integration, err)
			}
		}
	}

	for _, sw := range w.secrets {
		if sw.action == ActionUnchanged {
			continue
		}
		val := []byte(secrets[sw.ref.Name].Reveal())
		err := s.vault.PutTx(ctx, tx, sw.ref.Name, val)
		clear(val)
		if err != nil {
			// The vault's errors name the secret, never its value.
			return fmt.Errorf("importer: secret %s: %w", sw.ref.Name, err)
		}
	}

	if w.record.action != ActionUnchanged {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			RecordKey(p.Source), string(w.record.value), now); err != nil {
			return fmt.Errorf("importer: record: %w", err)
		}
	}
	return nil
}

// sameJSON compares two JSON documents as values.
func sameJSON(a, b []byte) bool {
	ca, errA := canonical(a)
	cb, errB := canonical(b)
	return errA == nil && errB == nil && bytes.Equal(ca, cb)
}

func canonical(b []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v) // map keys come out sorted
}

// freeSlug returns slug, or slug-2, slug-3, … if another board has it.
func freeSlug(ctx context.Context, q querier, slug string) (string, error) {
	base := slug
	for n := 2; ; n++ {
		var one int
		err := q.QueryRowContext(ctx, `SELECT 1 FROM boards WHERE slug = ?`, slug).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return slug, nil
		}
		if err != nil {
			return "", err
		}
		slug = base + "-" + strconv.Itoa(n)
	}
}

// freeName returns name, or "name (imported)", "name (imported 2)", … when
// an instance outside the plan (one the user made) already uses it for
// integration. Instances in the plan don't count: their own names are
// unique, and write renames them in two steps.
func freeName(ctx context.Context, q querier, integration, name string, planned map[string]bool) (string, error) {
	base := strings.TrimSpace(name)
	for n := 1; ; n++ {
		taken, err := nameTaken(ctx, q, integration, name, planned)
		if err != nil {
			return "", err
		}
		if !taken {
			return name, nil
		}
		if n == 1 {
			name = base + " (imported)"
		} else {
			name = fmt.Sprintf("%s (imported %d)", base, n)
		}
	}
}

func nameTaken(ctx context.Context, q querier, integration, name string, planned map[string]bool) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM integration_instances WHERE integration = ? AND name = ?`, integration, name)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return false, err
		}
		if !planned[id] {
			return true, nil
		}
	}
	return false, rows.Err()
}
