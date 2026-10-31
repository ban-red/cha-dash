package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/client"

	"dash.cha.sh/internal/nodeproto"
)

// UpdateGate is how long an updated container has to report healthy, or
// with no healthcheck to keep running, before it's rolled back.
const UpdateGate = 120 * time.Second

// settle is how long a container without a healthcheck must keep running,
// without a restart, to pass: one that crashes at start would otherwise
// pass in the moment it runs.
const settle = 10 * time.Second

// The outcomes of container.update, as the stacks module's self-update
// names them.
const (
	outcomeDeployed   = "deployed"
	outcomeUnchanged  = "unchanged"
	outcomeRolledBack = "rolled_back"
	outcomeStuck      = "stuck"
)

// update pulls a container's tag and, when it points at another image,
// recreates the container on the digest it pulled, holds it to the gate,
// and puts the previous image back if it fails (docker.update). It's
// self-update's sequence for any container the policy lets the node write:
// never the hub's or the node's own.
func (m *Module) update(ctx context.Context, req json.RawMessage, emit func(json.RawMessage)) (json.RawMessage, error) {
	t, err := decodeTarget(req)
	if err != nil {
		return nil, err
	}
	res, err := m.writable(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	old := res.Container
	name := strings.TrimPrefix(old.Name, "/")
	image := ""
	if old.Config != nil {
		image = old.Config.Image
	}
	tagged, why := tagOf(image)
	if why != "" {
		return nil, nodeproto.Errorf(nodeproto.CodeConflict, "%s can't be updated. %s", name, why)
	}
	tag := reference.FamiliarString(tagged)
	if old.HostConfig != nil && old.HostConfig.AutoRemove {
		return nil, nodeproto.Errorf(nodeproto.CodeConflict, "%s removes itself when it stops (--rm), so it can't be updated in place", name)
	}
	out := map[string]string{"name": name, "image": tag}
	finish := func(outcome, reason, id, current string) (json.RawMessage, error) {
		out["outcome"], out["reason"], out["id"], out["current"] = outcome, reason, id, current
		return reply(out)
	}
	line := func(s string) { step(emit, s) }

	// What to put back: the image it runs now, by digest when a registry
	// knows it, else by ID.
	prev := old.Image
	if img, err := m.cli.ImageInspect(ctx, old.Image); err == nil {
		for _, rd := range img.RepoDigests {
			if d, err := reference.ParseNormalizedNamed(rd); err == nil && d.Name() == tagged.Name() {
				if dg, ok := d.(reference.Digested); ok {
					prev = tag + "@" + dg.Digest().String()
					break
				}
			}
		}
	}
	out["previous"] = prev

	line("Pull " + tag)
	pulled, err := m.PullImage(ctx, tag, line)
	if err != nil {
		return nil, err
	}
	_, sum, _ := strings.Cut(pulled, "@")
	pinned := tag + "@" + sum
	if _, was, ok := strings.Cut(prev, "@"); ok && was == sum {
		return finish(outcomeUnchanged, name+" already runs the newest "+tag+".", old.ID, prev)
	}

	line("Recreate " + name + " with " + pinned)
	newID, err := m.RecreateImage(ctx, old.ID, pinned, line)
	if err != nil {
		return nil, err
	}
	wctx, cancel := waitCtx(ctx)
	defer cancel()
	gateErr := m.gate(wctx, newID, line)
	if gateErr == nil {
		line("Updated and healthy")
		return finish(outcomeDeployed, "", newID, pinned)
	}
	line("Health gate failed: " + gateErr.Error())
	line("Roll back to " + prev)
	backID, err := m.RecreateImage(wctx, newID, prev, line)
	if err != nil {
		return finish(outcomeStuck, fmt.Sprintf("%s; and recreating it with %s failed: %v", gateErr, prev, err), newID, pinned)
	}
	if err := m.gate(wctx, backID, line); err != nil {
		return finish(outcomeStuck, fmt.Sprintf("%s; and the rollback to %s failed too: %v", gateErr, prev, err), backID, prev)
	}
	line("Rolled back and healthy")
	return finish(outcomeRolledBack, fmt.Sprintf("%s. Rolled back to %s on its own, and it's healthy again.", gateErr, prev), backID, prev)
}

// gate waits up to UpdateGate for the container id to report healthy, or,
// without a healthcheck, to keep running for settle without a restart. It
// fails at once if the container exits, restarts or turns unhealthy.
func (m *Module) gate(ctx context.Context, id string, line func(string)) error {
	line(fmt.Sprintf("Health gate: healthy within %s", UpdateGate))
	start := m.now()
	var runningSince time.Time
	restarts := -1
	for {
		res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return fmt.Errorf("the container is gone: %w", mapErr(err))
		}
		c := res.Container
		name := strings.TrimPrefix(c.Name, "/")
		if c.State == nil {
			return fmt.Errorf("%s has no state", name)
		}
		switch {
		case c.State.Status == "exited" || c.State.Status == "dead":
			return fmt.Errorf("%s exited with code %d", name, c.State.ExitCode)
		case c.State.Restarting || c.State.Status == "restarting":
			return fmt.Errorf("%s is restarting", name)
		case restarts >= 0 && c.RestartCount > restarts:
			return fmt.Errorf("%s restarted", name)
		case c.State.Health != nil && c.State.Health.Status == "unhealthy":
			return fmt.Errorf("%s is unhealthy", name)
		case c.State.Health != nil && c.State.Health.Status == "healthy":
			return nil
		case c.State.Health == nil && c.State.Running:
			if runningSince.IsZero() {
				runningSince = m.now()
			}
			if m.now().Sub(runningSince) >= settle {
				return nil
			}
		}
		restarts = c.RestartCount
		if m.now().Sub(start) >= UpdateGate {
			return fmt.Errorf("%s didn't report healthy within %s", name, UpdateGate)
		}
		if err := m.sleep(ctx, m.poll); err != nil {
			return err
		}
	}
}

// sleepCtx waits d, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
