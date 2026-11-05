// Package clock is the hub's clock: real time, plus a skew that only a test
// clock may move (H2 in docs/plan/17-test-automation.md).
//
// A test hub (CHADASH_TEST_CLOCK, only with CHADASH_TEST_AUTOMATION) makes
// a test clock; a test moves it forward with POST /api/test/clock, and
// every schedule that reads it sees the new time at once: maintenance
// windows, notification grace, the morning digest, approval expiry, the
// demo's day. Time still runs between moves, so nothing stalls. It never
// goes back, so durations measured with it stay positive.
//
// The clock owns Now and the timers schedules wait on: Ticker, Timer and
// AfterFunc wake on real time as usual, and also right after each move,
// then fire if the clock says their time has come. Transport timeouts
// (sockets, HTTP, keepalives) stay on real time: they aren't schedules.
//
// A nil *Clock is the real clock, so a service's Clock option can be left
// out.
package clock

import (
	"errors"
	"sync"
	"time"
)

// Clock is real time plus a skew.
type Clock struct {
	test bool

	mu    sync.Mutex
	skew  time.Duration
	moved chan struct{} // closed, and replaced, on each move
}

// Real is a clock that can't be moved.
func Real() *Clock { return &Clock{moved: make(chan struct{})} }

// NewTest is a clock a test can move forward.
func NewTest() *Clock { return &Clock{test: true, moved: make(chan struct{})} }

// Errors from Advance and Set.
var (
	ErrNotTest   = errors.New("clock: only a test clock can be moved")
	ErrBackwards = errors.New("clock: the clock only goes forward")
)

// Now is the clock's time.
func (c *Clock) Now() time.Time {
	if c == nil {
		return time.Now()
	}
	c.mu.Lock()
	s := c.skew
	c.mu.Unlock()
	return time.Now().Add(s)
}

// Since is Now() - t.
func (c *Clock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

// Testing reports whether this is a test clock.
func (c *Clock) Testing() bool { return c != nil && c.test }

// Skew is how far ahead of real time the clock is.
func (c *Clock) Skew() time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skew
}

// Advance moves a test clock d forward (d > 0) and wakes its timers.
func (c *Clock) Advance(d time.Duration) error {
	if !c.Testing() {
		return ErrNotTest
	}
	if d <= 0 {
		return ErrBackwards
	}
	c.mu.Lock()
	c.skew += d
	close(c.moved)
	c.moved = make(chan struct{})
	c.mu.Unlock()
	return nil
}

// Set moves a test clock forward to t.
func (c *Clock) Set(t time.Time) error {
	if !c.Testing() {
		return ErrNotTest
	}
	d := time.Until(t) - c.Skew()
	if d <= 0 {
		return ErrBackwards
	}
	return c.Advance(d)
}

// wake is closed at the clock's next move; nil (never ready) for the real
// clock.
func (c *Clock) wake() <-chan struct{} {
	if !c.Testing() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.moved
}

// ── Ticker ──

// Ticker sends the clock's time on C every d of real time, and once right
// after each move of the clock. Like time.Ticker, C holds one tick at most.
type Ticker struct {
	C    <-chan time.Time
	stop chan struct{}
	once sync.Once
}

// NewTicker starts a ticker (d > 0).
func (c *Clock) NewTicker(d time.Duration) *Ticker {
	ch := make(chan time.Time, 1)
	t := &Ticker{C: ch, stop: make(chan struct{})}
	rt := time.NewTicker(d)
	go func() {
		defer rt.Stop()
		send := func() {
			select {
			case ch <- c.Now():
			default:
			}
		}
		for {
			select {
			case <-t.stop:
				return
			case <-rt.C:
				send()
			case <-c.wake():
				send()
			}
		}
	}()
	return t
}

// Stop stops the ticker. No more ticks are sent after it returns, apart
// from one already waiting in C.
func (t *Ticker) Stop() { t.once.Do(func() { close(t.stop) }) }

// ── Timer and AfterFunc ──

// Timer fires once, when the clock reaches its deadline: it sends the time
// on C, or with AfterFunc runs its function in its own goroutine.
type Timer struct {
	C <-chan time.Time

	c  *Clock
	ch chan time.Time
	f  func()

	mu   sync.Mutex
	stop chan struct{} // closes the current wait; nil when not waiting
}

// NewTimer starts a timer that fires d from now on the clock.
func (c *Clock) NewTimer(d time.Duration) *Timer {
	ch := make(chan time.Time, 1)
	t := &Timer{C: ch, c: c, ch: ch}
	t.start(d)
	return t
}

// AfterFunc runs f once the clock reaches d from now.
func (c *Clock) AfterFunc(d time.Duration, f func()) *Timer {
	t := &Timer{c: c, f: f}
	t.start(d)
	return t
}

func (t *Timer) start(d time.Duration) {
	deadline := t.c.Now().Add(d)
	stop := make(chan struct{})
	t.mu.Lock()
	t.stop = stop
	t.mu.Unlock()
	go func() {
		for {
			wait := deadline.Sub(t.c.Now())
			if wait <= 0 {
				break
			}
			rt := time.NewTimer(wait)
			select {
			case <-stop:
				rt.Stop()
				return
			case <-rt.C:
			case <-t.c.wake():
				rt.Stop()
			}
		}
		t.mu.Lock()
		if t.stop != stop { // stopped or reset meanwhile
			t.mu.Unlock()
			return
		}
		t.stop = nil
		t.mu.Unlock()
		if t.f != nil {
			go t.f()
			return
		}
		select {
		case t.ch <- t.c.Now():
		default:
		}
	}()
}

// Stop stops the timer. It reports whether that kept it from firing.
func (t *Timer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stop == nil {
		return false
	}
	close(t.stop)
	t.stop = nil
	return true
}

// Reset makes the timer fire d from now, whether or not it had fired. It
// reports whether it was still waiting. As with time.Timer since Go 1.23,
// a tick that hadn't been received is dropped.
func (t *Timer) Reset(d time.Duration) bool {
	waiting := t.Stop()
	if t.ch != nil {
		select {
		case <-t.ch:
		default:
		}
	}
	t.start(d)
	return waiting
}
