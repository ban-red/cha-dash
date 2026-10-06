package export

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

// Service exports the hub as a Home file and imports one.
type Service struct {
	st      *store.Store
	vault   *vault.Vault // nil: export only, with placeholders and no leak guard
	reg     *integration.Registry
	version string
	now     func() time.Time

	// Audit records security settings an HTTP import changed or was
	// refused (nil: none).
	Audit *authz.Recorder
	// AfterApply runs after an HTTP import changed something, so the running
	// hub takes up imported security settings and decisions (nil: nothing).
	AfterApply func(context.Context)
}

// Options configure a Service.
type Options struct {
	Registry   *integration.Registry // nil: integration.Default
	HubVersion string                // written to meta.hub_version
	Now        func() time.Time      // nil: time.Now
}

// New returns a Service. v may be nil for an export without the vault key
// (the CLI after a lost key): placeholders only, no bundle, no import.
func New(st *store.Store, v *vault.Vault, o Options) *Service {
	if o.Registry == nil {
		o.Registry = integration.Default
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{st: st, vault: v, reg: o.Registry, version: o.HubVersion, now: o.Now}
}

// ErrNoVault means the operation needs the vault and the Service has none.
var ErrNoVault = errors.New("export: the vault key isn't available")

// LeakError means an export would have held a stored secret's value in
// plain text: the value also appears in a non-secret field or setting.
type LeakError struct{ Secret string }

func (e *LeakError) Error() string {
	return fmt.Sprintf("export: refusing to write a Home file that holds the value of secret %s in plain text (it also appears in a non-secret field or setting); change that secret or that field", e.Secret)
}

// minGuardLen is the shortest secret value the leak guard looks for. A
// shorter one (a dictionary-word password) could match an ordinary value
// by chance; API keys and tokens are far longer.
const minGuardLen = 10

// Exported is a Home file and what went into it.
type Exported struct {
	Data      []byte
	Boards    int
	Instances int
	Settings  int
	// Secrets counts the placeholders; Bundled the values sealed in the
	// bundle. Missing names placeholders the vault had no value for.
	Secrets  int
	Bundled  int
	Missing  []string
	Warnings []Warning
}

// Export writes the hub as a Home file. With a passphrase it adds the
// encrypted bundle of every secret the instances reference.
func (s *Service) Export(ctx context.Context, passphrase string) (*Exported, error) {
	if passphrase != "" {
		if s.vault == nil {
			return nil, ErrNoVault
		}
		if err := CheckPassphrase(passphrase); err != nil {
			return nil, err
		}
	}
	d, out, err := s.read(ctx)
	if err != nil {
		return nil, err
	}

	// Every value a placeholder names, for the bundle and the leak guard.
	names := map[string]bool{}
	for _, in := range d.Instances {
		for _, n := range in.Secrets {
			names[n] = true
		}
	}
	values := importer.Secrets{}
	if s.vault != nil {
		for _, n := range sortedKeys(names) {
			v, err := s.vault.Get(ctx, n)
			switch {
			case err == nil:
				values[n] = importer.NewSecret(string(v))
				clear(v)
			case errors.Is(err, vault.ErrNotFound), errors.Is(err, vault.ErrWrongKey), errors.Is(err, vault.ErrCorrupt):
				out.Missing = append(out.Missing, n)
			default:
				return nil, err
			}
		}
	}
	for _, n := range out.Missing {
		out.Warnings = append(out.Warnings, Warning{Code: "secret_missing", ID: n,
			Message: "The vault has no readable value for " + n + "; the file names it, but no backup can restore it."})
	}

	if err := guard(d, values); err != nil {
		return nil, err
	}
	plain, err := Encode(d)
	if err != nil {
		return nil, err
	}
	out.Secrets = 0
	for _, in := range d.Instances {
		out.Secrets += len(in.Secrets)
	}
	out.Data = plain
	if passphrase != "" {
		b, err := sealBundle(passphrase, values)
		if err != nil {
			return nil, err
		}
		d.Bundle = b
		if out.Data, err = Encode(d); err != nil {
			return nil, err
		}
		out.Bundled = len(values)
	}
	return out, nil
}

// guard refuses a document that holds a stored secret's value in plain
// text. The encoder never sees a value (placeholders are names), so this
// only trips when a value was also typed into a non-secret field or ended
// up in a carried setting (a token in a link's query string): writing the
// file would leak it there. It looks at every string value in the
// document, never at keys.
func guard(d *Document, values importer.Secrets) error {
	var check []string
	for _, name := range sortedKeys(values) {
		if v := values[name].Reveal(); len(v) >= minGuardLen {
			check = append(check, name)
		}
	}
	if len(check) == 0 {
		return nil
	}
	var leak string
	visit := func(s string) {
		for _, name := range check {
			if leak == "" && strings.Contains(s, values[name].Reveal()) {
				leak = name
			}
		}
	}
	walkRaw := func(raw json.RawMessage) error {
		v, err := decodeJSON(raw)
		if err != nil {
			return err
		}
		walkStrings(v, visit)
		return nil
	}
	visit(d.Meta.HubVersion)
	for _, b := range d.Boards {
		visit(b.ID)
		visit(b.Name)
		visit(b.Slug)
		if b.Layout != nil {
			if err := walkRaw(b.Layout.Document); err != nil {
				return err
			}
		}
		if err := walkRaw(b.Tiles); err != nil {
			return err
		}
	}
	for _, in := range d.Instances {
		visit(in.ID)
		visit(in.Name)
		visit(in.Integration)
		if err := walkRaw(in.Config); err != nil {
			return err
		}
		if err := walkRaw(in.TLS); err != nil {
			return err
		}
	}
	for _, s := range d.Settings {
		if err := walkRaw(s.Value); err != nil {
			return err
		}
	}
	for _, c := range d.Correlation {
		visit(c.From)
		visit(c.To)
		visit(c.Rule)
	}
	if leak != "" {
		return &LeakError{Secret: leak}
	}
	return nil
}

// walkStrings calls f with every string value in v (never a key).
func walkStrings(v any, f func(string)) {
	switch t := v.(type) {
	case string:
		f(t)
	case map[string]any:
		for _, x := range t {
			walkStrings(x, f)
		}
	case []any:
		for _, x := range t {
			walkStrings(x, f)
		}
	}
}

// read builds the document from the database.
func (s *Service) read(ctx context.Context) (*Document, *Exported, error) {
	q := s.st.Reader()
	out := &Exported{}
	d := &Document{Meta: Meta{ExportedAt: s.now().UTC().Truncate(time.Second), HubVersion: s.version}}

	bs, err := boards.ReadAll(ctx, q)
	if err != nil {
		return nil, nil, fmt.Errorf("export: boards: %w", err)
	}
	for _, b := range bs {
		fb := Board{ID: b.ID, Name: b.Name, Slug: b.Slug, Position: b.Position}
		if b.Layout.Document != nil {
			fb.Layout = &Layout{FormatVersion: b.Layout.FormatVersion, Document: b.Layout.Document}
		}
		fb.Tiles = exportTiles(b)
		d.Boards = append(d.Boards, fb)
	}

	rows, err := loadRows(ctx, q)
	if err != nil {
		return nil, nil, fmt.Errorf("export: instances: %w", err)
	}
	for _, r := range rows {
		in, warns, err := s.instanceOf(r)
		if err != nil {
			return nil, nil, err
		}
		out.Warnings = append(out.Warnings, warns...)
		d.Instances = append(d.Instances, in)
	}

	srows, err := q.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, nil, fmt.Errorf("export: settings: %w", err)
	}
	defer srows.Close()
	for srows.Next() {
		var k, v string
		if err := srows.Scan(&k, &v); err != nil {
			return nil, nil, err
		}
		if carried(k) {
			d.Settings = append(d.Settings, Setting{Key: k, Value: json.RawMessage(v)})
		}
	}
	if err := srows.Err(); err != nil {
		return nil, nil, err
	}
	sec, err := readSecurity(ctx, q)
	if err != nil {
		return nil, nil, fmt.Errorf("export: security settings: %w", err)
	}
	d.Security = &sec
	if d.Correlation, err = readDecisions(ctx, q); err != nil {
		return nil, nil, fmt.Errorf("export: correlation decisions: %w", err)
	}
	for i := range d.Correlation {
		d.Correlation[i] = d.Correlation[i].normalized()
	}
	slices.SortFunc(d.Correlation, func(a, b Decision) int { return strings.Compare(a.key(), b.key()) })
	out.Boards, out.Instances, out.Settings = len(d.Boards), len(d.Instances), len(d.Settings)
	return d, out, nil
}

