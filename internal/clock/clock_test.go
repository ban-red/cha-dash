package clock

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// within fails unless ch delivers within d.
func within[T any](t *testing.T, ch <-chan T, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("%s didn't happen within %s", what, d)
	}
}

func never[T any](t *testing.T, ch <-chan T, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s happened", what)
	case <-time.After(d):
	}
}

func TestNilAndRealClocksAreRealTime(t *testing.T) {
	for name, c := range map[string]*Clock{"nil": nil, "real": Real()} {
		if d := time.Until(c.Now()); d > time.Second || d < -time.Second {
			t.Errorf("%s: Now is %s off", name, d)
		}
		if c.Testing() || c.Skew() != 0 {
			t.Errorf("%s: not a test clock", name)
		}
		if err := c.Advance(time.Hour); !errors.Is(err, ErrNotTest) {
			t.Errorf("%s: Advance = %v", name, err)
		}
		tm := c.NewTimer(20 * time.Millisecond)
		within(t, tm.C, time.Second, name+" timer")
	}
}

func TestAdvanceAndSet(t *testing.T) {
	c := NewTest()
	before := c.Now()
	if err := c.Advance(2 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if d := c.Now().Sub(before); d < 2*time.Hour || d > 2*time.Hour+time.Second {
		t.Errorf("after Advance(2h) the clock moved %s", d)
	}
	if err := c.Advance(0); !errors.Is(err, ErrBackwards) {
		t.Errorf("Advance(0) = %v", err)
	}
	if err := c.Set(time.Now()); !errors.Is(err, ErrBackwards) {
		t.Errorf("Set to real now, behind the clock: %v", err)
	}
	target := time.Now().Add(72 * time.Hour)
	if err := c.Set(target); err != nil {
		t.Fatal(err)
	}
	if d := c.Now().Sub(target); d < 0 || d > time.Second {
		t.Errorf("after Set the clock is %s from the target", d)
	}
}

// A timer an hour out fires as soon as the clock is moved past it, not
// before; moving it part of the way doesn't fire it.
func TestTimerFiresOnAMove(t *testing.T) {
	c := NewTest()
	tm := c.NewTimer(time.Hour)
	never(t, tm.C, 50*time.Millisecond, "an hour's timer")
	_ = c.Advance(30 * time.Minute)
	never(t, tm.C, 50*time.Millisecond, "the timer half-way")
	_ = c.Advance(31 * time.Minute)
	within(t, tm.C, time.Second, "the timer after the move")
}

func TestStopAndReset(t *testing.T) {
	c := NewTest()
	tm := c.NewTimer(time.Hour)
	if !tm.Stop() {
		t.Error("Stop on a waiting timer should report it")
	}
	_ = c.Advance(2 * time.Hour)
	never(t, tm.C, 50*time.Millisecond, "a stopped timer")
	if tm.Stop() {
		t.Error("a second Stop has nothing to stop")
	}
	tm.Reset(time.Minute)
	_ = c.Advance(2 * time.Minute)
	within(t, tm.C, time.Second, "the reset timer")
}

func TestAfterFunc(t *testing.T) {
	c := NewTest()
	var ran atomic.Int32
	done := make(chan struct{})
	c.AfterFunc(15*time.Minute, func() { ran.Add(1); close(done) })
	stopped := c.AfterFunc(15*time.Minute, func() { ran.Add(10) })
	stopped.Stop()
	_ = c.Advance(16 * time.Minute)
	within(t, done, time.Second, "AfterFunc")
	time.Sleep(50 * time.Millisecond)
	if got := ran.Load(); got != 1 {
		t.Errorf("ran %d, want only the one that wasn't stopped", got)
	}
}

// A ticker ticks on real time, and once after each move.
func TestTickerWakesOnAMove(t *testing.T) {
	c := NewTest()
	tk := c.NewTicker(time.Hour)
	defer tk.Stop()
	never(t, tk.C, 50*time.Millisecond, "an hourly tick")
	_ = c.Advance(time.Minute)
	select {
	case at := <-tk.C:
		if c.Now().Sub(at) > time.Second {
			t.Errorf("the tick carries %s, not the moved clock", at)
		}
	case <-time.After(time.Second):
		t.Fatal("no tick after the move")
	}
	fast := Real().NewTicker(10 * time.Millisecond)
	within(t, fast.C, time.Second, "a real tick")
	fast.Stop()
}
