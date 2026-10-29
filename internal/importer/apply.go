package importer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// RecordKey is the settings row where an import keeps what the board and
// instances don't: explicit links, discovery hints, board settings, and the
// instance ids it created (so a re-import can report stale ones). Tile
// configuration lives with the board's layout (internal/boards); records
// written before migration 00004 kept it here, under "tiles".
func RecordKey(source string) string { return "import/" + source }

// Record is the stored import record (the settings row's JSON value).
type Record struct {
	Source    string         `json:"source"`
	BoardID   string         `json:"board_id"`
	Instances []string       `json:"instances"`
	Links     []Link         `json:"links"`
	Discovery []Discovery    `json:"discovery"`
	Settings  map[string]any `json:"settings,omitempty"`
}

// Applied is what Apply returns (and POST /api/import/<source> answers).
type Applied struct {
	// ImportID is the History row, when the import changed something.
	ImportID    string   `json:"import_id,omitempty"`
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
		action string // the document's
		tiles  string // the tiles document's
		doc    []byte
		tdoc   []byte
		next   int64
	}
	// items are the layout items the board didn't have before.
	items     []string
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
	return s.DiffWith(ctx, res, secrets, Choices{})
}

// DiffWith is Diff after applying the Review sheet's choices to the plan. It
// sets res.PlanHash, which ApplyWith checks.
func (s *Service) DiffWith(ctx context.Context, res *Result, secrets Secrets, c Choices) error {
	w, err := s.compare(ctx, s.st.Reader(), res, secrets, c)
	if err != nil {
		return err
	}
	res.Plan.Changes = w.changes
	res.PlanHash = Hash(res)
	return nil
}

// Apply applies the plan in one write transaction: the board with its layout
// and tiles (one new revision when either differs), the instances, the
// secrets (sealed inside the same transaction) and the import record. Anything already equal is left alone, so applying the same
// plan twice changes nothing the second time. userID may be empty.
func (s *Service) Apply(ctx context.Context, res *Result, secrets Secrets, userID string) (Applied, error) {
	return s.ApplyWith(ctx, res, secrets, Choices{}, Meta{UserID: userID})
}

// Meta is what ApplyWith records beside the plan.
type Meta struct {
	UserID string
	// Files are the source files' names, for History. Never their text.
	Files []string
	// Hash, when set, is the reviewed plan's hash: ApplyWith refuses with
	// ErrPlanChanged if the plan it would apply now hashes differently.
	Hash string
}

// ApplyWith is Apply with the Review sheet's choices. An import that
// changes something also leaves a row in History (the imports table).
func (s *Service) ApplyWith(ctx context.Context, res *Result, secrets Secrets, c Choices, m Meta) (Applied, error) {
	var w *work
	var importID string
	err := s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		if w, err = s.compare(ctx, tx, res, secrets, c); err != nil {
			return err
		}
		res.Plan.Changes = w.changes
		if m.Hash != "" && Hash(res) != m.Hash {
			return ErrPlanChanged
		}
		if err := s.write(ctx, tx, &res.Plan, w, secrets, m.UserID); err != nil {
			return err
		}
		if changed(w.changes) {
			importID, err = s.writeHistory(ctx, tx, res, w, c, m)
		}
		return err
	})
	if err != nil {
		return Applied{}, err
	}
	res.Plan.Changes = w.changes
	ids := make([]string, 0, len(res.Plan.Instances))
	for _, in := range res.Plan.Instances {
		ids = append(ids, in.ID)
	}
	return Applied{ImportID: importID, BoardID: res.Plan.Board.ID, InstanceIDs: ids, Changes: w.changes}, nil
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

func (s *Service) compare(ctx context.Context, q querier, res *Result, secrets Secrets, c Choices) (*work, error) {
	p := &res.Plan
	w := &work{}
	add := func(kind, id, name, action string) *Change {
		w.changes = append(w.changes, Change{Kind: kind, ID: id, Name: name, Action: action})
		return &w.changes[len(w.changes)-1]
	}
	if err := s.choose(ctx, q, res, secrets, c); err != nil {
		return nil, err
	}
	if p.Board.ID != "" {
		if err := s.compareBoard(ctx, q, p, w); err != nil {
			return nil, err
		}
		add("board", p.Board.ID, p.Board.Name, w.board.action)
		add("layout", p.Board.ID, p.Board.Name, w.layout.action)
		add("tiles", p.Board.ID, p.Board.Name, w.layout.tiles)
	}
	if err := s.compareRest(ctx, q, res, secrets, w, add); err != nil {
		return nil, err
	}
	return w, nil
}

