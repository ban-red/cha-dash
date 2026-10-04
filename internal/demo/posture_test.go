package demo

import (
	"testing"
	"time"

	"dash.cha.sh/internal/security"
	"dash.cha.sh/sim"
)

// The Demo Home's standing hints, by their hub IDs.
var demoHints = []string{"traefik.hint.api_no_auth", "docker-jupiter.hint.docker_endpoint", "frigate.unauthenticated-admin"}

// TestDemoFollowsThePosture: the Demo Home's security findings reach the
// inbox at the posture's severity, as real instances' do, from the first
// snapshot, through steps, and at once when the posture changes. Nothing
// else moves.
func TestDemoFollowsThePosture(t *testing.T) {
	d, tp, clock := newDriver(t, Options{Seed: 1, At: DefaultAt, Posture: security.Strict})
	check := func(when, want string) {
		t.Helper()
		for _, id := range demoHints {
			in := incident(t, tp, id)
			if in.Category != sim.CategorySecurity || string(in.Severity) != want || in.Status != sim.StatusOpen {
				t.Fatalf("%s: %s is %s/%s/%s, want security/%s/open", when, id, in.Category, in.Severity, in.Status, want)
			}
		}
		// Not a security finding: as the simulator says.
		if in := incident(t, tp, "plex-token-revoked"); in.Category != "" || in.Severity != sim.SeverityAttention {
			t.Fatalf("%s: plex = %s/%s", when, in.Category, in.Severity)
		}
	}
	check("strict at start", "attention")

	clock.Advance(10 * time.Minute)
	d.Step()
	check("strict after a step", "attention")

	d.SetPosture(security.Relaxed)
	check("relaxed", "info")
	d.SetPosture(security.Standard) // the simulator declares its hints info
	check("standard", "info")
	d.SetPosture(security.Strict)
	check("strict again", "attention")

	// A new day reloads the snapshot under the current posture.
	clock.Advance(day - 10*time.Minute)
	d.Step()
	check("strict on a new day", "attention")
}
