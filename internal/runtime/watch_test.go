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

// TestRecordInterest: while the hub keeps history, nobody watching still
// reads like a board on screen, every kind and no sheet, at RecordEvery.
// A tab narrowed to a kind gets every kind, and turning history off goes
// back to idle.
func TestRecordInterest(t *testing.T) {
	w := NewWatch()
	changed := w.Changed()
	w.SetRecord(true)
	select {
	case <-changed:
	default:
		t.Fatal("recording changes the interest")
	}
	i := w.Interest()
	if !i.Watched || !i.Metrics || !i.Record || i.Kinds != nil || i.Resources != nil {
		t.Fatalf("recording, nobody watching = %+v", i)
	}
	w.add("graph:kind/container", 1)
	if i := w.Interest(); i.Record || !i.Watched || i.Kinds != nil {
		t.Fatalf("a tab on containers while recording = %+v", i)
	}
	w.add("graph:kind/container", -1)
	w.SetRecord(false)
	if i := w.Interest(); i.Watched || i.Record {
		t.Fatalf("history off, nobody watching = %+v", i)
	}

	c := &coordinator{man: integration.Manifest{Poll: integration.Poll{Interval: 30 * time.Second, Idle: 5 * time.Minute}}}
	for _, tc := range []struct {
		want integration.Interest
		d    time.Duration
	}{
		{integration.Interest{}, 5 * time.Minute},
		{integration.Interest{Watched: true, Metrics: true, Record: true}, RecordEvery},
		{integration.Interest{Watched: true}, 30 * time.Second},
	} {
		if d := c.interval(nil, tc.want); d != tc.d {
			t.Errorf("interval for %+v = %v, want %v", tc.want, d, tc.d)
		}
	}
	// An instance slower than a minute keeps its own pace; uptime checks keep theirs.
	slow := intervals{plain: 2 * time.Minute}
	if d := c.interval(slow, integration.Interest{Watched: true, Record: true}); d != 2*time.Minute {
		t.Fatalf("slow instance while recording = %v", d)
	}
	c.man.Poll.Background = true
	if d := c.interval(nil, integration.Interest{Watched: true, Record: true}); d != 30*time.Second {
		t.Fatalf("background instance while recording = %v", d)
	}
}