// instanceOf splits a stored row into config, TLS policy and placeholders.
// A secret reference always becomes a placeholder. A value that should
// have been a reference (a secret field holding plain text, or a reference
// nested inside the config) is left out with a warning; it never reaches
// the file.
func (s *Service) instanceOf(r storedRow) (Instance, []Warning, error) {
	_, man, known := s.reg.Get(r.integration)
	in := Instance{ID: r.id, Integration: r.integration, Name: r.name, Enabled: r.enabled, Secrets: map[string]string{}}
	cfg := map[string]any{}
	var warns []Warning
	for _, k := range sortedKeys(r.config) {
		v := r.config[k]
		if k == "tls" {
			t, err := json.Marshal(v)
			if err != nil {
				return Instance{}, nil, err
			}
			in.TLS = t
			continue
		}
		if s, ok := v.(string); ok {
			if name, ok := integration.ParseSecretRef(s); ok {
				in.Secrets[k] = name
				continue
			}
		}
		if (known && man.IsSecret(k)) || mentionsRef(v) {
			warns = append(warns, Warning{Code: "value_left_out", Kind: "instance", ID: r.id,
				Message: fmt.Sprintf("%s: %s holds a value outside the vault, so it was left out of the file. Enter it again in Cha Dash to move it into the vault.", r.name, k)})
			continue
		}
		cfg[k] = v
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return Instance{}, nil, err
	}
	in.Config = b
	return in, warns, nil
}

