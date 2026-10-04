package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"dash.cha.sh/internal/integration"
)

// Scheduling bounds.
const (
	// MaxBackoff caps the exponential backoff on transient errors (or the
	// idle interval, when that's longer).
	MaxBackoff = 10 * time.Minute
	// RepairRetry is how often an instance that needs repair tries again, so
	// fixing the upstream (re-enabling a token) clears the repair on its own.
	RepairRetry = 5 * time.Minute
	// connectTimeout bounds Integration.Connect, which shouldn't do I/O.
	connectTimeout = 30 * time.Second
	// repairID is the local incident ID of an instance's repair.
	repairID = "repair"
)

// coordinator runs one instance: connect, then sync on a jittered schedule
// at the watched or idle interval, back off on errors, turn repair-class
// errors into an inbox Repair and clear it on recovery, and run the
// optional subscription while someone is watching.
type coordinator struct {
	name   string
	integ  integration.Integration
	man    integration.Manifest // as registered: Poll has its defaults
	cfg    integration.Config
	deps   func() (integration.Deps, func()) // fresh deps for one Connect; the func releases them
	emit   *Emitter
	st     *state
	watch  *Watch
	log    *slog.Logger
	red    redactor
	jitter func(time.Duration) time.Duration // nil: ±10 %

	cancel context.CancelFunc
	done   chan struct{}
}

func (c *coordinator) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.done = make(chan struct{})
	if c.jitter == nil {
		c.jitter = jitter
	}
	go c.run(ctx)
}

// stop ends the coordinator and waits for it.
func (c *coordinator) stop() {
	c.cancel()
	<-c.done
}

// jitter spreads d by ±10 %, so instances started together drift apart.
func jitter(d time.Duration) time.Duration {
	if d < 10 {
		return d
	}
	return d - d/10 + rand.N(d/5)
}

func (c *coordinator) run(ctx context.Context) {
	defer close(c.done)
	c.publishSelf()

	var (
		inst    integration.Instance
		release = func() {}
		sub     *subscription
		last    time.Time // start of the last attempt
		backoff bool      // next was set by a failure
	)
	defer func() {
		sub.stop()
		if inst != nil {
			if err := safeClose(inst); err != nil {
				c.log.Debug("closing the instance failed", "err", c.red.scrub(err.Error()))
			}
		}
		release()
	}()

	interest := c.watch.Interest()
	first := min(2*time.Second, c.interval(nil, interest)/10)
	next := time.Now().Add(time.Duration(rand.Int64N(int64(first) + 1)))
	c.schedule(nil, interest, next)
	for {
		changed := c.watch.Changed()
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-changed:
			timer.Stop()
			interest = c.watch.Interest()
			if inst != nil {
				sub = c.subscribe(ctx, inst, interest, sub)
			}
			// Re-plan from the last attempt at the new rate. A tab opening
			// makes a stale instance sync now; backoff keeps its own pace.
			if !backoff && !last.IsZero() {
				next = last.Add(c.jitter(c.interval(inst, interest)))
			}
			c.schedule(inst, interest, next)
			continue
		case <-timer.C:
		}

		interest = c.watch.Interest()
		start := time.Now()
		if inst == nil {
			deps, done := c.deps()
			in, err := c.connect(ctx, deps)
			if ctx.Err() != nil {
				done()
				return
			}
			if err != nil {
				done()
				class := c.fail(start, time.Since(start), err)
				last, backoff = start, true
				next = start.Add(c.failureDelay(class, nil, interest))
				c.schedule(nil, interest, next)
				continue
			}
			inst, release = in, done
		}
		sub = c.subscribe(ctx, inst, interest, sub)

		err := c.sync(ctx, inst, interest)
		if ctx.Err() != nil {
			return // stopping; a cancelled sync isn't a failure
		}
		last = start
		if err != nil {
			class := c.fail(start, time.Since(start), err)
			backoff = true
			next = start.Add(c.failureDelay(class, inst, interest))
		} else {
			c.succeed(start, time.Since(start))
			backoff = false
			next = start.Add(c.jitter(c.interval(inst, interest)))
		}
		c.schedule(inst, interest, next)
	}
}

// interval is the time between syncs for the current interest.
func (c *coordinator) interval(inst integration.Instance, want integration.Interest) time.Duration {
	iv := c.man.Poll.Interval
	if i, ok := inst.(integration.Intervaler); ok {
		if d := i.Interval(); d > 0 {
			iv = max(d, integration.MinInterval)
		}
	}
	if !want.Watched && !c.man.Poll.Background {
		return max(c.man.Poll.Idle, iv)
	}
	return iv
}

// failureDelay is the wait after a failed attempt: exponential for
// transient errors, slow and steady for ones that need the user.
func (c *coordinator) failureDelay(class integration.Class, inst integration.Instance, want integration.Interest) time.Duration {
	base := c.interval(inst, want)
	if class != integration.Transient {
		return c.jitter(max(base, RepairRetry))
	}
	n := min(c.st.snapshot().Failures, 16)
	d := base << n
	if d <= 0 || d > max(MaxBackoff, base) {
		d = max(MaxBackoff, base)
	}
	return c.jitter(d)
}

