package homepage

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/importer"
)

var update = flag.Bool("update", false, "rewrite testdata/importers/homepage/dogfood/expected-plan.json")

// The acceptance test: testdata/importers/homepage/dogfood/README.md, criterion
// by criterion. Live-run expectations (the Docker proxy probe, PVE grants,
// the Plex and Karakeep repairs, label discovery) belong to the integrations
// and are recorded here only as the hints and discovery entries the import
// leaves for them.
func TestDogfoodPlan(t *testing.T) {
	res, secrets := build(t, dogfood(t))
	p := res.Plan

	t.Run("board", func(t *testing.T) {
		if p.Board.Name != "Alpaca Homepage" || p.Board.Slug != "alpaca-homepage" || p.Board.FormatVersion != importer.LayoutVersion {
			t.Fatalf("board = %q %q v%d", p.Board.Name, p.Board.Slug, p.Board.FormatVersion)
		}
		if p.Board.Layout.Version != 1 || p.Board.Layout.Columns != 12 {
			t.Fatalf("layout header = %+v", p.Board.Layout)
		}
		ql, _ := p.Board.Settings["quicklaunch"].(map[string]any)
		if ql["search_descriptions"] != true {
			t.Errorf("quicklaunch = %v, want searchDescriptions honored", p.Board.Settings["quicklaunch"])
		}
		locale, _ := p.Board.Settings["locale"].(map[string]any)
		if locale["first_day_of_week"] != "sunday" {
			t.Errorf("locale = %v, want firstDayInWeek: sunday as a locale setting", p.Board.Settings["locale"])
		}
	})

	t.Run("sections in order with column hints", func(t *testing.T) {
		// Homepage renders settings.layout's groups first, in that order, so
		// Apps (label-only) sits between Data and Archive/Backup.
		arr := func(integ string) string { // queue tiles are bound to their instance
			for _, in := range p.Instances {
				if in.Integration == integ {
					return "arr@" + integ + ":" + in.ID + "/app"
				}
			}
			return "no " + integ + " instance"
		}
		want := []struct {
			id, title string
			span      int
			items     []string
		}{
			{"home", "Home", 4, []string{"homeassistant", "cameras"}},
			{"servers", "Servers", 4, []string{"opnsense",
				"service@homepage:servers/proxmoxpluto", "service@homepage:servers/proxmoxminig5",
				"service@homepage:servers/proxmoxmarscluster", "service@homepage:servers/proxmoxio",
				"service@homepage:servers/jupiter", "service@homepage:servers/jupiternas",
				"service@homepage:servers/jupiternascockpit"}},
			{"services", "Services", 5, []string{"traefik", "uptimekuma"}},
			{"media", "Media", 5, []string{"plex", arr("radarr"), arr("sonarr"), "service@homepage:media/bazarr"}},
			{"data", "", 2, []string{"calendar", "service@homepage:data/tautulli"}},
			{"apps", "Apps", 4, []string{}},
			{"archive-backup", "Archive/Backup", 4, []string{"service@homepage:archive-backup/linkding", "service@homepage:archive-backup/hoarder-karakeep"}},
			{"downloads", "Downloads", 4, []string{"downloads", "service@homepage:downloads/prowlarr"}},
		}
		got := p.Board.Layout.Sections
		if len(got) != len(want) {
			t.Fatalf("%d sections, want %d", len(got), len(want))
		}
		for i, w := range want {
			s := got[i]
			title := ""
			if s.Title != nil {
				title = *s.Title
			}
			if s.ID != w.id || title != w.title || s.Span != w.span {
				t.Errorf("section %d = %s %q span %d, want %s %q span %d", i, s.ID, title, s.Span, w.id, w.title, w.span)
			}
			if w.id == "data" && s.Title != nil {
				t.Error("Data has header: false and must be untitled")
			}
			ids := []string{}
			for _, it := range s.Items {
				ids = append(ids, it.ID)
				if !slices.Contains(it.SupportedSizes, it.Size) || importer.SizeWidth(it.Size) > s.Span {
					t.Errorf("%s: size %s doesn't fit or isn't supported", it.ID, it.Size)
				}
			}
			if !slices.Equal(ids, w.items) {
				t.Errorf("section %s items = %v, want %v", s.ID, ids, w.items)
			}
		}
	})

	t.Run("integration instances", func(t *testing.T) {
		want := [][2]string{
			{"homeassistant", "HomeAssistant"},
			{"frigate", "Frigate"},
			{"opnsense", "OPNSense"},
			{"proxmox", "Pluto PVE (main opnsense)"},
			{"proxmox", "MiniG5 PVE (homeassistant/frigate)"},
			{"proxmox", "Mars PVE Cluster (pvedocker, 2 nodes)"},
			{"proxmox", "IO PVE (windows gaming vm, iolinux ubuntu vm)"},
			{"proxmox", "JupiterNas PVE"},
			{"traefik", "Traefik"},
			{"uptimekuma", "UptimeKuma"},
			{"plex", "Plex"},
			{"radarr", "Radarr"},
			{"sonarr", "Sonarr"},
			{"bazarr", "Bazarr"},
			{"tautulli", "Tautulli"},
			{"karakeep", "Hoarder/Karakeep"},
			{"qbittorrent", "qBittorrent"},
			{"prowlarr", "Prowlarr"},
			{"docker", "my-docker"},
			{"docker", "my-unraid"},
			{"checks", "Homepage"},
		}
		var got [][2]string
		for _, in := range p.Instances {
			got = append(got, [2]string{in.Integration, in.Name})
		}
		if !slices.Equal(got, want) {
			t.Fatalf("instances =\n%v\nwant\n%v", got, want)
		}
		byName := instancesByName(p)
		if c := byName["UptimeKuma"].Config; c["slug"] != "current" || c["url"] != "https://uptime.lab.example" {
			t.Errorf("uptimekuma config = %v", c)
		}
		if c := byName["Pluto PVE (main opnsense)"].Config; c["token_id"] != "api@pam!homepage" || c["url"] != "https://pluto.lan:8006" {
			t.Errorf("proxmox config = %v", c)
		}
		if tls, _ := byName["Pluto PVE (main opnsense)"].Config["tls"].(map[string]any); tls["mode"] != "tofu" {
			t.Errorf("a .lan PVE host gets trust-on-first-use TLS, got %v", tls)
		}
		if tls, _ := byName["Plex"].Config["tls"].(map[string]any); tls["mode"] != "verify" {
			t.Errorf("a public name verifies TLS, got %v", tls)
		}
		if c := byName["my-docker"].Config; c["endpoint"] != "tcp://dockerproxy:2375" {
			t.Errorf("my-docker = %v", c)
		}
		if c := byName["my-unraid"].Config; c["endpoint"] != "tcp://10.0.11.120:2375" {
			t.Errorf("my-unraid = %v", c)
		}
		if f := byName["Frigate"]; len(f.SecretFields) != 0 || tileFor(p, f.ID).Options["recent_events"] != true {
			t.Errorf("frigate: no auth, enableRecentEvents → camera events on; got %+v", f)
		}
		for _, name := range []string{"Plex", "Radarr", "Sonarr"} {
			if fields := tileFor(p, byName[name].ID).Options["fields"]; fields == nil {
				t.Errorf("%s: fields → tile stats missing", name)
			}
		}
	})

	t.Run("18 secrets moved, refs only", func(t *testing.T) {
		want := [][2]string{
			{"HomeAssistant", "token"},
			{"OPNSense", "key"}, {"OPNSense", "secret"},
			{"Pluto PVE (main opnsense)", "token_secret"},
			{"MiniG5 PVE (homeassistant/frigate)", "token_secret"},
			{"Mars PVE Cluster (pvedocker, 2 nodes)", "token_secret"},
			{"IO PVE (windows gaming vm, iolinux ubuntu vm)", "token_secret"},
			{"JupiterNas PVE", "token_secret"},
			{"Traefik", "password"},
			{"UptimeKuma", "key"},
			{"Plex", "token"},
			{"Radarr", "api_key"}, {"Sonarr", "api_key"}, {"Bazarr", "api_key"},
			{"Tautulli", "api_key"},
			{"Hoarder/Karakeep", "api_key"},
			{"qBittorrent", "password"},
			{"Prowlarr", "api_key"},
		}
		names := map[string]string{}
		for _, in := range p.Instances {
			names[in.ID] = in.Name
		}
		var got [][2]string
		for _, s := range p.Secrets {
			got = append(got, [2]string{names[s.Instance], s.Field})
			if _, ok := secrets[s.Name]; !ok {
				t.Errorf("no value for %s", s.Name)
			}
			if s.Name != "instance/"+s.Instance+"/"+s.Field {
				t.Errorf("secret name %q", s.Name)
			}
		}
		if p.SecretsMoved != 18 || len(secrets) != 18 || !slices.Equal(got, want) {
			t.Fatalf("secrets moved %d (values %d):\n%v\nwant\n%v", p.SecretsMoved, len(secrets), got, want)
		}
		for _, in := range p.Instances {
			for _, f := range in.SecretFields {
				ref, _ := in.Config[f].(string)
				if ref != importer.SecretRefValue("instance/"+in.ID+"/"+f) {
					t.Errorf("%s.%s = %q, want a ${secret:…} ref", in.Name, f, ref)
				}
			}
		}
	})

	t.Run("no secret in the plan", func(t *testing.T) {
		j, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		assertNoLeak(t, "the plan JSON", string(j), fixtureSecrets(t))
		if leaks := secrets.Leaks(string(j)); len(leaks) > 0 {
			t.Errorf("the plan JSON contains the values of %v", leaks)
		}
	})

	t.Run("explicit container links", func(t *testing.T) {
		want := map[string][]string{
			"my-docker": {"traefik", "uptimekuma"},
			"my-unraid": {"Plex-Current", "radarr", "sonarr", "bazarr", "tautulli", "binhex-qbittorrentvpn", "binhex-prowlarr"},
		}
		got := map[string][]string{}
		byID := map[string]importer.Instance{}
		for _, in := range p.Instances {
			byID[in.ID] = in
		}
		for _, l := range p.Links {
			got[l.Endpoint] = append(got[l.Endpoint], l.Container)
			if l.Type != "runs_on" || byID[l.Docker].Name != l.Endpoint || l.Instance == "" || l.Tile == "" {
				t.Errorf("link %+v", l)
			}
		}
		for ep, cs := range want {
			if !slices.Equal(got[ep], cs) {
				t.Errorf("%s containers = %v, want %v", ep, got[ep], cs)
			}
		}
	})

	t.Run("links and checks without an integration", func(t *testing.T) {
		checks := instancesByName(p)["Homepage"]
		targets, _ := checks.Config["targets"].([]any)
		want := map[string]string{
			"Jupiter Unraid":    "http://10.0.11.120",
			"JupiterNasCockpit": "https://jupiter-nas-debian.lan:9090/",
			"Linkding":          "https://links.lab.example",
		}
		if len(targets) != len(want) {
			t.Fatalf("check targets = %v", targets)
		}
		for _, tg := range targets {
			m := tg.(map[string]any)
			if want[m["name"].(string)] != m["url"] || m["kind"] != "http" {
				t.Errorf("target %v", m)
			}
		}
		for _, tl := range p.Tiles {
			switch tl.Name {
			case "Jupiter Unraid":
				if tl.Href != "https://jupiter.lab.example/" || tl.Check != "Jupiter Unraid" || tl.Instance != checks.ID {
					t.Errorf("Jupiter tile = %+v (deep-links to its UI, checks siteMonitor)", tl)
				}
			case "Linkding":
				if tl.Description != "Shared bookmarks" || tl.Check != "Linkding" {
					t.Errorf("Linkding tile = %+v", tl)
				}
			}
		}
	})

	t.Run("media calendar", func(t *testing.T) {
		var cal *importer.Tile
		for i := range p.Tiles {
			if p.Tiles[i].Widget == "calendar" {
				cal = &p.Tiles[i]
			}
		}
		if cal == nil || !cal.Chromeless || cal.Section != "data" {
			t.Fatalf("calendar tile = %+v", cal)
		}
		byName := instancesByName(p)
		if len(cal.Sources) != 2 || cal.Sources[0].Instance != byName["Sonarr"].ID || cal.Sources[1].Instance != byName["Radarr"].ID {
			t.Fatalf("calendar sources = %+v", cal.Sources)
		}
		for _, s := range cal.Sources {
			if s.Capability != "media.calendar" || s.Options["unmonitored"] != true {
				t.Errorf("source %+v: binds by capability, unmonitored preserved", s)
			}
		}
		// It reads the Sonarr and Radarr instances; it has none of its own.
		if cal.Instance != "" {
			t.Errorf("calendar duplicates an instance: %s", cal.Instance)
		}
	})

	t.Run("not imported", func(t *testing.T) {
		var settings []string
		for _, ig := range p.Ignored {
			if ig.File == settingsFile {
				settings = append(settings, ig.Key)
			}
		}
		want := []string{"providers", "background", "hideVersion", "useEqualHeights", "color", "statusStyle", "disableCollapse", "headerStyle"}
		if !slices.Equal(settings, want) {
			t.Errorf("ignored settings = %v, want %v", settings, want)
		}
		// Comments aren't config.
		j, _ := json.Marshal(res)
		for _, gone := range []string{"Mini-Titan", "ProxmoxMiniTitan", "Overseer", "NZBget", "nzbget", "Linkwarden", "linkwarden", "sensor.total_power"} {
			if bytes.Contains(j, []byte(gone)) {
				t.Errorf("commented-out %q was imported", gone)
			}
		}
	})

	t.Run("hints", func(t *testing.T) {
		var inbox []string
		for _, h := range res.Hints {
			if h.Inbox {
				inbox = append(inbox, h.Code+": "+h.Message)
			}
		}
		want := []string{
			"reused_secret: The same password is used by Traefik and qBittorrent. Give each service its own.",
			"plain_http_credentials: OPNSense sends its credentials over plain http:// to 10.0.11.1. Use https:// if it supports it.",
			"plain_http_credentials: Traefik sends its credentials over plain http:// to traefik.lab.example. Use https:// if it supports it.",
		}
		if !slices.Equal(inbox, want) {
			t.Errorf("inbox hints =\n%s\nwant\n%s", strings.Join(inbox, "\n"), strings.Join(want, "\n"))
		}
		var docker, self int
		for _, h := range res.Hints {
			switch h.Code {
			case "docker_endpoint":
				docker++
				if h.Inbox || h.Severity != "info" {
					t.Errorf("a Docker endpoint note is not a security notice: %+v", h)
				}
			case "self_host":
				self++
				if !strings.Contains(h.Message, "pvedocker") {
					t.Errorf("self_host = %q", h.Message)
				}
			}
		}
		if docker != 2 || self != 1 {
			t.Errorf("docker notes %d, self hints %d", docker, self)
		}
	})

	t.Run("discovery", func(t *testing.T) {
		var kinds []string
		for _, d := range p.Discovery {
			kinds = append(kinds, d.Kind+":"+d.Section+d.Endpoint)
		}
		want := []string{"label_section:apps", "label_endpoint:my-docker", "label_endpoint:my-unraid"}
		if !slices.Equal(kinds, want) {
			t.Errorf("discovery = %v, want %v", kinds, want)
		}
	})

	t.Run("zero unmapped", func(t *testing.T) {
		if len(res.Unmapped) != 0 || p.Summary.Unmapped != 0 {
			t.Fatalf("unmapped = %+v", res.Unmapped)
		}
		// Every active entry lands as a tile: 22 entries in services.yaml.
		if len(p.Tiles) != 22 || p.Summary.Tiles != 22 {
			t.Fatalf("%d tiles, want 22", len(p.Tiles))
		}
	})
}

