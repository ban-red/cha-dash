package notify

import (
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
