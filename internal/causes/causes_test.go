package causes

import (
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/topics"
)

var t0 = time.Date(2026, 10, 3, 14, 3, 0, 0, time.UTC)

type home2 struct {
	t   *testing.T
	doc graph.Doc
}

func newDoc(t *testing.T) *home2 {
	return &home2{t: t, doc: graph.Doc{Resources: map[graph.RID]*graph.Resource{}, Relations: map[string]*graph.Relation{}}}
}

// add adds a resource with a health; kv are attrs.
func (h *home2) add(id, kind, name string, health graph.Health, kv ...any) *home2 {
	r := &graph.Resource{ID: graph.RID(id), Kind: graph.Kind(kind), Name: name, Health: health, Attrs: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Attrs[kv[i].(string)] = kv[i+1]
	}
	h.doc.Resources[r.ID] = r
	return h
}

func (h *home2) rel(from, typ, to string) *home2 {
	r := &graph.Relation{From: graph.RID(from), Type: graph.RelType(typ), To: graph.RID(to)}
	h.doc.Relations[r.Key()] = r
	return h
}

func (h *home2) find(opts ...func(*Input)) map[graph.RID][]topics.Cause {
	in := Input{Doc: h.doc, Now: t0.Add(2 * time.Minute)}
	for _, o := range opts {
		o(&in)
	}
	return Find(in)
}

func since(t time.Time) string { return t.Format(time.RFC3339) }

// arr is Sonarr, which depends on qBittorrent, which runs in a container.
func arr(t *testing.T) *home2 {
	return newDoc(t).
		add("app:sonarr", "app", "Sonarr", graph.HealthAttention, integration.AttrHealthSince, since(t0.Add(time.Minute)),
			integration.AttrHealthReason, "can't reach qBittorrent").
		add("app:qbit", "app", "qBittorrent", graph.HealthDown, integration.AttrHealthSince, since(t0)).
		add("ct:qbit", "container", "qbittorrent", graph.HealthDown, "oom_killed", true, "exit_code", 137.0, "finished_at", since(t0)).
		rel("app:sonarr", "depends_on", "app:qbit").
		rel("app:qbit", "runs_on", "ct:qbit")
}

// The demo moment: the root first, the app in between left out, and the
// sentence ends at the app its container is named for.
func TestOOMIsTheRoot(t *testing.T) {
	got := arr(t).find()["app:sonarr"]
	if len(got) != 1 {
		t.Fatalf("causes: %+v", got)
	}
	c := got[0]
	if c.Resource != "ct:qbit" || c.Rule != RuleOOMKilled || !c.At.Equal(t0) {
		t.Fatalf("cause: %+v", c)
	}
	if want := "qBittorrent's container was OOM-killed at 14:03. Sonarr depends on qBittorrent."; c.Text != want {
		t.Fatalf("text %q, want %q", c.Text, want)
	}
	if c.Args["what"] != "qBittorrent's container was OOM-killed at 14:03." || c.Args["why"] != "Sonarr depends on qBittorrent." {
		t.Fatalf("args: %+v", c.Args)
	}
	if len(c.Path) != 2 || c.Path[0].Type != "depends_on" || c.Path[1].To != "ct:qbit" || c.Message != "cause.oom_killed" {
		t.Fatalf("path/message: %+v", c)
	}
}

// A compose stack is its containers' worst health: never why one of them,
// or what runs in it, is in trouble.
func TestStackIsNoCause(t *testing.T) {
	h := arr(t).
		add("stack:media", "stack", "media", graph.HealthAttention, integration.AttrHealthSince, since(t0),
			integration.AttrHealthReason, "qbittorrent: OOM-killed").
		rel("ct:qbit", "part_of", "stack:media")
	got := h.find()
	if c := got["app:sonarr"]; len(c) != 1 || c[0].Resource != "ct:qbit" {
		t.Fatalf("Sonarr's causes: %+v", c)
	}
	if c := got["ct:qbit"]; len(c) != 0 {
		t.Fatalf("the container blamed on its own stack: %+v", c)
	}
}

