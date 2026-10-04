package correlate

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/topics"
)

var t0 = time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// live loads a synthetic home into a topic set the way the hub holds it.
func live(t *testing.T, h *home) (*topics.Set, *Correlator, *clock) {
	t.Helper()
	g := graph.New()
	if _, err := g.Update(func(tx *graph.Tx) error {
		for _, r := range h.doc.Resources {
			if err := tx.Upsert(*r); err != nil {
				return err
			}
		}
		for _, rel := range h.doc.Relations {
			if err := tx.Relate(*rel); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: t0}
	set := topics.New(g, t0)
	return set, New(Options{Topics: set, Now: clk.now}), clk
}

func syncOK(t *testing.T, c *Correlator) {
	t.Helper()
	if err := c.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func incident(t *testing.T, set *topics.Set, id string) (integration.Incident, bool) {
	t.Helper()
	b := set.Inbox.Item(topics.Incidents, id)
	if b == nil {
		return integration.Incident{}, false
	}
	var in integration.Incident
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	return in, true
}

func relation(set *topics.Set, from string, typ graph.RelType, to string) (graph.Relation, bool) {
	_, doc := set.Graph.Snapshot(nil)
	rel, ok := doc.Relations[graph.Relation{From: graph.RID(from), Type: typ, To: graph.RID(to)}.Key()]
	if !ok {
		return graph.Relation{}, false
	}
	return *rel, true
}

func TestSyncPublishesAndConverges(t *testing.T) {
	set, c, _ := live(t, jupiter(t))
	syncOK(t, c)
	rel, ok := relation(set, "unraid:i_u/container/sonarr", SameAs, "docker:i_d/container/sonarr")
	if !ok || rel.Source != Source {
		t.Fatalf("same_as = %+v, %v", rel, ok)
	}
	if _, ok := relation(set, "docker:i_d/engine", RunsOn, "unraid:i_u/host"); !ok {
		t.Fatal("no engine placement")
	}
	// The sources' relations are untouched.
	if rel, ok := relation(set, "docker:i_d/container/sonarr", "runs_on", "docker:i_d/engine"); !ok || rel.Source != "integration:i_d" {
		t.Fatalf("source relation = %+v, %v", rel, ok)
	}
	v := set.Graph.Version()
	syncOK(t, c)
	syncOK(t, c)
	if set.Graph.Version() != v {
		t.Fatal("a pass with nothing new changed the graph")
	}

	// Unraid stops reporting the container: the merge goes with it, and
	// the Docker container stays.
	if _, err := set.Graph.Update(func(tx *graph.Tx) error { tx.Remove("unraid:i_u/container/sonarr"); return nil }); err != nil {
		t.Fatal(err)
	}
	syncOK(t, c)
	if _, ok := relation(set, "unraid:i_u/container/radarr", SameAs, "docker:i_d/container/radarr"); !ok {
		t.Fatal("the other merge went too")
	}
	if len(c.Links()) != len(Correlate(snapshot(set), nil).Links) {
		t.Fatal("Links() is stale")
	}
}

func snapshot(set *topics.Set) graph.Doc {
	_, doc := set.Graph.Snapshot(nil)
	return doc
}

func proxyHop(t *testing.T) *home {
	return jupiter(t).
		add("docker:i_d/container/nginx-proxy", "container", "nginx-proxy", "container_id", "cc"+fullID[2:12], "published", []string{"7780->80/tcp"}).
		rel("docker:i_d/container/nginx-proxy", "runs_on", "docker:i_d/engine").
		add("integration:i_s", "integration", "Sonarr").
		add("sonarr:i_s/app", "app", "Sonarr", "type", "sonarr", "url", "https://jupiter.lab.example/sonarr")
}

func TestSuggestionLifecycle(t *testing.T) {
	set, c, clk := live(t, proxyHop(t))
	syncOK(t, c)
	ss := c.Suggestions()
	if len(ss) != 1 || ss[0].From != "sonarr:i_s/app" || ss[0].To != "docker:i_d/container/sonarr" || ss[0].Type != RunsOn {
		t.Fatalf("suggestions = %+v", ss)
	}
	id := ss[0].ID
	in, ok := incident(t, set, id)
	if !ok || in.Class != ClassSuggestion || in.Severity != integration.SeverityInfo || in.Status != integration.StatusOpen ||
		in.Title != "These look like the same thing. Merge?" || in.Suggested.ID != ActionConfirm || in.Alternatives[0].ID != ActionReject ||
		in.Target != "sonarr:i_s/app" || !strings.Contains(in.Summary, "Sonarr (Sonarr) and the container sonarr") {
		t.Fatalf("suggestion in the inbox = %+v", in)
	}
	v := set.Inbox.Version()
	syncOK(t, c)
	if set.Inbox.Version() != v {
		t.Fatal("re-suggesting changed the inbox")
	}

	if _, err := c.DecideSuggestion(t.Context(), "correlate.nope", Confirm, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown suggestion: %v", err)
	}
	clk.advance(time.Minute)
	d, err := c.DecideSuggestion(t.Context(), id, Confirm, "u_owner")
	if err != nil || d.Verdict != Confirm || d.Rule != "name" || d.ID == "" {
		t.Fatalf("decide = %+v, %v", d, err)
	}
	if _, ok := relation(set, "sonarr:i_s/app", RunsOn, "docker:i_d/container/sonarr"); !ok {
		t.Fatal("the confirmed link isn't in the graph")
	}
	in, _ = incident(t, set, id)
	if in.Status != integration.StatusResolved || in.ResolvedBy != "action" || in.Resolution == "" || !in.ResolvedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("decided suggestion = %+v", in)
	}
	if len(c.Suggestions()) != 0 {
		t.Fatal("a decided suggestion is still listed")
	}
	// Answering twice is a 404: it isn't open any more.
	if _, err := c.DecideSuggestion(t.Context(), id, Reject, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second answer: %v", err)
	}

	// Forgetting the decision brings the question back.
	ds, _ := c.Decisions(t.Context())
	if len(ds) != 1 {
		t.Fatalf("decisions = %+v", ds)
	}
	if err := c.Forget(t.Context(), ds[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(t.Context(), ds[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgetting twice: %v", err)
	}
	if in, _ := incident(t, set, id); in.Status != integration.StatusOpen {
		t.Fatalf("after forgetting: %+v", in)
	}
	if _, ok := relation(set, "sonarr:i_s/app", RunsOn, "docker:i_d/container/sonarr"); ok {
		t.Fatal("the forgotten link is still in the graph")
	}

	// Rejected, it resolves and stays away; resolved items go after a day.
	if _, err := c.DecideSuggestion(t.Context(), id, Reject, ""); err != nil {
		t.Fatal(err)
	}
	if in, _ := incident(t, set, id); in.Status != integration.StatusResolved || !strings.Contains(in.Resolution, "Kept separate") {
		t.Fatalf("rejected = %+v", in)
	}
	clk.advance(ResolvedRetention + time.Minute)
	syncOK(t, c)
	if _, ok := incident(t, set, id); ok {
		t.Fatal("the resolved suggestion outstayed its day")
	}
	if len(c.Suggestions()) != 0 {
		t.Fatal("a rejected pair is suggested again")
	}
}

// A suggestion whose evidence goes away leaves the inbox.
func TestSuggestionWithdrawn(t *testing.T) {
	set, c, _ := live(t, proxyHop(t))
	syncOK(t, c)
	id := c.Suggestions()[0].ID
	if _, err := set.Graph.Update(func(tx *graph.Tx) error { tx.Remove("sonarr:i_s/app"); return nil }); err != nil {
		t.Fatal(err)
	}
	syncOK(t, c)
	if _, ok := incident(t, set, id); ok || len(c.Suggestions()) != 0 {
		t.Fatal("the suggestion stayed")
	}
}

func TestDecideValidates(t *testing.T) {
	_, c, _ := live(t, jupiter(t))
	ctx := t.Context()
	for _, tc := range []struct {
		from, to string
		typ      graph.RelType
		v        Verdict
		want     error
	}{
		{"docker:i_d/engine", "unraid:i_u/host", "owns", Confirm, ErrInvalid},
		{"docker:i_d/engine", "unraid:i_u/host", RunsOn, "maybe", ErrInvalid},
		{"docker:i_d/engine", "docker:i_d/engine", SameAs, Confirm, ErrInvalid},
		{"docker:i_d/engine", "nope:x", RunsOn, Confirm, ErrNotFound},
	} {
		if _, err := c.Decide(ctx, graph.RID(tc.from), tc.typ, graph.RID(tc.to), tc.v, ""); !errors.Is(err, tc.want) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	// Splitting an automatic merge.
	d, err := c.Decide(ctx, "unraid:i_u/container/sonarr", SameAs, "docker:i_d/container/sonarr", Reject, "")
	if err != nil || d.From != "docker:i_d/container/sonarr" { // same_as pairs are stored sorted
		t.Fatalf("decide = %+v, %v", d, err)
	}
	for _, l := range c.Links() {
		if l.From == "unraid:i_u/container/sonarr" && l.Type == SameAs {
			t.Fatalf("still merged: %+v", l)
		}
	}
	// Deciding the same pair again replaces the decision.
	if d2, err := c.Decide(ctx, "docker:i_d/container/sonarr", SameAs, "unraid:i_u/container/sonarr", Confirm, ""); err != nil || d2.ID != d.ID {
		t.Fatalf("re-decide = %+v, %v", d2, err)
	}
	if ds, _ := c.Decisions(ctx); len(ds) != 1 || ds[0].Verdict != Confirm {
		t.Fatalf("decisions = %+v", ds)
	}
}

// backups is the spike's story: PBS on jupiter, two PVE instances whose
// nightly jobs write to it, every instance with its own emitter.
type backups struct {
	set           *topics.Set
	c             *Correlator
	clk           *clock
	pbs, mars, pl *runtime.Emitter
}

func newBackups(t *testing.T) *backups {
	clk := &clock{t: t0}
	set := topics.New(graph.New(), t0)
	b := &backups{set: set, c: New(Options{Topics: set, Now: clk.now}), clk: clk,
		pbs: runtime.NewEmitter(set, "i_b", clk.now), mars: runtime.NewEmitter(set, "i_m", clk.now), pl: runtime.NewEmitter(set, "i_p", clk.now)}
	for e, name := range map[*runtime.Emitter]string{b.pbs: "PBS", b.mars: "mars", b.pl: "pluto"} {
		if err := e.PublishSelf(integration.Resource{Kind: "integration", Name: name, Attrs: map[string]any{"status": "ok"},
			Health: integration.Health{Status: integration.HealthOK}}); err != nil {
			t.Fatal(err)
		}
	}
	ok := integration.Health{Status: integration.HealthOK}
	if err := b.pbs.Replace("", integration.Facts{Resources: []integration.Resource{
		{ID: "pbs:i_b/app", Kind: "app", Name: "PBS", Attrs: map[string]any{"type": "pbs"}, Health: ok,
			Links: []integration.Link{{Title: "Proxmox Backup Server", URL: "https://10.0.11.120:8007/"}}},
		{ID: "pbs:i_b/datastore/mars", Kind: "backup_target", Name: "mars", Attrs: map[string]any{"store": "mars"},
			Health: integration.Health{Status: integration.HealthAttention, Reason: "92% used · full in about 3 weeks"}},
		{ID: "pbs:i_b/datastore/pluto", Kind: "backup_target", Name: "pluto", Attrs: map[string]any{"store": "pluto"}, Health: ok},
	}, Relations: []integration.Relation{
		{From: "pbs:i_b/datastore/mars", Type: "part_of", To: "pbs:i_b/app"},
		{From: "pbs:i_b/datastore/pluto", Type: "part_of", To: "pbs:i_b/app"},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		e          *runtime.Emitter
		inst, node string
	}{{b.mars, "i_m", "mars"}, {b.pl, "i_p", "pluto"}} {
		stor, job := "proxmox:"+p.inst+"/storage/pbs-"+p.node, "proxmox:"+p.inst+"/backup/daily"
		if err := p.e.Replace("", integration.Facts{Resources: []integration.Resource{
			{ID: stor, Kind: "storage", Name: "pbs-" + p.node, Attrs: map[string]any{"pbs.server": "10.0.11.120", "pbs.datastore": p.node}, Health: ok},
			{ID: job, Kind: "backup_job", Name: "daily 03:00", Health: integration.Health{Status: integration.HealthAttention}},
		}, Relations: []integration.Relation{{From: job, Type: "stores_on", To: stor}}}); err != nil {
			t.Fatal(err)
		}
		b.raiseBackup(t, p.e, p.inst, p.node, "")
	}
	return b
}

func (b *backups) raiseBackup(t *testing.T, e *runtime.Emitter, inst, node, extra string) {
	t.Helper()
	stor, job := "proxmox:"+inst+"/storage/pbs-"+node, "proxmox:"+inst+"/backup/daily"
	if err := e.Raise(integration.Incident{ID: "backup.daily", Scenario: "backup-target-unreachable", Class: integration.ClassProblem,
		Severity: integration.SeverityAttention, Title: "Backups on " + node + " couldn't reach pbs-" + node, Summary: "Nothing was backed up." + extra,
		Target: job, Facts: map[string]any{"endpoint": "10.0.11.120:8007", "cause": "target_unreachable"},
		Causes: []integration.Cause{{Text: "pbs-" + node + " didn't answer at 03:00 (No route to host).", Resource: stor}}}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupFailuresShareTheirPBS(t *testing.T) {
	b := newBackups(t)
	syncOK(t, b.c)
	if _, ok := relation(b.set, "proxmox:i_m/storage/pbs-mars", StoresOn, "pbs:i_b/datastore/mars"); !ok {
		t.Fatal("the storage isn't joined to its datastore")
	}
	mars, _ := incident(t, b.set, "i_m.backup.daily")
	if len(mars.Causes) != 3 {
		t.Fatalf("mars causes = %+v", mars.Causes)
	}
	// The datastore it writes to is nearly full (its health says so).
	if c := mars.Causes[0]; c.Resource != "pbs:i_b/datastore/mars" || !strings.Contains(c.Text, "PBS datastore mars on PBS") || !strings.Contains(c.Text, "92% used") {
		t.Fatalf("root cause = %+v", c)
	}
	// Pluto's backups to the same PBS failed too.
	if c := mars.Causes[1]; c.Resource != "proxmox:i_p/backup/daily" || !strings.Contains(c.Text, "Backups on pluto to the same PBS failed too") {
		t.Fatalf("peer cause = %+v", c)
	}
	if mars.Causes[2].Resource != "proxmox:i_m/storage/pbs-mars" {
		t.Fatalf("the instance's own cause moved: %+v", mars.Causes)
	}
	// Pluto's datastore is fine: only the peer.
	pluto, _ := incident(t, b.set, "i_p.backup.daily")
	if len(pluto.Causes) != 2 || pluto.Causes[0].Resource != "proxmox:i_m/backup/daily" {
		t.Fatalf("pluto causes = %+v", pluto.Causes)
	}

	// PBS raises the filling incident on the shared filesystem: it becomes
	// the cause for both, and gains the jobs in its blast radius.
	if err := b.pbs.Raise(integration.Incident{ID: "filling.mars", Scenario: "pbs-datastore-filling", Class: integration.ClassProblem,
		Severity: integration.SeverityAttention, Title: "PBS datastores are filling up", Summary: "92% full.", Target: "pbs:i_b/datastore/mars",
		Facts: map[string]any{"stores": []string{"mars", "pluto"}, "used_pct": 92}}); err != nil {
		t.Fatal(err)
	}
	syncOK(t, b.c)
	pluto, _ = incident(t, b.set, "i_p.backup.daily")
	if c := pluto.Causes[0]; c.Resource != "pbs:i_b/datastore/pluto" || !strings.Contains(c.Text, "has a problem of its own: PBS datastores are filling up") {
		t.Fatalf("pluto root cause = %+v", pluto.Causes)
	}
	filling, _ := incident(t, b.set, "i_b.filling.mars")
	if len(filling.BlastRadius) != 2 || filling.BlastRadius[0].Resource != "proxmox:i_m/backup/daily" || filling.BlastRadius[1].Via != "stores_on" {
		t.Fatalf("filling blast radius = %+v", filling.BlastRadius)
	}
	if len(filling.Causes) != 0 {
		t.Fatalf("the root incident gained causes: %+v", filling.Causes)
	}

	// The instance rewrites its incident: the links come back, once.
	b.raiseBackup(t, b.mars, "i_m", "mars", " Again.")
	syncOK(t, b.c)
	mars, _ = incident(t, b.set, "i_m.backup.daily")
	if len(mars.Causes) != 3 || !strings.HasSuffix(mars.Summary, "Again.") || mars.Causes[2].Resource != "proxmox:i_m/storage/pbs-mars" {
		t.Fatalf("after a rewrite: %+v", mars)
	}
	v := b.set.Inbox.Version()
	syncOK(t, b.c)
	if b.set.Inbox.Version() != v {
		t.Fatal("a pass with nothing new changed the inbox")
	}
	// The instance's own idempotent re-raise doesn't disturb it either.
	b.raiseBackup(t, b.mars, "i_m", "mars", " Again.")
	if b.set.Inbox.Version() != v {
		t.Fatal("the emitter re-published an unchanged incident")
	}

	// Pluto's backup succeeds: its incident resolves, untouched, and mars
	// no longer lists it.
	if err := b.pl.Resolve("backup.daily", "The next run succeeded."); err != nil {
		t.Fatal(err)
	}
	syncOK(t, b.c)
	pluto, _ = incident(t, b.set, "i_p.backup.daily")
	if pluto.Status != integration.StatusResolved || len(pluto.Causes) != 1 {
		t.Fatalf("resolved pluto = %+v", pluto)
	}
	mars, _ = incident(t, b.set, "i_m.backup.daily")
	if len(mars.Causes) != 2 || mars.Causes[0].Resource != "pbs:i_b/datastore/mars" {
		t.Fatalf("mars after pluto resolved = %+v", mars.Causes)
	}
	filling, _ = incident(t, b.set, "i_b.filling.mars")
	if len(filling.BlastRadius) != 2 {
		t.Fatalf("filling blast radius = %+v", filling.BlastRadius) // the jobs still write there
	}
}

// Without a PBS instance, two PVE instances failing to reach one endpoint
// still explain each other.
func TestBackupFailuresShareAnEndpoint(t *testing.T) {
	b := newBackups(t)
	b.pbs.Clear()
	syncOK(t, b.c)
	mars, _ := incident(t, b.set, "i_m.backup.daily")
	if len(mars.Causes) != 2 || mars.Causes[0].Resource != "proxmox:i_p/backup/daily" || !strings.Contains(mars.Causes[0].Text, "can't reach 10.0.11.120:8007 either") {
		t.Fatalf("mars causes = %+v", mars.Causes)
	}
}

// A PBS that Cha Dash can't read either is a cause too, even when its
// last-known datastore looks fine.
func TestBackupFailureAndUnreadablePBS(t *testing.T) {
	b := newBackups(t)
	b.mars.Clear()
	if err := b.pbs.PublishSelf(integration.Resource{Kind: "integration", Name: "PBS", Attrs: map[string]any{"status": "error"},
		Health: integration.Health{Status: integration.HealthAttention}}); err != nil {
		t.Fatal(err)
	}
	syncOK(t, b.c)
	pluto, _ := incident(t, b.set, "i_p.backup.daily")
	if len(pluto.Causes) != 2 || !strings.Contains(pluto.Causes[0].Text, "Cha Dash can't read that PBS either (error)") {
		t.Fatalf("pluto causes = %+v", pluto.Causes)
	}
}
