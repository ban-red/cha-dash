package updates

import (
	"context"
	"slices"
	"testing"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/clock"
	"dash.cha.sh/internal/integration"
)

// A rollout scheduled into a window starts when the hub's clock reaches the
// window, with the service's own loop running (Run) and nobody calling
// RunDue: what a test hub does when a test moves its clock
// (docs/plan/17-test-automation.md, H2; 14b's manual test 1).
func TestScheduledRolloutRunsWhenTheHubClockReachesItsWindow(t *testing.T) {
	e := newEnv(t)
	hub := clock.NewTest()
	s, err := New(Options{Store: e.st, Graph: e.g, Inbox: e.inbox, Clock: hub, Refresh: func() int { return 1 }})
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	e.container("sonarr", integration.UpdateAvailable, "sha256:s1")
	f := &fakeUpdates{}
	eng, _ := e.engine(f)
	e.s.users = func(_ context.Context, id string) (auth.UserInfo, error) {
		return auth.UserInfo{ID: id, Username: "op", Role: "operator"}, nil
	}
	now := hub.Now()
	w, err := e.s.CreateWindow(t.Context(), WindowInput{Name: "Tonight", Hosts: []string{"docker:i_a/engine"},
		StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour)}, "u_op")
	if err != nil {
		t.Fatal(err)
	}
	if _, task := rollout(t, eng, operator, `{"window":"`+w.ID+`"}`); len(f.got()) != 0 {
		t.Fatalf("ran at once: %+v %v", task, f.got())
	}
	ros, _ := e.s.Rollouts(t.Context())
	if len(ros) != 1 || ros[0].State != RolloutScheduled {
		t.Fatalf("scheduled = %+v", ros)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go e.s.Run(ctx)

	time.Sleep(200 * time.Millisecond)
	if len(f.got()) != 0 {
		t.Fatal("ran before its window")
	}
	if err := hub.Advance(61 * time.Minute); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ro, _ := e.s.Rollout(t.Context(), ros[0].ID)
		if ro.State == RolloutSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the clock is in the window, and the rollout is %+v", ro)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !slices.Equal(f.got(), []string{"update sonarr"}) {
		t.Fatalf("calls = %v", f.got())
	}
}
