package homepage

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/importer"
)

func tileIDs(p importer.Plan) []string {
	var ids []string
	for _, t := range p.Tiles {
		ids = append(ids, t.Section+"|"+t.ItemID+"|"+t.Name)
	}
	return ids
}

func instanceNames(p importer.Plan) []string {
	var out []string
	for _, in := range p.Instances {
		out = append(out, in.Integration+":"+in.Name)
	}
	return out
}

func unmappedKinds(res *importer.Result) []string {
	var out []string
	for _, u := range res.Unmapped {
		out = append(out, u.Kind)
	}
	return out
}

func TestListAndMapGroupsAreEquivalent(t *testing.T) {
	list := `
- Media:
    - Sonarr:
        href: https://sonarr.example
        widget:
          type: sonarr
          url: https://sonarr.example
          key: FAKE-sonarr-key
    - Wiki:
        href: https://wiki.example
- Empty:
`
	mapped := `
Media:
  Sonarr:
    href: https://sonarr.example
    widget: {type: sonarr, url: "https://sonarr.example", key: FAKE-sonarr-key}
  Wiki:
    href: https://wiki.example
Empty: ~
`
	a, as := build(t, Input{Services: list})
	b, bs := build(t, Input{Services: mapped})
	if !slices.Equal(tileIDs(a.Plan), tileIDs(b.Plan)) || !slices.Equal(instanceNames(a.Plan), instanceNames(b.Plan)) {
		t.Fatalf("list form %v %v\nmap form %v %v", tileIDs(a.Plan), instanceNames(a.Plan), tileIDs(b.Plan), instanceNames(b.Plan))
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) || len(as) != 1 || len(bs) != 1 {
		t.Fatal("list and map forms produce different plans")
	}
	if got := len(a.Plan.Board.Layout.Sections); got != 2 {
		t.Fatalf("%d sections, want Media and Empty", got)
	}
}

func TestMissingDockerYAML(t *testing.T) {
	in := dogfood(t)
	in.Docker = ""
	res, secrets := build(t, in)
	for _, in := range res.Plan.Instances {
		if in.Integration == "docker" {
			t.Fatalf("docker instance without docker.yaml: %+v", in)
		}
	}
	if len(res.Plan.Links) != 0 {
		t.Fatalf("links = %+v", res.Plan.Links)
	}
	// The nine server + container pairs can't be linked; nothing else is lost.
	kinds := unmappedKinds(res)
	if len(kinds) != 9 || slices.ContainsFunc(kinds, func(k string) bool { return k != "container_link" }) {
		t.Fatalf("unmapped = %v", kinds)
	}
	if !strings.Contains(res.Unmapped[0].Reason, `"my-docker" isn't in docker.yaml`) {
		t.Errorf("reason = %q", res.Unmapped[0].Reason)
	}
	if res.Plan.SecretsMoved != 18 || len(secrets) != 18 || len(res.Plan.Tiles) != 22 {
		t.Fatalf("secrets %d tiles %d", res.Plan.SecretsMoved, len(res.Plan.Tiles))
	}
	for _, d := range res.Plan.Discovery {
		if d.Kind == "label_endpoint" {
			t.Errorf("discovery on an endpoint that doesn't exist: %+v", d)
		}
	}
}

