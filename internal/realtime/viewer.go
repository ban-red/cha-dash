package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"dash.cha.sh/internal/jsonpatch"
	"dash.cha.sh/internal/livedoc"
)

// ContextSource is a Source whose feeds depend on who is watching. The hub
// calls OpenContext with the connection's context, which carries the
// request's (the signed-in session's) values, instead of Open. Mux and
// wrappers forward it; OpenWith falls back to Open for a plain Source.
type ContextSource interface {
	Source
	OpenContext(ctx context.Context, topic string, notify chan<- struct{}) (Feed, error)
}

// OpenWith opens topic on src for the viewer in ctx.
func OpenWith(ctx context.Context, src Source, topic string, notify chan<- struct{}) (Feed, error) {
	if cs, ok := src.(ContextSource); ok {
		return cs.OpenContext(ctx, topic, notify)
	}
	return src.Open(topic, notify)
}

// OpenContext implements ContextSource.
func (m Mux) OpenContext(ctx context.Context, topic string, notify chan<- struct{}) (Feed, error) {
	name, _, _ := strings.Cut(topic, ":")
	src, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
	}
	return OpenWith(ctx, src, topic, notify)
}

// ViewerDocSource serves a live document whose collection Coll each viewer
// sees only part of: Visible, called once per subscription with its
// context, decides per item. Members other than Coll pass through.
//
// Visibility must not change over an item's life (a task's owner doesn't),
// so the feed decides once, when it first sees an item whole (in the
// snapshot, or added or replaced whole by a diff), and remembers: field ops
// and removals then pass exactly for the items the viewer has. A viewer
// therefore never gets an op for an item it doesn't hold, and its document
// stays a valid JSON Patch target.
type ViewerDocSource struct {
	Doc   *livedoc.Doc
	Topic string
	Coll  string
	// Visible returns the viewer's filter: whether the item with key and
	// encoded value is theirs to see.
	Visible func(ctx context.Context) func(key string, item json.RawMessage) bool
	// MaxPending bounds each feed's undelivered operations before it lags.
	MaxPending int
}

// Open implements Source. Without a viewer, Visible gets a bare context.
func (s ViewerDocSource) Open(topic string, notify chan<- struct{}) (Feed, error) {
	return s.OpenContext(context.Background(), topic, notify)
}

// OpenContext implements ContextSource.
func (s ViewerDocSource) OpenContext(ctx context.Context, topic string, notify chan<- struct{}) (Feed, error) {
	if topic != s.Topic {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
	}
	return &viewerFeed{w: s.Doc.Watch(notify, s.MaxPending), coll: s.Coll, visible: s.Visible(ctx), has: map[string]bool{}}, nil
}

type viewerFeed struct {
	w       *livedoc.Watcher
	coll    string
	visible func(key string, item json.RawMessage) bool
	has     map[string]bool // items the viewer holds; only the subscription's goroutine touches it
}

func (f *viewerFeed) Snapshot() (uint64, any) {
	seq, doc := f.w.Snapshot()
	clear(f.has)
	if items, ok := doc[f.coll].(map[string]json.RawMessage); ok {
		for k, v := range items {
			if f.visible(k, v) {
				f.has[k] = true
			} else {
				delete(items, k)
			}
		}
	}
	return seq, doc
}

func (f *viewerFeed) Drain() (uint64, []jsonpatch.Op, error) {
	seq, ops, err := f.w.Drain()
	if errors.Is(err, livedoc.ErrLagged) {
		return 0, nil, fmt.Errorf("%w: %w", ErrResync, err)
	}
	if err != nil || len(ops) == 0 {
		return seq, ops, err
	}
	prefix := jsonpatch.Pointer(f.coll) + "/"
	out := make([]jsonpatch.Op, 0, len(ops))
	for _, op := range ops {
		rest, ok := strings.CutPrefix(op.Path, prefix)
		if !ok {
			out = append(out, op)
			continue
		}
		key, field, _ := strings.Cut(rest, "/")
		key = strings.NewReplacer("~1", "/", "~0", "~").Replace(key)
		switch {
		case field != "":
			if f.has[key] {
				out = append(out, op)
			}
		case op.Op == jsonpatch.Remove:
			if f.has[key] {
				delete(f.has, key)
				out = append(out, op)
			}
		default: // the whole item, added or replaced
			if f.has[key] || f.visible(key, encoded(op.Value)) {
				if !f.has[key] {
					op.Op = jsonpatch.Add // the viewer didn't have it, so replace would fail
				}
				f.has[key] = true
				out = append(out, op)
			}
		}
	}
	return seq, out, nil
}

func (f *viewerFeed) Close() { f.w.Close() }

