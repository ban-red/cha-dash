package runtime

import (
	"slices"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/topics"
)

func finding(id, severity string) integration.Incident {
	return integration.Incident{ID: id, Scenario: "credential-hygiene", Class: integration.ClassProblem, Severity: severity,
		Category: integration.CategorySecurity, Title: id, Summary: "…", Target: "app:x"}
}

// TestEmitterPosture: security findings reach the inbox at the posture's
// severity, the emitter keeps the declared one, and a change re-applies to
// every finding at once, keeping what the correlator added.
func TestEmitterPosture(t *testing.T) {
	e, set, clk := newTestEmitter()
	p := security.Relaxed
	e.SetPosture(func() security.Posture { return p })

	exposure := finding("hint.api_no_auth", integration.SeverityAttention)
	advice := finding("hint.docker_endpoint", integration.SeverityInfo)
	plain := integration.Incident{ID: "check.sonarr", Class: integration.ClassProblem, Severity: integration.SeverityAttention, Title: "Sonarr is slow", Target: "app:sonarr"}
	for _, in := range []integration.Incident{exposure, advice, plain} {
		if err := e.Raise(in); err != nil {
			t.Fatal(err)
		}
	}
	sev := func(local string) string { return incident(t, set, "in_a."+local).Severity }
	want := func(level string, exp, adv string) {
		t.Helper()
		if got := []string{sev(exposure.ID), sev(advice.ID), sev(plain.ID)}; !slices.Equal(got, []string{exp, adv, "attention"}) {
			t.Fatalf("%s: severities %v", level, got)
		}
		if got := incident(t, set, "in_a."+exposure.ID).Category; got != "security" {
			t.Fatalf("%s: category %q", level, got)
		}
	}
	want("relaxed", "info", "info")

	// The correlator adds a cause in place; a posture change keeps it.
	in := incident(t, set, "in_a."+exposure.ID)
	if _, err := set.Inbox.Update(func(tx *livedoc.Tx) error {
		in.Causes = []integration.Cause{{Text: "the same Traefik as app:traefik", Resource: "app:traefik"}}
		tx.Put(topics.Incidents, in.ID, in)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated := incident(t, set, "in_a."+exposure.ID).UpdatedAt

	clk.t = clk.t.Add(time.Minute)
	p = security.Strict
	e.Reapply()
	want("strict", "attention", "attention")
	got := incident(t, set, "in_a."+exposure.ID)
	if len(got.Causes) != 1 || !got.UpdatedAt.Equal(updated) {
		t.Fatalf("re-applied = %+v", got)
	}

	p = security.Standard
	e.Reapply()
	want("standard", "attention", "info")

	// Re-applying an unchanged posture publishes nothing; nor does raising
	// the same finding again (the declared severity didn't change).
	v := set.Inbox.Version()
	e.Reapply()
	if err := e.Raise(advice); err != nil {
		t.Fatal(err)
	}
	if set.Inbox.Version() != v {
		t.Fatal("nothing changed, yet the inbox did")
	}

	// Phases and resolutions publish at the posture's severity too.
	p = security.Strict
	e.setPhase(advice.ID, PhaseFixing)
	if sev(advice.ID) != "attention" {
		t.Fatal("setPhase published the declared severity")
	}
	if err := e.Resolve(advice.ID, "No longer applies."); err != nil {
		t.Fatal(err)
	}
	if got := incident(t, set, "in_a."+advice.ID); got.Severity != "attention" || got.Status != "resolved" {
		t.Fatalf("resolved = %+v", got)
	}
	// A resolved finding follows the posture as well, so it reads the same
	// in Resolved as it did while open.
	p = security.Relaxed
	e.Reapply()
	if sev(advice.ID) != "info" {
		t.Fatal("a resolved finding didn't follow the posture")
	}

	// Only "security" is a category.
	bad := finding("x", integration.SeverityInfo)
	bad.Category = "privacy"
	if err := e.Raise(bad); err == nil {
		t.Fatal("accepted an unknown category")
	}
}

// TestManagerSetPosture: the Manager's posture reaches every instance at once.
func TestManagerSetPosture(t *testing.T) {
	set := topics.New(graph.New(), time.Now())
	m := &Manager{o: Options{Topics: set, Now: time.Now}, reg: integration.NewRegistry(), insts: map[string]*managed{}}
	m.posture.Store(security.Relaxed)
	a, b := m.add(row{ID: "in_a"}), m.add(row{ID: "in_b"})
	if err := a.emit.Raise(finding("hint.label_secrets", integration.SeverityAttention)); err != nil {
		t.Fatal(err)
	}
	if err := b.emit.Raise(finding("unauthenticated-admin", integration.SeverityInfo)); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		for _, id := range []string{"in_a.hint.label_secrets", "in_b.unauthenticated-admin"} {
			if got := incident(t, set, id).Severity; got != want {
				t.Fatalf("%s at %s, want %s", id, got, want)
			}
		}
	}
	check("info")
	m.SetPosture(security.Strict)
	if m.Posture() != security.Strict {
		t.Fatal(m.Posture())
	}
	check("attention")
	m.SetPosture(security.Relaxed)
	check("info")
}
