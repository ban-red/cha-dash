package realtime

import (
	"context"
	"errors"
	"time"

	"dash.cha.sh/internal/jsonpatch"
)

// sub is one client subscription: one or more topic feeds sharing a notify
// channel, a coalescing timer and a single JSON document on the client.
type sub struct {
	id     int64
	ctx    context.Context
	cancel context.CancelFunc
	notify chan struct{}
	feeds  []topicFeed
}

type topicFeed struct {
	topic  string
	prefix string // JSON Pointer of the topic's member in the subscription document
	feed   Feed
}

func (s *sub) snapshot() (uint64, map[string]any) {
	data := make(map[string]any, len(s.feeds))
	var seq uint64
	for _, f := range s.feeds {
		fs, d := f.feed.Snapshot()
		data[f.topic] = d
		seq = max(seq, fs)
	}
	return seq, data
}

// drain collects every feed's pending patches into one compacted patch.
func (s *sub) drain() (uint64, []jsonpatch.Op, error) {
	var all []jsonpatch.Op
	var seq uint64
	for _, f := range s.feeds {
		fs, ops, err := f.feed.Drain()
		if err != nil {
			return 0, nil, err
		}
		seq = max(seq, fs)
		all = append(all, jsonpatch.Prefix(f.prefix, ops)...)
	}
	return seq, jsonpatch.Compact(all), nil
}

func (s *sub) close() {
	s.cancel()
	for _, f := range s.feeds {
		f.feed.Close()
	}
}

// run forwards updates until the subscription or connection ends.
//
// Backpressure policy: diffs are never queued without bound. If a feed
// overflows (ErrResync) or the connection's send queue is full, pending diffs
// are dropped and the client gets a fresh snapshot instead (resync: true).
// The snapshot send may wait for room; meanwhile feeds keep accumulating up to
// their own bound, so memory stays bounded however slow the client is. A
// client that can't accept a frame within WriteTimeout is disconnected.
func (s *sub) run(c *conn) {
	defer s.close()
	// The snapshot, sent just before the loop, is the first frame. Counting
	// it means the first change waits out the window too, so one graph
	// change, which reaches each topic's feed in turn, arrives as one diff
	// rather than as one per feed.
	last := time.Now()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.notify:
		}
		// Coalesce: hold off until Coalesce has passed since the last frame, so
		// a burst of graph changes becomes one diff.
		if wait := time.Until(last.Add(c.h.opts.Coalesce)); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-s.ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}

		seq, patch, err := s.drain()
		switch {
		case errors.Is(err, ErrResync):
			if !s.resync(c) {
				return
			}
		case err != nil:
			c.h.opts.Log.Error("realtime: feed failed", "sub", s.id, "err", err)
			return
		case len(patch) == 0:
			continue // e.g. an add and a remove that cancelled out
		case !c.trySend(diffMsg{Op: "diff", ID: s.id, Seq: seq, Patch: patch}):
			c.h.opts.Log.Debug("realtime: client behind; resyncing", "sub", s.id)
			if !s.resync(c) {
				return
			}
		}
		last = time.Now()
	}
}

// resync replaces the client's state with a fresh snapshot.
func (s *sub) resync(c *conn) bool {
	seq, data := s.snapshot()
	return c.send(s.ctx, snapMsg{Op: "snap", ID: s.id, Seq: seq, Data: data, Resync: true}) == nil
}
