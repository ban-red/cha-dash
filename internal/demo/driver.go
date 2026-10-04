// Package demo runs the Demo Home simulator (sim/) inside the hub: the
// simulated home becomes the Home Graph, its metrics feed the metrics topic,
// and its incidents feed the inbox. It powers `chadash serve --demo`.
//
// The simulated day starts at 14:05 by default, when every incident of the
// default script is open, and then runs in real time. At midnight a new
// day begins with the same seed, so a long-running demo replays the script
// every day instead of going quiet.
package demo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/sim"
)

// DefaultAt is the default start time of day: the moment every incident in
// the default script is open.
const DefaultAt = sim.DefaultAllOpen

// day is the length of one simulated day, after which the script restarts.
const day = 24 * time.Hour

// maxCatchUp bounds how far the driver replays events after a stall. A
// bigger gap (a suspended host, a clock that jumped on a board without an
// RTC) restarts the day at the new time instead of replaying hours.
const maxCatchUp = time.Hour

// Options configure demo mode.
type Options struct {
	Seed int64
	// WallClock makes the simulated clock the real one (CHADASH_DEMO_AT=now).
	// Otherwise the demo starts at At on the simulator's own day,
	// 2026-10-03, and runs in real time from there.
	WallClock bool
	At        time.Duration // time of day, used when !WallClock
	// SampleInterval is how often metrics move; 0 means the simulator's
	// default (30 s). The driver advances once per interval.
	SampleInterval time.Duration
	Clock          func() time.Time // the real clock; nil means time.Now
	Log            *slog.Logger
	// Posture is the security posture the Demo Home's hints are published
	// under, as real instances' are (see SetPosture). Empty publishes the
	// simulator's own severities.
	Posture security.Posture
}

// Driver owns the simulator and keeps the topics in step with it.
type Driver struct {
	t     *topics.Set
	opts  Options
	clock func() time.Time
	log   *slog.Logger

	mu     sync.Mutex
	sim    *sim.Sim
	day    time.Time // midnight UTC that started the current simulated day
	wall0  time.Time // in fixed mode, the real time at which the simulated clock read sim0
	sim0   time.Time
	mirror *mirror
}

// New builds the simulator at its starting moment and loads it into t. It
// replaces whatever the graph holds: in demo mode the simulator owns it.
func New(t *topics.Set, opts Options) (*Driver, error) {
	d := &Driver{t: t, opts: opts, clock: opts.Clock, log: opts.Log, mirror: &mirror{t: t, posture: opts.Posture}}
	if d.clock == nil {
		d.clock = time.Now
	}
	if d.log == nil {
		d.log = slog.New(slog.DiscardHandler)
	}
	if opts.SampleInterval == 0 {
		d.opts.SampleInterval = 30 * time.Second
	}
	if !opts.WallClock && (opts.At < 0 || opts.At >= day) {
		return nil, fmt.Errorf("demo: start time %v is not a time of day", opts.At)
	}
	wall := d.clock()
	d.wall0, d.sim0 = wall, sim.DefaultStart.Add(opts.At)
	if err := d.reset(d.simTime(wall)); err != nil {
		return nil, err
	}
	return d, nil
}

// simTime is the simulated time at real time wall.
func (d *Driver) simTime(wall time.Time) time.Time {
	if d.opts.WallClock {
		return wall.UTC().Round(0).Truncate(time.Second)
	}
	// Monotonic: a wall-clock step doesn't move the demo.
	return d.sim0.Add(wall.Sub(d.wall0)).Truncate(time.Second)
}

// reset starts a fresh simulator whose day contains at, skips to at and
// loads it into the topics. The caller holds d.mu, or d isn't shared yet.
func (d *Driver) reset(at time.Time) error {
	start := at.UTC().Truncate(day)
	s, err := sim.New(d.opts.Seed, sim.WithStart(start), sim.WithSampleInterval(d.opts.SampleInterval))
	if err != nil {
		return err
	}
	s.Skip(at.Sub(start))
	if err := d.mirror.load(s.Snapshot()); err != nil {
		return err
	}
	d.sim, d.day = s, start
	return nil
}

