package nodeproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Handler serves one incoming request. emit sends a stream item (progress,
// a log line) before the reply; the returned value ends the call. ctx ends
// when the caller cancels, or the session does.
type Handler func(ctx context.Context, method string, params json.RawMessage, emit func(json.RawMessage) error) (json.RawMessage, error)

// Session defaults.
const (
	// DefaultKeepAlive is how often each side pings: the browser socket's
	// interval, inside the idle timeouts of common reverse proxies.
	DefaultKeepAlive = 30 * time.Second
	// DefaultWriteTimeout bounds writing one frame.
	DefaultWriteTimeout = 30 * time.Second
	// DefaultMaxInFlight bounds requests served at once on one session.
	DefaultMaxInFlight = 64
	// itemBuffer is how many stream items wait for a slow caller before the
	// session stops reading (backpressure, through TCP, to the sender).
	itemBuffer = 64
)

// SessionOptions configure a Session.
type SessionOptions struct {
	// Handler serves the other side's requests. Nil answers not_found.
	Handler Handler
	// KeepAlive is the ping interval (DefaultKeepAlive when 0). IdleTimeout
	// is how long the session waits for any frame before it gives up on the
	// peer; the default is two pings and a half.
	KeepAlive   time.Duration
	IdleTimeout time.Duration
	// WriteTimeout bounds writing one frame (DefaultWriteTimeout when 0).
	WriteTimeout time.Duration
	// MaxInFlight bounds concurrent incoming requests (DefaultMaxInFlight
	// when 0); more are answered busy.
	MaxInFlight int
}

func (o SessionOptions) withDefaults() SessionOptions {
	if o.KeepAlive <= 0 {
		o.KeepAlive = DefaultKeepAlive
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = o.KeepAlive*5/2 + time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = DefaultWriteTimeout
	}
	if o.MaxInFlight <= 0 {
		o.MaxInFlight = DefaultMaxInFlight
	}
	return o
}

// Session errors.
var (
	// ErrClosed is returned by calls on a session that has ended.
	ErrClosed = errors.New("nodeproto: session closed")
	// ErrIdle ends a session whose peer sent nothing, not even a pong,
	// within the idle timeout.
	ErrIdle = errors.New("nodeproto: the peer went quiet")
)

// Session multiplexes RPC in both directions over one Channel, with
// streamed replies, cancellation and keepalive pings.
type Session struct {
	ch *Channel
	o  SessionOptions

	mu      sync.Mutex
	next    uint64
	calls   map[uint64]*call
	serving map[uint64]context.CancelFunc
	sem     chan struct{}

	done chan struct{}
	err  error // set before done closes
}

type call struct {
	c    chan Frame
	gone chan struct{} // closed when Call returns, so Run never blocks on it
}

// NewSession wraps ch. Call Run to start it.
func NewSession(ch *Channel, o SessionOptions) *Session {
	o = o.withDefaults()
	return &Session{
		ch: ch, o: o,
		calls: map[uint64]*call{}, serving: map[uint64]context.CancelFunc{},
		sem: make(chan struct{}, o.MaxInFlight), done: make(chan struct{}),
	}
}

// Channel is the session's channel.
func (s *Session) Channel() *Channel { return s.ch }

// Done closes when Run returns.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the session ended, once Done is closed.
func (s *Session) Err() error {
	<-s.done
	return s.err
}

// Close ends the session; Run returns soon after.
func (s *Session) Close() error { return s.ch.Close() }

// Run reads frames until the channel fails, the peer goes quiet or ctx
// ends, then closes the channel and cancels every request it was serving.
func (s *Session) Run(ctx context.Context) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		s.ch.Close()
		s.mu.Lock()
		for _, c := range s.serving {
			c()
		}
		s.mu.Unlock()
		s.err = err
		close(s.done)
	}()
	pingErr := make(chan error, 1)
	go func() {
		t := time.NewTicker(s.o.KeepAlive)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.send(ctx, Frame{Op: OpPing}); err != nil {
					pingErr <- err
					s.ch.Close()
					return
				}
			}
		}
	}()
	for {
		b, err := s.ch.Recv(ctx, s.o.IdleTimeout)
		if err != nil {
			if errors.Is(err, ErrIdle) {
				return err // the reason, even if a ping failed on the dying connection
			}
			select {
			case perr := <-pingErr:
				return perr
			default:
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		var f Frame
		if err := json.Unmarshal(b, &f); err != nil {
			return fmt.Errorf("nodeproto: a frame isn't JSON: %w", err)
		}
		switch f.Op {
		case OpReq:
			s.accept(ctx, f)
		case OpItem, OpRes, OpErr:
			s.mu.Lock()
			c := s.calls[f.ID]
			s.mu.Unlock()
			if c == nil {
				continue // a call that already gave up
			}
			select {
			case c.c <- f:
			case <-c.gone:
			case <-ctx.Done():
				return ctx.Err()
			}
		case OpCancel:
			s.mu.Lock()
			if c := s.serving[f.ID]; c != nil {
				c()
			}
			s.mu.Unlock()
		case OpPing:
			go func() { _ = s.send(ctx, Frame{Op: OpPong}) }()
		case OpPong:
		case OpRefused:
			if f.Error != nil {
				return f.Error
			}
			return &Error{Code: CodeRevoked, Message: "the hub ended the session"}
		}
	}
}

// accept starts serving a request, or answers busy.
func (s *Session) accept(ctx context.Context, f Frame) {
	select {
	case s.sem <- struct{}{}:
	default:
		go func() {
			_ = s.send(ctx, Frame{Op: OpErr, ID: f.ID, Error: Errorf(CodeBusy, "too many requests at once; try again")})
		}()
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if old := s.serving[f.ID]; old != nil {
		old() // a reused ID: the peer gave up on the first
	}
	s.serving[f.ID] = cancel
	s.mu.Unlock()
	go s.serve(rctx, cancel, f)
}

func (s *Session) serve(ctx context.Context, cancel context.CancelFunc, f Frame) {
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.serving, f.ID)
		s.mu.Unlock()
		<-s.sem
	}()
	emit := func(data json.RawMessage) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.send(ctx, Frame{Op: OpItem, ID: f.ID, Data: data})
	}
	res, err := s.handle(ctx, f, emit)
	if err == nil {
		if err = s.send(ctx, Frame{Op: OpRes, ID: f.ID, Data: res}); errors.Is(err, ErrFrameTooLarge) {
			err = Errorf(CodeFailed, "the reply is larger than the channel's frame limit")
		} else if err == nil {
			return
		}
	}
	e, ok := errors.AsType[*Error](err)
	if !ok {
		e = &Error{Code: CodeFailed, Message: err.Error()}
	}
	if ctx.Err() != nil && e.Code == CodeFailed {
		e = Errorf(CodeFailed, "cancelled")
	}
	_ = s.send(context.WithoutCancel(ctx), Frame{Op: OpErr, ID: f.ID, Error: e})
}