// mentionsRef reports whether any string in v holds a secret reference.
func mentionsRef(v any) bool {
	switch t := v.(type) {
	case string:
		return bytes.Contains([]byte(t), []byte("${secret:"))
	case map[string]any:
		for _, x := range t {
			if mentionsRef(x) {
				return true
			}
		}
	case []any:
		if slices.ContainsFunc(t, mentionsRef) {
			return true
		}
	}
	return false
}

// unlock opens the file's bundle with passphrase. No bundle or no
// passphrase gives nil secrets: placeholders then resolve against the vault
// or are needed.
func unlock(d *Document, passphrase string) (importer.Secrets, error) {
	if d.Bundle == nil || passphrase == "" {
		return nil, nil
	}
	return d.Bundle.Open(passphrase)
}

// Plan compares a Home file with the hub and returns what importing it
// would do, without writing anything. A wrong passphrase is
// ErrWrongPassphrase; a file that doesn't fit this hub's integrations is a
// *ParseError.
func (s *Service) Plan(ctx context.Context, d *Document, passphrase string) (*Plan, error) {
	if s.vault == nil {
		return nil, ErrNoVault
	}
	secrets, err := unlock(d, passphrase)
	if err != nil {
		return nil, err
	}
	w, err := s.compare(ctx, s.st.Reader(), d, secrets)
	if err != nil {
		return nil, err
	}
	noteUnusedPassphrase(d, passphrase, &w.plan)
	return &w.plan, nil
}

func noteUnusedPassphrase(d *Document, passphrase string, p *Plan) {
	if d.Bundle == nil && passphrase != "" {
		p.Warnings = append(p.Warnings, Warning{Code: "no_bundle", Message: "This file has no encrypted secrets, so the passphrase wasn't needed."})
	}
}