// Each relation the walk follows, and provides_network_for backwards.
func TestEachRelation(t *testing.T) {
	for _, c := range []struct{ rel, from, to, want string }{
		{"runs_on", "s", "x", "S runs on X."},
		{"part_of", "s", "x", "S is part of X."},
		{"depends_on", "s", "x", "S depends on X."},
		{"stores_on", "s", "x", "S stores on X."},
		{"routes_to", "s", "x", "S routes to X."},
		{"monitors", "s", "x", "S watches X."},
		{"provides_network_for", "x", "s", "S gets its network from X."},
	} {
		h := newDoc(t).add("s", "app", "S", graph.HealthDown).add("x", "host", "X", graph.HealthDown).rel(c.from, c.rel, c.to)
		got := h.find()["s"]
		if len(got) != 1 || !strings.HasSuffix(got[0].Text, c.want) || got[0].Rule != RuleHealth {
			t.Errorf("%s: %+v", c.rel, got)
		}
	}
	// Not the other way: a host isn't explained by what runs on it.
	h := newDoc(t).add("s", "app", "S", graph.HealthDown).add("x", "host", "X", graph.HealthDown).rel("s", "runs_on", "x")
	if got := h.find()["x"]; len(got) != 0 {
		t.Fatalf("x: %+v", got)
	}
}

// The walk goes through same_as: Unraid's view of a container leads to
// Docker's, and the cause is the primary.
func TestSameAs(t *testing.T) {
	h := newDoc(t).
		add("app:s", "app", "S", graph.HealthDown).
		add("unraid:ct", "container", "web", graph.HealthOK).
		add("docker:ct", "container", "web", graph.HealthOK, "started_at", since(t0.Add(-5*time.Minute))).
		add("docker:engine", "engine", "E", graph.HealthOK).
		rel("unraid:ct", "same_as", "docker:ct").
		rel("app:s", "runs_on", "unraid:ct").
		rel("docker:ct", "runs_on", "docker:engine")
	got := h.find(func(in *Input) { in.Now = t0 })["app:s"]
	if len(got) != 1 || got[0].Resource != "docker:ct" || got[0].Rule != RuleStarted {
		t.Fatalf("causes: %+v", got)
	}
	if want := "S's container started at 13:58."; got[0].Text != want {
		t.Fatalf("text %q, want %q", got[0].Text, want)
	}
}

// A change counts from 30 minutes before the symptom began to 2 after.
func TestWindowEdges(t *testing.T) {
	began := t0
	for _, c := range []struct {
		at   time.Duration
		hits bool
	}{{-Before, true}, {-Before - time.Second, false}, {After, true}, {After + time.Second, false}} {
		h := newDoc(t).
			add("s", "app", "S", graph.HealthDown, integration.AttrHealthSince, since(began)).
			add("ct", "container", "c", graph.HealthOK, "started_at", since(began.Add(c.at))).
			rel("s", "runs_on", "ct")
		got := h.find(func(in *Input) { in.Now = began.Add(10 * time.Minute) })["s"]
		if (len(got) == 1) != c.hits {
			t.Errorf("started %v from the start: %+v", c.at, got)
		}
	}
}

// The root is the one furthest along with no cause of its own; the rest
// follow, nearer first, then closer in time; at most three.
func TestRanking(t *testing.T) {
	h := newDoc(t).
		add("s", "app", "S", graph.HealthDown, integration.AttrHealthSince, since(t0)).
		add("a", "app", "A", graph.HealthOK).
		add("ct", "container", "a", graph.HealthOK, "started_at", since(t0.Add(-time.Minute))).
		add("host", "host", "H", graph.HealthAttention, integration.AttrHealthSince, since(t0.Add(-20*time.Minute)), integration.AttrHealthReason, "memory at 95%").
		add("b", "app", "B", graph.HealthDown).
		add("c", "app", "C", graph.HealthDown, integration.AttrHealthSince, since(t0.Add(-time.Minute))).
		add("d", "app", "D", graph.HealthDown).
		rel("s", "depends_on", "a").rel("a", "runs_on", "ct").rel("ct", "runs_on", "host").
		rel("s", "depends_on", "b").rel("s", "depends_on", "c").rel("s", "depends_on", "d")
	got := h.find()["s"]
	if len(got) != MaxCauses {
		t.Fatalf("%d causes: %+v", len(got), got)
	}
	if got[0].Resource != "host" || !strings.HasPrefix(got[0].Text, "H has needed attention (memory at 95%) since 13:43.") {
		t.Fatalf("root: %+v", got[0])
	}
	// Nearer first: C (down since a minute before), then B, and the
	// container two steps away doesn't make the three.
	if got[1].Resource != "c" || got[2].Resource != "b" {
		t.Fatalf("the rest: %+v", got)
	}
}

