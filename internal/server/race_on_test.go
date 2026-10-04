//go:build race

package server

// raceEnabled: the race detector slows everything several times over, so
// timing budgets skip under it. CI runs them in a separate step without
// -race.
const raceEnabled = true
