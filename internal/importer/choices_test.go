package importer

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// richPlan has a Plex with its token, a Sonarr whose key is missing, and a
// link tile watched by the checks instance.
func richPlan() (*Result, Secrets) {
	res, secrets := samplePlan("FAKE-token-1")
	p := &res.Plan
	p.Board.Layout.Sections[0].Items = append(p.Board.Layout.Sections[0].Items,
		LayoutItem{ID: "service@sonarr", Type: "service", Size: "small", SupportedSizes: []string{"small"}},
		LayoutItem{ID: "service@wiki", Type: "service", Size: "small", SupportedSizes: []string{"small"}},
	)
	p.Tiles = append(p.Tiles,
		Tile{ItemID: "service@sonarr", Section: "s", Widget: "service", Size: "small", Name: "Sonarr", Instance: "i_sonarr"},
		Tile{ItemID: "service@wiki", Section: "s", Widget: "service", Size: "small", Name: "Wiki", Instance: "i_checks", Check: "Wiki"},
	)
	p.Instances = append(p.Instances,
		Instance{ID: "i_sonarr", Integration: "sonarr", Name: "Sonarr", Config: map[string]any{"url": "https://sonarr.example"}, SecretFields: []string{}, MissingSecrets: []string{"api_key"}},
		Instance{ID: "i_checks", Integration: "checks", Name: "Checks", Config: map[string]any{"targets": []map[string]any{{"name": "Wiki", "kind": "http", "url": "https://wiki.example"}}}, SecretFields: []string{}},
	)
	res.Hints = []Hint{{Code: "missing_secret", Message: "Sonarr has no api_key", Subjects: []Subject{{Instance: "i_sonarr", Field: "api_key"}}}}
	if err := p.Board.Layout.Pack(); err != nil {
		panic(err)
	}
	return res, secrets
}

func ids[T any](xs []T, id func(T) string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, id(x))
	}
	slices.Sort(out)
	return out
}

func tileIDs(p *Plan) []string { return ids(p.Tiles, func(t Tile) string { return t.ItemID }) }
func instIDs(p *Plan) []string { return ids(p.Instances, func(i Instance) string { return i.ID }) }
func layoutIDs(p *Plan) []string {
	var out []string
	for _, s := range p.Board.Layout.Sections {
		for _, it := range s.Items {
			out = append(out, it.ID)
		}
	}
	slices.Sort(out)
	return out
}

func TestSkipLeavesOutDependents(t *testing.T) {
	svc, _, _ := newService(t)
	res, secrets := richPlan()
	if err := svc.DiffWith(t.Context(), res, secrets, Choices{Skip: []string{"instance:i_one", "tile:service@wiki"}}); err != nil {
		t.Fatal(err)
	}
	p := &res.Plan
	if got := instIDs(p); !slices.Equal(got, []string{"i_sonarr"}) {
		t.Fatalf("instances = %v (the checks instance should go with its last target)", got)
	}
	if got := tileIDs(p); !slices.Equal(got, []string{"service@sonarr"}) {
		t.Fatalf("tiles = %v", got)
	}
	if got := layoutIDs(p); !slices.Equal(got, []string{"service@sonarr"}) {
		t.Fatalf("layout = %v", got)
	}
	if len(p.Secrets) != 0 || len(secrets) != 0 || p.Summary.Tiles != 1 || p.SecretsMoved != 0 {
		t.Fatalf("secrets %v %v, summary %+v", p.Secrets, secrets, p.Summary)
	}
	if !strings.HasPrefix(res.PlanHash, "sha256:") {
		t.Fatalf("hash = %q", res.PlanHash)
	}
}