func (s *Session) handle(ctx context.Context, f Frame, emit func(json.RawMessage) error) (res json.RawMessage, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = nil, Errorf(CodeFailed, "the %s handler failed", f.Method)
		}
	}()
	if s.o.Handler == nil {
		return nil, Errorf(CodeNotFound, "no verb %q here", f.Method)
	}
	return s.o.Handler(ctx, f.Method, f.Data, emit)
}

// Call sends a request and waits for the reply. Stream items go to onItem
// (which may be nil) on the calling goroutine, in order. When ctx ends the
// peer is told to stop.
func (s *Session) Call(ctx context.Context, method string, params any, onItem func(json.RawMessage)) (json.RawMessage, error) {
	var data json.RawMessage
	switch p := params.(type) {
	case nil:
	case json.RawMessage:
		data = p
	default:
		b, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		data = b
	}
	select {
	case <-s.done:
		return nil, ErrClosed
	default:
	}
	c := &call{c: make(chan Frame, itemBuffer), gone: make(chan struct{})}
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
	if err := s.send(ctx, Frame{Op: OpReq, ID: id, Method: method, Data: data}); err != nil {
		if errors.Is(err, ErrFrameTooLarge) {
			return nil, err
		}
		select {
		case <-s.done:
			return nil, ErrClosed
		default:
		}
		return nil, err
	}
	for {
		select {
		case f := <-c.c:
			switch f.Op {
			case OpItem:
				if onItem != nil {
					onItem(f.Data)
				}
			case OpRes:
				return f.Data, nil
			default:
				if f.Error == nil {
					return nil, Errorf(CodeFailed, "the peer failed without saying why")
				}
				return nil, f.Error
			}
		case <-s.done:
			return nil, ErrClosed
		case <-ctx.Done():
			// ctx is what just ended, so the cancel gets a context of its own.
			go func() { //nolint:gosec // G118: see above
				cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = s.send(cctx, Frame{Op: OpCancel, ID: id})
			}()
			return nil, ctx.Err()
		}
	}
}

func (s *Session) send(ctx context.Context, f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return s.ch.Send(ctx, b, s.o.WriteTimeout)
}

// SendFrame writes one control frame on ch (hello, welcome, refused),
// before a Session runs on it.
func SendFrame(ctx context.Context, ch *Channel, op string, data any, e *Error) error {
	f := Frame{Op: op, Error: e}
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
	return ch.Send(ctx, b, DefaultWriteTimeout)
}

// RecvFrame reads one control frame, waiting at most timeout.
func RecvFrame(ctx context.Context, ch *Channel, timeout time.Duration) (Frame, error) {
	b, err := ch.Recv(ctx, timeout)
	if err != nil {
		return Frame{}, err
	}
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return Frame{}, fmt.Errorf("nodeproto: a frame isn't JSON: %w", err)
	}
	return f, nil
}
