package runtime

import (
	"context"
	"slices"
	"strings"
	"sync"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/topics"
)

// Watch tracks what browser tabs are subscribed to, so coordinators poll at
// full rate only while someone is looking. It wraps the realtime Source and
// counts open feeds per topic.
//
// It's deliberately coarse for v0.1: "watched" is hub-wide. Any tab on the
// graph or the metrics topic makes every instance watched; graph:kind/…
// filters narrow Interest.Kinds, which integrations may use to skip work.
// A metrics:resource/<id> feed (a resource's sheet) lists that resource in
// Interest.Resources. Per-widget interest needs lens topics (graph:lens/…),
// which arrive with lenses.
type Watch struct {
	mu      sync.Mutex
	open    map[string]int // topic → open feeds
	current integration.Interest
	changed chan struct{} // closed when current changes, then replaced
}

// NewWatch returns a tracker with nobody watching.
func NewWatch() *Watch {
	return &Watch{open: map[string]int{}, changed: make(chan struct{})}
}

// Source wraps src so every feed it opens is counted until it's closed.
func (w *Watch) Source(src realtime.Source) realtime.Source { return watchedSource{src: src, w: w} }

// Interest returns what's being watched right now.
func (w *Watch) Interest() integration.Interest {
	w.mu.Lock()
	defer w.mu.Unlock()
	i := w.current
	i.Kinds = slices.Clone(i.Kinds)
	i.Resources = slices.Clone(i.Resources)
	return i
}

// Changed returns a channel that's closed the next time Interest changes.
func (w *Watch) Changed() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.changed
}

func (w *Watch) add(topic string, delta int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.open[topic] += delta
	if w.open[topic] <= 0 {
		delete(w.open, topic)
	}
	next := interestOf(w.open)
	if next.Watched == w.current.Watched && next.Metrics == w.current.Metrics &&
		slices.Equal(next.Kinds, w.current.Kinds) && slices.Equal(next.Resources, w.current.Resources) {
		return
	}
	w.current = next
	close(w.changed)
	w.changed = make(chan struct{})
}

// interestOf derives the Interest from the open topics.
func interestOf(open map[string]int) integration.Interest {
	var i integration.Interest
	all := false
	kinds := map[string]bool{}
	for topic := range open {
		switch {
		case topic == "graph":
			all = true
		case strings.HasPrefix(topic, "graph:kind/"):
			for k := range strings.SplitSeq(strings.TrimPrefix(topic, "graph:kind/"), ",") {
				kinds[k] = true
			}
		case strings.HasPrefix(topic, "graph:"):
			all = true // a view we can't narrow (lenses, later)
		case topic == "metrics":
			i.Metrics = true
		case strings.HasPrefix(topic, topics.ResourceMetrics):
			i.Metrics = true
			i.Resources = append(i.Resources, strings.TrimPrefix(topic, topics.ResourceMetrics))
		default:
			continue
		}
		i.Watched = true
	}
	slices.Sort(i.Resources)
	if !all && len(kinds) > 0 {
		for k := range kinds {
			i.Kinds = append(i.Kinds, k)
		}
		slices.Sort(i.Kinds)
	}
	return i
}

type watchedSource struct {
	src realtime.Source
	w   *Watch
}

func (s watchedSource) Open(topic string, notify chan<- struct{}) (realtime.Feed, error) {
	return s.OpenContext(context.Background(), topic, notify)
}

// OpenContext passes the viewer on (realtime.ContextSource).
func (s watchedSource) OpenContext(ctx context.Context, topic string, notify chan<- struct{}) (realtime.Feed, error) {
	f, err := realtime.OpenWith(ctx, s.src, topic, notify)
	if err != nil {
		return nil, err
	}
	s.w.add(topic, 1)
	return &watchedFeed{Feed: f, w: s.w, topic: topic}, nil
}

type watchedFeed struct {
	realtime.Feed
	w     *Watch
	topic string
	once  sync.Once
}

func (f *watchedFeed) Close() {
	f.once.Do(func() { f.w.add(f.topic, -1) })
	f.Feed.Close()
}
