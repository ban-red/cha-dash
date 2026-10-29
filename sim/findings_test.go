package sim

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSpikeFindings checks that the Demo Home's findings are shaped as the
// v0.1 integrations and the correlator report them.
func TestSpikeFindings(t *testing.T) {
	s := mustNew(t, 1)
	s.Skip(DefaultAllOpen)
	st := s.Snapshot()
	inc := func(id string) Incident {
		t.Helper()
		in, ok := st.Incident(id)
		if !ok || in.Status != StatusOpen {
			t.Fatalf("%s isn't open at 14:05", id)
		}
		return in
	}
	res := func(id string) Resource {
		t.Helper()
		r, ok := st.Resource(id)
		if !ok {
			t.Fatalf("no resource %s", id)
		}
		return r
	}

	// Mixed Proxmox versions, and aviary as a 3-node cluster.
	versions := map[string]bool{}
	for _, r := range st.Resources {
		if r.Kind == KindHost && r.Attrs["platform"] == "pve" {
			versions[r.Attrs["pve_version"].(string)[:3]] = true
		}
	}
	if !versions["9.0"] || !versions["9.1"] || !versions["9.2"] {
		t.Errorf("PVE versions %v, want 9.0, 9.1 and 9.2 mixed", versions)
	}
	if c := res("pve:aviary"); c.Kind != KindCluster || c.Attrs["nodes"] != 3 || c.Attrs["quorate"] != true {
		t.Errorf("aviary should be a quorate 3-node cluster: %+v", c)
	}

	// The PBS outage: the datastore incident is a cause of every PVE backup
	// failure, first, and the failures name each other.
	pbs := inc("pbs-datastore-filling")
	for _, id := range []string{"proxmox-wren.backup.daily", "proxmox-owl.backup.daily", "backup-failed"} {
		in := inc(id)
		if c := in.Causes[0]; c.Resource != pbsTarget || !strings.Contains(c.Text, pbs.Title) || !c.At.Equal(pbs.OpenedAt) {
			t.Errorf("%s: first cause %+v, want the PBS datastore's incident", id, c)
		}
		peers := 0
		for _, c := range in.Causes {
			if strings.Contains(c.Text, "to the same PBS failed too") {
				peers++
			}
		}
		if peers != 2 {
			t.Errorf("%s names %d other failing hosts, want 2", id, peers)
		}
	}
	if in := inc("proxmox-wren.backup.daily"); in.Scenario != "backup-target-unreachable" || in.Facts["endpoint"] != pbsEndpoint ||
		in.Facts["reason"] != "No route to host" || in.Facts["storage"] != "pbs-magpie" {
		t.Errorf("wren's failure: %s %v", in.Scenario, in.Facts)
	}
	// Resolved, the correlator's causes go: PBS is pruned at 16:22, the
	// failures stay open until the next night.
	s2 := mustNew(t, 1)
	s2.Skip(16*time.Hour + 30*time.Minute)
	if in, _ := s2.Snapshot().Incident("proxmox-wren.backup.daily"); in.Status != StatusOpen || slices.ContainsFunc(in.Causes, func(c Cause) bool { return c.Resource == pbsTarget }) {
		t.Errorf("at 16:30 wren's failure is %s with causes %+v: the PBS cause should be gone", in.Status, in.Causes)
	}

	// The GPU passthrough on owl: both guests and the device named.
	owl := inc("proxmox-owl.backup.daily")
	if owl.Scenario != "backup-passthrough-conflict" || owl.Target != ptGuest || owl.Facts["pci_device"] != ptDevice || owl.Facts["in_use_by"] != 302 {
		t.Errorf("owl's failure: %s %s %v", owl.Scenario, owl.Target, owl.Facts)
	}
	if text := owl.Causes[len(owl.Causes)-2].Text; !strings.Contains(text, "win-games") || !strings.Contains(text, "studio") || !strings.Contains(text, "0000:03:00") {
		t.Errorf("owl's cause: %q", text)
	}

	// stork's VM in internal-error.
	if in := inc("proxmox-aviary.guest.221"); in.Severity != SeverityDown || in.Scenario != "guest-error" || in.Facts["node"] != "stork" {
		t.Errorf("sandbox: %+v", in)
	}
	if g := res(geGuest); g.State != StateError || g.Health.Status != HealthDown {
		t.Errorf("sandbox is %s, %s", g.State, g.Health.Status)
	}

	// Hints: quiet problems.
	for _, id := range standingHints {
		if in := inc(id); in.Class != ClassProblem || in.Severity != SeverityInfo {
			t.Errorf("hint %s: %s %s", id, in.Class, in.Severity)
		}
	}
	if res("docker:magpie").Attrs["access"] != "broad-proxy" {
		t.Error("magpie's Docker engine should read through a broad proxy")
	}
	if in := inc("adguard.hint.plain_http"); in.Category != CategorySecurity || in.Scenario != "credential-hygiene" || in.Target != "app:adguard" ||
		in.Suggested.ID != "integration.settings" || in.Suggested.Target != "integration:adguard" || in.Facts["url"] != "http://"+ipAdGuard {
		t.Errorf("AdGuard's hint: %+v", in)
	}

	// Repairs, with the hub's codes.
	for id, code := range map[string]string{"unraid-magpie.repair": "unsupported_version", "plex-token-revoked": "auth", "karakeep-api-changed": "api_changed"} {
		if in := inc(id); in.Class != ClassRepair || in.Facts["code"] != code {
			t.Errorf("%s: %s %v", id, in.Class, in.Facts)
		}
	}

	// Spun-down disks report no temperature: null, not stale, as Unraid
	// reported it before its API stopped being read.
	byKey := map[string]Series{}
	for _, se := range st.Series {
		byKey[se.Resource+"|"+se.Metric] = se
	}
	if se := byKey["unraid:magpie/disk/disk5|temp.c"]; se.Value.Valid() || se.Stale {
		t.Errorf("disk5 (spun down) temp: %+v", se)
	}
	if se := byKey["unraid:magpie/disk/disk1|temp.c"]; !se.Value.Valid() || !se.Stale {
		t.Errorf("disk1 temp during Unraid's repair should be stale with a value: %+v", se)
	}
	if res("unraid:magpie/disk/disk5").State != StateIdle {
		t.Error("disk5 should be idle (spun down)")
	}

	// Conventions: every app says which integration it is; label discovery
	// has containers to offer; the weather and the checks are there.
	for _, r := range st.Resources {
		if r.Kind == KindApp && r.Attrs["type"] == nil {
			t.Errorf("%s has no attrs.type", r.ID)
		}
	}
	labelled := 0
	for _, r := range st.Resources {
		if r.Kind == KindContainer && r.Labels["homepage.group"] != "" && r.Labels["homepage.name"] != "" {
			labelled++
		}
	}
	if labelled != 3 {
		t.Errorf("%d containers carry homepage labels, want 3", labelled)
	}
	w := res(weatherID)
	if w.Kind != KindWeather || w.Attrs["location"] != "Greenwich" || len(w.Attrs["hourly"].([]weatherHour)) != 6 || len(w.Attrs["daily"].([]weatherDay)) != 5 {
		t.Errorf("weather: %+v", w)
	}
	for _, k := range []string{"temp.c", "feels.c", "humidity.pct", "wind.kmh", "precip.mm"} {
		if se := byKey[weatherID+"|"+k]; !se.Value.Valid() || se.Stale {
			t.Errorf("weather %s: %+v", k, se)
		}
	}
	checks := 0
	for _, r := range st.Resources {
		if r.Kind == KindCheck {
			checks++
			if byKey[r.ID+"|http.ms"].Unit != "ms" {
				t.Errorf("check %s has no latency", r.ID)
			}
		}
	}
	if checks != 2 {
		t.Errorf("%d checks, want 2", checks)
	}
}

