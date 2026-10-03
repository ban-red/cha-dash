// Package sim is the Demo Home simulator: a deterministic stand-in for a real
// homelab, shaped like the dogfood home in docs/plan/05-dogfood-home.md.
//
// It powers the M0 demo (a live front page with no lab attached), the public
// demo later, and the JSON snapshots in sim/snapshots that design prototypes
// read.
//
// The home is a graph of resources and relations using the Home Graph
// vocabulary from docs/plan/03-architecture.md, plus metric series with
// short histories, plus incidents that carry what the Inbox needs: severity,
// cause hints, blast radius, a suggested action with its safety tier, and
// how the incident resolves.
//
// Usage:
//
//	s, err := sim.New(1)              // seed 1, default script, starts 2026-10-03 00:00 UTC
//	st := s.Snapshot()                // full state, sorted, with sparkline history
//	events := s.Advance(time.Minute)  // everything that changed, in order
//	events, err = s.Fix("plex-token-revoked") // press the incident's suggested action
//
// Everything is a pure function of (seed, script, fixes, time): randomness is
// counter-based hashing, not a stateful generator, so the same seed always
// gives the same home and the result of Advance does not depend on step size.
package sim
