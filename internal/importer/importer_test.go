package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
)

func TestSecretRedactsEverywhere(t *testing.T) {
	const value = "FAKE-secret-value-for-tests"
	s := NewSecret(value)
	secrets := Secrets{"instance/i_x/token": s}
	wrapped := struct {
		Name string
		Val  Secret
		All  Secrets
	}{"x", s, secrets}

	var out []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%10s"} {
		out = append(out, fmt.Sprintf(verb, s), fmt.Sprintf(verb, secrets), fmt.Sprintf(verb, wrapped))
	}
	out = append(out, fmt.Sprint(s), fmt.Sprintln(secrets))
	j, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, string(j))
	for _, h := range []func(*bytes.Buffer) slog.Handler{
		func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		var buf bytes.Buffer
		slog.New(h(&buf)).Info("import", "secret", s, "secrets", secrets, "wrapped", wrapped)
		out = append(out, buf.String())
	}
	for _, o := range out {
		if strings.Contains(o, value) {
			t.Fatalf("secret leaked: %s", o)
		}
	}
	if s.Reveal() != value || !s.Equal(value) {
		t.Fatal("Reveal/Equal lost the value")
	}
	if got := secrets.Leaks("prefix " + value + " suffix"); !slices.Equal(got, []string{"instance/i_x/token"}) {
		t.Fatalf("Leaks = %v", got)
	}
	if got := secrets.Leaks(strings.Join(out, "\n")); len(got) != 0 {
		t.Fatalf("Leaks over redacted output = %v", got)
	}
}

func TestClosestFit(t *testing.T) {
	for _, tc := range []struct {
		candidates []string
		preferred  string
		columns    int
		want       string
	}{
		{[]string{"wide", "tall"}, "wide", 2, "tall"},           // same height doesn't fit: closest width
		{[]string{"wide", "large", "hero"}, "hero", 4, "large"}, // same height first
		{[]string{"small", "wide"}, "wide", 5, "wide"},
		{[]string{"small", "wide"}, "wide", 3, "small"},
		{[]string{"wide", "large"}, "wide", 2, ""},
		{[]string{"small"}, "nope", 4, ""},
	} {
		if got := ClosestFit(tc.candidates, tc.preferred, tc.columns); got != tc.want {
			t.Errorf("ClosestFit(%v, %s, %d) = %q, want %q", tc.candidates, tc.preferred, tc.columns, got, tc.want)
		}
	}
}

func TestPackIsDenseZOrder(t *testing.T) {
	item := func(id, size string) LayoutItem {
		return LayoutItem{ID: id, Type: "t", Size: size, SupportedSizes: []string{size}}
	}
	d := LayoutDocument{Version: LayoutVersion, Columns: LayoutColumns, Sections: []LayoutSection{
		{ID: "a", Span: 4, Items: []LayoutItem{item("w", "wide"), item("s1", "small"), item("s2", "small"), item("l", "large")}},
		// Dense back-fill: the last small fills the gap the wide one left.
		{ID: "b", Span: 6, Items: []LayoutItem{item("s1", "small"), item("w", "wide"), item("t", "tall"), item("s2", "small")}},
		{ID: "c", Span: 5, Items: []LayoutItem{item("w", "wide"), item("s", "small"), item("g", "glyph")}},
	}}
	if err := d.Pack(); err != nil {
		t.Fatal(err)
	}
	want := map[string][][2]int{
		"a": {{0, 0}, {0, 2}, {2, 2}, {0, 4}},
		"b": {{0, 0}, {2, 0}, {0, 2}, {2, 2}},
		"c": {{0, 0}, {0, 2}, {4, 0}},
	}
	for _, s := range d.Sections {
		for i, it := range s.Items {
			if got := [2]int{it.X, it.Y}; got != want[s.ID][i] {
				t.Errorf("section %s item %s at %v, want %v", s.ID, it.ID, got, want[s.ID][i])
			}
		}
	}

	bad := LayoutDocument{Sections: []LayoutSection{{ID: "x", Span: 2, Items: []LayoutItem{item("w", "wide")}}}}
	if err := bad.Pack(); err == nil {
		t.Fatal("a 4-wide item packed into 2 columns")
	}
	if err := (&LayoutDocument{Sections: []LayoutSection{{ID: "x", Span: 13}}}).Pack(); err == nil {
		t.Fatal("span 13 accepted")
	}
}