func TestUnknownWidgetIsUnmappedAndNeverEchoed(t *testing.T) {
	src := `
- Media:
  - Overseerr:
      href: https://requests.example
      icon: overseerr.png
      widget:
        type: overseerr
        url: https://requests.example
        key: FAKE-overseerr-key-0001
        password: FAKE-overseerr-password
  - Mystery:
      href: https://mystery.example
      widget:
        url: https://mystery.example
        key: FAKE-mystery-key
`
	res, secrets := build(t, Input{Services: src})
	if len(secrets) != 0 || len(res.Plan.Secrets) != 0 {
		t.Fatalf("an unmapped widget's credentials were moved: %v", res.Plan.Secrets)
	}
	if len(res.Unmapped) != 2 {
		t.Fatalf("unmapped = %+v", res.Unmapped)
	}
	u := res.Unmapped[0]
	if u.Kind != "widget" || u.Type != "overseerr" || !slices.Equal(u.Fields, []string{"type", "url", "key", "password"}) || u.Origin.Entry != "Overseerr" {
		t.Errorf("unmapped = %+v", u)
	}
	if res.Unmapped[1].Reason != "the widget has no type" {
		t.Errorf("untyped widget = %+v", res.Unmapped[1])
	}
	// The entry still lands, as a service tile watched through its href.
	if len(res.Plan.Tiles) != 2 || res.Plan.Tiles[0].Widget != "service" || res.Plan.Tiles[0].Check != "Overseerr" {
		t.Fatalf("tiles = %+v", res.Plan.Tiles)
	}
	j, _ := json.Marshal(res)
	assertNoLeak(t, "the plan", string(j), []string{"FAKE-overseerr-key-0001", "FAKE-overseerr-password", "FAKE-mystery-key"})
}

func TestWidgetsList(t *testing.T) {
	src := `
- Media:
  - Arr:
      href: https://arr.example
      widgets:
        - type: sonarr
          url: https://arr.example/sonarr
          key: FAKE-k1
        - type: radarr
          url: https://arr.example/radarr
          key: FAKE-k2
`
	res, secrets := build(t, Input{Services: src})
	if got := instanceNames(res.Plan); !slices.Equal(got, []string{"sonarr:Arr", "radarr:Arr (radarr)"}) {
		t.Fatalf("instances = %v", got)
	}
	if got := tileIDs(res.Plan); !slices.Equal(got, []string{"media|arr@app:sonarr|Arr", "media|arr@app:radarr|Arr"}) {
		t.Fatalf("tiles = %v", got)
	}
	if len(secrets) != 2 || len(res.Unmapped) != 0 {
		t.Fatalf("secrets %d unmapped %v", len(secrets), res.Unmapped)
	}
	// No check: the entry has integrations.
	if slices.ContainsFunc(res.Plan.Instances, func(i importer.Instance) bool { return i.Integration == "checks" }) {
		t.Error("an integrated entry got a check")
	}
}

func TestNestedGroupsWeightAndLayoutOrder(t *testing.T) {
	services := `
- Infra:
    - Zeta:
        href: https://zeta.example
    - Alpha:
        href: https://alpha.example
        weight: 50
    - Storage:
        - NAS:
            href: https://nas.example
- Media:
    - Plex:
        href: https://plex.example
`
	settings := `
layout:
  - Media:
      style: row
      columns: 6
  - Infra:
      style: column
      Storage:
        style: row
        columns: 2
        header: false
`
	res, _ := build(t, Input{Services: services, Settings: settings})
	var got []string
	for _, s := range res.Plan.Board.Layout.Sections {
		title := "-"
		if s.Title != nil {
			title = *s.Title
		}
		got = append(got, fmt.Sprintf("%s:%s:%d", s.ID, title, s.Span))
	}
	want := []string{"media:Media:6", "infra:Infra:4", "infra-storage:-:2"}
	if !slices.Equal(got, want) {
		t.Fatalf("sections = %v, want %v", got, want)
	}
	if ids := tileIDs(res.Plan); !slices.Equal(ids, []string{
		"media|service@homepage:media/plex|Plex",
		"infra|service@homepage:infra/alpha|Alpha", // weight 50 sorts before Zeta (200)
		"infra|service@homepage:infra/zeta|Zeta",
		"infra-storage|service@homepage:infra-storage/nas|NAS",
	}) {
		t.Fatalf("tiles = %v", ids)
	}
}