// choose applies the Review sheet's choices to the plan, before it's
// compared with the hub.
func (s *Service) choose(ctx context.Context, q querier, res *Result, secrets Secrets, c Choices) error {
	if err := c.validate(); err != nil {
		return err
	}
	p := &res.Plan
	for planned, existing := range c.UseExisting {
		i := slices.IndexFunc(p.Instances, func(in Instance) bool { return in.ID == planned })
		if i < 0 {
			continue
		}
		var integ string
		err := q.QueryRowContext(ctx, `SELECT integration FROM integration_instances WHERE id = ?`, existing).Scan(&integ)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && integ != p.Instances[i].Integration) {
			return fmt.Errorf("%w: %s can't use %s instead", ErrInvalidChoices, p.Instances[i].Name, existing)
		}
		if err != nil {
			return err
		}
	}
	if err := fillSecrets(res, secrets, c.Secrets); err != nil {
		return err
	}
	reuse(res, secrets, c.UseExisting)
	skip(res, secrets, c.Skip)
	if c.Target == TargetNone {
		noBoard(p)
	} else if id, ok := c.TargetBoard(); ok {
		if err := s.onto(ctx, q, p, id); err != nil {
			return err
		}
	} else if err := p.Board.Layout.Pack(); err != nil {
		return err
	}
	if c.Target != TargetNone && len(p.Tiles) == 0 && len(c.Skip) > 0 {
		noBoard(p) // every tile left out: no empty board
	}
	recount(res)
	return nil
}

// onto moves the plan's tiles onto an existing board, after its own items:
// into the section with the same ID, else into a new section at the end.
// Items the board already has stay as they are.
func (s *Service) onto(ctx context.Context, q querier, p *Plan, boardID string) error {
	var title, slug, doc, tiles string
	var fv int
	err := q.QueryRowContext(ctx, `SELECT b.title, b.slug, l.format_version, l.document, l.tiles FROM boards b
		JOIN layouts l ON l.board_id = b.id WHERE b.id = ? ORDER BY l.revision DESC LIMIT 1`, boardID).Scan(&title, &slug, &fv, &doc, &tiles)
	if errors.Is(err, sql.ErrNoRows) {
		var one int
		if q.QueryRowContext(ctx, `SELECT 1 FROM boards WHERE id = ?`, boardID).Scan(&one) != nil {
			return fmt.Errorf("%w: no board %s", ErrInvalidChoices, boardID)
		}
		doc, tiles, fv = `{"version":1,"columns":12,"sections":[]}`, `{}`, LayoutVersion
		err = q.QueryRowContext(ctx, `SELECT title, slug FROM boards WHERE id = ?`, boardID).Scan(&title, &slug)
	}
	if err != nil {
		return err
	}
	var base LayoutDocument
	if err := json.Unmarshal([]byte(doc), &base); err != nil {
		return fmt.Errorf("importer: board %s: layout: %w", boardID, err)
	}
	var baseTiles boards.Tiles
	if tiles != "" {
		if err := json.Unmarshal([]byte(tiles), &baseTiles); err != nil {
			return fmt.Errorf("importer: board %s: tiles: %w", boardID, err)
		}
	}
	have := map[string]bool{}
	for _, sec := range base.Sections {
		for _, it := range sec.Items {
			have[it.ID] = true
		}
	}
	for _, sec := range p.Board.Layout.Sections {
		var items []LayoutItem
		for _, it := range sec.Items {
			if !have[it.ID] {
				items = append(items, it)
			}
		}
		if len(items) == 0 {
			continue
		}
		i := slices.IndexFunc(base.Sections, func(b LayoutSection) bool { return b.ID == sec.ID })
		if i < 0 {
			sec.Items = []LayoutItem{}
			base.Sections = append(base.Sections, sec)
			i = len(base.Sections) - 1
		}
		dst := &base.Sections[i]
		for _, it := range items {
			if SizeWidth(it.Size) > dst.Span {
				it.Size = ClosestFit(it.SupportedSizes, it.Size, dst.Span)
			}
			dst.Items = append(dst.Items, it)
		}
	}
	if err := base.Pack(); err != nil {
		return err
	}
	p.Board.ID, p.Board.Name, p.Board.Slug, p.Board.FormatVersion, p.Board.Layout = boardID, title, slug, fv, base
	p.baseTiles = baseTiles
	return nil
}

