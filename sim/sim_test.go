package sim

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func mustNew(t testing.TB, seed int64, opts ...Option) *Sim {
	t.Helper()
	s, err := New(seed, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSameSeedSameResult(t *testing.T) {
	a, b := mustNew(t, 7), mustNew(t, 7)
	for _, d := range []time.Duration{0, 2*time.Hour + 45*time.Minute, 11*time.Hour + 20*time.Minute, 9*time.Hour + 55*time.Minute} {
		ea, eb := a.Advance(d), b.Advance(d)
		if !bytes.Equal(encode(ea), encode(eb)) {
			t.Fatalf("events differ after advancing to %v", a.Now())
		}
		if !bytes.Equal(EncodeState(a.Snapshot()), EncodeState(b.Snapshot())) {
			t.Fatalf("snapshots differ at %v", a.Now())
		}
	}
	if bytes.Equal(EncodeState(mustNew(t, 7).Snapshot()), EncodeState(mustNew(t, 8).Snapshot())) {
		t.Fatal("seeds 7 and 8 gave identical snapshots")
	}
}

func TestStepSizeDoesNotMatter(t *testing.T) {
	a := mustNew(t, 3)
	ea := a.Advance(26 * time.Hour)
	b := mustNew(t, 3)
	var eb []Event
	steps := []time.Duration{7*time.Minute + 13*time.Second, 41 * time.Second, 3 * time.Hour, time.Second, 59 * time.Minute, 1500 * time.Millisecond}
	for i := 0; b.Now().Before(a.Now()); i++ {
		eb = append(eb, b.Advance(min(steps[i%len(steps)], a.Now().Sub(b.Now())))...)
	}
	if len(ea) != len(eb) {
		t.Fatalf("one step gave %d events, many steps gave %d", len(ea), len(eb))
	}
	for i := range ea {
		if !bytes.Equal(encode(ea[i]), encode(eb[i])) {
			t.Fatalf("event %d differs:\n%s\n%s", i, encode(ea[i]), encode(eb[i]))
		}
	}
	if !bytes.Equal(EncodeState(a.Snapshot()), EncodeState(b.Snapshot())) {
		t.Fatal("final snapshots differ")
	}
}

func TestSkipMatchesAdvance(t *testing.T) {
	a, b := mustNew(t, 5), mustNew(t, 5)
	a.Advance(DefaultAllOpen)
	b.Skip(DefaultAllOpen)
	sa, sb := a.Snapshot(), b.Snapshot()
	sa.Seq, sb.Seq = 0, 0
	if !bytes.Equal(EncodeState(sa), EncodeState(sb)) {
		t.Fatal("Skip and Advance reach different states")
	}
}

// TestEventReplay applies the event stream to the starting snapshot, checks
// referential integrity after every event, and expects to land exactly on
// the final snapshot.
func TestEventReplay(t *testing.T) {
	s := mustNew(t, 11)
	st := s.Snapshot()
	res := map[string]Resource{}
	for _, r := range st.Resources {
		res[r.ID] = r
	}
	rels := map[Relation]bool{}
	for _, r := range st.Relations {
		rels[r] = true
	}
	incs := map[string]Incident{}
	for _, in := range st.Incidents { // the standing hints
		incs[in.ID] = in
	}
	var lastSeq uint64
	var lastAt time.Time
	for _, ev := range s.Advance(30 * time.Hour) {
		if ev.Seq != lastSeq+1 {
			t.Fatalf("seq jumped from %d to %d", lastSeq, ev.Seq)
		}
		if ev.At.Before(lastAt) {
			t.Fatalf("event %d goes back in time", ev.Seq)
		}
		lastSeq, lastAt = ev.Seq, ev.At
		switch ev.Type {
		case EventResourceUpsert:
			res[ev.Resource.ID] = *ev.Resource
		case EventResourceRemove:
			for r := range rels {
				if r.From == ev.ID || r.To == ev.ID {
					t.Fatalf("event %d removes %s while relation %v still points at it", ev.Seq, ev.ID, r)
				}
			}
			delete(res, ev.ID)
		case EventRelationUpsert:
			if _, ok := res[ev.Relation.From]; !ok {
				t.Fatalf("event %d: relation from missing %s", ev.Seq, ev.Relation.From)
			}
			if _, ok := res[ev.Relation.To]; !ok {
				t.Fatalf("event %d: relation to missing %s", ev.Seq, ev.Relation.To)
			}
			rels[*ev.Relation] = true
		case EventRelationRemove:
			delete(rels, *ev.Relation)
		case EventIncidentOpen, EventIncidentUpdate, EventIncidentResolve:
			if _, seen := incs[ev.Incident.ID]; seen == (ev.Type == EventIncidentOpen) {
				t.Fatalf("event %d: %s for incident %s in the wrong order", ev.Seq, ev.Type, ev.Incident.ID)
			}
			if (ev.Type == EventIncidentResolve) != (ev.Incident.Status == StatusResolved) {
				t.Fatalf("event %d: %s with status %s", ev.Seq, ev.Type, ev.Incident.Status)
			}
			incs[ev.Incident.ID] = *ev.Incident
		case EventMetrics:
			for _, smp := range ev.Samples {
				if _, ok := res[smp.Resource]; !ok {
					t.Fatalf("event %d: sample for missing %s", ev.Seq, smp.Resource)
				}
			}
		default:
			t.Fatalf("unknown event type %q", ev.Type)
		}
	}
	end := s.Snapshot()
	if end.Seq != lastSeq {
		t.Fatalf("snapshot seq %d, last event %d", end.Seq, lastSeq)
	}
	var got []Resource
	for _, id := range sortedKeys(res) {
		got = append(got, res[id])
	}
	if !bytes.Equal(encode(got), encode(end.Resources)) {
		t.Fatal("replayed resources differ from the snapshot")
	}
	var gotRels []Relation
	for r := range rels {
		gotRels = append(gotRels, r)
	}
	slices.SortFunc(gotRels, cmpRel)
	if !bytes.Equal(encode(gotRels), encode(end.Relations)) {
		t.Fatal("replayed relations differ from the snapshot")
	}
	var gotIncs []Incident
	for _, id := range sortedKeys(incs) {
		gotIncs = append(gotIncs, incs[id])
	}
	wantIncs := slices.Clone(end.Incidents)
	slices.SortFunc(wantIncs, func(a, b Incident) int { return strings.Compare(a.ID, b.ID) })
	if !bytes.Equal(encode(gotIncs), encode(wantIncs)) {
		t.Fatal("replayed incidents differ from the snapshot")
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestReferencesExist(t *testing.T) {
	s := mustNew(t, 1)
	base := s.top.res
	check := func(st State) {
		t.Helper()
		ids := map[string]bool{}
		for _, r := range st.Resources {
			ids[r.ID] = true
		}
		for _, r := range st.Relations {
			if !ids[r.From] || !ids[r.To] {
				t.Fatalf("%v: relation %v references a missing resource", st.Now, r)
			}
		}
		for _, se := range st.Series {
			if base[se.Resource] == nil {
				t.Fatalf("series for unknown resource %s", se.Resource)
			}
		}
		for _, in := range st.Incidents {
			refs := []string{in.Target, in.Suggested.Target}
			for _, c := range in.Causes {
				refs = append(refs, c.Resource)
			}
			for _, a := range append([]Action{in.Suggested}, in.Alternatives...) {
				refs = append(refs, a.Target)
				for _, im := range a.Impact {
					refs = append(refs, im.Resource)
				}
			}
			for _, im := range in.BlastRadius {
				refs = append(refs, im.Resource)
			}
			for _, id := range refs {
				if base[id] == nil {
					t.Fatalf("incident %s references unknown resource %q", in.ID, id)
				}
			}
			// Hints carry no causes and no blast radius, as the hub's do;
			// anything else has a blast radius unless nothing depends on
			// its target.
			if in.BlastRadius == nil || in.Causes == nil {
				t.Errorf("incident %s has a null list (the hub sends [])", in.ID)
			}
			if len(in.BlastRadius) == 0 && !isHint(in) && len(BlastRadius(s.top.rels, in.Target)) > 0 {
				t.Errorf("incident %s has an empty blast radius", in.ID)
			}
			if in.Suggested.Tier == "" || in.ResolvesWhen == "" || (len(in.Causes) == 0 && !isHint(in)) || in.Title == "" || in.Summary == "" {
				t.Errorf("incident %s is missing Inbox fields", in.ID)
			}
		}
	}
	// Every change point plus a regular grid, across the whole default day.
	times := s.changePoints(s.Start(), s.Start().Add(26*time.Hour))
	for d := time.Duration(0); d <= 26*time.Hour; d += 20 * time.Minute {
		times = append(times, s.Start().Add(d))
	}
	slices.SortFunc(times, time.Time.Compare)
	check(s.Snapshot())
	for _, at := range slices.CompactFunc(times, time.Time.Equal) {
		if at.After(s.Now()) {
			s.Skip(at.Sub(s.Now()))
			st := s.Snapshot()
			st.Series = nil
			check(st)
		}
	}
}

func TestMetricsInRange(t *testing.T) {
	for seed := int64(1); seed <= 4; seed++ {
		s := mustNew(t, seed)
		specs := map[string]*metricSpec{}
		for _, m := range s.top.specs {
			specs[m.res+"|"+m.key] = m
		}
		inRange := func(res, key string, v float64, at time.Time) {
			m := specs[res+"|"+key]
			if m == nil {
				t.Fatalf("sample for unknown metric %s %s", res, key)
			}
			if math.IsNaN(v) || math.IsInf(v, 0) || v < m.min || v > m.max {
				t.Fatalf("seed %d, %v: %s %s = %v outside [%v, %v]", seed, at, res, key, v, m.min, m.max)
			}
			if m.dec == 0 && v != math.Trunc(v) {
				t.Fatalf("%s %s = %v should be a whole number", res, key, v)
			}
		}
		for _, ev := range s.Advance(24 * time.Hour) {
			for _, smp := range ev.Samples {
				if !smp.Value.Valid() {
					if m := specs[smp.Resource+"|"+smp.Metric]; m == nil || !m.awake {
						t.Fatalf("seed %d, %v: %s %s sampled as unknown, but only spun-down disks report unknown", seed, ev.At, smp.Resource, smp.Metric)
					}
					continue
				}
				inRange(smp.Resource, smp.Metric, float64(smp.Value), ev.At)
			}
		}
		for _, se := range s.Snapshot().Series {
			for _, v := range se.History {
				if v.Valid() {
					inRange(se.Resource, se.Metric, float64(v), se.AsOf)
				}
			}
			if se.Band[0] > se.Band[1] || se.Min > se.Max {
				t.Fatalf("%s %s: bad band or scale", se.Resource, se.Metric)
			}
		}
	}
}

// expected opens and resolutions of the default script. The backup
// failures v0.1 can only report clear with the next night's runs.
var defaultExpect = []struct {
	id           string
	open, closes time.Duration
	by           string
}{
	{"proxmox-pluto.backup.daily", 2 * time.Hour, 26*time.Hour + 4*time.Minute, "auto"},
	{"backup-failed", 2*time.Hour + 14*time.Minute, 15*time.Hour + 34*time.Minute, "action"},
	{"proxmox-mars.backup.daily", 2*time.Hour + 20*time.Minute, 26*time.Hour + 27*time.Minute, "auto"},
	{"pbs-datastore-filling", 2*time.Hour + 44*time.Minute, 16*time.Hour + 22*time.Minute, "action"},
	{"proxmox-io.backup.daily", 2*time.Hour + 55*time.Minute, 27*time.Hour + time.Minute, "auto"},
	{"karakeep-api-changed", 9*time.Hour + 5*time.Minute, 19*time.Hour + 30*time.Second, "action"},
	{"proxmox-jupiter-nas.guest.101", 11*time.Hour + 20*time.Minute, 18*time.Hour + 5*time.Minute, "auto"},
	{"unraid-jupiter.repair", 12*time.Hour + 5*time.Minute, 16*time.Hour + 45*time.Minute, "auto"},
	{"plex-token-revoked", 13*time.Hour + 27*time.Minute, 18*time.Hour + 10*time.Second, "action"},
	{"host-memory-pressure", 13*time.Hour + 30*time.Minute, 17*time.Hour + 10*time.Minute, "action"},
	{"qbittorrent-oom", 14*time.Hour + 3*time.Minute, 20*time.Hour + 2*time.Minute, "action"},
}

// standingHints are the default script's hints: open before the day starts,
// open after it ends.
var standingHints = []string{"docker-jupiter.hint.docker_endpoint", "frigate.unauthenticated-admin", "traefik.hint.api_no_auth"}

func isHint(in Incident) bool {
	return in.Class == ClassProblem && (in.Scenario == "credential-hygiene" || in.Scenario == "frigate-unauthenticated-admin")
}

// openIDs are the open incidents' IDs, sorted.
func openIDs(st State) []string {
	var ids []string
	for _, in := range st.Open() {
		ids = append(ids, in.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestDefaultScriptTimeline(t *testing.T) {
	s := mustNew(t, 1)
	start := s.Start()
	healthy := s.Snapshot()
	// t0: nothing needs you; only the standing hints are open, quietly.
	if got := openIDs(healthy); !slices.Equal(got, standingHints) || len(healthy.Incidents) != len(standingHints) {
		t.Fatalf("t0 has incidents %v, want only the hints %v", got, standingHints)
	}
	for _, in := range healthy.Incidents {
		if in.Severity != SeverityInfo || !isHint(in) || len(in.Causes) != 0 || len(in.BlastRadius) != 0 {
			t.Errorf("t0: hint %s should be a quiet problem (info, no causes, no blast radius): %+v", in.ID, in)
		}
	}
	for _, r := range healthy.Resources {
		if r.Health.Status != HealthOK {
			t.Errorf("t0: %s is %s (%s)", r.ID, r.Health.Status, r.Health.Reason)
		}
	}
	for _, se := range healthy.Series {
		if se.Warn > 0 && se.Value.Valid() && float64(se.Value) >= se.Warn {
			t.Errorf("t0: %s %s = %v is past its warning threshold %v", se.Resource, se.Metric, se.Value, se.Warn)
		}
	}

	opened, resolved := map[string]time.Time{}, map[string]Event{}
	collect := func(evs []Event) {
		for _, ev := range evs {
			switch ev.Type {
			case EventIncidentOpen:
				if _, dup := opened[ev.Incident.ID]; dup {
					t.Errorf("%s opened twice", ev.Incident.ID)
				}
				opened[ev.Incident.ID] = ev.At
			case EventIncidentResolve:
				if _, dup := resolved[ev.Incident.ID]; dup {
					t.Errorf("%s resolved twice", ev.Incident.ID)
				}
				resolved[ev.Incident.ID] = ev
			}
		}
	}
	collect(s.Advance(DefaultAllOpen))

	// 14:05: everything open at once, and the metrics agree with the story.
	st := s.Snapshot()
	if got := len(st.Open()); got != len(defaultExpect)+len(standingHints) {
		t.Fatalf("at %v, %d incidents open, want %d: %v", st.Now, got, len(defaultExpect)+len(standingHints), openIDs(st))
	}
	series := map[string]Series{}
	for _, se := range st.Series {
		series[se.Resource+"|"+se.Metric] = se
	}
	if v := float64(series["pbs:main|used.pct"].Value); v < 91.5 || v > 92.6 {
		t.Errorf("PBS datastore at %v%%, want about 92%%", v)
	}
	if v := float64(series["pve:jupiter-nas|mem.pct"].Value); v < 93.5 || v > 97.5 {
		t.Errorf("jupiter-nas memory at %v%%, want about 95%%", v)
	}
	for _, k := range []string{"app:plex|streams", "app:karakeep|bookmarks", "unraid:jupiter|cpu.pct", "unraid:jupiter/array|used.pct"} {
		if se := series[k]; !se.Stale || !se.Value.Valid() {
			t.Errorf("%s should be stale with a last-known value, got %+v", k, se)
		}
	}
	if se := series["app:plex|streams"]; !se.AsOf.Equal(start.Add(13*time.Hour + 26*time.Minute + 30*time.Second)) {
		t.Errorf("Plex streams as of %v, want the last sample before 13:27", se.AsOf)
	}
	if se := series["unraid:jupiter|cpu.pct"]; !se.AsOf.Equal(start.Add(12*time.Hour + 4*time.Minute + 30*time.Second)) {
		t.Errorf("jupiter's CPU as of %v, want the last sample before Unraid's repair at 12:05", se.AsOf)
	}
	for _, k := range []string{"app:plex|http.ms", "check:checks/jupiter-unraid|http.ms"} {
		if se := series[k]; se.Stale || !se.Value.Valid() {
			t.Errorf("%s should still be live: a broken integration is not an outage", k)
		}
	}
	if r, _ := st.Resource(qbCt); r.Attrs["restart_count"] != 1 {
		t.Errorf("qBittorrent restart_count = %v, want 1", r.Attrs["restart_count"])
	}

	collect(s.Advance(30*time.Hour - DefaultAllOpen))
	for _, want := range defaultExpect {
		if got, ok := opened[want.id]; !ok || !got.Equal(start.Add(want.open)) {
			t.Errorf("%s opened at %v, want %v", want.id, got, start.Add(want.open))
		}
		ev, ok := resolved[want.id]
		if !ok || !ev.At.Equal(start.Add(want.closes)) {
			t.Errorf("%s resolved at %v, want %v", want.id, ev.At, start.Add(want.closes))
			continue
		}
		if ev.Incident.ResolvedBy != want.by || ev.Incident.Resolution == "" || ev.Incident.Phase != "resolved" {
			t.Errorf("%s resolved by %q (%q, phase %s), want %q", want.id, ev.Incident.ResolvedBy, ev.Incident.Resolution, ev.Incident.Phase, want.by)
		}
	}
	end := s.Snapshot()
	if got := openIDs(end); !slices.Equal(got, standingHints) {
		t.Errorf("open at the end of the day: %v, want only the hints", got)
	}
	for _, r := range end.Resources {
		if r.Health.Status != HealthOK {
			t.Errorf("end of day: %s is %s (%s)", r.ID, r.Health.Status, r.Health.Reason)
		}
	}
	if r, _ := end.Resource(qbCt); r.Attrs["mem_limit_mib"] != 6144 {
		t.Errorf("qBittorrent memory limit %v after the fix, want 6144", r.Attrs["mem_limit_mib"])
	}
}

func TestFix(t *testing.T) {
	s := mustNew(t, 1, WithScript(Script{{Scenario: ScenarioPlexToken, Open: Offset(time.Hour)}}))
	if _, err := s.Fix("plex-token-revoked"); err == nil {
		t.Fatal("fixing an incident before it opens should fail")
	}
	if _, err := s.Fix("nope"); !errors.Is(err, ErrUnknownIncident) {
		t.Fatalf("got %v, want ErrUnknownIncident", err)
	}
	s.Advance(5 * time.Hour)
	if len(s.Snapshot().Open()) != 1 {
		t.Fatal("an unscripted fix should leave the incident open")
	}
	if _, err := s.Fix("plex-token-revoked"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fix("plex-token-revoked"); err == nil {
		t.Fatal("a second fix should fail")
	}
	var res *Event
	for _, ev := range s.Advance(time.Minute) {
		if ev.Type == EventIncidentResolve {
			res = &ev
		}
	}
	if res == nil || !res.At.Equal(s.Start().Add(5*time.Hour+10*time.Second)) || res.Incident.ResolvedBy != "action" {
		t.Fatalf("expected a resolve 10s after the fix, got %+v", res)
	}
}

func TestFixRecreatesContainer(t *testing.T) {
	s := mustNew(t, 1, WithScript(Script{{Scenario: ScenarioQbitOOM, Open: Offset(time.Hour)}}))
	s.Advance(2 * time.Hour)
	ev, err := s.Fix("qbittorrent-oom")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range ev {
		kinds = append(kinds, string(e.Type))
		if e.Type == EventResourceUpsert && e.Resource.ID != qbApp {
			t.Errorf("the fix should only touch qBittorrent, but changed %s", e.Resource.ID)
		}
	}
	if got := strings.Join(kinds, " "); got != "relation.remove relation.remove relation.remove resource.remove resource.upsert incident.update" {
		t.Fatalf("fix events: %s", got)
	}
	ev = s.Advance(15 * time.Second)
	kinds = kinds[:0]
	for _, e := range ev {
		if e.Type != EventMetrics {
			kinds = append(kinds, string(e.Type))
		}
	}
	if got := strings.Join(kinds, " "); !strings.HasPrefix(got, "resource.upsert") || strings.Count(got, "relation.upsert") != 3 {
		t.Fatalf("recreate events: %s", got)
	}
}

func TestAutoResolve(t *testing.T) {
	s := mustNew(t, 1, WithScript(Script{
		{Scenario: ScenarioBackupFailed, Open: Offset(2*time.Hour + 14*time.Minute)},
		{Scenario: ScenarioQbitOOM, Open: Offset(6 * time.Hour)},
	}))
	got := map[string]Incident{}
	at := map[string]time.Time{}
	for _, ev := range s.Advance(31 * time.Hour) {
		if ev.Type == EventIncidentResolve {
			got[ev.Incident.ID], at[ev.Incident.ID] = *ev.Incident, ev.At
		}
	}
	if !at["backup-failed"].Equal(s.Start().Add(26*time.Hour+14*time.Minute)) || got["backup-failed"].ResolvedBy != "auto" {
		t.Errorf("backup-failed: resolved %v by %q, want the next nightly run", at["backup-failed"], got["backup-failed"].ResolvedBy)
	}
	if !at["qbittorrent-oom"].Equal(s.Start().Add(30*time.Hour)) || got["qbittorrent-oom"].ResolvedBy != "auto" {
		t.Errorf("qbittorrent-oom: resolved %v by %q, want 24h after the kill", at["qbittorrent-oom"], got["qbittorrent-oom"].ResolvedBy)
	}
	r, _ := s.Snapshot().Resource(bfGuest)
	if r.Attrs["backup.last_status"] != "ok" {
		t.Errorf("homeassistant backup status %v after the next run", r.Attrs["backup.last_status"])
	}
}

func TestQuietScript(t *testing.T) {
	s := mustNew(t, 2, WithScript(nil))
	for _, ev := range s.Advance(24 * time.Hour) {
		if strings.HasPrefix(string(ev.Type), "incident.") || ev.Type == EventResourceRemove {
			t.Fatalf("quiet home produced %s", ev.Type)
		}
	}
	for _, r := range s.Snapshot().Resources {
		if r.Health.Status != HealthOK {
			t.Errorf("%s is %s", r.ID, r.Health.Status)
		}
	}
}

func TestScriptValidate(t *testing.T) {
	for name, sc := range map[string]Script{
		"unknown":   {{Scenario: "meteor-strike", Open: Offset(time.Hour)}},
		"duplicate": {{Scenario: ScenarioPlexToken, Open: Offset(time.Hour), Fix: Offset(2 * time.Hour)}, {Scenario: ScenarioKarakeepAPI, ID: "plex-token-revoked", Open: Offset(time.Hour)}},
		"fix-first": {{Scenario: ScenarioPlexToken, Open: Offset(2 * time.Hour), Fix: Offset(time.Hour)}},
		"overlap":   {{Scenario: ScenarioQbitOOM, ID: "a", Open: Offset(time.Hour)}, {Scenario: ScenarioQbitOOM, ID: "b", Open: Offset(5 * time.Hour)}},
		"open-end":  {{Scenario: ScenarioPlexToken, ID: "a", Open: Offset(time.Hour)}, {Scenario: ScenarioPlexToken, ID: "b", Open: Offset(50 * time.Hour)}},
	} {
		if err := sc.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if _, err := New(1, WithScript(sc)); err == nil {
			t.Errorf("%s: New accepted an invalid script", name)
		}
	}
	ok := Script{{Scenario: ScenarioQbitOOM, ID: "a", Open: Offset(time.Hour)}, {Scenario: ScenarioQbitOOM, ID: "b", Open: Offset(48 * time.Hour)}}
	if err := ok.Validate(); err != nil {
		t.Errorf("repeated incidents two days apart: %v", err)
	}
	b, err := json.Marshal(DefaultScript())
	if err != nil {
		t.Fatal(err)
	}
	var back Script
	if err := json.Unmarshal(b, &back); err != nil || !slices.Equal(back, DefaultScript()) {
		t.Fatalf("script JSON round trip failed (%v): %s", err, b)
	}
	if !strings.Contains(string(b), `"open":"2h14m0s"`) {
		t.Errorf("offsets should encode as duration strings: %s", b)
	}
}

func TestBlastRadius(t *testing.T) {
	s := mustNew(t, 1)
	has := func(origin string, want, notWant []string) {
		t.Helper()
		got := map[string]Impact{}
		for _, im := range s.BlastRadius(origin) {
			got[im.Resource] = im
		}
		for _, id := range want {
			if _, ok := got[id]; !ok {
				t.Errorf("blast radius of %s should include %s", origin, id)
			}
		}
		for _, id := range notWant {
			if _, ok := got[id]; ok {
				t.Errorf("blast radius of %s should not include %s", origin, id)
			}
		}
	}
	// Rebooting pluto takes down the router, and with it everything,
	// including Cha Dash itself.
	has("pve:pluto", []string{"net:opnsense", "net:opnsense/wan", "unraid:jupiter", "pve:mars-2", "app:chadash", "frigate:camera/driveway", "pbs:main", "app:plex"}, nil)
	// Stopping jupiter's array stops Docker there, so PBS backups pause, but
	// the guests themselves keep running.
	has("unraid:jupiter/array", []string{"docker:jupiter", "docker:jupiter/pbs", "pbs:main", "pve:minig5/qemu/100", "app:immich"},
		[]string{"pve:minig5", "app:homeassistant", "unraid:jupiter/pool/cache"})
	// qBittorrent going down hurts the *arrs, not Plex.
	has(qbCt, []string{"app:qbittorrent", "app:sonarr", "app:radarr", "app:bazarr", "traefik:route/torrents"}, []string{"app:plex", "docker:jupiter"})
	// A broken integration only makes what it reads stale.
	has(plexInt, []string{"app:plex"}, []string{"docker:jupiter/Plex-Current"})
	for _, im := range s.BlastRadius(bfGuest) {
		if im.Resource == "pbs:main" {
			t.Error("a guest's blast radius must not walk backed_up_by forwards")
		}
	}
}

// TestChangePointsComplete checks that the non-metric world only changes at
// declared change points, which is what makes Advance step-independent.
func TestChangePointsComplete(t *testing.T) {
	s := mustNew(t, 1)
	fp := func(at time.Time) []byte {
		w := s.world(at, true)
		var rs []*Resource
		for _, id := range sortedKeys(w.res) {
			rs = append(rs, w.res[id])
		}
		return encode([]any{rs, w.rels, w.incidents})
	}
	from, to := s.Start().Add(-time.Hour), s.Start().Add(27*time.Hour)
	cps := append([]time.Time{from}, s.changePoints(from, to)...)
	cps = append(cps, to)
	for i := 0; i+1 < len(cps); i++ {
		a, b := cps[i], cps[i+1]
		want := fp(a)
		for _, f := range []float64{0.2, 0.5, 0.8, 0.999} {
			at := a.Add(time.Duration(float64(b.Sub(a)) * f)).Truncate(time.Second)
			if !at.After(a) {
				continue
			}
			if !bytes.Equal(fp(at), want) {
				t.Fatalf("world changed at %v, between change points %v and %v", at, a, b)
			}
		}
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	s := mustNew(t, 1)
	s.Skip(DefaultAllOpen)
	before := EncodeState(s.Snapshot())
	st := s.Snapshot()
	for i := range st.Resources {
		if r := &st.Resources[i]; r.Attrs != nil && r.Labels != nil {
			r.Attrs["mutated"] = true
			r.Labels["mutated"] = "yes"
		}
		st.Resources[i].Health.Status = HealthDown
	}
	for i := range st.Incidents {
		st.Incidents[i].Facts["mutated"] = true
		if len(st.Incidents[i].BlastRadius) > 0 {
			st.Incidents[i].BlastRadius[0].Resource = "nope"
		}
		if len(st.Incidents[i].Causes) > 0 {
			st.Incidents[i].Causes[0].Text = "nope"
		}
	}
	if !bytes.Equal(before, EncodeState(s.Snapshot())) {
		t.Fatal("changing a snapshot changed the simulation")
	}
}

func TestJSONStable(t *testing.T) {
	s := mustNew(t, 1)
	s.Skip(DefaultAllOpen)
	st := s.Snapshot()
	if !slices.IsSortedFunc(st.Resources, func(a, b Resource) int { return strings.Compare(a.ID, b.ID) }) {
		t.Error("resources not sorted by ID")
	}
	if !slices.IsSortedFunc(st.Relations, cmpRel) {
		t.Error("relations not sorted")
	}
	if !slices.IsSortedFunc(st.Series, func(a, b Series) int {
		if c := strings.Compare(a.Resource, b.Resource); c != 0 {
			return c
		}
		return strings.Compare(a.Metric, b.Metric)
	}) {
		t.Error("series not sorted")
	}
	a, b := EncodeState(st), EncodeState(s.Snapshot())
	if !bytes.Equal(a, b) {
		t.Fatal("two snapshots of the same moment encode differently")
	}
	if !json.Valid(a) {
		t.Fatal("EncodeState produced invalid JSON")
	}
	var back State
	if err := json.Unmarshal(a, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Resources) != len(st.Resources) || len(back.Series) != len(st.Series) || len(back.Incidents) != len(st.Incidents) {
		t.Fatal("round trip lost items")
	}
	if escaped := []byte(`\` + "u0026"); bytes.Contains(a, escaped) || !bytes.Contains(a, []byte("&&")) {
		t.Error("HTML escaping should be off (Traefik rules keep their &&)")
	}
}

// TestSanitized guards the public repo: only 10.0.11.x addresses, only
// *.lab.example and *.family.example names, plus the public weather service
// the weather integration credits.
func TestSanitized(t *testing.T) {
	s := mustNew(t, 1)
	s.Skip(DefaultAllOpen)
	text := string(EncodeState(s.Snapshot()))
	for _, ip := range regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`).FindAllString(text, -1) {
		if !strings.HasPrefix(ip, "10.0.11.") {
			t.Errorf("unsanitized address %s", ip)
		}
	}
	host := regexp.MustCompile(`^(10\.0\.11\.\d+|[a-z0-9-]+|[a-z0-9.-]+\.(lab|family)\.example|open-meteo\.com)$`)
	for _, m := range regexp.MustCompile(`(?:https?|tcp)://([^/:"\s]+)`).FindAllStringSubmatch(text, -1) {
		if !host.MatchString(m[1]) {
			t.Errorf("unsanitized host %s", m[1])
		}
	}
	if strings.Contains(strings.ReplaceAll(text, "api@internal", ""), "@") { // api@internal is Traefik's own service
		t.Error("snapshot contains an @ (an address or user?)")
	}
}

func TestTopologyShape(t *testing.T) {
	st := mustNew(t, 1).Snapshot()
	rel := map[Relation]bool{}
	for _, r := range st.Relations {
		rel[r] = true
	}
	pve, endpoints := 0, map[string]bool{}
	for _, r := range st.Resources {
		if r.Kind == KindHost && r.Attrs["platform"] == "pve" {
			pve++
			endpoints[r.Source] = true
		}
	}
	if pve != 6 || len(endpoints) != 5 {
		t.Errorf("%d PVE hosts on %d endpoints, want 6 on 5", pve, len(endpoints))
	}
	for _, want := range []Relation{
		{"pve:mars-1", RelPartOf, "pve:mars"},
		{"pve:mars-2", RelPartOf, "pve:mars"},
		{"net:opnsense", RelRunsOn, "pve:pluto/qemu/100"},
		{"app:homeassistant", RelRunsOn, "pve:minig5/qemu/100"},
		{"app:frigate", RelRunsOn, "pve:minig5/lxc/101"},
		{"docker:pvedocker", RelRunsOn, "pve:mars-1/lxc/110"},
		{"docker:jupiter", RelRunsOn, "unraid:jupiter"},
		{"app:pbs", RelRunsOn, "docker:jupiter/pbs"},
		{"pbs:main", RelPartOf, "app:pbs"},
		{"app:linkding", RelRunsOn, "docker:pvedocker/linkding"},
		{"app:karakeep", RelRunsOn, "docker:pvedocker/karakeep-web-1"},
		{"app:sonarr", RelDependsOn, "app:qbittorrent"},
		{"pve:minig5/qemu/100", RelBackedUpBy, "pve:minig5/backup/daily"},
		{"net:opnsense", RelProvidesNetworkFor, "pve:pluto"},
	} {
		if !rel[want] {
			t.Errorf("missing relation %v", want)
		}
	}
	if r, ok := st.Resource("unraid:jupiter"); !ok || r.Attrs["platform"] != "unraid" {
		t.Error("jupiter should be the Unraid host")
	}
}