func TestTypedSecretsGoToTheVault(t *testing.T) {
	svc, st, v := newService(t)
	ctx := t.Context()
	res, secrets := richPlan()
	c := Choices{Secrets: map[string]map[string]Secret{"i_sonarr": {"api_key": NewSecret("FAKE-sonarr-key")}}}
	applied, err := svc.ApplyWith(ctx, res, secrets, c, Meta{Files: []string{"services.yaml"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Get(ctx, "instance/i_sonarr/api_key"); string(got) != "FAKE-sonarr-key" {
		t.Fatalf("vault = %q", got)
	}
	if len(res.Hints) != 0 {
		t.Fatalf("hints = %+v", res.Hints)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b)+snapshot(t, st), "FAKE-sonarr-key") {
		t.Fatal("a typed secret leaked into the result or a row")
	}
	if applied.ImportID == "" {
		t.Fatal("no History row")
	}
	e, err := svc.Entry(ctx, applied.ImportID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Created.Board != "b_test" || !slices.Contains(e.Created.Instances, "i_sonarr") || !slices.Contains(e.Created.Secrets, "instance/i_sonarr/api_key") ||
		len(e.Created.Items) != 3 || !slices.Equal(e.Files, []string{"services.yaml"}) || e.Target != TargetNew {
		t.Fatalf("entry = %+v", e)
	}

	// A field that isn't missing can't be typed.
	res, secrets = richPlan()
	bad := Choices{Secrets: map[string]map[string]Secret{"i_one": {"token": NewSecret("x")}}}
	if err := svc.DiffWith(ctx, res, secrets, bad); !errors.Is(err, ErrInvalidChoices) {
		t.Fatalf("err = %v", err)
	}

	// Re-applying the same files and choices changes nothing and adds no History row.
	res, secrets = richPlan()
	again, err := svc.ApplyWith(ctx, res, secrets, c, Meta{})
	if err != nil || again.Changed() || again.ImportID != "" {
		t.Fatalf("again = %+v, %v", again, err)
	}
	if h, _ := svc.History(ctx, 10); len(h) != 1 {
		t.Fatalf("history = %+v", h)
	}
}

func TestUseExistingAndResemblance(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := t.Context()
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO integration_instances (id, integration, name, config, enabled, created_at, updated_at)
		VALUES ('i_mine', 'plex', 'My Plex', '{"url":"https://PLEX.example/"}', 1, 0, 0), ('i_named', 'sonarr', 'Sonarr', '{"url":"http://elsewhere.example"}', 1, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	res, secrets := richPlan()
	if err := svc.Diff(ctx, res, secrets); err != nil {
		t.Fatal(err)
	}
	match := map[string]string{}
	for _, c := range res.Plan.Changes {
		if c.Existing != "" {
			match[c.ID] = c.Existing + "/" + c.Match
		}
	}
	if match["i_one"] != "i_mine/url" || match["i_sonarr"] != "i_named/name" {
		t.Fatalf("matches = %v", match)
	}

	res, secrets = richPlan()
	if _, err := svc.ApplyWith(ctx, res, secrets, Choices{UseExisting: map[string]string{"i_one": "i_mine"}}, Meta{}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM integration_instances WHERE id = 'i_one'`).Scan(&n)
	if n != 0 {
		t.Fatal("the reused instance was created anyway")
	}
	var tiles string
	_ = st.Reader().QueryRowContext(ctx, `SELECT tiles FROM layouts WHERE board_id = 'b_test' ORDER BY revision DESC LIMIT 1`).Scan(&tiles)
	if !strings.Contains(tiles, `"instance":"i_mine"`) {
		t.Fatalf("tiles = %s", tiles)
	}

	res, secrets = richPlan()
	err := svc.DiffWith(ctx, res, secrets, Choices{UseExisting: map[string]string{"i_one": "i_named"}})
	if !errors.Is(err, ErrInvalidChoices) {
		t.Fatalf("reuse across integrations: %v", err)
	}
}

func TestTargetNoneAndOntoABoard(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := t.Context()

	res, secrets := richPlan()
	applied, err := svc.ApplyWith(ctx, res, secrets, Choices{Target: TargetNone}, Meta{})
	if err != nil {
		t.Fatal(err)
	}
	var boards int
	_ = st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM boards`).Scan(&boards)
	if boards != 0 || applied.BoardID != "" || len(applied.InstanceIDs) != 3 {
		t.Fatalf("boards %d, applied %+v", boards, applied)
	}

	// A board of the person's own, with an item of its own and overrides.
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO boards (id, slug, title, position, created_at, updated_at) VALUES ('b_mine', 'mine', 'Mine', 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"columns":12,"sections":[{"id":"s","span":2,"items":[{"id":"clock","type":"clock","size":"small","supportedSizes":["small"],"x":0,"y":0}],"overrides":{"md":{"span":2}}}]}`
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO layouts (board_id, revision, format_version, document, tiles, created_at) VALUES ('b_mine', 1, 1, ?, '{"clock":{"name":"My clock"}}', 0)`, doc); err != nil {
		t.Fatal(err)
	}
	res, secrets = richPlan()
	applied, err = svc.ApplyWith(ctx, res, secrets, Choices{Target: "board:b_mine"}, Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if applied.BoardID != "b_mine" {
		t.Fatalf("board = %s", applied.BoardID)
	}
	var gotDoc, gotTiles string
	_ = st.Reader().QueryRowContext(ctx, `SELECT document, tiles FROM layouts WHERE board_id = 'b_mine' ORDER BY revision DESC LIMIT 1`).Scan(&gotDoc, &gotTiles)
	if !strings.Contains(gotDoc, `"overrides":{"md":{"span":2}}`) || !strings.Contains(gotDoc, `"id":"clock"`) || !strings.Contains(gotDoc, "service@sonarr") {
		t.Fatalf("document = %s", gotDoc)
	}
	if !strings.Contains(gotTiles, `"My clock"`) || !strings.Contains(gotTiles, `"Sonarr"`) {
		t.Fatalf("tiles = %s", gotTiles)
	}
	e, _ := svc.Entry(ctx, applied.ImportID)
	if e.Created.Board != "" || len(e.Created.Items) != 3 || e.Target != "board:b_mine" {
		t.Fatalf("entry = %+v", e)
	}

	res, secrets = richPlan()
	if err := svc.DiffWith(ctx, res, secrets, Choices{Target: "board:b_nope"}); !errors.Is(err, ErrInvalidChoices) {
		t.Fatalf("unknown board: %v", err)
	}
}

func TestApplyRefusesAChangedPlan(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := t.Context()
	res, secrets := richPlan()
	if err := svc.Diff(ctx, res, secrets); err != nil {
		t.Fatal(err)
	}
	hash := res.PlanHash
	// Someone makes a Plex by hand in between: the plan now renames ours.
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO integration_instances (id, integration, name, config, enabled, created_at, updated_at) VALUES ('i_hand', 'plex', 'One', '{}', 1, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	res, secrets = richPlan()
	if _, err := svc.ApplyWith(ctx, res, secrets, Choices{}, Meta{Hash: hash}); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("err = %v", err)
	}
	var n int
	_ = st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM boards`).Scan(&n)
	if n != 0 {
		t.Fatal("a refused apply wrote the board")
	}
	res, secrets = richPlan()
	if err := svc.Diff(ctx, res, secrets); err != nil {
		t.Fatal(err)
	}
	fresh := res.PlanHash
	res, secrets = richPlan()
	if _, err := svc.ApplyWith(ctx, res, secrets, Choices{}, Meta{Hash: fresh}); err != nil {
		t.Fatalf("fresh hash refused: %v", err)
	}
}

func TestRevertRemovesOnlyWhatsUntouched(t *testing.T) {
	svc, st, v := newService(t)
	ctx := t.Context()
	res, secrets := richPlan()
	applied, err := svc.ApplyWith(ctx, res, secrets, Choices{}, Meta{})
	if err != nil {
		t.Fatal(err)
	}
	// The person edits Sonarr after the import.
	if _, err := st.Writer().ExecContext(ctx, `UPDATE integration_instances SET name = 'Sonarr 4K', updated_at = updated_at + 60000 WHERE id = 'i_sonarr'`); err != nil {
		t.Fatal(err)
	}
	plan, err := svc.Revert(ctx, applied.ImportID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Connections || len(plan.Keep) != 1 || plan.Keep[0].ID != "i_sonarr" {
		t.Fatalf("plan = %+v", plan)
	}
	var n int
	_ = st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM integration_instances`).Scan(&n)
	if n != 3 {
		t.Fatal("a dry run removed something")
	}
	if _, err := svc.Revert(ctx, applied.ImportID, false); err != nil {
		t.Fatal(err)
	}
	_ = st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM integration_instances`).Scan(&n)
	if n != 1 {
		t.Fatalf("instances left = %d", n)
	}
	_ = st.Reader().QueryRowContext(ctx, `SELECT count(*) FROM boards`).Scan(&n)
	if n != 0 {
		t.Fatal("the board the import made is still there")
	}
	if _, err := v.Get(ctx, "instance/i_one/token"); err == nil {
		t.Fatal("the removed instance's secret is still in the vault")
	}
	if _, err := svc.Revert(ctx, applied.ImportID, true); !errors.Is(err, ErrReverted) {
		t.Fatalf("second revert: %v", err)
	}
	if e, _ := svc.Entry(ctx, applied.ImportID); e.RevertedAt == 0 {
		t.Fatal("not marked reverted")
	}
}
