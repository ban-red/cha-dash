package action

import (
	"context"
	"slices"
	"testing"

	"dash.cha.sh/internal/graph"
)

// TestImpactNamesTheHub: when Cha Dash runs on the target (chadash:hub
// runs_on plex here), the plan's impact lists chadash:hub, whether the Def
// walks the graph or brings its own blast radius.
func TestImpactNamesTheHub(t *testing.T) {
	var calls []string
	own := reboot(&calls)
	own.ID, own.Title = "test.guest.own", "Own"
	own.BlastRadius = func(context.Context, Request) []Impact {
		return []Impact{{Resource: "app:plex", Via: "runs_on", Depth: 1}}
	}
	e := newEnv(t, OwnerOnly{}, reboot(&calls), own)
	if _, err := e.g.Update(func(tx *graph.Tx) error {
		if err := tx.Upsert(graph.Resource{ID: HubResource, Kind: "app", Name: "Cha Dash", State: "running"}); err != nil {
			return err
		}
		return tx.Relate(graph.Relation{From: HubResource, To: "app:plex", Type: "runs_on"})
	}); err != nil {
		t.Fatal(err)
	}
	for _, act := range []string{"proxmox.guest.reboot", "test.guest.own"} {
		p := e.mustPlan(owner, act, "proxmox:i_x/qemu/201")
		i := slices.IndexFunc(p.Impact, func(x Impact) bool { return x.Resource == HubResource })
		if i < 0 || p.Impact[i].Via != "runs_on" || p.Impact[i].Depth != 2 {
			t.Errorf("%s: impact = %+v", act, p.Impact)
		}
	}
	// Not on the target's chain: not in the impact.
	if _, err := e.g.Update(func(tx *graph.Tx) error {
		tx.Unrelate(HubResource, "runs_on", "app:plex")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := e.mustPlan(owner, "test.guest.own", "proxmox:i_x/qemu/201")
	if slices.ContainsFunc(p.Impact, func(x Impact) bool { return x.Resource == HubResource }) {
		t.Fatalf("impact = %+v", p.Impact)
	}
}
