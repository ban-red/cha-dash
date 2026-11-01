package notify

import (
	"strings"
	"testing"
	"time"
)

func acked(by string) incOpt {
	return func(m map[string]any) {
		m["acknowledged"] = map[string]any{"by": by, "at": "2026-10-06T02:00:00Z"}
	}
}

// TestAcknowledgedStopsEscalating: an attention problem someone
// acknowledged doesn't notify after its grace, nor when it gets worse;
// taking the acknowledgement back lets the rules apply again.
func TestAcknowledgedStopsEscalating(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	e.put(inc("ups", "problem", "attention", "open", "open"))
	e.settle()
	// Acknowledged inside its grace: nothing after it.
	e.put(inc("ups", "problem", "attention", "open", "open", acked("sam")))
	e.tick(10 * time.Minute)
	e.tick(DefaultGroupWindow)
	if got := n.msgs(); len(got) != 0 {
		t.Fatalf("an acknowledged attention notified: %+v", got)
	}
	// Worse, still acknowledged: nothing.
	e.put(inc("ups", "problem", "down", "open", "open", acked("sam")))
	e.settle()
	e.tick(DefaultGroupWindow)
	if got := n.msgs(); len(got) != 0 {
		t.Fatalf("an acknowledged escalation notified: %+v", got)
	}
	if e.s.det.pendingGrace() {
		t.Fatal("an acknowledged incident keeps the loop looking")
	}
	// Taken back: down notifies, as it would have.
	e.put(inc("ups", "problem", "down", "open", "open"))
	e.settle()
	e.tick(DefaultGroupWindow)
	if got := n.msgs(); len(got) != 1 || got[0].Priority != 4 {
		t.Fatalf("after taking it back: %+v", got)
	}
}

// TestAcknowledgedAfterOpeningStillResolves: a down problem notified, then
// acknowledged; its resolution still follows its opening.
func TestAcknowledgedAfterOpeningStillResolves(t *testing.T) {
	e := newEnv(t)
	n := newNtfy(t)
	e.addNtfy(n)
	e.put(inc("nas", "problem", "down", "open", "open"))
	e.settle()
	e.tick(DefaultGroupWindow)
	e.put(inc("nas", "problem", "down", "open", "open", acked("sam")))
	e.settle()
	e.put(inc("nas", "problem", "info", "resolved", "resolved"))
	e.settle()
	e.tick(DefaultGroupWindow)
	if got := n.msgs(); len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
}

// TestDigestNamesAcknowledged: the digest lists what someone is on apart
// from what needs attention.
func TestDigestNamesAcknowledged(t *testing.T) {
	e, n := digestEnv(t)
	e.put(inc("owl", "problem", "attention", "open", "open", withTitle("owl's backup failed"), acked("sam")))
	e.tick(30 * time.Minute) // 07:30
	e.settle()
	var digest string
	for _, m := range n.msgs() {
		if m.Title == "Morning digest" {
			digest = m.Message
		}
	}
	for _, want := range []string{"Nothing open needs attention.", "Acknowledged, someone is on it: 1.", "· owl's backup failed (sam)"} {
		if !strings.Contains(digest, want) {
			t.Fatalf("digest lacks %q:\n%s", want, digest)
		}
	}
	if strings.Contains(digest, "Open now") {
		t.Fatalf("an acknowledged incident counted as needing attention:\n%s", digest)
	}

	// Beside one that isn't: both, apart.
	in := digestInput{since: e.clock.Now(), now: e.clock.Now(), loc: time.UTC,
		open: []incident{{Title: "ups is down", Severity: "down"}},
		acked: []incident{{Title: "owl's backup failed", Severity: "attention", Acknowledged: &struct {
			By string `json:"by"`
		}{By: "sam"}}}}
	ev, ok := composeDigest(in, false)
	if !ok || !strings.Contains(ev.Body, "Open now: 1.") || !strings.Contains(ev.Body, "Acknowledged, someone is on it: 1.") || ev.Severity != "down" {
		t.Fatalf("both: %+v", ev)
	}
}
