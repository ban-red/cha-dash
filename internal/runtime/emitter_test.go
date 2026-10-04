package runtime

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/topics"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestEmitter() (*Emitter, *topics.Set, *clock) {
	set := topics.New(graph.New(), time.Now())
	clk := &clock{t: time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)}
	return NewEmitter(set, "in_a", clk.now), set, clk
}

func res(id string, health string) integration.Resource {
	return integration.Resource{ID: id, Kind: "app", Name: id, Health: integration.Health{Status: health}}
}

func TestEmitterReplace(t *testing.T) {
	e, set, clk := newTestEmitter()
	if err := e.PublishSelf(integration.Resource{Kind: "integration", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	facts := integration.Facts{
		Resources: []integration.Resource{
			{ID: "app:a", Kind: "app", Name: "A", State: "running", Desired: "running",
				Health: integration.Health{Status: "attention", Reason: "slow"},
				Labels: map[string]string{"chadash.group": "Media"}, Attrs: map[string]any{"version": "1.2", "when": clk.t},
				Links: []integration.Link{{Title: "A", URL: "https://a.lab.example"}}, Caps: []string{"media.requests"}},
			res("app:b", "ok"),
		},
		Relations: []integration.Relation{
			{From: e.Self(), Type: "monitors", To: "app:a"},
			{From: "app:a", Type: "depends_on", To: "app:b"},
			{From: "app:a", Type: "runs_on", To: "docker:elsewhere/a"}, // not in the graph: skipped
		},
	}
	if err := e.Replace("", facts); err != nil {
		t.Fatal(err)
	}
	a, ok := set.Graph.Get("app:a")
	if !ok || a.Source != "integration:in_a" || a.Health != "attention" || a.Labels["chadash.group"] != "Media" {
		t.Fatalf("app:a = %+v", a)
	}
	// The demo's attr mapping: desired, health reason and since, links, caps;
	// attrs are plain JSON.
	if a.Attrs["desired"] != "running" || a.Attrs["health.reason"] != "slow" || a.Attrs["health.since"] != "2026-10-03T14:05:00Z" ||
		a.Attrs["when"] != "2026-10-03T14:05:00Z" {
		t.Fatalf("attrs = %v", a.Attrs)
	}
	if links := a.Attrs["links"].([]any); links[0].(map[string]any)["url"] != "https://a.lab.example" {
		t.Fatalf("links = %v", links)
	}
	_, doc := set.Graph.Snapshot(nil)
	if len(doc.Relations) != 2 || e.Stats().SkippedRelations != 1 {
		t.Fatalf("relations = %v, stats %+v", doc.Relations, e.Stats())
	}

	// Health since holds while the status holds and moves when it changes.
	clk.t = clk.t.Add(10 * time.Minute)
	v := set.Graph.Version()
	if err := e.Replace("", facts); err != nil {
		t.Fatal(err)
	}
	if set.Graph.Version() != v {
		t.Fatal("re-publishing identical facts must not change the graph")
	}
	facts.Resources[0].Health.Status = "ok"
	facts.Resources = facts.Resources[:1] // app:b disappears, and its relation with it
	facts.Relations = facts.Relations[:1]
	if err := e.Replace("", facts); err != nil {
		t.Fatal(err)
	}
	a, _ = set.Graph.Get("app:a")
	if a.Attrs["health.since"] != "2026-10-03T14:15:00Z" {
		t.Fatalf("since after a change = %v", a.Attrs["health.since"])
	}
	if _, ok := set.Graph.Get("app:b"); ok {
		t.Fatal("app:b should be gone")
	}
	if _, doc := set.Graph.Snapshot(nil); len(doc.Relations) != 1 {
		t.Fatalf("relations = %v", doc.Relations)
	}

	// Scopes are independent; a resource belongs to one.
	if err := e.Replace("backups", integration.Facts{Resources: []integration.Resource{res("backup:a", "ok")}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Replace("", integration.Facts{Resources: []integration.Resource{res("app:a", "ok")}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := set.Graph.Get("backup:a"); !ok {
		t.Fatal("replacing one scope removed another's resources")
	}
	if err := e.Replace("other", integration.Facts{Resources: []integration.Resource{res("backup:a", "ok")}}); err == nil {
		t.Fatal("one resource in two scopes")
	}

	// Mistakes are refused whole.
	for name, f := range map[string]integration.Facts{
		"self":      {Resources: []integration.Resource{{ID: e.Self(), Kind: "integration"}}},
		"duplicate": {Resources: []integration.Resource{res("app:x", "ok"), res("app:x", "ok")}},
		"no kind":   {Resources: []integration.Resource{{ID: "app:x"}}},
		"spaces":    {Resources: []integration.Resource{res("app:x y", "ok")}},
		"metric":    {Metrics: []integration.Metric{{Resource: "app:a", Key: "a b"}}},
	} {
		if err := e.Replace("", f); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, ok := set.Graph.Get("app:a"); !ok {
		t.Fatal("a refused batch must not change anything")
	}

	// Clear removes everything, the instance's own resource included.
	e.Clear()
	_, doc = set.Graph.Snapshot(nil)
	if len(doc.Resources) != 0 || len(doc.Relations) != 0 {
		t.Fatalf("after Clear: %v", doc)
	}
}

func TestEmitterSeriesHistory(t *testing.T) {
	e, set, clk := newTestEmitter()
	m := integration.Metric{Resource: "app:a", Key: "http.ms", Unit: "ms", Min: 1, Max: 5000, Band: [2]float64{1, 300}, Warn: 800, Crit: 2000}
	publish := func(v float64) seriesDoc {
		t.Helper()
		m.Value = v
		if err := e.Replace("", integration.Facts{Resources: []integration.Resource{res("app:a", "ok")}, Metrics: []integration.Metric{m}}); err != nil {
			t.Fatal(err)
		}
		return series(t, set, "app:a#http.ms")
	}
	s := publish(40)
	if s.Value != 40 || !s.AsOf.Equal(clk.t) || s.History[HistoryPoints-1] != 40 || s.History[0].Valid() || s.Unit != "ms" || s.Warn != 800 {
		t.Fatalf("first sample = %+v", s)
	}
	// Within the same 30-minute slot only the value moves.
	clk.t = clk.t.Add(5 * time.Minute)
	s = publish(55)
	if s.Value != 55 || s.History[HistoryPoints-1] != 40 {
		t.Fatalf("same slot = %+v", s.History[HistoryPoints-2:])
	}
	// The next slot scrolls the history by one; a gap of two slots by two.
	clk.t = time.Date(2026, 10, 3, 14, 31, 0, 0, time.UTC)
	s = publish(60)
	if s.History[HistoryPoints-1] != 60 || s.History[HistoryPoints-2] != 40 {
		t.Fatalf("next slot = %v", s.History[HistoryPoints-3:])
	}
	clk.t = clk.t.Add(time.Hour)
	s = publish(math.NaN())
	if s.Value.Valid() || s.History[HistoryPoints-1].Valid() || s.History[HistoryPoints-2].Valid() || s.History[HistoryPoints-3] != 60 {
		t.Fatalf("gap = %v", s.History[HistoryPoints-4:])
	}
	// The wire shape is the simulator's.
	b := set.Metrics.Item(topics.Series, "app:a#http.ms")
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	for _, k := range []string{"resource", "metric", "unit", "min", "max", "band", "warn", "crit", "value", "as_of", "history"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("series lacks %q: %s", k, b)
		}
	}
	if raw["value"] != nil {
		t.Fatalf("unknown must be null, never 0: %s", b)
	}

	// Stale: last-known value kept and flagged; the next sample clears it.
	s = publish(42)
	e.MarkStale()
	if s = series(t, set, "app:a#http.ms"); !s.Stale || s.Value != 42 {
		t.Fatalf("stale = %+v", s)
	}
	if s = publish(43); s.Stale {
		t.Fatal("a fresh sample clears stale")
	}
	// A series not published again disappears.
	if err := e.Replace("", integration.Facts{Resources: []integration.Resource{res("app:a", "ok")}}); err != nil {
		t.Fatal(err)
	}
	if set.Metrics.Item(topics.Series, "app:a#http.ms") != nil {
		t.Fatal("dropped series should be deleted")
	}
}

func TestEmitterIncidents(t *testing.T) {
	e, set, clk := newTestEmitter()
	in := integration.Incident{ID: "check.sonarr", Class: integration.ClassProblem, Severity: integration.SeverityDown,
		Title: "Sonarr isn't answering", Summary: "…", Target: "check:in_a/sonarr"}
	if err := e.Raise(in); err != nil {
		t.Fatal(err)
	}
	got := incident(t, set, "in_a.check.sonarr")
	if got.Status != "open" || got.Scenario != "problem" || got.Causes == nil || got.BlastRadius == nil || got.Suggested.ID == "" ||
		!got.OpenedAt.Equal(clk.t) || !got.UpdatedAt.Equal(clk.t) {
		t.Fatalf("raised = %+v", got)
	}
	raw := string(set.Inbox.Item(topics.Incidents, "in_a.check.sonarr"))
	if !strings.Contains(raw, `"causes":[]`) || !strings.Contains(raw, `"blast_radius":[]`) {
		t.Fatalf("arrays must be [] not null: %s", raw)
	}

	// Raising the same thing again changes nothing; a change keeps opened_at.
	clk.t = clk.t.Add(time.Minute)
	v := set.Inbox.Version()
	_ = e.Raise(in)
	if set.Inbox.Version() != v {
		t.Fatal("an identical raise must not publish")
	}
	in.Summary = "worse"
	_ = e.Raise(in)
	got = incident(t, set, "in_a.check.sonarr")
	if got.Summary != "worse" || !got.OpenedAt.Equal(clk.t.Add(-time.Minute)) || !got.UpdatedAt.Equal(clk.t) {
		t.Fatalf("updated = %+v", got)
	}

	// The user acts: being fixed until the next result.
	e.setPhase("check.sonarr", PhaseFixing)
	if got = incident(t, set, "in_a.check.sonarr"); got.Phase != PhaseFixing {
		t.Fatalf("phase = %q", got.Phase)
	}
	_ = e.Raise(in) // still failing: open again
	if got = incident(t, set, "in_a.check.sonarr"); got.Phase != PhaseOpen {
		t.Fatalf("phase after another failure = %q", got.Phase)
	}
	clk.t = clk.t.Add(time.Minute)
	if err := e.Resolve("check.sonarr", "Sonarr answered again at 14:07."); err != nil {
		t.Fatal(err)
	}
	got = incident(t, set, "in_a.check.sonarr")
	if got.Status != "resolved" || got.Phase != PhaseResolved || got.ResolvedBy != "auto" || !got.ResolvedAt.Equal(clk.t) || got.Resolution == "" {
		t.Fatalf("resolved = %+v", got)
	}
	_ = e.Resolve("check.sonarr", "again") // not open: nothing happens
	if incident(t, set, "in_a.check.sonarr").Resolution != "Sonarr answered again at 14:07." {
		t.Fatal("resolving twice changed the incident")
	}

	// Invalid incidents are refused.
	for _, bad := range []integration.Incident{
		{Class: "problem", Severity: "down", Title: "x"},
		{ID: "x", Class: "outage", Severity: "down", Title: "x"},
		{ID: "x", Class: "problem", Severity: "critical", Title: "x"},
		{ID: "x", Class: "problem", Severity: "down"},
	} {
		if err := e.Raise(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}

	// Resolved incidents are pruned after the retention.
	e.Prune(clk.t.Add(-time.Hour))
	if set.Inbox.Item(topics.Incidents, "in_a.check.sonarr") == nil {
		t.Fatal("pruned too early")
	}
	e.Prune(clk.t.Add(time.Second))
	if set.Inbox.Item(topics.Incidents, "in_a.check.sonarr") != nil {
		t.Fatal("not pruned")
	}
}

func TestWatch(t *testing.T) {
	w := NewWatch()
	src := w.Source(realtime.Mux{"graph": realtime.GraphSource{Graph: graph.New()}})
	if w.Interest().Watched {
		t.Fatal("nobody watches yet")
	}
	changed := w.Changed()
	notify := make(chan struct{}, 1)
	f, err := src.Open("graph:kind/vm,lxc", notify)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("opening a feed should signal a change")
	}
	if i := w.Interest(); !i.Watched || len(i.Kinds) != 2 || !i.WantsKind("vm") || i.WantsKind("container") {
		t.Fatalf("interest = %+v", i)
	}
	g, _ := src.Open("graph", notify)
	if i := w.Interest(); i.Kinds != nil || !i.WantsKind("container") {
		t.Fatalf("an unfiltered graph feed means every kind: %+v", i)
	}
	if _, err := src.Open("nope", notify); err == nil {
		t.Fatal("unknown topic")
	}
	g.Close()
	g.Close() // closing twice counts once
	f.Close()
	if w.Interest().Watched {
		t.Fatalf("all feeds closed: %+v", w.Interest())
	}

	// A subscription that's only the inbox doesn't make anything watched.
	w2 := NewWatch()
	w2.add("inbox", 1)
	if w2.Interest().Watched {
		t.Fatal("the inbox alone isn't watching")
	}
	w2.add("metrics", 1)
	if i := w2.Interest(); !i.Watched || !i.Metrics {
		t.Fatalf("metrics = %+v", i)
	}
}