// SetPosture changes the security posture and re-publishes the Demo Home's
// security findings under it before returning.
func (d *Driver) SetPosture(p security.Posture) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.mirror.setPosture(p); err != nil {
		d.log.Error("demo: re-publishing the security findings failed", "err", err)
	}
}

// Seed is the simulator's seed.
func (d *Driver) Seed() int64 { return d.opts.Seed }

// Now is the simulated time right now. The topics are at most one sample
// interval behind it.
func (d *Driver) Now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.simTime(d.clock())
}

// Run advances the simulator in real time until ctx ends, waking at each
// sample tick.
func (d *Driver) Run(ctx context.Context) {
	timer := time.NewTimer(d.untilNextTick())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		d.Step()
		timer.Reset(d.untilNextTick())
	}
}

// untilNextTick is how long until the simulator's next sample tick, which
// falls on a multiple of the sample interval since the Unix epoch.
func (d *Driver) untilNextTick() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	every := int64(d.opts.SampleInterval / time.Second)
	now := d.simTime(d.clock())
	next := time.Unix((now.Unix()/every+1)*every, 0)
	// A hair late rather than early, so the tick is inside the step.
	return next.Sub(now) + 5*time.Millisecond
}

// Step brings the simulator up to the current time and publishes what
// changed.
func (d *Driver) Step() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.step()
}

// step is Step with d.mu held.
func (d *Driver) step() {
	target := d.simTime(d.clock())
	now := d.sim.Now()
	switch {
	case !target.After(now):
		return
	case target.Sub(now) > maxCatchUp:
		d.log.Warn("demo: clock jumped; restarting the simulated day", "from", now, "to", target)
		d.mustReset(target)
		return
	}
	for {
		end := d.day.Add(day)
		if target.Before(end) {
			break
		}
		d.apply(d.sim.AdvanceTo(end))
		if !d.mustReset(end) {
			return
		}
		d.log.Info("demo: a new simulated day begins", "day", end.Format(time.DateOnly))
	}
	d.apply(d.sim.AdvanceTo(target))
}

// apply publishes events. If they don't apply cleanly, the topics are
// rebuilt from a fresh snapshot: clients then get a larger diff, not a
// wrong one.
func (d *Driver) apply(events []sim.Event) {
	if err := d.mirror.apply(events, d.sim.Now()); err != nil {
		d.log.Error("demo: events didn't apply; reloading the snapshot", "err", err)
		if err := d.mirror.load(d.sim.Snapshot()); err != nil {
			d.log.Error("demo: reload failed", "err", err)
		}
	}
}

// mustReset is reset for the running driver. Only invalid options can make
// it fail, and New already succeeded with the same ones, so a failure is
// logged and the old simulator keeps going.
func (d *Driver) mustReset(at time.Time) bool {
	if err := d.reset(at); err != nil {
		d.log.Error("demo: could not restart the simulator", "err", err)
		return false
	}
	return true
}

// ErrUnknownIncident is returned by Fix for an incident the script doesn't have.
var ErrUnknownIncident = sim.ErrUnknownIncident

// Fix applies an open incident's suggested action now, as if the user had
// pressed it. The incident then resolves the way its scenario describes.
// Errors other than ErrUnknownIncident mean it can't be fixed right now:
// it isn't open yet, is already resolved, or is already being fixed.
func (d *Driver) Fix(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.step() // so the fix lands at the right moment
	events, err := d.sim.Fix(id)
	if err != nil {
		return fixError{err}
	}
	d.apply(events)
	return nil
}

// CanFix reports whether Fix would accept the incident now: nil, or the
// error Fix would return. It changes nothing.
func (d *Driver) CanFix(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.step()
	if err := d.sim.CanFix(id); err != nil {
		return fixError{err}
	}
	return nil
}

// fixError turns the simulator's refusals into sentences for the API.
type fixError struct{ err error }

func (e fixError) Error() string {
	switch {
	case errors.Is(e.err, sim.ErrNotOpen):
		return "That incident isn't open yet."
	case errors.Is(e.err, sim.ErrResolved):
		return "That incident is already resolved."
	case errors.Is(e.err, sim.ErrFixing):
		return "That incident is already being fixed."
	}
	return strings.TrimPrefix(e.err.Error(), "sim: ")
}

func (e fixError) Unwrap() error { return e.err }