func TestLondonOffset(t *testing.T) {
	for at, want := range map[string]int{
		"2026-03-29T00:59:59Z": 0, "2026-03-29T01:00:00Z": 3600,
		"2026-10-03T12:00:00Z": 3600,
		"2026-10-25T00:59:59Z": 3600, "2026-10-25T01:00:00Z": 0,
		"2027-01-15T12:00:00Z": 0,
	} {
		tm, _ := time.Parse(time.RFC3339, at)
		if got := londonOffset(tm); got != want {
			t.Errorf("londonOffset(%s) = %d, want %d", at, got, want)
		}
	}
}

// TestNewStories checks the incidents that aren't any integration's finding
// (the line slows down, Nextcloud stays in maintenance mode) and the
// household's Saturday, against the series they shape.
func TestNewStories(t *testing.T) {
	s := mustNew(t, 1)
	day := s.Start()
	val := func(res, key string, at time.Time) float64 {
		t.Helper()
		m := s.top.spec(res, key)
		if m == nil {
			t.Fatalf("no series %s %s", res, key)
		}
		return float64(s.value(m, at, s.world(at, false)))
	}

	// The slow line: every speed test from 09:40 to 16:10 measures about
	// 180 Mb/s, and the WAN never goes above it meanwhile. Outside that
	// window the tests measure the usual ~930 and the WAN bursts past 180.
	from, to := day.Add(9*time.Hour+40*time.Minute), day.Add(16*time.Hour+10*time.Minute)
	outside := 0.0
	for at := day; at.Before(day.Add(24 * time.Hour)); at = at.Add(30 * time.Second) {
		dl, wan := val(stApp, "dl.mbps", at), val(stWAN, "rx.mbps", at)
		if within(at, from, to) {
			if dl < stSlow-10 || dl > stSlow+10 || wan > stSlow {
				t.Fatalf("%s: speed test %v, WAN %v, while the line is slow", s.clock(at), dl, wan)
			}
			continue
		}
		if dl < lineDown-15 || dl > lineDown+15 {
			t.Fatalf("%s: speed test %v outside the slow window", s.clock(at), dl)
		}
		outside = max(outside, wan)
	}
	if outside <= stSlow {
		t.Errorf("the WAN never went above %v outside the slow window (max %v)", stSlow, outside)
	}

	// The household: 4, then the kids at football, an adult out for
	// groceries, the kids back, everyone home.
	for hm, want := range map[string]float64{"09:00": 4, "09:45": 2, "11:00": 1, "12:15": 3, "13:00": 4} {
		at, _ := time.Parse("15:04", hm)
		if got := val("app:homeassistant", "people_home", day.Add(time.Duration(at.Hour())*time.Hour+time.Duration(at.Minute())*time.Minute)); got != want {
			t.Errorf("people home at %s: %v, want %v", hm, got, want)
		}
	}

	s.Skip(DefaultAllOpen)
	st := s.Snapshot()
	series := map[string]Series{}
	for _, se := range st.Series {
		series[se.Resource+"|"+se.Metric] = se
	}
	sp, _ := st.Incident("speedtest-slow")
	if sp.Status != StatusOpen || sp.Severity != SeverityAttention || sp.Suggested.ID != "link.open" || sp.Suggested.Target != stApp || sp.Facts["download_mbps"] != stSlow {
		t.Errorf("speedtest-slow: %+v", sp)
	}
	if r, _ := st.Resource(stApp); r.Health.Status != HealthAttention {
		t.Errorf("Speedtest Tracker is %s", r.Health.Status)
	}

	// Nextcloud: down, a T1 fix, its check live and its stats stale, and
	// Uptime Kuma counting it down.
	nc, _ := st.Incident("nextcloud-maintenance")
	if nc.Status != StatusOpen || nc.Severity != SeverityDown || nc.Suggested.ID != "nextcloud.maintenance.off" || nc.Suggested.Tier != T1 || nc.Facts["status"] != 503 {
		t.Errorf("nextcloud-maintenance: %+v", nc)
	}
	if r, _ := st.Resource(ncApp); r.Health.Status != HealthDown || r.State != StateRunning {
		t.Errorf("Nextcloud is %s, %s", r.State, r.Health.Status)
	}
	if se := series[ncApp+"|http.ms"]; se.Stale || !se.Value.Valid() {
		t.Errorf("Nextcloud's check should stay live: %+v", se)
	}
	if se := series[ncApp+"|files"]; !se.Stale {
		t.Errorf("Nextcloud's stats should be stale while it answers 503: %+v", se)
	}
	if v := float64(series["app:uptimekuma|monitors.down"].Value); v < 1 {
		t.Errorf("Uptime Kuma counts %v down, want Nextcloud among them", v)
	}

	// Pressing the fix by hand: maintenance mode off, resolved 30 s later.
	s = mustNew(t, 1, WithScript(Script{{Scenario: ScenarioNextcloudMaintenance, Open: Offset(time.Hour)}}))
	s.Advance(3 * time.Hour)
	if _, err := s.Fix("nextcloud-maintenance"); err != nil {
		t.Fatal(err)
	}
	var res *Event
	for _, ev := range s.Advance(time.Minute) {
		if ev.Type == EventIncidentResolve {
			res = &ev
		}
	}
	if res == nil || !res.At.Equal(s.Start().Add(3*time.Hour+ncSettle)) || res.Incident.ResolvedBy != "action" {
		t.Fatalf("expected a resolve 30 s after the fix, got %+v", res)
	}
	if r, _ := s.Snapshot().Resource(ncApp); r.Attrs["version"] != ncTo || r.Health.Status != HealthOK {
		t.Errorf("Nextcloud after the fix: %v %s", r.Attrs["version"], r.Health.Status)
	}
}
