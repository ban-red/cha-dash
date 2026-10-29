package runtime

import (
	"context"
	"slices"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
)

// TestResourceInterest: a sheet's metrics:resource/<id> feed lists its
// resource in Interest.Resources while it's open, sorted, once per
// resource however many feeds name it.
func TestResourceInterest(t *testing.T) {
	w := NewWatch()
	changed := w.Changed()
	w.add("metrics:resource/docker:i_d/container/web", 1)
	w.add("metrics:resource/docker:i_d/container/db", 1)
	w.add("metrics:resource/docker:i_d/container/db", 1)
	select {
	case <-changed:
	default:
		t.Fatal("opening a sheet changes the interest")
	}
	i := w.Interest()
	if !i.Watched || !i.Metrics || !slices.Equal(i.Resources, []string{"docker:i_d/container/db", "docker:i_d/container/web"}) {
		t.Fatalf("interest = %+v", i)
	}
	if !i.WantsResource("docker:i_d/container/web") || i.WantsResource("docker:i_d/container/x") {
		t.Fatal("WantsResource")
	}
	w.add("metrics:resource/docker:i_d/container/db", -1)
	if !w.Interest().WantsResource("docker:i_d/container/db") {
		t.Fatal("another tab still has db's sheet open")
	}
	w.add("metrics:resource/docker:i_d/container/db", -1)
	w.add("metrics:resource/docker:i_d/container/web", -1)
	if i := w.Interest(); i.Watched || i.Resources != nil {
		t.Fatalf("after the sheets close: %+v", i)
	}
}

type intervals struct{ plain, forWant time.Duration }

func (intervals) Test(context.Context) integration.TestResult      { return integration.TestResult{} }
func (intervals) Sync(context.Context, integration.Interest) error { return nil }
func (intervals) Close() error                                     { return nil }
func (i intervals) Interval() time.Duration                        { return i.plain }
func (i intervals) IntervalFor(want integration.Interest) time.Duration {
	if len(want.Resources) > 0 {
		return i.forWant
	}
	return i.plain
}

// TestIntervalFor: an instance's IntervalFor wins over Interval, and the
// floor still holds.
func TestIntervalFor(t *testing.T) {
	c := &coordinator{man: integration.Manifest{Poll: integration.Poll{Interval: time.Minute, Idle: 5 * time.Minute}}}
	inst := intervals{plain: 30 * time.Second, forWant: 10 * time.Second}
	watched := integration.Interest{Watched: true}
	if d := c.interval(inst, watched); d != 30*time.Second {
		t.Fatalf("watched = %v", d)
	}
	watched.Resources = []string{"x"}
	if d := c.interval(inst, watched); d != 10*time.Second {
		t.Fatalf("closely watched = %v", d)
	}
	inst.forWant = time.Second
	if d := c.interval(inst, watched); d != integration.MinInterval {
		t.Fatalf("floor = %v", d)
	}
}