// TestDogfoodImport runs the import end to end through the HTTP route:
// dry run (the golden plan), apply, the database and vault afterwards,
// logs, and a re-import that changes nothing.
func TestDogfoodImport(t *testing.T) {
	h := newHub(t)
	ctx := t.Context()
	body, err := json.Marshal(dogfood(t))
	if err != nil {
		t.Fatal(err)
	}
	leaks := fixtureSecrets(t)

	status, dry := h.post(t, "?dry_run=1", body)
	if status != 200 {
		t.Fatalf("dry run: %d %s", status, dry)
	}
	assertNoLeak(t, "the dry-run response", string(dry), leaks)
	golden(t, dry)
	var plan struct {
		Plan     importer.Plan       `json:"plan"`
		Hints    []importer.Hint     `json:"hints"`
		Unmapped []importer.Unmapped `json:"unmapped"`
	}
	if err := json.Unmarshal(dry, &plan); err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Plan.Changes {
		if c.Action != importer.ActionCreate {
			t.Errorf("fresh hub: %+v", c)
		}
	}
	if n, _ := h.vault.List(ctx); len(n) != 0 {
		t.Fatalf("a dry run wrote %d secrets", len(n))
	}

	status, out := h.post(t, "", body)
	if status != 200 {
		t.Fatalf("apply: %d %s", status, out)
	}
	assertNoLeak(t, "the apply response", string(out), leaks)
	var applied importer.Applied
	if err := json.Unmarshal(out, &applied); err != nil {
		t.Fatal(err)
	}
	if applied.BoardID != plan.Plan.Board.ID || len(applied.InstanceIDs) != 21 || !applied.Changed() {
		t.Fatalf("applied = %+v", applied)
	}

	// The vault holds exactly the 18 values, under the names the plan gave.
	names, err := h.vault.List(ctx)
	if err != nil || len(names) != 18 {
		t.Fatalf("vault holds %d secrets (%v)", len(names), err)
	}
	_, secrets := build(t, dogfood(t))
	for _, ref := range plan.Plan.Secrets {
		got, err := h.vault.Get(ctx, ref.Name)
		if err != nil || !secrets[ref.Name].Equal(string(got)) {
			t.Errorf("vault %s: %v", ref.Name, err)
		}
	}

	// Rows: the board, one layout revision, 21 instances with refs only.
	var dump strings.Builder
	rows, err := h.st.Reader().QueryContext(ctx, `SELECT integration, name, config FROM integration_instances ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var integ, name, cfg string
		if err := rows.Scan(&integ, &name, &cfg); err != nil {
			t.Fatal(err)
		}
		n++
		dump.WriteString(cfg)
	}
	rows.Close()
	if n != 21 {
		t.Fatalf("%d instance rows", n)
	}
	assertNoLeak(t, "integration_instances.config", dump.String(), leaks)
	if c := strings.Count(dump.String(), "${secret:instance/"); c != 18 {
		t.Errorf("%d secret refs in config, want 18", c)
	}
	var title, doc string
	var rev, fv int
	if err := h.st.Reader().QueryRowContext(ctx, `SELECT b.title, l.revision, l.format_version, l.document FROM boards b JOIN layouts l ON l.board_id = b.id WHERE b.id = ?`, applied.BoardID).
		Scan(&title, &rev, &fv, &doc); err != nil {
		t.Fatal(err)
	}
	if title != "Alpaca Homepage" || rev != 1 || fv != 1 {
		t.Fatalf("board %q rev %d fv %d", title, rev, fv)
	}
	var stored importer.LayoutDocument
	if err := json.Unmarshal([]byte(doc), &stored); err != nil || len(stored.Sections) != 8 {
		t.Fatalf("stored layout: %v", err)
	}

	// The record keeps tiles, links and discovery; GET serves it.
	status, rec := h.get(t)
	if status != 200 {
		t.Fatalf("GET record: %d %s", status, rec)
	}
	assertNoLeak(t, "the import record", string(rec), leaks)
	var r struct{ Record importer.Record }
	if err := json.Unmarshal(rec, &r); err != nil || len(r.Record.Tiles) != 22 || len(r.Record.Links) != 9 || len(r.Record.Instances) != 21 {
		t.Fatalf("record: %v %s", err, rec)
	}

	// Re-import: the dry run shows nothing to do, applying changes no row.
	before := dumpTables(t, h)
	status, dry2 := h.post(t, "?dry_run=1", body)
	if status != 200 {
		t.Fatal(status)
	}
	if err := json.Unmarshal(dry2, &plan); err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Plan.Changes {
		if c.Action != importer.ActionUnchanged {
			t.Errorf("re-import dry run: %+v", c)
		}
	}
	status, out = h.post(t, "", body)
	if status != 200 {
		t.Fatal(status)
	}
	if err := json.Unmarshal(out, &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Changed() {
		t.Errorf("re-import changed: %+v", applied.Changes)
	}
	if after := dumpTables(t, h); after != before {
		t.Error("re-import touched rows")
	}

	assertNoLeak(t, "the hub's logs", h.logs.String(), leaks)
	if !strings.Contains(h.logs.String(), "homepage import applied") {
		t.Errorf("logs = %s", h.logs.String())
	}
}

func golden(t *testing.T, got []byte) {
	t.Helper()
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, got, "", "  "); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixtureDir, "expected-plan.json")
	if *update {
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/importer/homepage -run TestDogfoodImport -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(pretty.Bytes()), bytes.TrimSpace(want)) {
		t.Errorf("the dry-run plan differs from %s; if the change is intended, rerun with -update and review the diff", path)
	}
}

func dumpTables(t *testing.T, h *hub) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id || slug || title || position || updated_at FROM boards`,
		`SELECT board_id || revision || document || created_at FROM layouts ORDER BY revision`,
		`SELECT id || integration || name || config || enabled || updated_at FROM integration_instances ORDER BY id`,
		`SELECT name || hex(data_key) || hex(ciphertext) || updated_at FROM vault_secrets ORDER BY name`,
		`SELECT key || value || updated_at FROM settings ORDER BY key`,
	} {
		rows, err := h.st.Reader().QueryContext(context.Background(), q)
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

func instancesByName(p importer.Plan) map[string]importer.Instance {
	m := map[string]importer.Instance{}
	for _, in := range p.Instances {
		m[in.Name] = in
	}
	return m
}

func tileFor(p importer.Plan, instance string) importer.Tile {
	for _, t := range p.Tiles {
		if t.Instance == instance {
			return t
		}
	}
	return importer.Tile{}
}
