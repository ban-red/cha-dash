package notify

import (
	"strings"
	"testing"
	"time"
)

// A problem on a host in an open maintenance window is expected: it doesn't
// notify while the window lasts, and notifies when it ends if it's still
// open. One that resolves inside the window says nothing.
func TestExpectedInAWindowWaitsForItsEnd(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	windowEnds := e.clock.Now().Add(30 * time.Minute)
	e.s.det.expected = func(target string, at time.Time) bool {
		return (target == "app:sonarr" || target == "app:radarr") && at.Before(windowEnds)
	}

	e.put(inc("sonarr", "problem", "down", "open", "open"), inc("radarr", "problem", "attention", "open", "open"))
	e.settle()
	e.tick(10 * time.Minute)
	e.tick(DefaultGroupWindow)
	if len(n.msgs()) != 0 {
		t.Fatalf("notified inside the window: %+v", n.msgs())
	}
	// radarr recovers inside the window: nothing, then or later.
	e.put(inc("radarr", "problem", "info", "resolved", "resolved"))
	e.settle()

	e.tick(25 * time.Minute) // past the window's end
	e.tick(DefaultGroupWindow)
	got := n.msgs()
	if len(got) != 1 || got[0].Title != "sonarr is broken" {
		t.Fatalf("after the window = %+v", got)
	}
}

// An attention problem still open when the window ends waits its grace
// from then: nothing at the end, one message at the end plus the grace.
// 14b's manual test 1, the part about the window's end
// (docs/plan/17-test-automation.md).
func TestAttentionAfterAWindowWaitsItsGrace(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	windowEnds := e.clock.Now().Add(30 * time.Minute)
	e.s.det.expected = func(target string, at time.Time) bool {
		return target == "app:radarr" && at.Before(windowEnds)
	}
	grace := time.Duration(e.s.settings(RuleAttention).GraceMinutes) * time.Minute
	if grace < 2*time.Minute {
		t.Fatalf("grace %s", grace)
	}

	e.put(inc("radarr", "problem", "attention", "open", "open"))
	e.settle()
	e.tick(20 * time.Minute) // past its grace, inside the window
	e.tick(DefaultGroupWindow)
	if got := n.msgs(); len(got) != 0 {
		t.Fatalf("notified inside the window: %+v", got)
	}
	// Just past the window's end: the grace has only begun.
	e.clock.Advance(windowEnds.Add(time.Second).Sub(e.clock.Now()))
	e.step()
	e.tick(DefaultGroupWindow)
	if got := n.msgs(); len(got) != 0 {
		t.Fatalf("notified at the window's end, before its grace: %+v", got)
	}
	// The end plus the grace: now it does, once.
	e.clock.Advance(windowEnds.Add(grace).Sub(e.clock.Now()) + time.Second)
	e.step()
	e.tick(DefaultGroupWindow)
	e.tick(time.Minute)
	if got := n.msgs(); len(got) != 1 || !strings.Contains(got[0].Title, "radarr") {
		t.Fatalf("at the window's end plus the grace = %+v", got)
	}
}