func (c *coordinator) schedule(inst integration.Instance, want integration.Interest, next time.Time) {
	c.st.schedule(want.Watched, c.interval(inst, want), next)
}

// panicError is an integration that panicked. It's a bug, reported as
// "internal", and the hub keeps running.
type panicError struct{ v any }

func (e *panicError) Error() string { return fmt.Sprintf("the integration crashed: %v", e.v) }

func (c *coordinator) connect(ctx context.Context, deps integration.Deps) (inst integration.Instance, err error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	defer func() {
		if v := recover(); v != nil {
			inst, err = nil, &panicError{v}
		}
	}()
	return c.integ.Connect(ctx, c.cfg, deps)
}

func (c *coordinator) sync(ctx context.Context, inst integration.Instance, want integration.Interest) (err error) {
	ctx, cancel := context.WithTimeout(ctx, c.man.Poll.Timeout)
	defer cancel()
	defer func() {
		if v := recover(); v != nil {
			err = &panicError{v}
		}
	}()
	return inst.Sync(ctx, want)
}

func safeClose(inst integration.Instance) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &panicError{v}
		}
	}()
	return inst.Close()
}

// classify is integration.Classify plus the runtime's own errors.
func classify(err error) (string, integration.Class, string) {
	var pe *panicError
	if errors.As(err, &pe) {
		return integration.CodeInternal, integration.Transient, pe.Error()
	}
	if errors.Is(err, ErrResponseTooLarge) {
		return integration.CodeUpstream, integration.Transient, "the response was larger than Cha Dash accepts"
	}
	return integration.Classify(err)
}

func (c *coordinator) fail(start time.Time, d time.Duration, err error) integration.Class {
	code, class, msg := classify(err)
	msg = c.red.scrub(msg)
	before := c.st.status()
	c.st.failure(start.UTC().Truncate(time.Second), d, code, class, msg)
	c.emit.MarkStale()
	if class == integration.NeedsRepair {
		if rerr := c.raiseRepair(code, msg, start); rerr != nil {
			c.log.Error("raising a repair failed", "err", rerr)
		}
	}
	if after := c.st.status(); after != before {
		c.log.Warn("integration instance is "+after, "code", code, "err", msg)
	} else {
		c.log.Debug("sync failed", "code", code, "err", msg)
	}
	c.publishSelf()
	return class
}

func (c *coordinator) succeed(start time.Time, d time.Duration) {
	before := c.st.status()
	c.st.success(start.UTC().Truncate(time.Second), d)
	if c.emit.open(repairID) {
		msg := fmt.Sprintf("Cha Dash could read %s again at %s.", c.name, hhmm(start))
		if err := c.emit.Resolve(repairID, msg); err != nil {
			c.log.Error("resolving a repair failed", "err", err)
		}
	}
	if after := c.st.status(); after != before {
		c.log.Info("integration instance is "+after, "took", d.Round(time.Millisecond))
	}
	c.publishSelf()
}

func hhmm(t time.Time) string { return t.Local().Format("15:04") }

