// Package topics is the set of realtime topics the hub serves, and the shape
// of each topic's document:
//
//	graph     {"resources": {id: resource}, "relations": {key: relation}}
//	metrics   {"series": {"<resource id>#<metric key>": series}}
//	inbox     {"now": RFC 3339, "incidents": {id: incident}}
//
// graph also answers graph:kind/<kind>[,<kind>…] (see realtime.GraphSource).
// The action engine adds tasks ({"tasks": {id: task}}, filtered per viewer)
// through SourceWith, and the hub replaces inbox with InboxSource, which
// gives the household role its projection (household.go).
// Series and incidents use the Demo Home simulator's JSON shapes (see
// sim/README.md). Demo mode fills metrics and inbox from the simulator;
// outside it they start empty, and real integrations will write the same
// shapes.
package topics

import (
	"context"
	"maps"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/realtime"
)

// Topic names.
const (
	Graph   = "graph"
	Metrics = "metrics"
	Inbox   = "inbox"
)

// Document members.
const (
	Series    = "series"    // metrics: collection keyed by SeriesKey
	Now       = "now"       // inbox: the clock incidents are relative to
	Incidents = "incidents" // inbox: collection keyed by incident ID
)

// Set is the hub's live state, one document per topic.
type Set struct {
	Graph   *graph.Graph
	Metrics *livedoc.Doc
	Inbox   *livedoc.Doc
}

// New returns a set around g with empty metrics and an empty inbox whose
// clock reads now.
func New(g *graph.Graph, now time.Time) *Set {
	s := &Set{Graph: g, Metrics: livedoc.New(Series), Inbox: livedoc.New(Incidents)}
	s.Inbox.Update(func(tx *livedoc.Tx) error {
		SetNow(tx, now)
		return nil
	})
	return s
}

// Source routes every topic in the set.
func (s *Set) Source() realtime.Source {
	return realtime.Mux{
		Graph:   realtime.GraphSource{Graph: s.Graph},
		Metrics: realtime.DocSource{Doc: s.Metrics, Topic: Metrics},
		Inbox:   realtime.DocSource{Doc: s.Inbox, Topic: Inbox},
	}
}

// SourceWith routes every topic in the set plus extra, topics other
// packages own (the action engine's tasks).
func (s *Set) SourceWith(extra realtime.Mux) realtime.Source {
	m := s.Source().(realtime.Mux)
	maps.Copy(m, extra)
	return m
}

// SeriesKey is a metric series' key in the metrics document.
func SeriesKey(resource, metric string) string { return resource + "#" + metric }

// SetNow stages the inbox clock: UTC, whole seconds, RFC 3339.
func SetNow(tx *livedoc.Tx, t time.Time) {
	tx.Set(Now, t.UTC().Truncate(time.Second).Format(time.RFC3339))
}

// RunClock keeps the inbox clock current until ctx ends. Demo mode doesn't
// need it: there the simulator's clock is the inbox clock.
func (s *Set) RunClock(ctx context.Context, every time.Duration, now func() time.Time) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Inbox.Update(func(tx *livedoc.Tx) error {
				SetNow(tx, now())
				return nil
			})
		}
	}
}
