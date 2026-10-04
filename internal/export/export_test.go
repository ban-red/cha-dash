package export

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/boards"
)

func TestExportHoldsTheHomeAndNothingElse(t *testing.T) {
	h := newHub(t)
	seed(t, h)
	out, err := h.svc.Export(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out.Data)
	mustNotLeak(t, "the export", text)
	if out.Boards != 3 || out.Instances != 4 || out.Settings != 1 || out.Secrets != 4 || out.Bundled != 0 || len(out.Missing) != 0 {
		t.Fatalf("counts = %+v", out)
	}
	for _, want := range []string{
		"# Cha Dash Home file (chadash/v1alpha1)",
		"apiVersion: chadash/v1alpha1\nkind: Home\nmeta:\n  exported_at: \"2026-10-04T09:30:00Z\"\n  hub_version: 0.1.0-test\n",
		"  - id: b_home\n    name: Home\n    slug: home\n    position: 0\n    layout:\n      format_version: 1\n      document:\n        version: 1\n        columns: 12\n",
		"{id: 'service@homepage:media/sonarr', type: service, size: small, supportedSizes: [small, wide], x: 0, y: 0}",
		"  - id: b_empty\n    name: Never saved\n    slug: empty\n    position: 2\ninstances:\n",
		"  - id: i_radarr\n    integration: keyed\n    name: Radarr\n    enabled: false\n    config:\n      url: https://radarr.home.arpa:7878\n    tls: {mode: verify}\n    secrets:\n      api_key: !secret instance/i_radarr/api_key\n",
		"    tls: {mode: tofu, pin: " + strings.Repeat("ab", 32) + "}\n",
		"      password: !secret instance/i_traefik/password\n",
		"    integration: gone\n    name: Legacy\n    config:\n      port: 8443\n      url: https://legacy.home.arpa\n    secrets:\n      token: !secret instance/i_legacy/token\n",
		"settings:\n  import/homepage:\n    source: homepage\n    board_id: b_home\n    instances: [i_sonarr]\n    links: []\n    discovery: []\n",
		"    tiles:\n      service@homepage:media/sonarr:\n        name: Sonarr\n        href: https://sonarr.home.arpa\n        bind: {instance: i_sonarr}\n",
		"        bind:\n          sources:\n            - {instance: i_radarr, capability: media.calendar}\n        options: {unmonitored: true}\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("export lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\nsecrets_bundle:") || strings.Contains(text, "client_id") || strings.Contains(text, "${secret:") {
		t.Fatalf("export holds what it shouldn't:\n%s", text)
	}
	// It parses back to the same document.
	d := parse(t, out.Data)
	if len(d.Boards) != 3 || d.Boards[2].Layout != nil || len(d.Instances) != 4 || d.Instances[1].Enabled || d.Bundle != nil {
		t.Fatalf("parsed = %+v", d)
	}
	again, err := Encode(d)
	if err != nil || string(again) != text {
		t.Fatalf("re-encoding changed the file (%v):\n%s", err, again)
	}
}

func TestRoundTripFullBackup(t *testing.T) {
	ctx := t.Context()
	a := newHub(t)
	seed(t, a)
	plain, err := a.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	full, err := a.svc.Export(ctx, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	mustNotLeak(t, "the full backup", string(full.Data))
	if full.Bundled != 4 || !strings.Contains(string(full.Data), "secrets_bundle:\n  cipher: xchacha20poly1305\n  kdf: {name: scrypt, log_n: 10, r: 8, p: 1, salt: ") {
		t.Fatalf("full backup:\n%s", full.Data)
	}
	if !strings.HasPrefix(withoutMeta(full.Data), withoutMeta(plain.Data)) {
		t.Fatal("the bundle should be the only thing a full backup adds")
	}

	// Into a fresh hub: everything is new, and the bundle restores every secret.
	b := newHub(t)
	d := parse(t, full.Data)
	plan, err := b.svc.Plan(ctx, d, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	mustNotLeak(t, "the plan", jsonOf(t, plan))
	for k, act := range actions(plan) {
		if act != actionCreate {
			t.Errorf("fresh hub: %s = %s", k, act)
		}
	}
	if len(plan.SecretsNeeded) != 0 || plan.Summary.SecretsRestored != 4 || plan.Bundle != (BundleState{Present: true, Unlocked: true, Secrets: 4}) {
		t.Fatalf("plan = %+v", plan)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w Warning) bool { return w.Code == "unknown_integration" && w.ID == "i_legacy" }) {
		t.Fatalf("warnings = %+v", plan.Warnings)
	}
	applied, err := b.svc.Apply(ctx, d, passphrase, "")
	if err != nil {
		t.Fatal(err)
	}
	mustNotLeak(t, "the applied result", jsonOf(t, applied))
	if !applied.Changed || !slices.Equal(applied.BoardIDs, []string{"b_home", "b_lab", "b_empty"}) || len(applied.InstanceIDs) != 4 {
		t.Fatalf("applied = %+v", applied)
	}
	for id, key := range map[string]string{"instance/i_sonarr/api_key": "sonarr", "instance/i_radarr/api_key": "radarr", "instance/i_traefik/password": "traefik", "instance/i_legacy/token": "legacy"} {
		if got := vaultValue(t, b.vault, id); got != secretValues[key] {
			t.Fatalf("%s restored wrong", id)
		}
	}

	// The fresh hub now exports the same file.
	back, err := b.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if withoutMeta(back.Data) != withoutMeta(plain.Data) {
		t.Fatalf("round trip differs:\n--- original\n%s\n--- after\n%s", plain.Data, back.Data)
	}
	// Users, sessions and audit stayed behind.
	for _, table := range []string{"users", "sessions", "audit", "passkeys", "correlation_decisions"} {
		if n := rowCount(t, b.st, table); n != 0 {
			t.Fatalf("%s has %d rows after an import", table, n)
		}
	}

	// Importing it again changes nothing, with or without the passphrase.
	before := snapshot(t, b.st)
	for _, pass := range []string{passphrase, ""} {
		plan, err := b.svc.Plan(ctx, parse(t, full.Data), pass)
		if err != nil {
			t.Fatal(err)
		}
		for k, act := range actions(plan) {
			if act != actionUnchanged {
				t.Errorf("re-import (passphrase %v): %s = %s", pass != "", k, act)
			}
		}
		if plan.Changed() || len(plan.SecretsNeeded) != 0 || plan.Summary.SecretsRestored != 0 {
			t.Fatalf("re-import plan = %+v", plan.Summary)
		}
		again, err := b.svc.Apply(ctx, parse(t, full.Data), pass, "")
		if err != nil || again.Changed {
			t.Fatalf("re-import applied = %+v, %v", again, err)
		}
	}
	if after := snapshot(t, b.st); after != before {
		t.Fatalf("a re-import wrote rows:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestReimportIntoTheSameHubChangesNothing(t *testing.T) {
	ctx := t.Context()
	h := newHub(t)
	seed(t, h)
	before := snapshot(t, h.st)
	for _, pass := range []string{"", passphrase} {
		out, err := h.svc.Export(ctx, pass)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := h.svc.Plan(ctx, parse(t, out.Data), pass)
		if err != nil {
			t.Fatal(err)
		}
		for k, act := range actions(plan) {
			if act != actionUnchanged {
				t.Errorf("same hub: %s = %s", k, act)
			}
		}
		if len(plan.SecretsNeeded) != 0 {
			t.Fatalf("the vault has every secret: %+v", plan.SecretsNeeded)
		}
		applied, err := h.svc.Apply(ctx, parse(t, out.Data), pass, "")
		if err != nil || applied.Changed {
			t.Fatalf("applied = %+v %v", applied, err)
		}
	}
	if after := snapshot(t, h.st); after != before {
		t.Fatalf("re-import wrote rows:\n%s\n---\n%s", before, after)
	}
}

func TestImportWithoutSecretsListsThemAsNeeded(t *testing.T) {
	ctx := t.Context()
	a := newHub(t)
	seed(t, a)
	plain, err := a.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	b := newHub(t)
	plan, err := b.svc.Plan(ctx, parse(t, plain.Data), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []NeededSecret{
		{Instance: "i_sonarr", Name: "Sonarr", Integration: "keyed", Field: "api_key", Secret: "instance/i_sonarr/api_key"},
		{Instance: "i_radarr", Name: "Radarr", Integration: "keyed", Field: "api_key", Secret: "instance/i_radarr/api_key"},
		{Instance: "i_traefik", Name: "Traefik", Integration: "pair", Field: "password", Secret: "instance/i_traefik/password"},
		{Instance: "i_legacy", Name: "Legacy", Integration: "gone", Field: "token", Secret: "instance/i_legacy/token"},
	}
	slices.SortFunc(plan.SecretsNeeded, func(x, y NeededSecret) int { return strings.Compare(x.Instance, y.Instance) })
	slices.SortFunc(want, func(x, y NeededSecret) int { return strings.Compare(x.Instance, y.Instance) })
	if !slices.Equal(plan.SecretsNeeded, want) || plan.Summary.SecretsNeeded != 4 || plan.Bundle.Present {
		t.Fatalf("needed = %+v", plan.SecretsNeeded)
	}
	sonarr := change(t, plan, "instance", "i_sonarr")
	if sonarr.Action != actionCreate || len(sonarr.Secrets) != 1 || sonarr.Secrets[0] != (SecretChange{Field: "api_key", Secret: "instance/i_sonarr/api_key", Action: SecretNeeded}) {
		t.Fatalf("sonarr = %+v", sonarr)
	}
	if _, err := b.svc.Apply(ctx, parse(t, plain.Data), "", ""); err != nil {
		t.Fatal(err)
	}
	var cfg string
	if err := b.st.Reader().QueryRowContext(ctx, `SELECT config FROM integration_instances WHERE id = 'i_sonarr'`).Scan(&cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg, "api_key") || !strings.Contains(cfg, `"url":"https://sonarr.home.arpa"`) {
		t.Fatalf("an instance with a needed secret is created with it unset: %s", cfg)
	}

	// Re-importing changes nothing; the same secrets are still needed.
	before := snapshot(t, b.st)
	again, err := b.svc.Apply(ctx, parse(t, plain.Data), "", "")
	if err != nil || again.Changed || len(again.Plan.SecretsNeeded) != 4 {
		t.Fatalf("re-import = %+v %v", again, err)
	}
	if after := snapshot(t, b.st); after != before {
		t.Fatal("a re-import wrote rows")
	}
	// The owner enters one (the API stores it under the instance's name).
	if err := b.vault.Put(ctx, "instance/i_sonarr/api_key", []byte(secretValues["sonarr"])); err != nil {
		t.Fatal(err)
	}
	b.exec(t, `UPDATE integration_instances SET config = json_set(config, '$.api_key', '${secret:instance/i_sonarr/api_key}') WHERE id = 'i_sonarr'`)
	before = snapshot(t, b.st)
	again, err = b.svc.Apply(ctx, parse(t, plain.Data), "", "")
	if err != nil || again.Changed || len(again.Plan.SecretsNeeded) != 3 {
		t.Fatalf("after entering one = %+v %v", again.Plan.Summary, err)
	}
	if c := change(t, again.Plan, "instance", "i_sonarr"); c.Action != actionUnchanged || c.Secrets[0].Action != actionUnchanged || c.Secrets[0].Source != sourceVault {
		t.Fatalf("sonarr after entering its key = %+v", c)
	}
	if after := snapshot(t, b.st); after != before {
		t.Fatal("a re-import wrote rows")
	}
}

func TestLockedBundleAndWrongPassphrase(t *testing.T) {
	ctx := t.Context()
	a := newHub(t)
	seed(t, a)
	full, err := a.svc.Export(ctx, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	b := newHub(t)
	plan, err := b.svc.Plan(ctx, parse(t, full.Data), "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Bundle != (BundleState{Present: true, Unlocked: false, Secrets: 4}) || len(plan.SecretsNeeded) != 4 || !plan.SecretsNeeded[0].InBundle {
		t.Fatalf("locked plan = %+v %+v", plan.Bundle, plan.SecretsNeeded)
	}
	for _, wrong := range []string{"not the passphrase at all", strings.ToUpper(passphrase)} {
		_, err := b.svc.Plan(ctx, parse(t, full.Data), wrong)
		if !errors.Is(err, ErrWrongPassphrase) {
			t.Fatalf("wrong passphrase: %v", err)
		}
		mustNotLeak(t, "the error", err.Error())
		if _, err := b.svc.Apply(ctx, parse(t, full.Data), wrong, ""); !errors.Is(err, ErrWrongPassphrase) {
			t.Fatalf("apply with a wrong passphrase: %v", err)
		}
	}
	if n := rowCount(t, b.st, "integration_instances"); n != 0 {
		t.Fatal("a failed import wrote rows")
	}
	// A passphrase for a file with no bundle is unused, and said so.
	plain, _ := a.svc.Export(ctx, "")
	plan, err = b.svc.Plan(ctx, parse(t, plain.Data), passphrase)
	if err != nil || !slices.ContainsFunc(plan.Warnings, func(w Warning) bool { return w.Code == "no_bundle" }) {
		t.Fatalf("no bundle: %+v %v", plan, err)
	}
}

func TestDiffShapes(t *testing.T) {
	ctx := t.Context()
	h := newHub(t)
	seed(t, h)
	out, err := h.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	d := parse(t, out.Data)
	d.Boards[0].Name = "Front page"
	d.Boards[0].Layout.Document = []byte(`{"version":1,"columns":12,"sections":[` +
		`{"id":"net","span":4,"items":[{"id":"router@opnsense:i_x/router","type":"router","size":"large","supportedSizes":["large"],"x":0,"y":0}]},` +
		`{"id":"media","title":"Media","span":8,"items":[` +
		`{"id":"service@homepage:media/sonarr","type":"service","size":"wide","supportedSizes":["small","wide"],"x":0,"y":0},` +
		`{"id":"clock","type":"clock","size":"small","supportedSizes":["small"],"x":4,"y":0}]}]}`)
	inst := func(id string) *Instance {
		i := slices.IndexFunc(d.Instances, func(in Instance) bool { return in.ID == id })
		if i < 0 {
			t.Fatalf("no %s in the export", id)
		}
		return &d.Instances[i]
	}
	inst("i_sonarr").Config = []byte(`{"label":"TV","url":"https://tv.home.arpa"}`)
	inst("i_sonarr").TLS = []byte(`{"mode":"verify"}`)
	inst("i_radarr").Enabled = true
	d.Instances = slices.DeleteFunc(d.Instances, func(in Instance) bool { return in.ID == "i_traefik" })
	d.Instances = append(d.Instances, Instance{ID: "i_new", Integration: "pair", Name: "Proxy", Enabled: true,
		Config: []byte(`{"url":"https://proxy.home.arpa","username":"viewer"}`), Secrets: map[string]string{"password": "instance/i_new/password"}})
	d.Settings[0].Value = []byte(strings.Replace(record, `["i_sonarr"]`, `["i_sonarr","i_radarr"]`, 1))

	plan, err := h.svc.Plan(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	mustNotLeak(t, "the plan", jsonOf(t, plan))

	if c := change(t, plan, "board", "b_home"); c.Action != actionUpdate ||
		jsonOf(t, c.Fields) != `[{"path":"/name","op":"replace","from":"Home","to":"Front page"}]` {
		t.Fatalf("board = %+v", c)
	}
	if c := change(t, plan, "layout", "b_home"); c.Action != actionUpdate || jsonOf(t, c.Fields) != `[`+
		`{"path":"/document/sections/media/items/service@homepage:media~1radarr","op":"remove","from":{"id":"service@homepage:media/radarr","size":"wide","supportedSizes":["small","wide"],"type":"service","x":2,"y":0}},`+
		`{"path":"/document/sections/media/items/service@homepage:media~1sonarr/size","op":"replace","from":"small","to":"wide"},`+
		`{"path":"/document/sections/media/items/clock","op":"add","to":{"id":"clock","size":"small","supportedSizes":["small"],"type":"clock","x":4,"y":0}},`+
		`{"path":"/document/sections","op":"reorder","from":["media","net"],"to":["net","media"]}]` {
		t.Fatalf("layout fields = %s", jsonOf(t, c.Fields))
	}
	if c := change(t, plan, "layout", "b_lab"); c.Action != actionUnchanged || c.Fields != nil {
		t.Fatalf("lab layout = %+v", c)
	}
	if c := change(t, plan, "instance", "i_sonarr"); c.Action != actionUpdate || c.Integration != "keyed" || jsonOf(t, c.Fields) != `[`+
		`{"path":"/config/url","op":"replace","from":"https://sonarr.home.arpa","to":"https://tv.home.arpa"},`+
		`{"path":"/tls/mode","op":"replace","from":"tofu","to":"verify"},`+
		`{"path":"/tls/pin","op":"remove","from":"`+strings.Repeat("ab", 32)+`"}]` ||
		jsonOf(t, c.Secrets) != `[{"field":"api_key","secret":"instance/i_sonarr/api_key","action":"unchanged","source":"vault"}]` {
		t.Fatalf("sonarr = %s / %s", jsonOf(t, c.Fields), jsonOf(t, c.Secrets))
	}
	if c := change(t, plan, "instance", "i_radarr"); c.Action != actionUpdate ||
		jsonOf(t, c.Fields) != `[{"path":"/enabled","op":"replace","from":false,"to":true}]` {
		t.Fatalf("radarr = %s", jsonOf(t, c.Fields))
	}
	if c := change(t, plan, "instance", "i_new"); c.Action != actionCreate || c.Name != "Proxy" || jsonOf(t, c.Fields) != `[`+
		`{"path":"/config/url","op":"add","to":"https://proxy.home.arpa"},{"path":"/config/username","op":"add","to":"viewer"}]` ||
		jsonOf(t, c.Secrets) != `[{"field":"password","secret":"instance/i_new/password","action":"needed"}]` {
		t.Fatalf("new = %s / %s", jsonOf(t, c.Fields), jsonOf(t, c.Secrets))
	}
	if c := change(t, plan, "instance", "i_traefik"); c.Action != actionStale || c.Name != "Traefik" || c.Integration != "pair" {
		t.Fatalf("traefik = %+v", c)
	}
	if c := change(t, plan, "setting", "import/homepage"); c.Action != actionUpdate ||
		jsonOf(t, c.Fields) != `[{"path":"/instances","op":"replace","from":["i_sonarr"],"to":["i_sonarr","i_radarr"]}]` {
		t.Fatalf("record = %s", jsonOf(t, c.Fields))
	}
	if plan.Summary != (Summary{Create: 1, Update: 5, Unchanged: 5, Stale: 1, SecretsNeeded: 1}) {
		t.Fatalf("summary = %+v", plan.Summary)
	}
	// Stale objects come last.
	if last := plan.Changes[len(plan.Changes)-1]; last.Action != actionStale {
		t.Fatalf("last change = %+v", last)
	}

	// Applying keeps history: the board's layout gains a revision, the stale
	// instance stays.
	applied, err := h.svc.Apply(ctx, d, "", "")
	if err != nil || !applied.Changed {
		t.Fatalf("applied = %+v %v", applied, err)
	}
	if n := rowCount(t, h.st, "layouts"); n != 3 {
		t.Fatalf("layouts = %d, want the original two plus one", n)
	}
	if n := rowCount(t, h.st, "integration_instances"); n != 5 {
		t.Fatalf("instances = %d: stale ones are never deleted", n)
	}
	// And it's now idempotent from the new state.
	again, err := h.svc.Apply(ctx, d, "", "")
	if err != nil || again.Changed {
		t.Fatalf("again = %+v %v", again.Plan.Summary, err)
	}
}

func TestBoardMatchesByItsSlug(t *testing.T) {
	ctx := t.Context()
	a := newHub(t)
	seed(t, a)
	out, err := a.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	// A fresh hub that someone opened already has its default Home board.
	b := newHub(t)
	list, err := boards.New(b.st).List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	home := list[0].ID
	plan, err := b.svc.Plan(ctx, parse(t, out.Data), "")
	if err != nil {
		t.Fatal(err)
	}
	c := change(t, plan, "board", home)
	if c.FileID != "b_home" || c.Action != actionUnchanged {
		t.Fatalf("board = %+v", c)
	}
	if l := change(t, plan, "layout", home); l.Action != actionCreate || l.Detail != "2 sections, 3 items" {
		t.Fatalf("layout = %+v", l)
	}
	if _, err := b.svc.Apply(ctx, parse(t, out.Data), "", ""); err != nil {
		t.Fatal(err)
	}
	if list, _ := boards.New(b.st).List(ctx); len(list) != 3 || list[0].ID != home {
		t.Fatalf("boards = %+v", list)
	}
	// The import record follows the board it describes.
	var rec string
	if err := b.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'import/homepage'`).Scan(&rec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec, `"board_id":"`+home+`"`) {
		t.Fatalf("record = %s", rec)
	}
	again, err := b.svc.Apply(ctx, parse(t, out.Data), "", "")
	if err != nil || again.Changed {
		t.Fatalf("again = %+v %v", again.Plan.Summary, err)
	}
}

func TestNameClashesAndSlugClashes(t *testing.T) {
	ctx := t.Context()
	a := newHub(t)
	a.addBoard(t, "b_media", "media", "Media", 0, "")
	a.addInstance(t, "i_one", "keyed", "Sonarr", true, map[string]any{"url": "https://one.home.arpa"}, map[string]string{"api_key": secretValues["sonarr"]})
	out, err := a.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	b := newHub(t)
	b.addBoard(t, "b_other", "media", "Other media", 0, "")
	b.addInstance(t, "i_mine", "keyed", "Sonarr", true, map[string]any{"url": "https://mine.home.arpa"}, map[string]string{"api_key": secretValues["radarr"]})
	b.exec(t, `UPDATE boards SET slug = 'media' WHERE id = 'b_other'`)
	// b_other isn't in the file, so it matches by slug...
	plan, err := b.svc.Plan(ctx, parse(t, out.Data), "")
	if err != nil {
		t.Fatal(err)
	}
	if c := change(t, plan, "board", "b_other"); c.FileID != "b_media" || c.Action != actionUpdate {
		t.Fatalf("board = %+v", c)
	}
	// ...and the instance the owner made keeps its name.
	if c := change(t, plan, "instance", "i_one"); c.Name != "Sonarr (imported)" || c.Action != actionCreate {
		t.Fatalf("instance = %+v", c)
	}
	if c := change(t, plan, "instance", "i_mine"); c.Action != actionStale {
		t.Fatalf("mine = %+v", c)
	}
	if _, err := b.svc.Apply(ctx, parse(t, out.Data), "", ""); err != nil {
		t.Fatal(err)
	}
	if again, err := b.svc.Apply(ctx, parse(t, out.Data), "", ""); err != nil || again.Changed {
		t.Fatalf("again = %+v %v", again, err)
	}
}

func TestLeakGuard(t *testing.T) {
	h := newHub(t)
	// The key was also typed into a plain field: exporting would leak it there.
	h.addInstance(t, "i_x", "keyed", "Sonarr", true,
		map[string]any{"url": "https://sonarr.home.arpa", "label": "key " + secretValues["sonarr"]},
		map[string]string{"api_key": secretValues["sonarr"]})
	for _, pass := range []string{"", passphrase} {
		_, err := h.svc.Export(t.Context(), pass)
		var leak *LeakError
		if !errors.As(err, &leak) || leak.Secret != "instance/i_x/api_key" {
			t.Fatalf("export with a leaked value: %v", err)
		}
		mustNotLeak(t, "the error", err.Error())
	}

	// A token in a carried setting's link would leak too.
	h = newHub(t)
	h.addInstance(t, "i_y", "keyed", "Radarr", true, map[string]any{"url": "https://radarr.home.arpa"}, map[string]string{"api_key": secretValues["radarr"]})
	h.exec(t, `INSERT INTO settings (key, value, updated_at) VALUES ('import/homepage', ?, 1)`,
		`{"tiles":[{"item_id":"x","href":"https://radarr.home.arpa/?apikey=`+secretValues["radarr"]+`"}]}`)
	var leak *LeakError
	if _, err := h.svc.Export(t.Context(), ""); !errors.As(err, &leak) || leak.Secret != "instance/i_y/api_key" {
		t.Fatalf("a token in a setting: %v", err)
	}

	// Keys and short values never trip it: a password that happens to be a
	// word the file uses as a key, or a short one that appears in a value.
	h = newHub(t)
	h.addBoard(t, "b_home", "home", "Home", 0, homeLayout)
	h.addInstance(t, "i_a", "pair", "Traefik", true, map[string]any{"url": "http://traefik.home.arpa", "username": "password"}, map[string]string{"password": "supportedSizes"})
	h.addInstance(t, "i_b", "pair", "Proxy", true, map[string]any{"url": "http://proxy.home.arpa", "username": "service"}, map[string]string{"password": "service"})
	if _, err := h.svc.Export(t.Context(), passphrase); err != nil {
		t.Fatalf("false positive: %v", err)
	}
}

func TestValuesOutsideTheVaultAreLeftOut(t *testing.T) {
	h := newHub(t)
	// Rows written behind the API's back: a plain value in a secret field,
	// and a reference nested where none belongs.
	h.addInstance(t, "i_x", "pair", "Traefik", true,
		map[string]any{"url": "http://traefik.home.arpa", "password": secretValues["traefik"], "extra": map[string]any{"k": "${secret:elsewhere}"}}, nil)
	out, err := h.svc.Export(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	mustNotLeak(t, "the export", string(out.Data))
	if strings.Contains(string(out.Data), "password") || strings.Contains(string(out.Data), "extra") || len(out.Warnings) != 2 ||
		out.Warnings[0].Code != "value_left_out" {
		t.Fatalf("export = %+v\n%s", out.Warnings, out.Data)
	}
}

func TestPlanRefusesSecretValues(t *testing.T) {
	h := newHub(t)
	file := func(instance string) []byte {
		return []byte("apiVersion: chadash/v1alpha1\nkind: Home\ninstances:\n" + instance)
	}
	for name, tc := range map[string]struct{ file, want string }{
		"value in a secret field's config": {
			"- {id: i_x, integration: keyed, name: X, config: {url: 'https://x.home.arpa', api_key: " + secretValues["sonarr"] + "}}\n",
			"instances[0].config.api_key: is a secret field of Keyed",
		},
		"placeholder on a plain field": {
			"- {id: i_x, integration: keyed, name: X, config: {url: 'https://x.home.arpa'}, secrets: {label: !secret a/b}}\n",
			"instances[0].secrets.label: isn't a secret field of Keyed",
		},
	} {
		d, err := Parse(file(tc.file))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		_, err = h.svc.Plan(t.Context(), d, "")
		var pe *ParseError
		if !errors.As(err, &pe) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: %v", name, err)
		}
		mustNotLeak(t, name, err.Error())
	}
}

func TestPlanWarnsAboutInvalidConfig(t *testing.T) {
	h := newHub(t)
	d := parse(t, []byte("apiVersion: chadash/v1alpha1\nkind: Home\ninstances:\n"+
		"- {id: i_x, integration: keyed, name: X, config: {url: 'not a url'}, secrets: {api_key: !secret instance/i_x/api_key}}\n"))
	plan, err := h.svc.Plan(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Code != "invalid_config" || !strings.Contains(plan.Warnings[0].Message, "X (instances[0]): /url") {
		t.Fatalf("warnings = %+v", plan.Warnings)
	}
	// The missing key isn't a config problem: it's a needed secret.
	if len(plan.SecretsNeeded) != 1 {
		t.Fatalf("needed = %+v", plan.SecretsNeeded)
	}
}

func TestTiles(t *testing.T) {
	ctx := t.Context()
	h := newHub(t)
	seed(t, h)
	out, err := h.svc.Export(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	d := parse(t, out.Data)
	if !sameRaw(d.Boards[0].Tiles, json.RawMessage(homeTiles)) || d.Boards[1].Tiles != nil || d.Boards[2].Tiles != nil {
		t.Fatalf("exported tiles: %s / %s", d.Boards[0].Tiles, d.Boards[1].Tiles)
	}
	revisions := func() int { return rowCount(t, h.st, "layouts") }
	before := revisions()

	// A tile edit is a tiles change; the layout is untouched but the board
	// gains one revision that carries both.
	d.Boards[0].Tiles = json.RawMessage(strings.Replace(homeTiles, `"name":"Radarr"`, `"name":"Films"`, 1))
	plan, err := h.svc.Plan(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	if c := change(t, plan, "layout", "b_home"); c.Action != actionUnchanged {
		t.Fatalf("layout = %+v", c)
	}
	if c := change(t, plan, "tiles", "b_home"); c.Action != actionUpdate ||
		jsonOf(t, c.Fields) != `[{"path":"/service@homepage:media~1radarr/name","op":"replace","from":"Radarr","to":"Films"}]` {
		t.Fatalf("tiles = %s", jsonOf(t, c.Fields))
	}
	if _, err := h.svc.Apply(ctx, d, "", ""); err != nil {
		t.Fatal(err)
	}
	l, err := boards.New(h.st).Layout(ctx, "b_home")
	if err != nil || !sameRaw(l.Document, json.RawMessage(homeLayout)) || !strings.Contains(string(l.Tiles), `"name":"Films"`) || revisions() != before+1 {
		t.Fatalf("after: %s %s %v (%d revisions)", l.Document, l.Tiles, err, revisions())
	}

	// A file without tiles keeps the board's; tiles: {} clears them.
	d.Boards[0].Tiles = nil
	if plan, _ := h.svc.Plan(ctx, d, ""); slices.ContainsFunc(plan.Changes, func(c Change) bool { return c.Kind == "tiles" }) || plan.Changed() {
		t.Fatalf("no tiles key: %+v", plan.Changes)
	}
	d.Boards[0].Tiles = json.RawMessage(`{}`)
	if _, err := h.svc.Apply(ctx, d, "", ""); err != nil {
		t.Fatal(err)
	}
	if l, _ := boards.New(h.st).Layout(ctx, "b_home"); string(l.Tiles) != "{}" {
		t.Fatalf("cleared tiles = %s", l.Tiles)
	}

	// Into a fresh hub, tiles arrive with their board's first revision.
	b := newHub(t)
	created, err := b.svc.Apply(ctx, parse(t, out.Data), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c := change(t, created.Plan, "tiles", "b_home"); c.Action != actionCreate || c.Detail != "2 tiles" {
		t.Fatalf("created tiles = %+v", c)
	}
	if l, _ := boards.New(b.st).Layout(ctx, "b_home"); l.Revision != 1 || !sameRaw(l.Tiles, json.RawMessage(homeTiles)) {
		t.Fatalf("fresh hub: revision %d, tiles %s", l.Revision, l.Tiles)
	}

	// Tiles that break the hub's rules refuse the file; tiles with no layout
	// to keep them with are reported and skipped.
	bad := parse(t, out.Data)
	bad.Boards[0].Tiles = json.RawMessage(`{"x":{"href":"ftp://files.home.arpa"}}`)
	if _, err := h.svc.Plan(ctx, bad, ""); err == nil || !strings.Contains(err.Error(), "boards[0].tiles: invalid tiles: x: href must be an http(s) URL") {
		t.Fatalf("bad tiles: %v", err)
	}
	orphan := parse(t, []byte("apiVersion: chadash/v1alpha1\nkind: Home\nboards:\n- {id: b_x, name: X, slug: x, tiles: {a: {name: A}}}\n"))
	plan, err = h.svc.Plan(ctx, orphan, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) != 1 || plan.Warnings[0].Code != "tiles_ignored" || change(t, plan, "tiles", "b_x").Action != actionUnchanged {
		t.Fatalf("orphan tiles: %+v %+v", plan.Warnings, plan.Changes)
	}
	if _, err := h.svc.Apply(ctx, orphan, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestExportWithoutTheVault(t *testing.T) {
	h := newHub(t)
	seed(t, h)
	svc := New(h.st, nil, Options{Registry: registry(t)})
	out, err := svc.Export(t.Context(), "")
	if err != nil || out.Secrets != 4 {
		t.Fatalf("placeholders-only export without the key: %+v %v", out, err)
	}
	mustNotLeak(t, "the export", string(out.Data))
	if _, err := svc.Export(t.Context(), passphrase); !errors.Is(err, ErrNoVault) {
		t.Fatalf("a bundle needs the vault: %v", err)
	}
	if _, err := svc.Plan(t.Context(), parse(t, out.Data), ""); !errors.Is(err, ErrNoVault) {
		t.Fatalf("an import needs the vault: %v", err)
	}
	if _, err := h.svc.Export(t.Context(), "short"); !errors.Is(err, ErrWeakPassphrase) {
		t.Fatalf("weak passphrase: %v", err)
	}
}