func (s *Service) compareBoard(ctx context.Context, q querier, p *Plan, w *work) error {
	// Board.
	var title, slug string
	err := q.QueryRowContext(ctx, `SELECT title, slug FROM boards WHERE id = ?`, p.Board.ID).Scan(&title, &slug)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		w.board.action = ActionCreate
		if w.board.slug, err = freeSlug(ctx, q, p.Board.Slug); err != nil {
			return err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(position) + 1, 0) FROM boards`).Scan(&w.board.position); err != nil {
			return err
		}
	case err != nil:
		return err
	case title != p.Board.Name:
		w.board.action, w.board.slug = ActionUpdate, slug
	default:
		w.board.action, w.board.slug = ActionUnchanged, slug
	}
	p.Board.Slug = w.board.slug

	// Layout and tiles: compared as JSON values, so key order and whitespace
	// don't count. They share a revision, so either one changing writes both.
	doc, err := json.Marshal(p.Board.Layout)
	if err != nil {
		return err
	}
	tdoc, err := json.Marshal(TileConfigs(p))
	if err != nil {
		return err
	}
	w.layout.doc, w.layout.tdoc = doc, tdoc
	var rev sql.NullInt64
	var cur, curTiles sql.NullString
	if w.board.action != ActionCreate {
		err := q.QueryRowContext(ctx, `SELECT revision, document, tiles FROM layouts WHERE board_id = ? ORDER BY revision DESC LIMIT 1`, p.Board.ID).Scan(&rev, &cur, &curTiles)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	compareDoc := func(stored sql.NullString, planned []byte) string {
		switch {
		case !stored.Valid:
			return ActionCreate
		case sameJSON([]byte(stored.String), planned):
			return ActionUnchanged
		default:
			return ActionUpdate
		}
	}
	w.layout.action = compareDoc(cur, doc)
	w.layout.tiles = compareDoc(curTiles, tdoc)
	w.layout.next = rev.Int64 + 1
	stored := map[string]bool{}
	if cur.Valid {
		var prev LayoutDocument
		if json.Unmarshal([]byte(cur.String), &prev) == nil {
			for _, sec := range prev.Sections {
				for _, it := range sec.Items {
					stored[it.ID] = true
				}
			}
		}
	}
	for _, sec := range p.Board.Layout.Sections {
		for _, it := range sec.Items {
			if !stored[it.ID] {
				w.items = append(w.items, it.ID)
			}
		}
	}
	return nil
}

func (s *Service) compareRest(ctx context.Context, q querier, res *Result, secrets Secrets, w *work, add func(kind, id, name, action string) *Change) error {
	p := &res.Plan
	var err error
	// Instances.
	planned := make(map[string]bool, len(p.Instances))
	for _, in := range p.Instances {
		planned[in.ID] = true
	}
	for i := range p.Instances {
		in := &p.Instances[i]
		cfg, err := json.Marshal(in.Config)
		if err != nil {
			return err
		}
		var integ, name, stored string
		err = q.QueryRowContext(ctx, `SELECT integration, name, config FROM integration_instances WHERE id = ?`, in.ID).Scan(&integ, &name, &stored)
		action := ActionUnchanged
		switch {
		case errors.Is(err, sql.ErrNoRows):
			action = ActionCreate
		case err != nil:
			return err
		case integ != in.Integration || name != in.Name || !sameJSON([]byte(stored), cfg):
			action = ActionUpdate
		}
		asked := in.Name
		if action != ActionUnchanged {
			if in.Name, err = freeName(ctx, q, in.Integration, in.Name, planned); err != nil {
				return err
			}
			if action == ActionUpdate && integ == in.Integration && name == in.Name && sameJSON([]byte(stored), cfg) {
				action = ActionUnchanged // the only difference was a name we had to change anyway
			}
		}
		w.instances = append(w.instances, instanceWork{action: action, inst: in, config: cfg, oldName: name})
		ch := add("instance", in.ID, in.Name, action)
		if action == ActionCreate {
			if ch.Existing, ch.Match, err = resembles(ctx, q, in, asked, planned); err != nil {
				return err
			}
		}
	}

	// Secrets: equal values are left alone, so a re-import doesn't reseal.
	for _, ref := range p.Secrets {
		val, ok := secrets[ref.Name]
		if !ok {
			return fmt.Errorf("importer: plan names secret %q but carries no value for it", ref.Name)
		}
		action := ActionUnchanged
		old, err := s.vault.Get(ctx, ref.Name)
		switch {
		case errors.Is(err, vault.ErrNotFound):
			action = ActionCreate
		case errors.Is(err, vault.ErrCorrupt), errors.Is(err, vault.ErrWrongKey):
			action = ActionUpdate
		case err != nil:
			return err
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
		Source: p.Source, BoardID: p.Board.ID, Links: p.Links,
		Discovery: p.Discovery, Settings: p.Board.Settings,
		Instances: make([]string, 0, len(p.Instances)),
	}
	for _, in := range p.Instances {
		rec.Instances = append(rec.Instances, in.ID)
	}
	if w.record.value, err = json.Marshal(rec); err != nil {
		return err
	}
	var prevRaw string
	err = q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, RecordKey(p.Source)).Scan(&prevRaw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		w.record.action = ActionCreate
	case err != nil:
		return err
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
					return err
				}
			}
		}
	}
	add("record", RecordKey(p.Source), "", w.record.action)
	return nil
}

func (s *Service) write(ctx context.Context, tx *sql.Tx, p *Plan, w *work, secrets Secrets, userID string) error {
	now := store.NowMillis()
	if p.Board.ID == "" {
		w.board.action, w.layout.action, w.layout.tiles = ActionUnchanged, ActionUnchanged, ActionUnchanged
	}
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

	if w.layout.action != ActionUnchanged || w.layout.tiles != ActionUnchanged {
		createdBy := sql.NullString{String: userID, Valid: userID != ""}
		if _, err := tx.ExecContext(ctx, `INSERT INTO layouts (board_id, revision, format_version, document, tiles, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.Board.ID, w.layout.next, p.Board.FormatVersion, string(w.layout.doc), string(w.layout.tdoc), now, createdBy); err != nil {
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
