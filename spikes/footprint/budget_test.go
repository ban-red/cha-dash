//go:build footprint

package footprint

import (
	"fmt"
	"testing"
)

// rssBudgets are the hub's memory budgets (03 · Architecture → Performance
// budgets) applied to the scenarios that stand for them, in MB (10^6 bytes)
// of peak RSS over the run:
//
//   - the empty hub with nobody watching is "hub idle RSS, no integrations";
//   - the Demo Home with 20 tabs open stands in for the full dogfood home
//     with N tabs: the Demo Home is a subset of it (~130 of ~300
//     resources), so this is the looser of what the real home must meet.
//
// The footprint job is non-blocking (ADR-0016), so an over-budget run shows
// as a failed check without blocking a merge. Linux RSS from CI is the
// number that counts; macOS overstates it (see README.md).
var rssBudgets = map[string]float64{
	"empty hub, 0 clients": 60,
	"demo, 20 clients":     120,
}

// checkBudgets prints one "Budget:" line per budgeted scenario that ran
// (the job summary picks them up) and fails the test for any over budget.
func checkBudgets(t *testing.T, rs []result) {
	t.Helper()
	for _, r := range rs {
		limit, ok := rssBudgets[r.Scenario.Name]
		if !ok {
			continue
		}
		peak := float64(max(r.PeakRSS, r.Ready.RSS, r.End.RSS)) / 1e6
		verdict := "ok"
		if peak > limit {
			verdict = "OVER"
			t.Errorf("%s: peak RSS %.1f MB, over the %.0f MB budget", r.Scenario.Name, peak, limit)
		}
		fmt.Printf("Budget: %s, peak RSS %.1f MB (budget %.0f MB): %s\n", r.Scenario.Name, peak, limit, verdict)
	}
}