// depends_on both ways: the walk ends.
func TestCycle(t *testing.T) {
	h := newDoc(t).add("a", "app", "A", graph.HealthDown).add("b", "app", "B", graph.HealthDown).
		rel("a", "depends_on", "b").rel("b", "depends_on", "a")
	got := h.find()
	if len(got["a"]) != 1 || got["a"][0].Resource != "b" || len(got["b"]) != 1 || got["b"][0].Resource != "a" {
		t.Fatalf("causes: %+v", got)
	}
}

// The walk stops after three relations.
func TestDepth(t *testing.T) {
	h := newDoc(t).add("s", "app", "S", graph.HealthDown).add("1", "app", "1", graph.HealthOK).
		add("2", "app", "2", graph.HealthOK).add("3", "app", "3", graph.HealthOK).add("4", "app", "4", graph.HealthDown).
		rel("s", "depends_on", "1").rel("1", "depends_on", "2").rel("2", "depends_on", "3").rel("3", "depends_on", "4")
	if got := h.find()["s"]; len(got) != 0 {
		t.Fatalf("causes four steps away: %+v", got)
	}
}

// An ok resource with no open problem gets none, and neither do repairs,
// hints or security findings; an open problem makes a symptom of an ok
// resource, and names the problem as a cause.
func TestWhatGetsCauses(t *testing.T) {
	h := newDoc(t).add("s", "app", "S", graph.HealthOK).add("x", "host", "X", graph.HealthOK).
		add("r", "app", "R", graph.HealthOK).rel("s", "runs_on", "x").rel("r", "runs_on", "x")
	inc := func(class, cat, target string) integration.Incident {
		return integration.Incident{Status: integration.StatusOpen, Class: class, Category: cat, Target: target, Title: "Backups fail.", OpenedAt: t0}
	}
	got := h.find(func(in *Input) {
		in.Incidents = map[string]integration.Incident{
			"p": inc(integration.ClassProblem, "", "x"),
			"q": inc(integration.ClassRepair, "", "s"),
			"h": inc(integration.ClassProblem, integration.CategorySecurity, "r"),
		}
	})
	// x's problem is a cause for s and r (both run on it), but neither the
	// repair nor the finding makes a symptom: s and r are ok.
	if len(got) != 0 {
		t.Fatalf("causes for ok resources: %+v", got)
	}
	h.doc.Resources["s"].Health = graph.HealthDown
	got = h.find(func(in *Input) {
		in.Incidents = map[string]integration.Incident{"p": inc(integration.ClassProblem, "", "x")}
	})
	if c := got["s"]; len(c) != 1 || c[0].Rule != RuleIncident || c[0].Text != "X has a problem: Backups fail. S runs on X." {
		t.Fatalf("s: %+v", got)
	}
}

// History: a resource ok now that went down in the window is a cause.
func TestHistoryChange(t *testing.T) {
	h := newDoc(t).add("s", "app", "S", graph.HealthDown, integration.AttrHealthSince, since(t0)).
		add("x", "host", "X", graph.HealthOK).rel("s", "runs_on", "x")
	got := h.find(func(in *Input) {
		in.Changes = func(res string, from, to time.Time) []history.StateChange {
			if res != "x" {
				return nil
			}
			return []history.StateChange{{At: from, Status: "ok"}, {At: t0.Add(-4 * time.Minute), Status: "down"}, {At: t0.Add(-time.Minute), Status: "ok"}}
		}
	})["s"]
	if len(got) != 1 || got[0].Rule != RuleChanged || got[0].Text != "X went down at 13:59. S runs on X." {
		t.Fatalf("causes: %+v", got)
	}
}

