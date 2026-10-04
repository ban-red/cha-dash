package realtime

import (
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
}

// Open implements Source.
func (s GraphSource) Open(topic string, notify chan<- struct{}) (Feed, error) {
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
	return graphFeed{s.Graph.Watch(f, notify, s.MaxPending)}, nil
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
