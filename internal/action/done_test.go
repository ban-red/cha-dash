package action

import (
	"testing"
	"time"
)

// OnTaskDone hears about every task that ends, after its audit record, with
// the tier and the actor.
func TestOnTaskDone(t *testing.T) {
	var calls []string
	e := newEnv(t, OwnerOnly{}, reboot(&calls))
	got := make(chan TaskDone, 1)
	eng, err := New(t.Context(), Options{Store: e.st, Audit: e.audit, Registry: e.reg, Policy: e.policy, Graph: e.g,
		Sessions: Sessions{Lookup: lookup}, Now: e.clock.Now,
		OnTaskDone: func(d TaskDone) { got <- d }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	e.e.Close()
	e.e = eng

	p := e.mustPlan(owner, "proxmox.guest.reboot", "proxmox:i_x/qemu/201")
	task, err := e.approve(owner, p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		if d.Task.ID != task.ID || d.Task.State != StateSucceeded || d.Task.Audit == nil || d.Tier != T2 || d.Actor.ID != owner.ID {
			t.Fatalf("done = %+v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OnTaskDone wasn't called")
	}
}