func newService(t *testing.T) (*Service, *store.Store, *vault.Vault) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(t.Context(), filepath.Join(dir, "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(t.Context(), st, vault.KeyConfig{Path: filepath.Join(dir, "secret.key"), Generate: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return New(st, v), st, v
}

func samplePlan(secretValue string) (*Result, Secrets) {
	const secretName = "instance/i_one/token"
	doc := LayoutDocument{Version: LayoutVersion, Columns: LayoutColumns, Sections: []LayoutSection{
		{ID: "s", Span: 4, Items: []LayoutItem{{ID: "service@x", Type: "service", Size: "small", SupportedSizes: []string{"small", "wide"}}}},
	}}
	if err := doc.Pack(); err != nil {
		panic(err)
	}
	res := &Result{Plan: Plan{
		Source: "test",
		Board:  Board{ID: "b_test", Name: "Test Board", Slug: "test-board", FormatVersion: LayoutVersion, Layout: doc},
		Tiles:  []Tile{{ItemID: "service@x", Section: "s", Widget: "service", Size: "small", Name: "One", Instance: "i_one"}},
		Instances: []Instance{{
			ID: "i_one", Integration: "plex", Name: "One",
			Config:       map[string]any{"url": "https://plex.example", "token": SecretRefValue(secretName)},
			SecretFields: []string{"token"},
		}},
		Secrets:      []SecretRef{{Name: secretName, Instance: "i_one", Field: "token"}},
		SecretsMoved: 1,
	}}
	return res, Secrets{secretName: NewSecret(secretValue)}
}

func actions(cs []Change) map[string]string {
	m := map[string]string{}
	for _, c := range cs {
		m[c.Kind+":"+c.ID] = c.Action
	}
	return m
}

func TestApplyIsIdempotent(t *testing.T) {
	svc, st, v := newService(t)
	ctx := t.Context()

	res, secrets := samplePlan("FAKE-token-1")
	if err := svc.Diff(ctx, res, secrets); err != nil {
		t.Fatal(err)
	}
	for k, a := range actions(res.Plan.Changes) {
		if a != ActionCreate {
			t.Fatalf("fresh diff: %s = %s", k, a)
		}
	}
	applied, err := svc.Apply(ctx, res, secrets, "")
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Changed() || applied.BoardID != "b_test" || !slices.Equal(applied.InstanceIDs, []string{"i_one"}) {
		t.Fatalf("applied = %+v", applied)
	}
	if got, _ := v.Get(ctx, "instance/i_one/token"); string(got) != "FAKE-token-1" {
		t.Fatalf("vault = %q", got)
	}
	var cfg string
	if err := st.Reader().QueryRowContext(ctx, `SELECT config FROM integration_instances WHERE id = 'i_one'`).Scan(&cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg, "FAKE-token-1") || !strings.Contains(cfg, "${secret:instance/i_one/token}") {
		t.Fatalf("config = %s", cfg)
	}

	before := snapshot(t, st)
	res, secrets = samplePlan("FAKE-token-1")
	again, err := svc.Apply(ctx, res, secrets, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed() {
		t.Fatalf("re-apply changed: %+v", again.Changes)
	}
	if after := snapshot(t, st); after != before {
		t.Fatalf("re-apply touched rows:\n%s\n---\n%s", before, after)
	}

	// A new secret value is an update, and only that.
	res, secrets = samplePlan("FAKE-token-2")
	upd, err := svc.Apply(ctx, res, secrets, "")
	if err != nil {
		t.Fatal(err)
	}
	for k, a := range actions(upd.Changes) {
		want := ActionUnchanged
		if k == "secret:instance/i_one/token" {
			want = ActionUpdate
		}
		if a != want {
			t.Errorf("%s = %s, want %s", k, a, want)
		}
	}
	if got, _ := v.Get(ctx, "instance/i_one/token"); string(got) != "FAKE-token-2" {
		t.Fatalf("vault after update = %q", got)
	}
}

// snapshot dumps every row an import writes, timestamps included.
func snapshot(t *testing.T, st *store.Store) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id || slug || title || updated_at FROM boards ORDER BY id`,
		`SELECT board_id || revision || document FROM layouts ORDER BY board_id, revision`,
		`SELECT id || name || config || updated_at FROM integration_instances ORDER BY id`,
		`SELECT name || hex(ciphertext) || updated_at FROM vault_secrets ORDER BY name`,
		`SELECT key || value || updated_at FROM settings ORDER BY key`,
	} {
		rows, err := st.Reader().QueryContext(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			b.WriteString(s + "\n")
		}
		rows.Close()
	}
	return b.String()
}

func TestApplyRenamesAroundForeignInstancesAndReportsStale(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := t.Context()
	// An instance the user made by hand, with the name the import wants.
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO integration_instances (id, integration, name, config, created_at, updated_at)
		VALUES ('i_mine', 'plex', 'One', '{}', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	res, secrets := samplePlan("FAKE-token-1")
	if _, err := svc.Apply(ctx, res, secrets, ""); err != nil {
		t.Fatal(err)
	}
	if res.Plan.Instances[0].Name != "One (imported)" {
		t.Fatalf("name = %q", res.Plan.Instances[0].Name)
	}
	// Re-importing keeps the suffixed name rather than flapping.
	res, secrets = samplePlan("FAKE-token-1")
	if err := svc.Diff(ctx, res, secrets); err != nil {
		t.Fatal(err)
	}
	if changed(res.Plan.Changes) {
		t.Fatalf("re-import diff: %+v", res.Plan.Changes)
	}

	// The source drops the instance: it's reported stale, not deleted.
	res, _ = samplePlan("")
	res.Plan.Instances, res.Plan.Secrets, res.Plan.Tiles = nil, nil, nil
	if _, err := svc.Apply(ctx, res, Secrets{}, ""); err != nil {
		t.Fatal(err)
	}
	if a := actions(res.Plan.Changes)["instance:i_one"]; a != ActionStale {
		t.Fatalf("dropped instance = %q, want stale", a)
	}
	var n int
	if err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM integration_instances WHERE id = 'i_one'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stale instance deleted: %d %v", n, err)
	}
}

func TestApplySwapsNamesWithoutClashing(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := t.Context()
	plan := func(a, b string) *Result {
		res, _ := samplePlan("")
		res.Plan.Secrets, res.Plan.Tiles = nil, nil
		res.Plan.Instances = []Instance{
			{ID: "i_a", Integration: "sonarr", Name: a, Config: map[string]any{}, SecretFields: []string{}},
			{ID: "i_b", Integration: "sonarr", Name: b, Config: map[string]any{}, SecretFields: []string{}},
		}
		return res
	}
	if _, err := svc.Apply(ctx, plan("Sonarr", "Sonarr (Anime)"), Secrets{}, ""); err != nil {
		t.Fatal(err)
	}
	// The groups were reordered in Homepage: the two rows trade names.
	res := plan("Sonarr (Anime)", "Sonarr")
	if _, err := svc.Apply(ctx, res, Secrets{}, ""); err != nil {
		t.Fatalf("swap: %v", err)
	}
	for id, want := range map[string]string{"i_a": "Sonarr (Anime)", "i_b": "Sonarr"} {
		var name string
		if err := st.Reader().QueryRowContext(ctx, `SELECT name FROM integration_instances WHERE id = ?`, id).Scan(&name); err != nil || name != want {
			t.Errorf("%s = %q (%v), want %q", id, name, err, want)
		}
	}
	if res.Plan.Instances[0].Name != "Sonarr (Anime)" || res.Plan.Instances[1].Name != "Sonarr" {
		t.Errorf("plan names = %+v", res.Plan.Instances)
	}
}

func TestApplyRollsBackEverything(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := t.Context()
	// A secret the vault refuses (too large) fails the transaction after the
	// board, layout and instance rows were written.
	res, secrets := samplePlan(strings.Repeat("x", vault.MaxValueSize+1))
	if _, err := svc.Apply(ctx, res, secrets, ""); !errors.Is(err, vault.ErrTooLarge) {
		t.Fatalf("apply with an oversized secret = %v", err)
	}
	for _, table := range []string{"boards", "layouts", "integration_instances", "vault_secrets", "settings"} {
		var n int
		if err := st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s has %d rows after a failed apply (%v)", table, n, err)
		}
	}

	// A plan naming a secret it has no value for is refused up front.
	res, _ = samplePlan("x")
	if _, err := svc.Apply(ctx, res, Secrets{}, ""); err == nil {
		t.Fatal("apply without the secret value succeeded")
	}
}

func TestApplyLayoutRevisionsAndSlug(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := t.Context()
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO boards (id, slug, title, position, created_at, updated_at) VALUES ('b_other', 'test-board', 'Other', 0, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	res, secrets := samplePlan("FAKE-token-1")
	if _, err := svc.Apply(ctx, res, secrets, ""); err != nil {
		t.Fatal(err)
	}
	var slug string
	var pos int
	if err := st.Reader().QueryRowContext(ctx, `SELECT slug, position FROM boards WHERE id = 'b_test'`).Scan(&slug, &pos); err != nil {
		t.Fatal(err)
	}
	if slug != "test-board-2" || pos != 1 {
		t.Fatalf("slug %q position %d", slug, pos)
	}

	// The user edits the layout in the web app; a re-import puts the
	// imported layout back as a new revision (history keeps the edit).
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO layouts (board_id, revision, format_version, document, created_at) VALUES ('b_test', 2, 1, '{"version":1,"columns":12,"sections":[]}', 1)`); err != nil {
		t.Fatal(err)
	}
	res, secrets = samplePlan("FAKE-token-1")
	res.Plan.Board.Name = "Renamed"
	out, err := svc.Apply(ctx, res, secrets, "")
	if err != nil {
		t.Fatal(err)
	}
	a := actions(out.Changes)
	if a["layout:b_test"] != ActionUpdate || a["board:b_test"] != ActionUpdate {
		t.Fatalf("changes = %v", a)
	}
	var rev int
	if err := st.Reader().QueryRowContext(ctx, `SELECT MAX(revision) FROM layouts WHERE board_id = 'b_test'`).Scan(&rev); err != nil || rev != 3 {
		t.Fatalf("revision = %d %v", rev, err)
	}
	if err := st.Reader().QueryRowContext(ctx, `SELECT slug FROM boards WHERE id = 'b_test'`).Scan(&slug); err != nil || slug != "test-board-2" {
		t.Fatalf("slug changed on rename: %q", slug)
	}
}
