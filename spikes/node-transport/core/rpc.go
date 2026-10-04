package core

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// Frame is one message on the channel, JSON like the browser protocol.
//
// Control (the first frames after the handshake):
//
//	node → {"op":"hello","data":{"v":1}}                  always first
//	node → {"op":"hello","data":{"v":1,"token":"cdj_…","material":"…"}} not enrolled yet
//	hub  → {"op":"welcome","data":{"node":"n_…"}}         full session follows
//	hub  → {"op":"pending","data":{"node":"n_…"}}         waiting for an admin
//	hub  → {"op":"approved","data":{"node":"n_…","cred":"…"}} reconnect with it
//	hub  → {"op":"refused","error":"…"}                   then close
//
// RPC, in either direction (the hub calls node verbs; the node calls a few hub
// verbs such as node.rotate):
//
//	→ {"op":"req","id":1,"method":"logs.tail","data":{…}}
//	← {"op":"item","id":1,"data":…}                        zero or more: a stream
//	← {"op":"res","id":1,"data":…}  or  {"op":"err","id":1,"error":"…"}
type Frame struct {
	Op     string          `json:"op"`
	ID     uint64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Handler serves one incoming request. Calling emit turns the reply into a
// stream; the returned value ends it.
type Handler func(ctx context.Context, method string, params json.RawMessage, emit func(any) error) (any, error)

// RemoteError is an error the other side returned.
type RemoteError string

func (e RemoteError) Error() string { return string(e) }

// ErrClosed is returned by calls on a session that has ended.
var ErrClosed = errors.New("session closed")

// Session multiplexes calls in both directions over one Channel.
type Session struct {
	ch     Channel
	handle Handler

	mu    sync.Mutex
	next  uint64
	calls map[uint64]*call
	done  chan struct{}
}

type call struct {
	c    chan Frame
	gone chan struct{} // closed when Call returns, so Run never blocks on it
}

func NewSession(ch Channel, h Handler) *Session {
	return &Session{ch: ch, handle: h, calls: map[uint64]*call{}, done: make(chan struct{})}
}

// Run reads frames until the channel fails or ctx ends, then closes it.
func (s *Session) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		s.ch.Close()
		close(s.done)
	}()
	for {
		f, err := recvFrame(ctx, s.ch)
		if err != nil {
			return err
		}
		switch f.Op {
		case "req":
			go s.serve(ctx, f)
		case "item", "res", "err":
			s.mu.Lock()
			c := s.calls[f.ID]
			s.mu.Unlock()
			if c != nil {
				select {
				case c.c <- f:
				case <-c.gone:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		case "refused":
			return RemoteError(f.Error)
		}
	}
}

// Done closes when Run returns.
func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) serve(ctx context.Context, f Frame) {
	emit := func(v any) error { return s.send(ctx, Frame{Op: "item", ID: f.ID}, v) }
	var res any = struct{}{}
	err := errors.New("no handler")
	if s.handle != nil {
		res, err = s.handle(ctx, f.Method, f.Data, emit)
	}
	if err != nil {
		s.send(ctx, Frame{Op: "err", ID: f.ID, Error: err.Error()}, nil)
		return
	}
	s.send(ctx, Frame{Op: "res", ID: f.ID}, res)
}

// Call sends a request and waits for the reply. Stream items go to onItem,
// which may be nil for plain request/response.
func (s *Session) Call(ctx context.Context, method string, params any, onItem func(json.RawMessage)) (json.RawMessage, error) {
	c := &call{c: make(chan Frame, 16), gone: make(chan struct{})}
	s.mu.Lock()
	s.next++
	id := s.next
	s.calls[id] = c
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.calls, id)
		s.mu.Unlock()
		close(c.gone)
	}()
	if err := s.send(ctx, Frame{Op: "req", ID: id, Method: method}, params); err != nil {
		return nil, err
	}
	for {
		select {
		case f := <-c.c:
			switch f.Op {
			case "item":
				if onItem != nil {
					onItem(f.Data)
				}
			case "res":
				return f.Data, nil
			default:
				return nil, RemoteError(f.Error)
			}
		case <-s.done:
			return nil, ErrClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Refuse tells the peer why the session is ending and closes it.
func (s *Session) Refuse(ctx context.Context, reason string) {
	s.send(ctx, Frame{Op: "refused", Error: reason}, nil)
	s.ch.Close()
}

func (s *Session) send(ctx context.Context, f Frame, data any) error {
	return sendFrame(ctx, s.ch, f, data)
}

func sendFrame(ctx context.Context, ch Channel, f Frame, data any) error {
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		f.Data = b
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return ch.Send(ctx, b)
}

func recvFrame(ctx context.Context, ch Channel) (Frame, error) {
	b, err := ch.Recv(ctx)
	if err != nil {
		return Frame{}, err
	}
	var f Frame
	err = json.Unmarshal(b, &f)
	return f, err
}
