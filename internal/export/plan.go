package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/vault"
)

// Plan is the dry run: what importing a Home file would do to this hub.
// POST /api/import/chadash?dry_run=1 answers {"plan": Plan}. It never holds
// a secret value: secrets appear as field and vault names.
type Plan struct {
	Format     string     `json:"format"`
	ExportedAt *time.Time `json:"exported_at"`
	HubVersion string     `json:"hub_version,omitempty"`
	// Changes lists every object in the file and every board or instance
	// the hub has that the file doesn't (stale, never deleted).
	Changes []Change `json:"changes"`
	// SecretsNeeded are secret fields that end up unset: their instance is
	// created or kept without them and shows a Repair until they're entered.
	SecretsNeeded []NeededSecret `json:"secrets_needed"`
	Bundle        BundleState    `json:"bundle"`
	Summary       Summary        `json:"summary"`
	Warnings      []Warning      `json:"warnings"`
}

// Change is one object: the importer's change row (kind, id, name,
// action) plus what changes inside it. Kinds are board, layout, tiles,
// instance and setting; actions are create, update, unchanged and stale.
type Change struct {
	importer.Change
	// FileID is the object's ID in the file when the hub's differs: a board
	// matched by its slug.
	FileID      string `json:"file_id,omitempty"`
	Integration string `json:"integration,omitempty"`
	// Detail is a short description of a created object ("2 sections, 9
	// items") when listing its fields would be noise.
	Detail    string         `json:"detail,omitempty"`
	Fields    []FieldChange  `json:"fields,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Secrets   []SecretChange `json:"secrets,omitempty"`
}

func (c *Change) diffRaw(prefix string, a, b json.RawMessage) error {
	x, err := decodeJSON(a)
	if err != nil {
		return err
	}
	y, err := decodeJSON(b)
	if err != nil {
		return err
	}
	c.diff(prefix, x, y)
	return nil
}

func (c *Change) diff(prefix string, a, b any) {
	d := differ{out: c.Fields, truncated: c.Truncated}
	d.diff(prefix, a, b)
	c.Fields, c.Truncated = d.out, d.truncated
}

// SecretChange is what happens to one secret field. Never a value.
type SecretChange struct {
	Field  string `json:"field"`
	Secret string `json:"secret"` // the vault entry
	// Action is create (set where it wasn't), update (a new value or another
	// vault entry), unchanged, or needed (no value anywhere: left unset).
	Action string `json:"action"`
	// Source is where the value comes from: bundle (restored from the
	// file's encrypted bundle) or vault (already in this hub's vault).
	Source string `json:"source,omitempty"`
	// InBundle: needed, but the file's bundle has it; the passphrase would
	// restore it.
	InBundle bool `json:"in_bundle,omitempty"`
}

// NeededSecret is a secret field left unset.
type NeededSecret struct {
	Instance    string `json:"instance"`
	Name        string `json:"name"`
	Integration string `json:"integration"`
	Field       string `json:"field"`
	Secret      string `json:"secret"`
	InBundle    bool   `json:"in_bundle,omitempty"`
}

// BundleState says whether the file carries encrypted secrets and whether
// the passphrase opened them.
type BundleState struct {
	Present  bool `json:"present"`
	Unlocked bool `json:"unlocked"`
	Secrets  int  `json:"secrets"`
}

// Summary counts the changes.
type Summary struct {
	Create          int `json:"create"`
	Update          int `json:"update"`
	Unchanged       int `json:"unchanged"`
	Stale           int `json:"stale"`
	SecretsRestored int `json:"secrets_restored"`
	SecretsNeeded   int `json:"secrets_needed"`
}

// Warning is something the import does that the owner should know about.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Kind    string `json:"kind,omitempty"`
	ID      string `json:"id,omitempty"`
}

// Changed reports whether applying the plan writes anything.
func (p *Plan) Changed() bool { return p.Summary.Create > 0 || p.Summary.Update > 0 }

const (
	actionCreate    = importer.ActionCreate
	actionUpdate    = importer.ActionUpdate
	actionUnchanged = importer.ActionUnchanged
	actionStale     = importer.ActionStale
	// SecretNeeded is a secret field with no value to give it.
	SecretNeeded = "needed"

	sourceBundle = "bundle"
	sourceVault  = "vault"

	// actionTouch bumps an instance's updated_at without changing it.
	actionTouch = "touch"
)

type querier interface {
	boards.Querier
}

// work is a plan plus the writes that carry it out.
type work struct {
	plan      Plan
	boards    []boardWrite
	layouts   []layoutWrite
	instances []instanceWrite
	secrets   map[string]importer.Secret // vault name → value to store
	settings  []settingWrite
	boardIDs  []string
	instIDs   []string
}

type boardWrite struct {
	action string
	d      boards.Detail
}

type layoutWrite struct {
	boardID string
	fv      int64
	doc     json.RawMessage
	tiles   json.RawMessage
}

type instanceWrite struct {
	action      string
	id          string
	integration string
	name        string
	oldName     string
	enabled     bool
	config      []byte
}

type settingWrite struct {
	key   string
	value json.RawMessage
}

// storedRow is an integration_instances row as compare reads it.
type storedRow struct {
	id, integration, name string
	enabled               bool
	config                map[string]any
}

func (s *Service) compare(ctx context.Context, q querier, d *Document, bundle importer.Secrets) (*work, error) {
	w := &work{secrets: map[string]importer.Secret{}}
	p := &w.plan
	p.Format = APIVersion
	if !d.Meta.ExportedAt.IsZero() {
		t := d.Meta.ExportedAt.UTC()
		p.ExportedAt = &t
	}
	p.HubVersion = d.Meta.HubVersion
	p.Changes, p.SecretsNeeded, p.Warnings = []Change{}, []NeededSecret{}, []Warning{}
	if d.Bundle != nil {
		p.Bundle = BundleState{Present: true, Unlocked: bundle != nil, Secrets: len(d.Bundle.Names)}
	}

	remap, err := s.compareBoards(ctx, q, d, w)
	if err != nil {
		return nil, err
	}
	if err := s.compareInstances(ctx, q, d, bundle, w); err != nil {
		return nil, err
	}
	if err := s.compareSettings(ctx, q, d, remap, w); err != nil {
		return nil, err
	}

	// Stale objects go last, boards before instances.
	slices.SortStableFunc(p.Changes, func(a, b Change) int {
		return boolInt(a.Action == actionStale) - boolInt(b.Action == actionStale)
	})
	for _, c := range p.Changes {
		switch c.Action {
		case actionCreate:
			p.Summary.Create++
		case actionUpdate:
			p.Summary.Update++
		case actionUnchanged:
			p.Summary.Unchanged++
		case actionStale:
			p.Summary.Stale++
		}
		for _, sc := range c.Secrets {
			if sc.Source == sourceBundle {
				p.Summary.SecretsRestored++
			}
		}
	}
	p.Summary.SecretsNeeded = len(p.SecretsNeeded)
	return w, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// compareBoards plans the boards, their layouts and tile config. Boards
// match by ID, then by slug (so a fresh hub's default Home board takes the
// file's Home board instead of gaining a second one). It returns the file
// IDs that matched a board with another ID.
func (s *Service) compareBoards(ctx context.Context, q querier, d *Document, w *work) (map[string]string, error) {
	p := &w.plan
	existing, err := boards.ReadAll(ctx, q)
	if err != nil {
		return nil, err
	}
	byID := map[string]boards.Detail{}
	bySlug := map[string]boards.Detail{}
	for _, b := range existing {
		byID[b.ID], bySlug[b.Slug] = b, b
	}
	inFile := map[string]bool{}
	for _, b := range d.Boards {
		inFile[b.ID] = true
	}
	matched := map[string]bool{} // hub board IDs the file claims
	takenSlugs := map[string]string{}
	remap := map[string]string{}

	for i, fb := range d.Boards {
		cur, exists := byID[fb.ID]
		if !exists {
			if c, ok := bySlug[fb.Slug]; ok && !inFile[c.ID] && !matched[c.ID] {
				cur, exists = c, true
				remap[fb.ID] = c.ID
			}
		}
		target := fb.ID
		if exists {
			target = cur.ID
		}
		matched[target] = true

		slug := fb.Slug
		for n := 2; ; n++ {
			holder, held := bySlug[slug]
			if (!held || holder.ID == target) && takenSlugs[slug] == "" {
				break
			}
			slug = fb.Slug + "-" + strconv.Itoa(n)
		}
		takenSlugs[slug] = target
		want := boards.Detail{ID: target, Slug: slug, Name: fb.Name, Position: fb.Position}

		c := Change{}
		c.Kind, c.ID, c.Name = "board", target, fb.Name
		if target != fb.ID {
			c.FileID = fb.ID
		}
		view := func(b boards.Detail) map[string]any {
			return map[string]any{"name": b.Name, "slug": b.Slug, "position": json.Number(strconv.FormatInt(b.Position, 10))}
		}
		switch {
		case !exists:
			c.Action = actionCreate
			c.diff("", map[string]any{}, view(want))
		case cur.Name == want.Name && cur.Slug == want.Slug && cur.Position == want.Position:
			c.Action = actionUnchanged
		default:
			c.Action = actionUpdate
			c.diff("", view(cur), view(want))
		}
		if c.Action != actionUnchanged {
			w.boards = append(w.boards, boardWrite{action: c.Action, d: want})
		}
		w.boardIDs = append(w.boardIDs, target)
		p.Changes = append(p.Changes, c)

		// The layout and its tiles share a revision: either one changing
		// appends one revision that carries both.
		var layoutChanged bool
		if fb.Layout != nil {
			lc := Change{}
			lc.Kind, lc.ID, lc.Name = "layout", target, fb.Name
			switch {
			case !exists || cur.Layout.Document == nil:
				lc.Action = actionCreate
				lc.Detail = layoutDetail(fb.Layout.Document)
			case cur.Layout.FormatVersion == fb.Layout.FormatVersion && sameRaw(cur.Layout.Document, fb.Layout.Document):
				lc.Action = actionUnchanged
			default:
				lc.Action = actionUpdate
				if cur.Layout.FormatVersion != fb.Layout.FormatVersion {
					lc.Fields = append(lc.Fields, FieldChange{Path: "/format_version", Op: OpReplace,
						From: json.Number(strconv.FormatInt(cur.Layout.FormatVersion, 10)), To: json.Number(strconv.FormatInt(fb.Layout.FormatVersion, 10))})
				}
				if err := lc.diffRaw("/document", cur.Layout.Document, fb.Layout.Document); err != nil {
					return nil, fmt.Errorf("export: board %s: layout: %w", target, err)
				}
			}
			layoutChanged = lc.Action != actionUnchanged
			p.Changes = append(p.Changes, lc)
		}
		tw, err := planTiles(fmt.Sprintf("boards[%d].tiles", i), cur, exists, target, fb.Name, fb.Tiles)
		if err != nil {
			return nil, err
		}
		if tw.change != nil {
			p.Changes = append(p.Changes, *tw.change)
		}
		if layoutChanged || tw.write != nil {
			lw := layoutWrite{boardID: target, tiles: json.RawMessage(boards.EmptyTiles)}
			if exists && cur.Layout.Document != nil {
				lw.fv, lw.doc, lw.tiles = cur.Layout.FormatVersion, cur.Layout.Document, cur.Layout.Tiles
			}
			if fb.Layout != nil {
				lw.fv, lw.doc = fb.Layout.FormatVersion, fb.Layout.Document
			}
			if tw.write != nil {
				lw.tiles = tw.write
			}
			if lw.doc == nil {
				// Tiles belong to a layout revision; this board has none.
				p.Warnings = append(p.Warnings, Warning{Code: "tiles_ignored", Kind: "board", ID: target,
					Message: "The file has tile settings for " + fb.Name + " but no layout to keep them with; they weren't imported."})
				tw.change.Action, tw.change.Fields, tw.change.Detail = actionUnchanged, nil, ""
				p.Changes[len(p.Changes)-1] = *tw.change
			} else {
				w.layouts = append(w.layouts, lw)
			}
		}
	}

	for _, b := range existing {
		if !matched[b.ID] {
			c := Change{}
			c.Kind, c.ID, c.Name, c.Action = "board", b.ID, b.Name, actionStale
			p.Changes = append(p.Changes, c)
		}
	}
	return remap, nil
}

// layoutDetail describes a layout document: "3 sections, 22 items".
func layoutDetail(doc json.RawMessage) string {
	var l struct {
		Sections []struct {
			Items []json.RawMessage `json:"items"`
		} `json:"sections"`
	}
	if json.Unmarshal(doc, &l) != nil || l.Sections == nil {
		return ""
	}
	items := 0
	for _, s := range l.Sections {
		items += len(s.Items)
	}
	return plural(len(l.Sections), "section") + ", " + plural(items, "item")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func loadRows(ctx context.Context, q querier) ([]storedRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, integration, name, config, enabled FROM integration_instances ORDER BY integration, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedRow
	for rows.Next() {
		var r storedRow
		var cfg string
		var enabled int64
		if err := rows.Scan(&r.id, &r.integration, &r.name, &cfg, &enabled); err != nil {
			return nil, err
		}
		r.enabled = enabled != 0
		v, err := decodeJSON(json.RawMessage(cfg))
		if err != nil {
			return nil, fmt.Errorf("export: instance %s: config isn't JSON: %w", r.id, err)
		}
		r.config, _ = v.(map[string]any)
		if r.config == nil {
			r.config = map[string]any{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// refsOf returns the vault name each top-level secret reference names.
func refsOf(cfg map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range cfg {
		if s, ok := v.(string); ok {
			if name, ok := integration.ParseSecretRef(s); ok {
				out[k] = name
			}
		}
	}
	return out
}

// instanceView is the part of an instance its field diff covers: secrets
// are reported apart, by name.
func instanceView(name string, enabled bool, cfg map[string]any) map[string]any {
	plain := map[string]any{}
	v := map[string]any{"name": name, "enabled": enabled, "config": plain}
	for k, x := range cfg {
		switch {
		case k == "tls":
			v["tls"] = x
		case refsOf(map[string]any{k: x})[k] != "":
		default:
			plain[k] = x
		}
	}
	return v
}

func (s *Service) compareInstances(ctx context.Context, q querier, d *Document, bundle importer.Secrets, w *work) error {
	p := &w.plan
	rows, err := loadRows(ctx, q)
	if err != nil {
		return err
	}
	byID := map[string]storedRow{}
	for _, r := range rows {
		byID[r.id] = r
	}
	planned := map[string]bool{}
	for _, in := range d.Instances {
		planned[in.ID] = true
	}
	var bundleNames map[string]bool
	if d.Bundle != nil {
		bundleNames = map[string]bool{}
		for _, n := range d.Bundle.Names {
			bundleNames[n] = true
		}
	}
	// What the vault holds, read once per name.
	type held struct {
		value []byte
		ok    bool
	}
	vaultCache := map[string]held{}
	inVault := func(name string) (held, error) {
		if h, ok := vaultCache[name]; ok {
			return h, nil
		}
		v, err := s.vault.Get(ctx, name)
		var h held
		switch {
		case err == nil:
			h = held{value: v, ok: true}
		case errors.Is(err, vault.ErrNotFound), errors.Is(err, vault.ErrWrongKey), errors.Is(err, vault.ErrCorrupt):
			// Unreadable counts as missing: a restore overwrites it.
		default:
			return held{}, err
		}
		vaultCache[name] = h
		return h, nil
	}
	defer func() {
		for _, h := range vaultCache {
			clear(h.value)
		}
	}()

	for i, in := range d.Instances {
		path := fmt.Sprintf("instances[%d]", i)
		_, man, known := s.reg.Get(in.Integration)
		cfgAny, err := decodeJSON(in.Config)
		if err != nil {
			return err
		}
		cfg, _ := cfgAny.(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
		}
		if known {
			for _, k := range sortedKeys(cfg) {
				if man.IsSecret(k) {
					return &ParseError{Path: path + ".config." + k, Msg: "is a secret field of " + man.Name + ": give it a !secret placeholder under secrets; Home files never hold secret values"}
				}
			}
			for _, f := range sortedKeys(in.Secrets) {
				if !man.IsSecret(f) {
					return &ParseError{Path: path + ".secrets." + f, Msg: "isn't a secret field of " + man.Name}
				}
			}
		} else {
			p.Warnings = append(p.Warnings, Warning{Code: "unknown_integration", Kind: "instance", ID: in.ID,
				Message: fmt.Sprintf("%s uses the %q integration, which this hub doesn't include. It's imported, and shows an error until the integration is available.", in.Name, in.Integration)})
		}

		cur, exists := byID[in.ID]
		curRefs := map[string]string{}
		if exists {
			curRefs = refsOf(cur.config)
		}

		next := cloneMap(cfg)
		if len(in.TLS) > 0 {
			t, err := decodeJSON(in.TLS)
			if err != nil {
				return err
			}
			next["tls"] = t
		}

		c := Change{Integration: in.Integration}
		c.Kind, c.ID = "instance", in.ID
		secretWrites := false
		for _, field := range sortedKeys(in.Secrets) {
			name := in.Secrets[field]
			sc := SecretChange{Field: field, Secret: name}
			old := curRefs[field]
			setFrom := func(ref, source string) {
				next[field] = integration.SecretRef(ref)
				sc.Secret, sc.Source = ref, source
				switch old {
				case "":
					sc.Action = actionCreate
				case ref:
					sc.Action = actionUnchanged
				default:
					sc.Action = actionUpdate
				}
			}
			h, err := inVault(name)
			if err != nil {
				return err
			}
			if v, ok := bundle[name]; ok && !(h.ok && v.Equal(string(h.value))) {
				setFrom(name, sourceBundle)
				if sc.Action == actionUnchanged {
					sc.Action = actionUpdate // same entry, new value
				}
				w.secrets[name] = v
				secretWrites = true
			} else if h.ok {
				setFrom(name, sourceVault)
			} else if old != "" {
				if oh, err := inVault(old); err != nil {
					return err
				} else if oh.ok {
					// The file names another entry, but the hub already has a
					// working secret for this field: keep it.
					setFrom(old, sourceVault)
				}
			}
			if sc.Action == "" {
				sc.Action = SecretNeeded
				sc.InBundle = bundleNames[name] && bundle == nil
				delete(next, field)
			}
			if sc.Action == actionCreate || sc.Action == actionUpdate {
				secretWrites = true
			}
			c.Secrets = append(c.Secrets, sc)
		}
		// Secrets the file doesn't mention keep their value, as in the API.
		for field, ref := range curRefs {
			if _, mentioned := in.Secrets[field]; !mentioned {
				next[field] = integration.SecretRef(ref)
			}
		}

		name := in.Name
		if !exists || cur.name != in.Name {
			if name, err = freeName(rows, planned, in.Integration, in.Name); err != nil {
				return err
			}
		}
		c.Name = name
		config, err := json.Marshal(next)
		if err != nil {
			return err
		}
		wantView := instanceView(name, in.Enabled, next)
		rowChanged := false
		switch {
		case !exists:
			c.Action = actionCreate
			c.diff("", map[string]any{"name": name, "enabled": true, "config": map[string]any{}}, wantView)
		case cur.integration != in.Integration || cur.name != name || cur.enabled != in.Enabled || !equalJSON(cur.config, next):
			c.Action, rowChanged = actionUpdate, true
			c.diff("", instanceView(cur.name, cur.enabled, cur.config), wantView)
			if cur.integration != in.Integration {
				c.Fields = append(c.Fields, FieldChange{Path: "/integration", Op: OpReplace, From: cur.integration, To: in.Integration})
			}
		case secretWrites:
			c.Action = actionUpdate
		default:
			c.Action = actionUnchanged
		}
		switch {
		case !exists || rowChanged:
			w.instances = append(w.instances, instanceWrite{action: c.Action, id: in.ID, integration: in.Integration,
				name: name, oldName: cur.name, enabled: in.Enabled, config: config})
		case secretWrites:
			// Only a vault value changes: touch the row, so the runtime
			// restarts the instance with it.
			w.instances = append(w.instances, instanceWrite{action: actionTouch, id: in.ID})
		}
		for _, sc := range c.Secrets {
			if sc.Action == SecretNeeded {
				p.SecretsNeeded = append(p.SecretsNeeded, NeededSecret{Instance: in.ID, Name: name, Integration: in.Integration,
					Field: sc.Field, Secret: sc.Secret, InBundle: sc.InBundle})
			}
		}
		if known {
			s.checkConfig(man, path, in, name, cfg, next, p)
		}
		w.instIDs = append(w.instIDs, in.ID)
		p.Changes = append(p.Changes, c)
	}

	for _, r := range rows {
		if !planned[r.id] {
			c := Change{Integration: r.integration}
			c.Kind, c.ID, c.Name, c.Action = "instance", r.id, r.name, actionStale
			p.Changes = append(p.Changes, c)
		}
	}
	return nil
}

// checkConfig validates an instance's non-secret config against its
// integration's schema. Problems are warnings: the instance is imported
// and shows an error until it's fixed, as it would after a hand edit.
// Missing secrets aren't problems here; they're listed as needed.
func (s *Service) checkConfig(man integration.Manifest, path string, in Instance, name string, cfg, next map[string]any, p *Plan) {
	full := cloneMap(cfg)
	if t, ok := next["tls"]; ok {
		full["tls"] = t
	}
	for f := range in.Secrets {
		if _, set := next[f]; set {
			full[f] = "set" // a placeholder value: only presence matters here
		}
	}
	for _, fe := range man.ConfigSchema.Validate(full) {
		field := strings.TrimPrefix(fe.Path, "/")
		if man.IsSecret(field) {
			continue
		}
		p.Warnings = append(p.Warnings, Warning{Code: "invalid_config", Kind: "instance", ID: in.ID,
			Message: fmt.Sprintf("%s (%s): %s %s. It won't start until that's fixed.", name, path, fe.Path, fe.Message)})
	}
}

// freeName is the importer's rule: an instance outside the file that holds
// the name keeps it, and the imported one becomes "Name (imported)".
func freeName(rows []storedRow, planned map[string]bool, integ, name string) (string, error) {
	taken := func(n string) bool {
		for _, r := range rows {
			if !planned[r.id] && r.integration == integ && r.name == n {
				return true
			}
		}
		return false
	}
	base := strings.TrimSpace(name)
	for n := 1; n < 1000; n++ {
		if !taken(name) {
			return name, nil
		}
		if n == 1 {
			name = base + " (imported)"
		} else {
			name = fmt.Sprintf("%s (imported %d)", base, n)
		}
	}
	return "", fmt.Errorf("export: no free name for %s", base)
}

func (s *Service) compareSettings(ctx context.Context, q querier, d *Document, remap map[string]string, w *work) error {
	p := &w.plan
	for _, st := range d.Settings {
		if !carried(st.Key) {
			p.Warnings = append(p.Warnings, Warning{Code: "setting_ignored", Kind: "setting", ID: st.Key,
				Message: "The setting " + st.Key + " isn't one a Home file carries; it wasn't imported."})
			continue
		}
		value, err := remapRecord(st.Value, remap)
		if err != nil {
			return fmt.Errorf("export: setting %s: %w", st.Key, err)
		}
		c := Change{}
		c.Kind, c.ID = "setting", st.Key
		var cur string
		err = q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, st.Key).Scan(&cur)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			c.Action = actionCreate
		case err != nil:
			return err
		case sameRaw(json.RawMessage(cur), value):
			c.Action = actionUnchanged
		default:
			c.Action = actionUpdate
			if err := c.diffRaw("", json.RawMessage(cur), value); err != nil {
				return fmt.Errorf("export: setting %s: %w", st.Key, err)
			}
		}
		if c.Action != actionUnchanged {
			w.settings = append(w.settings, settingWrite{key: st.Key, value: value})
		}
		p.Changes = append(p.Changes, c)
	}
	return nil
}

// remapRecord points an import record at the hub's board when the file's
// board matched one with another ID.
func remapRecord(value json.RawMessage, remap map[string]string) (json.RawMessage, error) {
	if len(remap) == 0 {
		return value, nil
	}
	var probe struct {
		BoardID string `json:"board_id"`
	}
	if json.Unmarshal(value, &probe) != nil || remap[probe.BoardID] == "" {
		return value, nil
	}
	v, err := decodeJSON(value)
	if err != nil {
		return nil, err
	}
	m := v.(map[string]any)
	m["board_id"] = remap[probe.BoardID]
	return json.Marshal(m)
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneJSON(v)
	}
	return out
}

func cloneJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneJSON(x)
		}
		return out
	}
	return v
}
