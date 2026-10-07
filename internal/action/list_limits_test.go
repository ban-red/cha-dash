package action

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const vm = "proxmox:i_x/qemu/201" // win-gaming in newEnv's graph

func noop(context.Context, Request, *TaskRun) error { return nil }

// shorten sets the listing's limits for one test.
func shorten(t *testing.T, deadline time.Duration, parallel int) {
	t.Helper()
	d, p := listDeadline, listParallel
	listDeadline, listParallel = deadline, parallel
	t.Cleanup(func() { listDeadline, listParallel = d, p })
}

func graphResolve(t Target) func(context.Context, string) (Target, error) {
	return func(_ context.Context, target string) (Target, error) {
		if target != t.ID {
			return Target{}, ErrNotFound
		}
		return t, nil
	}
}

var winGaming = Target{ID: vm, Kind: "vm", Name: "win-gaming"}

func TestActionsForDeadline(t *testing.T) {
	shorten(t, 300*time.Millisecond, 4)
	release := make(chan struct{})
	defer close(release)
	defs := []Def{
		{ID: "x.fast", Title: "Fast", Targets: []string{"vm"}, Tier: T1, Resolve: graphResolve(winGaming), Execute: noop},
		// Resolve honours its context: it gives up at the deadline.
		{ID: "x.slow-resolve", Title: "Slow", Targets: []string{"vm"}, Tier: T1, Execute: noop,
			Resolve: func(ctx context.Context, _ string) (Target, error) {
				<-ctx.Done()
				return Target{}, Unavailable("Cha Dash couldn't read it just now: "+ctx.Err().Error()+".", "")
			}},
		// Resolve ignores its context: the listing doesn't wait for it.
		{ID: "x.stuck-resolve", Title: "Stuck", Targets: []string{"vm"}, Tier: T2, Execute: noop,
			Resolve: func(context.Context, string) (Target, error) {
				<-release
				return winGaming, nil
			}},
		// Resolves at once, but its precondition read hangs.
		{ID: "x.slow-check", Title: "Check", Targets: []string{"vm"}, Tier: T1, Execute: noop,
			Resolve: graphResolve(winGaming),
			Preconditions: func(context.Context, Request) error {
				<-release
				return nil
			}},
		// A slow action for hosts isn't listed on a VM: the graph says it
		// doesn't apply.
		{ID: "x.slow-host", Title: "Host", Targets: []string{"host"}, Tier: T1, Execute: noop,
			Resolve: func(context.Context, string) (Target, error) {
				<-release
				return Target{}, ErrNotFound
			}},
	}
	e := newEnv(t, rolePolicy{}, defs...)
	start := time.Now()
	offers, err := e.e.ActionsFor(t.Context(), owner, vm)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took < 300*time.Millisecond || took > 2*time.Second {
		t.Errorf("listing took %v, want about the 300ms deadline", took)
	}
	want := []Offer{
		{ID: "x.slow-check", Label: "Check win-gaming", Tier: T1, Unavailable: listTimedOut},
		{ID: "x.fast", Label: "Fast win-gaming", Tier: T1, Available: true},
		{ID: "x.slow-resolve", Label: "Slow win-gaming", Tier: T1, Unavailable: listTimedOut},
		{ID: "x.stuck-resolve", Label: "Stuck win-gaming", Tier: T2, Unavailable: listTimedOut},
	}
	if len(offers) != len(want) {
		t.Fatalf("offers = %+v", offers)
	}
	for i := range want {
		if offers[i] != want[i] {
			t.Errorf("offers[%d] = %+v, want %+v", i, offers[i], want[i])
		}
	}
}

func TestActionsForFailedReadIsUnavailable(t *testing.T) {
	down := func(context.Context, string) (Target, error) {
		return Target{}, Unavailable("Cha Dash couldn't read Proxmox VE just now: connection refused.", "i_x")
	}
	e := newEnv(t, rolePolicy{}, Def{ID: "x.down", Title: "Snapshot", Targets: []string{"vm"}, Tier: T1, Resolve: down, Execute: noop})
	offers, err := e.e.ActionsFor(t.Context(), owner, vm)
	if err != nil {
		t.Fatal(err)
	}
	want := Offer{ID: "x.down", Label: "Snapshot win-gaming", Tier: T1, Unavailable: "Cha Dash couldn't read Proxmox VE just now: connection refused.", Recipe: "i_x"}
	if len(offers) != 1 || offers[0] != want {
		t.Fatalf("offers = %+v, want %+v", offers, want)
	}
	// A target the graph doesn't know: nothing says the action applies.
	if offers, _ := e.e.ActionsFor(t.Context(), owner, "proxmox:i_x/qemu/999"); len(offers) != 0 {
		t.Fatalf("unknown target's offers = %+v", offers)
	}
}

func TestActionsForConcurrencyCap(t *testing.T) {
	shorten(t, 5*time.Second, 4)
	var running, peak, calls atomic.Int32
	resolve := func(context.Context, string) (Target, error) {
		n := running.Add(1)
		defer running.Add(-1)
		calls.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return winGaming, nil
	}
	var defs []Def
	for i := range 12 {
		defs = append(defs, Def{ID: fmt.Sprintf("x.a%02d", i), Title: fmt.Sprintf("A%02d", i), Targets: []string{"vm"}, Tier: T1, Resolve: resolve, Execute: noop})
	}
	e := newEnv(t, rolePolicy{}, defs...)
	offers, err := e.e.ActionsFor(t.Context(), owner, vm)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 12 || calls.Load() != 12 {
		t.Fatalf("%d offers, %d calls", len(offers), calls.Load())
	}
	for i, o := range offers {
		if o.ID != fmt.Sprintf("x.a%02d", i) || !o.Available {
			t.Errorf("offers[%d] = %+v", i, o)
		}
	}
	if p := peak.Load(); p > 4 || p < 2 {
		t.Errorf("peak concurrency %d, want 2 to 4", p)
	}
}