// ProjectedDocSource serves a live document whose collection Coll some
// viewers see through a projection: Project, called once per subscription
// with its context, returns the viewer's projection, or nil for the
// document as published. A projection turns an item into what the viewer
// may see, or says they don't see it at all; members other than Coll pass
// through.
//
// Unlike ViewerDocSource, what a viewer sees of an item may change over its
// life (an incident becomes relevant, or a field the projection reads
// changes). So for a projected viewer, each diff that touches an item
// re-reads it whole from the document, projects it, and sends the item
// whole when the projection changed: added when the viewer didn't have it,
// replaced when they did, removed when they no longer see it. Changes to
// fields the projection drops send nothing. The item read may be newer than
// the ops being drained; the next drain then finds nothing new to send, so
// every op applies to the viewer's document.
type ProjectedDocSource struct {
	Doc     *livedoc.Doc
	Topic   string
	Coll    string
	Project func(ctx context.Context) func(key string, item json.RawMessage) (json.RawMessage, bool)
	// MaxPending bounds each feed's undelivered operations before it lags.
	MaxPending int
}

// Open implements Source. Without a viewer, Project gets a bare context.
func (s ProjectedDocSource) Open(topic string, notify chan<- struct{}) (Feed, error) {
	return s.OpenContext(context.Background(), topic, notify)
}

// OpenContext implements ContextSource.
func (s ProjectedDocSource) OpenContext(ctx context.Context, topic string, notify chan<- struct{}) (Feed, error) {
	if topic != s.Topic {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
	}
	proj := s.Project(ctx)
	w := s.Doc.Watch(notify, s.MaxPending)
	if proj == nil {
		return docFeed{w}, nil
	}
	return &projectedFeed{w: w, doc: s.Doc, coll: s.Coll, project: proj, sent: map[string]string{}}, nil
}

type projectedFeed struct {
	w       *livedoc.Watcher
	doc     *livedoc.Doc
	coll    string
	project func(key string, item json.RawMessage) (json.RawMessage, bool)
	sent    map[string]string // item key → its projection as the viewer holds it
}

func (f *projectedFeed) Snapshot() (uint64, any) {
	seq, doc := f.w.Snapshot()
	clear(f.sent)
	if items, ok := doc[f.coll].(map[string]json.RawMessage); ok {
		for k, v := range items {
			if p, ok := f.project(k, v); ok {
				items[k] = p
				f.sent[k] = string(p)
			} else {
				delete(items, k)
			}
		}
	}
	return seq, doc
}

func (f *projectedFeed) Drain() (uint64, []jsonpatch.Op, error) {
	seq, ops, err := f.w.Drain()
	if errors.Is(err, livedoc.ErrLagged) {
		return 0, nil, fmt.Errorf("%w: %w", ErrResync, err)
	}
	if err != nil || len(ops) == 0 {
		return seq, ops, err
	}
	whole := jsonpatch.Pointer(f.coll)
	prefix := whole + "/"
	out := make([]jsonpatch.Op, 0, len(ops))
	var touched []string
	seen := map[string]bool{}
	for _, op := range ops {
		if op.Path == whole {
			// The collection itself was replaced: start again from a snapshot.
			return 0, nil, fmt.Errorf("%w: %s replaced whole", ErrResync, f.coll)
		}
		rest, ok := strings.CutPrefix(op.Path, prefix)
		if !ok {
			out = append(out, op)
			continue
		}
		key, _, _ := strings.Cut(rest, "/")
		key = strings.NewReplacer("~1", "/", "~0", "~").Replace(key)
		if !seen[key] {
			seen[key] = true
			touched = append(touched, key)
		}
	}
	for _, key := range touched {
		path := jsonpatch.Pointer(f.coll, key)
		had, wasSent := f.sent[key]
		var p json.RawMessage
		visible := false
		if raw := f.doc.Item(f.coll, key); raw != nil {
			p, visible = f.project(key, raw)
		}
		switch {
		case !visible && wasSent:
			delete(f.sent, key)
			out = append(out, jsonpatch.Op{Op: jsonpatch.Remove, Path: path})
		case !visible:
		case !wasSent:
			f.sent[key] = string(p)
			out = append(out, jsonpatch.Op{Op: jsonpatch.Add, Path: path, Value: p})
		case had != string(p):
			f.sent[key] = string(p)
			out = append(out, jsonpatch.Op{Op: jsonpatch.Replace, Path: path, Value: p})
		}
	}
	return seq, out, nil
}

func (f *projectedFeed) Close() { f.w.Close() }

func encoded(v any) json.RawMessage {
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	b, _ := json.Marshal(v)
	return b
}
