package sim

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSpikeFindings checks that the Demo Home shows what the M0 spikes found
// in the dogfood lab, shaped as the v0.1 integrations and the correlator
// report it.
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

	// Mixed Proxmox versions, and mars as a 2-node cluster.
	versions := map[string]bool{}
	for _, r := range st.Resources {
		if r.Kind == KindHost && r.Attrs["platform"] == "pve" {
			versions[r.Attrs["pve_version"].(string)[:3]] = true
		}
	}
	if !versions["8.4"] || !versions["9.0"] || !versions["9.2"] {
		t.Errorf("PVE versions %v, want 8.4 and 9.x mixed", versions)
	}
	if c := res("pve:mars"); c.Kind != KindCluster || c.Attrs["nodes"] != 2 {
		t.Errorf("mars should be a 2-node cluster: %+v", c)
	}

	// The PBS outage: the datastore incident is a cause of every PVE backup
	// failure, first, and the failures name each other.
	pbs := inc("pbs-datastore-filling")
	for _, id := range []string{"proxmox-pluto.backup.daily", "proxmox-mars.backup.daily", "proxmox-io.backup.daily", "backup-failed"} {
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
		if peers != 3 {
			t.Errorf("%s names %d other failing hosts, want 3", id, peers)
		}
	}
	for _, id := range []string{"proxmox-pluto.backup.daily", "proxmox-mars.backup.daily"} {
		if in := inc(id); in.Scenario != "backup-target-unreachable" || in.Facts["endpoint"] != pbsEndpoint || in.Facts["reason"] != "No route to host" {
			t.Errorf("%s: %s %v", id, in.Scenario, in.Facts)
		}
	}
	// Resolved, the correlator's causes go: PBS is pruned at 16:22, the
	// failures stay open until the next night.
	s2 := mustNew(t, 1)
	s2.Skip(16*time.Hour + 30*time.Minute)
	if in, _ := s2.Snapshot().Incident("proxmox-pluto.backup.daily"); in.Status != StatusOpen || slices.ContainsFunc(in.Causes, func(c Cause) bool { return c.Resource == pbsTarget }) {
		t.Errorf("at 16:30 pluto's failure is %s with causes %+v: the PBS cause should be gone", in.Status, in.Causes)
	}

	// The GPU passthrough on io: both guests and the device named.
	io := inc("proxmox-io.backup.daily")
	if io.Scenario != "backup-passthrough-conflict" || io.Target != ptGuest || io.Facts["pci_device"] != ptDevice || io.Facts["in_use_by"] != 202 {
		t.Errorf("io's failure: %s %s %v", io.Scenario, io.Target, io.Facts)
	}
	if text := io.Causes[len(io.Causes)-2].Text; !strings.Contains(text, "win-gaming") || !strings.Contains(text, "iolinux") || !strings.Contains(text, "0000:01:00") {
		t.Errorf("io's cause: %q", text)
	}

	// jupiter-nas's VM in internal-error.
	if in := inc("proxmox-jupiter-nas.guest.101"); in.Severity != SeverityDown || in.Scenario != "guest-error" {
		t.Errorf("scratch: %+v", in)
	}
	if g := res(geGuest); g.State != StateError || g.Health.Status != HealthDown {
		t.Errorf("scratch is %s, %s", g.State, g.Health.Status)
	}

	// Hints: quiet problems.
	for _, id := range standingHints {
		if in := inc(id); in.Class != ClassProblem || in.Severity != SeverityInfo {
			t.Errorf("hint %s: %s %s", id, in.Class, in.Severity)
		}
	}
	if res("docker:jupiter").Attrs["access"] != "broad-proxy" {
		t.Error("jupiter's Docker engine should read through a broad proxy")
	}

	// Repairs, with the hub's codes.
	for id, code := range map[string]string{"unraid-jupiter.repair": "unsupported_version", "plex-token-revoked": "auth", "karakeep-api-changed": "api_changed"} {
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
	if se := byKey["unraid:jupiter/disk/disk5|temp.c"]; se.Value.Valid() || se.Stale {
		t.Errorf("disk5 (spun down) temp: %+v", se)
	}
	if se := byKey["unraid:jupiter/disk/disk1|temp.c"]; !se.Value.Valid() || !se.Stale {
		t.Errorf("disk1 temp during Unraid's repair should be stale with a value: %+v", se)
	}
	if res("unraid:jupiter/disk/disk5").State != StateIdle {
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