func TestPlaceholdersAreMissingNotMoved(t *testing.T) {
	src := `
- Media:
  - Sonarr:
      widget:
        type: sonarr
        url: https://sonarr.example
        key: "{{HOMEPAGE_VAR_SONARR_KEY}}"
  - Radarr:
      widget:
        type: radarr
        url: "{{HOMEPAGE_VAR_RADARR_URL}}"
        key: FAKE-radarr
`
	res, secrets := build(t, Input{Services: src})
	son := instancesByName(res.Plan)["Sonarr"]
	if !slices.Equal(son.MissingSecrets, []string{"api_key"}) || len(son.SecretFields) != 0 || son.Config["api_key"] != nil {
		t.Fatalf("sonarr = %+v", son)
	}
	if len(secrets) != 1 {
		t.Fatalf("secrets = %d", len(secrets))
	}
	if kinds := unmappedKinds(res); !slices.Equal(kinds, []string{"placeholder"}) {
		t.Fatalf("unmapped = %v", kinds)
	}
	if instancesByName(res.Plan)["Radarr"].Config["url"] != nil {
		t.Error("a placeholder URL was imported")
	}
	var missing int
	for _, h := range res.Hints {
		if h.Code == "missing_secret" && !h.Inbox {
			missing++
		}
	}
	if missing != 1 {
		t.Errorf("missing_secret hints = %d", missing)
	}
}