// raiseRepair opens (or updates) the instance's Repair for a repair-class
// error, in the inbox's voice: symptom first, a specific time, calm.
func (c *coordinator) raiseRepair(code, msg string, at time.Time) error {
	self := c.emit.Self()
	opened := at
	if t, ok := c.emit.openedAt(repairID); ok {
		opened = t
	}
	since := hhmm(opened)
	in := integration.Incident{
		ID: repairID, Class: integration.ClassRepair, Severity: integration.SeverityAttention,
		Target: self, Facts: map[string]any{"code": code},
		Causes: []integration.Cause{{Text: msg, Resource: self, At: opened.UTC().Truncate(time.Second)}},
		Suggested: integration.Action{
			ID: "integration.reconnect", Label: "Reconnect " + c.name, Tier: "T0", Target: self,
			Detail: "Enter a new credential in the integration's settings. It goes into the vault.",
		},
		ResolvesWhen: fmt.Sprintf("Resolves when Cha Dash can read %s again.", c.name),
	}
	switch code {
	case integration.CodeAuth:
		in.Scenario, in.Phase = "auth-rejected", "rejected"
		in.Title = c.name + " rejected its credential"
		in.Summary = fmt.Sprintf("%s has rejected Cha Dash's credential since %s. Its tiles show values as of then.", c.name, since)
	case integration.CodeTLSPinMismatch:
		in.Scenario, in.Phase = "tls-pin-mismatch", "refused"
		in.Title = c.name + " presented a different certificate"
		in.Summary = fmt.Sprintf("Since %s, %s has presented a certificate that isn't the one Cha Dash pinned, so Cha Dash stopped talking to it.", since, c.name)
		in.Suggested = integration.Action{ID: "integration.tls", Label: "Review the certificate", Tier: "T0", Target: self,
			Detail: "Compare the new fingerprint with the one the server shows, then pin it."}
	case integration.CodeTLSUntrusted:
		in.Scenario, in.Phase = "tls-untrusted", "refused"
		in.Title = c.name + "'s certificate isn't trusted"
		in.Summary = fmt.Sprintf("Since %s, %s has presented a certificate Cha Dash can't verify.", since, c.name)
		in.Suggested = integration.Action{ID: "integration.tls", Label: "Review the certificate", Tier: "T0", Target: self,
			Detail: "Trust it on first use, or pin its fingerprint."}
	case integration.CodeAPIChanged, integration.CodeUnsupportedVersion:
		in.Scenario, in.Phase = "api-changed", "unsupported"
		in.Title = c.name + "'s API changed"
		in.Summary = fmt.Sprintf("Since %s, %s has answered in a way this integration doesn't support. Its tiles show values as of then.", since, c.name)
		in.Suggested = integration.Action{ID: "integration.settings", Label: "Open the integration", Tier: "T0", Target: self}
	default:
		in.Scenario, in.Phase = "needs-repair", "blocked"
		in.Title = "Cha Dash can't read " + c.name
		in.Summary = fmt.Sprintf("Since %s, Cha Dash hasn't been able to read %s.", since, c.name)
	}
	for _, id := range c.emit.owns(20) {
		in.BlastRadius = append(in.BlastRadius, integration.Impact{Resource: id, Via: "monitors", Depth: 1})
	}
	return c.emit.Raise(in)
}

// publishSelf writes the instance's own resource from its status.
func (c *coordinator) publishSelf() {
	snap := c.st.snapshot()
	attrs := map[string]any{"type": c.man.ID, "tier": string(c.man.Engine), "quality": string(c.man.Tier), "status": snap.Status}
	if snap.Version != "" {
		attrs["upstream_version"] = snap.Version
	}
	r := integration.Resource{Kind: "integration", Name: c.name, Attrs: attrs, Caps: capsOf(c.man)}
	reason := ""
	if snap.LastErr != nil {
		reason = snap.LastErr.Message
	}
	switch snap.Status {
	case StatusOK:
		r.State, r.Health.Status = "connected", integration.HealthOK
	case StatusDegraded:
		switch {
		case snap.LastOK.IsZero() && snap.Failures == 0:
			r.State, r.Health.Status = "connecting", integration.HealthUnknown
		case snap.Failures > 0:
			r.State, r.Health.Status, r.Health.Reason = "connected", integration.HealthAttention, reason
		default:
			r.State, r.Health.Status = "connected", integration.HealthAttention
			if len(snap.Warnings) > 0 {
				r.Health.Reason = snap.Warnings[0].Message
			}
		}
	case StatusRepair:
		r.State, r.Health.Status, r.Health.Reason = "error", integration.HealthRepair, reason
	default:
		r.State, r.Health.Status, r.Health.Reason = "error", integration.HealthAttention, reason
	}
	if err := c.emit.PublishSelf(r); err != nil {
		c.log.Error("publishing the instance's resource failed", "err", err)
	}
}

func capsOf(m integration.Manifest) []string {
	out := make([]string, len(m.Capabilities))
	for i, c := range m.Capabilities {
		out[i] = string(c)
	}
	return out
}

// subscription is a running Subscribe loop.
type subscription struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *subscription) stop() {
	if s == nil {
		return
	}
	s.cancel()
	<-s.done
}

// subscribe starts or stops the instance's push channel to match the
// interest: it runs only while someone is watching.
func (c *coordinator) subscribe(ctx context.Context, inst integration.Instance, want integration.Interest, cur *subscription) *subscription {
	sub, ok := inst.(integration.Subscriber)
	switch {
	case !ok:
		return nil
	case want.Watched && cur == nil:
		sctx, cancel := context.WithCancel(ctx)
		s := &subscription{cancel: cancel, done: make(chan struct{})}
		go c.subscribeLoop(sctx, sub, s.done)
		return s
	case !want.Watched && cur != nil:
		cur.stop()
		return nil
	}
	return cur
}

func (c *coordinator) subscribeLoop(ctx context.Context, sub integration.Subscriber, done chan struct{}) {
	defer close(done)
	delay := 5 * time.Second
	for {
		start := time.Now()
		err := func() (err error) {
			defer func() {
				if v := recover(); v != nil {
					err = &panicError{v}
				}
			}()
			return sub.Subscribe(ctx, c.emit)
		}()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.log.Debug("subscription ended", "err", c.red.scrub(err.Error()))
		}
		if time.Since(start) > time.Minute {
			delay = 5 * time.Second
		}
		t := time.NewTimer(c.jitter(delay))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		delay = min(delay*2, RepairRetry)
	}
}