func TestActionsForMemo(t *testing.T) {
	var reads, checks atomic.Int32
	shared := func(ctx context.Context, target string) (Target, error) {
		return Memo(ctx, "x/i_x/guests", func(context.Context) (Target, error) {
			reads.Add(1)
			time.Sleep(10 * time.Millisecond) // let the others arrive while it reads
			return winGaming, nil
		})
	}
	status := func(ctx context.Context, r Request) error {
		s, err := Memo(ctx, "x/i_x/status/"+r.Target.ID, func(context.Context) (string, error) {
			checks.Add(1)
			return "running", nil
		})
		if err != nil || s != "running" {
			return Precondition("not running")
		}
		return nil
	}
	var defs []Def
	for _, id := range []string{"x.start", "x.stop", "x.reboot", "x.backup", "x.snapshot", "x.console"} {
		defs = append(defs, Def{ID: id, Targets: []string{"vm"}, Tier: T1, Resolve: shared, Preconditions: status, Execute: noop})
	}
	e := newEnv(t, rolePolicy{}, defs...)
	offers, err := e.e.ActionsFor(t.Context(), owner, vm)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 6 {
		t.Fatalf("offers = %+v", offers)
	}
	if reads.Load() != 1 || checks.Load() != 1 {
		t.Fatalf("one listing read the guests %d times and the status %d times, want once each", reads.Load(), checks.Load())
	}
	// A second listing reads again: the memo lasts one listing.
	if _, err := e.e.ActionsFor(t.Context(), owner, vm); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 2 {
		t.Fatalf("a second listing read the guests %d times in all, want 2", reads.Load())
	}
	// Outside a listing, Memo just reads.
	e.mustPlan(owner, "x.start", vm)
	if reads.Load() != 3 {
		t.Fatalf("planning read the guests %d times in all, want 3", reads.Load())
	}
}

// tierPolicy lets everyone run T1 and refuses anything higher.
type tierPolicy struct{}

func (tierPolicy) Authorize(_ context.Context, _ Actor, _, _ string, tier Tier) error {
	if tier > T1 {
		return ErrForbidden
	}
	return nil
}

func TestActionsForChecksRoleBeforeReading(t *testing.T) {
	var mu sync.Mutex
	resolved := map[string]int{}
	counting := func(id string) func(context.Context, string) (Target, error) {
		return func(context.Context, string) (Target, error) {
			mu.Lock()
			resolved[id]++
			mu.Unlock()
			return winGaming, nil
		}
	}
	var checked atomic.Int32
	defs := []Def{
		{ID: "x.t1", Title: "Pause", Targets: []string{"vm"}, Tier: T1, Resolve: counting("x.t1"), Execute: noop},
		{ID: "x.t2", Title: "Reboot", Targets: []string{"vm"}, Tier: T2, Resolve: counting("x.t2"), Execute: noop,
			Preconditions: func(context.Context, Request) error { checked.Add(1); return nil }},
		// TierFor raises it past what the actor may run: resolved (the tier
		// depends on the target), then left out, its precondition unread.
		{ID: "x.raised", Title: "Stop", Targets: []string{"vm"}, Tier: T1, Resolve: counting("x.raised"), Execute: noop,
			TierFor:       func(Target) Tier { return T4 },
			Preconditions: func(context.Context, Request) error { checked.Add(1); return nil }},
	}
	e := newEnv(t, tierPolicy{}, defs...)
	offers, err := e.e.ActionsFor(t.Context(), other, vm)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].ID != "x.t1" {
		t.Fatalf("offers = %+v", offers)
	}
	if resolved["x.t2"] != 0 || checked.Load() != 0 {
		t.Fatalf("a refused action was read: resolved %v, %d precondition checks", resolved, checked.Load())
	}
	if resolved["x.t1"] != 1 || resolved["x.raised"] != 1 {
		t.Fatalf("resolved = %v", resolved)
	}

	// The viewer may run nothing: nothing is read at all.
	clear(resolved)
	e2 := newEnv(t, rolePolicy{}, defs...)
	viewer := Actor{Kind: KindUser, ID: "u_lee", Name: "lee", Role: "viewer", Session: "s_3"}
	if offers, err := e2.e.ActionsFor(t.Context(), viewer, vm); err != nil || len(offers) != 0 {
		t.Fatalf("viewer's offers = %+v, %v", offers, err)
	}
	if len(resolved) != 0 {
		t.Fatalf("the viewer's listing resolved %v", resolved)
	}
}

func TestActionsForCallerCancelled(t *testing.T) {
	shorten(t, 5*time.Second, 4)
	ctx, cancel := context.WithCancel(t.Context())
	e := newEnv(t, rolePolicy{}, Def{ID: "x.slow", Targets: []string{"vm"}, Tier: T1, Execute: noop,
		Resolve: func(ctx context.Context, _ string) (Target, error) {
			cancel()
			<-ctx.Done()
			return Target{}, ctx.Err()
		}})
	if _, err := e.e.ActionsFor(ctx, owner, vm); err == nil {
		t.Fatal("a cancelled listing answered")
	}
}