func TestInvalidYAMLNeverEchoes(t *testing.T) {
	for name, in := range map[string]Input{
		"unterminated quote": {Services: "- Media:\n  - Sonarr:\n      widget:\n        key: \"FAKE-unterminated-SECRET\n"},
		"tab indentation":    {Services: "- Media:\n\t- Sonarr:\n      key: FAKE-tabbed-SECRET\n"},
		"bad settings":       {Services: "- A:\n", Settings: "title: [FAKE-settings-SECRET\n"},
		"bad docker":         {Services: "- A:\n", Docker: "my: {host: FAKE-docker-SECRET\n"},
	} {
		_, _, err := Build(in)
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		if strings.Contains(err.Error(), "FAKE") || !strings.Contains(err.Error(), "is not valid YAML") {
			t.Errorf("%s: error = %q", name, err)
		}
	}
	for name, in := range map[string]Input{
		"scalar services": {Services: "just a string"},
		"list settings":   {Services: "- A:\n", Settings: "- a\n- b\n"},
		"list docker":     {Services: "- A:\n", Docker: "- a\n"},
	} {
		if _, _, err := Build(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAliasBombIsRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString("x0: &a0 [lol, lol, lol, lol, lol, lol, lol, lol, lol, lol]\n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&b, "x%d: &a%d [", i, i)
		for j := range 10 {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "*a%d", i-1)
		}
		b.WriteString("]\n")
	}
	b.WriteString("Media:\n  Plex:\n    widget:\n      type: plex\n      url: https://plex.example\n      key: FAKE\n      fields: *a9\n")
	start := time.Now()
	_, _, err := Build(Input{Services: b.String()})
	if err == nil || !strings.Contains(err.Error(), "too many YAML nodes") {
		t.Fatalf("alias bomb: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("alias bomb took %v", d)
	}
}

func TestDockerVariants(t *testing.T) {
	docker := `
local:
  socket: /var/run/docker.sock
secure:
  host: docker.example
  port: 2376
  tls:
    keyFile: /certs/key.pem
    caFile: /certs/ca.pem
    certFile: /certs/cert.pem
swarm:
  host: 10.0.0.5
  swarm: true
broken:
  port: 2375
`
	res, _ := build(t, Input{Services: "- A:\n", Docker: docker})
	eps := map[string]string{}
	for _, in := range res.Plan.Instances {
		eps[in.Name], _ = in.Config["endpoint"].(string)
	}
	want := map[string]string{"local": "unix:///var/run/docker.sock", "secure": "tcp://docker.example:2376", "swarm": "tcp://10.0.0.5:2375"}
	for k, v := range want {
		if eps[k] != v {
			t.Errorf("%s endpoint = %q, want %q", k, eps[k], v)
		}
	}
	if _, ok := eps["broken"]; ok {
		t.Error("a server without host or socket became an instance")
	}
	if kinds := unmappedKinds(res); !slices.Equal(kinds, []string{"docker_tls", "docker_server"}) {
		t.Fatalf("unmapped = %v", kinds)
	}
	if !slices.Equal(res.Unmapped[0].Fields, []string{"keyFile", "caFile", "certFile"}) {
		t.Errorf("docker_tls fields = %v", res.Unmapped[0].Fields)
	}
	var socketHint bool
	for _, h := range res.Hints {
		if h.Code == "docker_endpoint" && strings.Contains(h.Message, "socket proxy") && strings.Contains(h.Message, "unix://") {
			socketHint = true
		}
	}
	if !socketHint {
		t.Error("no hint for a raw Docker socket")
	}
	if !slices.ContainsFunc(res.Plan.Ignored, func(ig importer.Ignored) bool { return ig.Key == "swarm" }) {
		t.Error("swarm not listed as ignored")
	}
}

func TestSettingsVariants(t *testing.T) {
	services := `
- Narrow:
  - Plex:
      widget: {type: plex, url: "https://plex.example", key: FAKE}
- Huge:
  - A:
      href: https://a.example
- Plain:
  - B:
      href: https://b.example
`
	settings := `
layout:
  Narrow: {style: row, columns: 1}
  Huge: {style: row, columns: 20, tab: Second, icon: mdi-x}
quicklaunch:
  provider: google
cardBlur: md
somethingNew: true
`
	res, _ := build(t, Input{Services: services, Settings: settings})
	spans := map[string]int{}
	for _, s := range res.Plan.Board.Layout.Sections {
		spans[s.ID] = s.Span
	}
	// Plex is at narrowest "small" (2 wide): the 1-column section widens.
	if spans["narrow"] != 2 || spans["huge"] != 12 || spans["plain"] != 4 {
		t.Fatalf("spans = %v", spans)
	}
	if res.Plan.Board.Name != "Homepage" {
		t.Errorf("board name = %q", res.Plan.Board.Name)
	}
	if len(res.Plan.Summary.Notes) != 2 {
		t.Errorf("notes = %v", res.Plan.Summary.Notes)
	}
	var keys []string
	for _, ig := range res.Plan.Ignored {
		keys = append(keys, ig.Key)
	}
	for _, k := range []string{"layout.tab", "layout.icon", "quicklaunch.provider", "cardBlur", "somethingNew"} {
		if !slices.Contains(keys, k) {
			t.Errorf("%s not listed as ignored (got %v)", k, keys)
		}
	}
	if _, ok := res.Plan.Board.Settings["quicklaunch"]; ok {
		t.Error("quicklaunch setting without searchDescriptions")
	}
}

func TestCalendarSourcesThatDontResolve(t *testing.T) {
	src := `
- Media:
  - Sonarr:
      widget: {type: sonarr, url: "https://sonarr.example", key: FAKE}
- Data:
  - "":
      widget:
        type: calendar
        integrations:
          - {type: sonarr, service_group: Media, service_name: Sonarr}
          - {type: radarr, service_group: Media, service_name: Radarr}
          - {type: ical, url: "https://cal.example/feed.ics", name: Holidays}
`
	res, _ := build(t, Input{Services: src})
	var cal importer.Tile
	for _, tl := range res.Plan.Tiles {
		if tl.Widget == "calendar" {
			cal = tl
		}
	}
	if len(cal.Sources) != 1 || cal.Sources[0].Instance != instancesByName(res.Plan)["Sonarr"].ID {
		t.Fatalf("sources = %+v", cal.Sources)
	}
	if kinds := unmappedKinds(res); !slices.Equal(kinds, []string{"calendar_source", "calendar_source"}) {
		t.Fatalf("unmapped = %+v", res.Unmapped)
	}
}

func TestDuplicateNamesAndSingletons(t *testing.T) {
	src := `
- Media:
  - Sonarr:
      widget: {type: sonarr, url: "https://a.example", key: FAKE-one}
- Anime:
  - Sonarr:
      widget: {type: sonarr, url: "https://b.example", key: FAKE-one}
`
	res, _ := build(t, Input{Services: src})
	if got := instanceNames(res.Plan); !slices.Equal(got, []string{"sonarr:Sonarr", "sonarr:Sonarr (Anime)"}) {
		t.Fatalf("instances = %v", got)
	}
	if got := tileIDs(res.Plan); !slices.Equal(got, []string{"media|arr@app:sonarr|Sonarr", "anime|service@homepage:anime/sonarr|Sonarr"}) {
		t.Fatalf("tiles = %v", got)
	}
	if res.Plan.Instances[0].ID == res.Plan.Instances[1].ID {
		t.Fatal("ids collide")
	}
	var reused []string
	for _, h := range res.Hints {
		if h.Code == "reused_secret" {
			reused = append(reused, h.Message)
		}
	}
	if !slices.Equal(reused, []string{"The same secret is used by Sonarr and Sonarr (Anime). Give each service its own."}) {
		t.Fatalf("reused = %v", reused)
	}
}

func TestPingAndMonitors(t *testing.T) {
	src := `
- Net:
  - Router:
      href: https://router.example
      ping: router.example
  - Printer:
      ping: printer.lan
  - Switch:
      siteMonitor: switch.lan:22
  - Odd:
      siteMonitor: "not a target"
`
	res, _ := build(t, Input{Services: src})
	targets, _ := instancesByName(res.Plan)["Homepage"].Config["targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("targets = %v", targets)
	}
	if m := targets[0].(map[string]any); m["url"] != "https://router.example" || m["kind"] != "http" {
		t.Errorf("router check = %v", m)
	}
	if m := targets[1].(map[string]any); m["kind"] != "tcp" || m["host"] != "switch.lan" || m["port"] != 22 {
		t.Errorf("switch check = %v", m)
	}
	if kinds := unmappedKinds(res); !slices.Equal(kinds, []string{"check", "check"}) {
		t.Fatalf("unmapped = %+v", res.Unmapped)
	}
}

func TestEmptyServicesStillMakesABoard(t *testing.T) {
	res, _ := build(t, Input{Services: "# nothing yet\n", Settings: "title: Lab\nlayout:\n  Apps: {style: row, columns: 3}\n"})
	if res.Plan.Board.Name != "Lab" || len(res.Plan.Board.Layout.Sections) != 1 || len(res.Plan.Discovery) != 1 {
		t.Fatalf("plan = %+v", res.Plan)
	}
}

func TestHTTPErrors(t *testing.T) {
	h := newHub(t)
	if status, _ := h.get(t); status != 404 {
		t.Fatalf("GET before import: %d", status)
	}
	big := `{"services_yaml": "` + strings.Repeat("a", MaxBodyBytes) + `"}`
	for _, tc := range []struct {
		name, query, body string
		status            int
		code              string
	}{
		{"too large", "?dry_run=1", big, 413, "too_large"},
		{"unknown field", "?dry_run=1", `{"services_yaml": "- A:\n", "extra": 1}`, 400, "bad_json"},
		{"no services", "?dry_run=1", `{"docker_yaml": "a: {host: b}"}`, 422, "invalid"},
		{"bad yaml", "?dry_run=1", `{"services_yaml": "- A:\n  - B:\n      key: \"FAKE-in-broken-yaml\n"}`, 422, "invalid_config"},
		{"bad dry_run", "?dry_run=yes", `{"services_yaml": "- A:\n"}`, 400, "invalid"},
	} {
		status, body := h.post(t, tc.query, []byte(tc.body))
		var e struct {
			Error struct{ Code, Message string }
		}
		_ = json.Unmarshal(body, &e)
		if status != tc.status || e.Error.Code != tc.code {
			t.Errorf("%s: %d %s", tc.name, status, body)
		}
		if strings.Contains(string(body), "FAKE") {
			t.Errorf("%s: the error echoes the input: %s", tc.name, body)
		}
	}
	resp, err := h.srv.Client().Post(h.srv.URL+"/api/import/homepage", "text/plain", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 415 {
		t.Errorf("text/plain: %d", resp.StatusCode)
	}
	if strings.Contains(h.logs.String(), "FAKE") {
		t.Errorf("logs echo the input: %s", h.logs.String())
	}
}