// Remembered events outlive the graph: a restarted container no longer
// says it was OOM-killed.
func TestRememberedEvents(t *testing.T) {
	h := arr(t)
	ct := h.doc.Resources["ct:qbit"]
	ct.Health, ct.Attrs = graph.HealthOK, map[string]any{"started_at": since(t0.Add(time.Minute))}
	got := h.find(func(in *Input) { in.Events = map[graph.RID][]Event{"ct:qbit": {{Rule: RuleOOMKilled, At: t0}}} })["app:sonarr"]
	if len(got) == 0 || got[0].Rule != RuleOOMKilled {
		t.Fatalf("causes: %+v", got)
	}
}

// What began after the symptom (past the window's end) isn't its cause.
func TestLaterTroubleIsNoCause(t *testing.T) {
	h := newDoc(t).
		add("s", "vm", "S", graph.HealthDown, integration.AttrHealthSince, since(t0)).
		add("x", "host", "X", graph.HealthAttention, integration.AttrHealthSince, since(t0.Add(time.Hour))).
		add("i", "integration", "I", graph.HealthAttention).
		rel("s", "runs_on", "x").rel("i", "monitors", "s")
	got := h.find(func(in *Input) {
		in.Now = t0.Add(2 * time.Hour)
		in.Incidents = map[string]integration.Incident{"p": {Status: integration.StatusOpen, Class: integration.ClassProblem, Target: "x", Title: "X", OpenedAt: t0.Add(time.Hour)}}
	})
	if len(got) != 0 {
		t.Fatalf("causes: %+v", got)
	}
}

// Trouble doesn't pass through a dependency that's fine: Bazarr depends on
// Radarr, which is ok and unchanged, so Prowlarr's problem behind it is no
// cause of Bazarr's (seen on the dogfood home, 2026-10-31).
func TestNoBlameThroughAHealthyDependency(t *testing.T) {
	h := newDoc(t).
		add("app:bazarr", "app", "Bazarr", graph.HealthAttention, integration.AttrHealthSince, since(t0),
			integration.AttrHealthReason, "1 subtitle provider is throttled").
		add("app:radarr", "app", "Radarr", graph.HealthOK).
		add("app:prowlarr", "app", "Prowlarr", graph.HealthAttention, integration.AttrHealthSince, since(t0),
			integration.AttrHealthReason, "an indexer has no definition").
		rel("app:bazarr", "depends_on", "app:radarr").
		rel("app:radarr", "depends_on", "app:prowlarr")
	if c := h.find()["app:bazarr"]; len(c) != 0 {
		t.Fatalf("Bazarr blamed through a healthy Radarr: %+v", c)
	}
	// Radarr in trouble too: then it carries Prowlarr's.
	h.add("app:radarr", "app", "Radarr", graph.HealthAttention, integration.AttrHealthSince, since(t0.Add(time.Minute)))
	if c := h.find()["app:bazarr"]; len(c) == 0 || c[0].Resource != "app:prowlarr" {
		t.Fatalf("through an unhealthy Radarr: %+v", c)
	}
}

// Seen again after the hub was down (unknown, then attention) is no
// change: only leaving ok is. Otherwise every restart of the hub makes
// every standing problem look as if it began at once (dogfood home,
// 2026-10-31).
func TestHubRestartIsNoChange(t *testing.T) {
	h := newDoc(t).add("s", "app", "S", graph.HealthDown, integration.AttrHealthSince, since(t0)).
		add("x", "app", "X", graph.HealthOK).rel("s", "depends_on", "x")
	got := h.find(func(in *Input) {
		in.Changes = func(res string, from, to time.Time) []history.StateChange {
			if res != "x" {
				return nil
			}
			return []history.StateChange{{At: from, Status: "unknown"}, {At: t0.Add(-time.Minute), Status: "attention"}, {At: t0, Status: "ok"}}
		}
	})["s"]
	if len(got) != 0 {
		t.Fatalf("a restart read as a change: %+v", got)
	}
}
