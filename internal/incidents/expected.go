package incidents

import (
	"sync/atomic"
	"time"
)

// Expecter says whether an incident on a resource is expected at a time:
// it opened inside a maintenance window covering that resource's host. It
// returns the window's ID when it is.
//
// The rollouts track (wave 26) owns maintenance windows and implements it;
// the hub hands its Expecter to Expectations.Set. Until then nothing is
// expected. It's called from the inbox's watchers (the incident recorder,
// notifications) on every change, so it must be cheap and must not block:
// keep the windows in memory.
type Expecter interface {
	Expected(resource string, at time.Time) (window string, ok bool)
}

// ExpecterFunc adapts a function to Expecter.
type ExpecterFunc func(resource string, at time.Time) (string, bool)

// Expected implements Expecter.
func (f ExpecterFunc) Expected(resource string, at time.Time) (string, bool) { return f(resource, at) }

// Expectations is the hub's one Expecter, settable at any time and safe
// for concurrent use. The zero value, and a nil *Expectations, expect
// nothing.
//
// What it changes: an incident that opens while expected gets an
// "expected" event beside its "opened" one (the timeline shows it), and
// notifications hold it back while it stays expected; one still open when
// the window ends notifies then, through the usual rules.
type Expectations struct {
	p atomic.Pointer[box]
}

type box struct{ e Expecter }

// Set installs e; nil removes it.
func (x *Expectations) Set(e Expecter) {
	if e == nil {
		x.p.Store(nil)
		return
	}
	x.p.Store(&box{e: e})
}

// Expected asks the installed Expecter, if any.
func (x *Expectations) Expected(resource string, at time.Time) (string, bool) {
	if x == nil || resource == "" {
		return "", false
	}
	b := x.p.Load()
	if b == nil || b.e == nil {
		return "", false
	}
	return b.e.Expected(resource, at)
}