// Applied is what Apply did.
type Applied struct {
	Plan        *Plan    `json:"plan"`
	Changed     bool     `json:"changed"`
	BoardIDs    []string `json:"board_ids"`
	InstanceIDs []string `json:"instance_ids"`
}

// Apply imports a Home file in one write transaction: boards, layouts and
// their tiles (a change to either appends one revision, so the hub's own
// history stays), instances, secrets restored from the bundle (sealed in the
// same transaction) and carried settings. Objects already equal are left
// alone, so applying the same file twice writes nothing the second time.
// Nothing is deleted. userID may be empty.
func (s *Service) Apply(ctx context.Context, d *Document, passphrase, userID string) (*Applied, error) {
	if s.vault == nil {
		return nil, ErrNoVault
	}
	secrets, err := unlock(d, passphrase)
	if err != nil {
		return nil, err
	}
	var w *work
	err = s.st.WriteTx(ctx, func(tx *sql.Tx) error {
		var err error
		if w, err = s.compare(ctx, tx, d, secrets); err != nil {
			return err
		}
		return s.write(ctx, tx, w, userID)
	})
	if err != nil {
		return nil, err
	}
	noteUnusedPassphrase(d, passphrase, &w.plan)
	return &Applied{Plan: &w.plan, Changed: w.plan.Changed(), BoardIDs: nonNil(w.boardIDs), InstanceIDs: nonNil(w.instIDs)}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Service) write(ctx context.Context, tx *sql.Tx, w *work, userID string) error {
	for _, b := range w.boards {
		var err error
		if b.action == actionCreate {
			err = boards.CreateTx(ctx, tx, b.d)
		} else {
			err = boards.UpdateTx(ctx, tx, b.d)
		}
		if err != nil {
			return err
		}
	}
	for _, l := range w.layouts {
		if _, err := boards.AppendLayoutTx(ctx, tx, l.boardID, l.fv, l.doc, l.tiles, userID); err != nil {
			return err
		}
	}

	now := store.NowMillis()
	// Renames go through a placeholder first, so instances can trade names
	// without tripping UNIQUE (integration, name) halfway through.
	for _, iw := range w.instances {
		if iw.action == actionUpdate && iw.oldName != iw.name {
			if _, err := tx.ExecContext(ctx, `UPDATE integration_instances SET name = ? WHERE id = ?`, "~renaming~"+iw.id, iw.id); err != nil {
				return fmt.Errorf("export: instance %s: %w", iw.id, err)
			}
		}
	}
	for _, iw := range w.instances {
		var err error
		switch iw.action {
		case actionCreate:
			_, err = tx.ExecContext(ctx, `INSERT INTO integration_instances (id, integration, name, config, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				iw.id, iw.integration, iw.name, string(iw.config), boolInt(iw.enabled), now, now)
		case actionUpdate:
			_, err = tx.ExecContext(ctx, `UPDATE integration_instances SET integration = ?, name = ?, config = ?, enabled = ?, updated_at = MAX(updated_at + 1, ?) WHERE id = ?`,
				iw.integration, iw.name, string(iw.config), boolInt(iw.enabled), now, iw.id)
		case actionTouch:
			_, err = tx.ExecContext(ctx, `UPDATE integration_instances SET updated_at = MAX(updated_at + 1, ?) WHERE id = ?`, now, iw.id)
		}
		if err != nil {
			return fmt.Errorf("export: instance %s: %w", iw.id, err)
		}
	}

	for _, name := range sortedKeys(w.secrets) {
		val := []byte(w.secrets[name].Reveal())
		err := s.vault.PutTx(ctx, tx, name, val)
		clear(val)
		if err != nil {
			// The vault's errors name the secret, never its value.
			return fmt.Errorf("export: secret %s: %w", name, err)
		}
	}

	for _, st := range w.settings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			st.key, string(st.value), now); err != nil {
			return fmt.Errorf("export: setting %s: %w", st.key, err)
		}
	}
	return writeExtras(ctx, tx, w, userID, now)
}
