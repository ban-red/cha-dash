package realtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/jsonpatch"
	"dash.cha.sh/internal/livedoc"
)

// Source resolves topic names to feeds. GraphSource serves the Home Graph,
// DocSource a live document (metrics, inbox), and Mux combines them.
type Source interface {
	// Open starts a feed for topic. The feed sends a non-blocking signal on
	// notify (buffer 1, possibly shared) whenever it has updates.
	Open(topic string, notify chan<- struct{}) (Feed, error)
}

// Feed is one topic's stream of updates.
type Feed interface {
	// Snapshot returns the topic's current document and discards pending
	// updates, which the snapshot already includes.
	Snapshot() (seq uint64, data any)
	// Drain returns pending patches relative to the last Snapshot or Drain.
	// It returns an error wrapping ErrResync if updates were lost.
	Drain() (seq uint64, patch []jsonpatch.Op, err error)
	Close()
}

var (
	ErrUnknownTopic = errors.New("realtime: unknown topic")
	ErrResync       = errors.New("realtime: feed fell behind")
)

// GraphSource serves the Home Graph:
//
//	graph                              every resource and relation
//	graph:kind/<kind>[,<kind>…]        resources of those kinds, and relations among them
//
// Lens topics (graph:lens/home) arrive with lenses.
type GraphSource struct {
	Graph *graph.Graph
	// MaxPending bounds each feed's undelivered operations before it lags.
	MaxPending int
	// View, when set, says how the subscribing viewer (its connection's
	// context) sees the graph: which resources (Filter, combined with the
	// topic's kinds) and how each looks (Project). Nil, or a zero
	// GraphView: the whole graph as it is.
	View func(ctx context.Context) GraphView
}

// GraphView is one viewer's view of the graph.
type GraphView struct {
	// Filter drops resources (and with them their relations); nil keeps all.
	Filter graph.Filter
	// Project returns what the viewer sees of a resource; it must not
	// modify its argument, and returns it unchanged when there's nothing
	// to hide. Nil shows resources as they are.
	Project func(*graph.Resource) *graph.Resource
}

// Open implements Source: the whole graph, whatever View says.
func (s GraphSource) Open(topic string, notify chan<- struct{}) (Feed, error) {
	return s.open(GraphView{}, topic, notify)
}

// OpenContext implements ContextSource: the graph as View shows it to the
// viewer in ctx.
func (s GraphSource) OpenContext(ctx context.Context, topic string, notify chan<- struct{}) (Feed, error) {
	var v GraphView
	if s.View != nil {
		v = s.View(ctx)
	}
	return s.open(v, topic, notify)
}

func (s GraphSource) open(v GraphView, topic string, notify chan<- struct{}) (Feed, error) {
	var f graph.Filter
	switch {
	case topic == "graph":
	case strings.HasPrefix(topic, "graph:kind/"):
		var kinds []graph.Kind
		for k := range strings.SplitSeq(strings.TrimPrefix(topic, "graph:kind/"), ",") {
			if k == "" {
				return nil, fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
			}
			kinds = append(kinds, graph.Kind(k))
		}
		f = graph.KindFilter(kinds...)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
	}
	if v.Filter != nil {
		if kinds := f; kinds != nil {
			f = func(r *graph.Resource) bool { return kinds(r) && v.Filter(r) }
		} else {
			f = v.Filter
		}
	}
	feed := graphFeed{s.Graph.Watch(f, notify, s.MaxPending)}
	if v.Project != nil {
		return projectedGraphFeed{feed, v.Project}, nil
	}
	return feed, nil
}

// projectedGraphFeed shows each resource through project: in snapshots,
// and in the whole resources diffs carry (graph diffs replace resources
// whole, never fields).
type projectedGraphFeed struct {
	graphFeed
	project func(*graph.Resource) *graph.Resource
}

func (f projectedGraphFeed) Snapshot() (uint64, any) {
	seq, doc := f.w.Snapshot()
	return seq, ProjectGraph(doc, f.project)
}

func (f projectedGraphFeed) Drain() (uint64, []jsonpatch.Op, error) {
	seq, ops, err := f.graphFeed.Drain()
	for i, op := range ops {
		if r, ok := op.Value.(*graph.Resource); ok && strings.HasPrefix(op.Path, "/resources/") {
			ops[i].Value = f.project(r)
		}
	}
	return seq, ops, err
}

// ProjectGraph returns doc with every resource passed through project
// (relations as they are). doc's resources are shared: project must copy
// what it changes.
func ProjectGraph(doc graph.Doc, project func(*graph.Resource) *graph.Resource) graph.Doc {
	if project == nil {
		return doc
	}
	out := graph.Doc{Resources: make(map[graph.RID]*graph.Resource, len(doc.Resources)), Relations: doc.Relations}
	for id, r := range doc.Resources {
		out.Resources[id] = project(r)
	}
	return out
}

type graphFeed struct{ w *graph.Watcher }

func (f graphFeed) Snapshot() (uint64, any) {
	seq, doc := f.w.Snapshot()
	return seq, doc
}

func (f graphFeed) Drain() (uint64, []jsonpatch.Op, error) {
	seq, ops, err := f.w.Drain()
	if errors.Is(err, graph.ErrLagged) {
		return 0, nil, fmt.Errorf("%w: %w", ErrResync, err)
	}
	return seq, ops, err
}

func (f graphFeed) Close() { f.w.Close() }

// Mux routes each topic to the source registered under its name: the part
// before the first ':', so "graph:kind/vm" goes to the "graph" source.
type Mux map[string]Source

// Open implements Source.
func (m Mux) Open(topic string, notify chan<- struct{}) (Feed, error) {
	name, _, _ := strings.Cut(topic, ":")
	src, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
	}
	return src.Open(topic, notify)
}

// DocSource serves a live document as a single topic. Patch paths are
// relative to the document, like the graph's.
type DocSource struct {
	Doc   *livedoc.Doc
	Topic string
	// MaxPending bounds each feed's undelivered operations before it lags.
	MaxPending int
}

// Open implements Source.
func (s DocSource) Open(topic string, notify chan<- struct{}) (Feed, error) {
	if topic != s.Topic {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
	}
	return docFeed{s.Doc.Watch(notify, s.MaxPending)}, nil
}

type docFeed struct{ w *livedoc.Watcher }

func (f docFeed) Snapshot() (uint64, any) { return f.w.Snapshot() }

func (f docFeed) Drain() (uint64, []jsonpatch.Op, error) {
	seq, ops, err := f.w.Drain()
	if errors.Is(err, livedoc.ErrLagged) {
		return 0, nil, fmt.Errorf("%w: %w", ErrResync, err)
	}
	return seq, ops, err
}

func (f docFeed) Close() { f.w.Close() }
