package causes

import (
	"encoding/json"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/topics"
)

func loadGraph(t *testing.T, tp *topics.Set, doc graph.Doc) {
	t.Helper()
	if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
		for _, r := range doc.Resources {
			if err := tx.Upsert(*r); err != nil {
				return err
			}
		}
		for _, r := range doc.Relations {
			if err := tx.Relate(*r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func putIncident(t *testing.T, tp *topics.Set, in integration.Incident) {
	t.Helper()
	if _, err := tp.Inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Incidents, in.ID, in)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func incident(t *testing.T, tp *topics.Set, id string) integration.Incident {
	t.Helper()
	var in integration.Incident
	if err := json.Unmarshal(tp.Inbox.Item(topics.Incidents, id), &in); err != nil {
		t.Fatal(err)
	}
	return in
}

// The topic gets every symptom's causes; an open problem gains them after
// its own, once, and loses them when they go.
func TestServiceWritesTopicAndIncidents(t *testing.T) {
	now := t0.Add(2 * time.Minute)
	tp := topics.New(graph.New(), now)
	loadGraph(t, tp, arr(t).doc)
	own := integration.Cause{Text: "Sonarr can't reach its download client.", Resource: "app:sonarr"}
	putIncident(t, tp, integration.Incident{ID: "i_s.downloads", Class: integration.ClassProblem, Status: integration.StatusOpen,
		Title: "Sonarr can't download", Target: "app:sonarr", OpenedAt: t0, Causes: []integration.Cause{own}})
	putIncident(t, tp, integration.Incident{ID: "i_s.repair", Class: integration.ClassRepair, Status: integration.StatusOpen,
		Title: "Sonarr rejected its key", Target: "app:sonarr", OpenedAt: t0})
	s := New(Options{Topics: tp, Now: func() time.Time { return now }, Clock: func(t time.Time) string { return t.UTC().Format("15:04") }})
	for range 2 {
		if err := s.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := tp.Causes.Of("app:sonarr"); len(got) != 1 || got[0].Resource != "ct:qbit" {
		t.Fatalf("topic: %+v", got)
	}
	in := incident(t, tp, "i_s.downloads")
	if len(in.Causes) != 2 || in.Causes[0] != own || in.Causes[1].Resource != "ct:qbit" {
		t.Fatalf("incident causes: %+v", in.Causes)
	}
	if r := incident(t, tp, "i_s.repair"); len(r.Causes) != 0 {
		t.Fatalf("a repair got causes: %+v", r.Causes)
	}

	// qBittorrent restarts: the container no longer says OOM, but the
	// service remembers.
	if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: "ct:qbit", Kind: "container", Name: "qbittorrent", Health: graph.HealthOK, State: "running",
			Attrs: map[string]any{"started_at": since(t0.Add(time.Minute)), "restart_count": 1}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := tp.Causes.Of("app:sonarr"); len(got) == 0 || got[0].Rule != RuleOOMKilled {
		t.Fatalf("after the restart: %+v", got)
	}

	// Everything recovers: the topic empties and the incident is as its
	// instance wrote it.
	if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
		for _, id := range []graph.RID{"app:sonarr", "app:qbit"} {
			r, _ := tx.Get(id)
			r.Health = graph.HealthOK
			if err := tx.Upsert(r); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	putIncident(t, tp, integration.Incident{ID: "i_s.downloads", Class: integration.ClassProblem, Status: integration.StatusResolved,
		Title: "Sonarr can't download", Target: "app:sonarr", OpenedAt: t0, Causes: append([]integration.Cause{own}, in.Causes[1])})
	if err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := tp.Causes.Of("app:sonarr"); len(got) != 0 {
		t.Fatalf("topic after recovery: %+v", got)
	}
	if in := incident(t, tp, "i_s.downloads"); len(in.Causes) != 1 {
		t.Fatalf("resolved incident causes: %+v", in.Causes)
	}
}
