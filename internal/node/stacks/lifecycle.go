package stacks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/stackproto"
)

// A whole stack's lifecycle (plan 16, wave 32a): stop, start, restart,
// down and remove, through the Compose SDK as deploys are. Each takes the
// stack's lock, refuses Cha Dash's own stack and any stack with a container
// docker.protect names (a stack verb touches every container), and checks
// ExpectHead like a deploy. Stopped and down are states, never drift:
// drift is only the file on disk differing from its last commit.

// lifecycleTarget checks a lifecycle verb may act on a stack: a valid name,
// not protected, managed, at the head the hub planned against. It returns
// the stack, its committed file and its containers now.
func (m *Module) lifecycleTarget(ctx context.Context, name, expectHead, doing string) (stackRef, string, []Container, error) {
	s, err := m.resolve(name)
	if err != nil {
		return s, "", nil, err
	}
	cs, err := m.docker.Containers(ctx, name)
	if err != nil {
		return s, "", nil, fmt.Errorf("list %s's containers: %w", name, err)
	}
	committed := ""
	if s.managed {
		if committed, _, err = m.repo.file(nil, s.tracked); err != nil {
			return s, "", nil, err
		}
	}
	if why := m.protectedReason(name, []string{committed}, cs); why != "" {
		return s, "", nil, stackproto.Errorf(stackproto.CodeProtected, "%s", why)
	}
	if !s.managed {
		if s.exists || len(cs) > 0 {
			return s, "", nil, stackproto.Errorf(stackproto.CodeNotManaged, "%s isn't managed by Cha Dash yet. Adopt it first.", name)
		}
		return s, "", nil, stackproto.Errorf(stackproto.CodeNotFound, "There's no stack called %s on this node.", name)
	}
	head, err := m.headOf(s)
	if err != nil {
		return s, "", nil, err
	}
	if expectHead != "" && expectHead != head {
		return s, "", nil, stackproto.Errorf(stackproto.CodeConflict, "%s changed since this was planned (now at %s). Plan it again.", name, short(head))
	}
	var hit []string
	for _, c := range cs {
		if m.protect(c.Name, c.Service) {
			hit = append(hit, strings.TrimPrefix(c.Name, "/"))
		}
	}
	if len(hit) > 0 {
		slices.Sort(hit)
		return s, "", nil, stackproto.Errorf(stackproto.CodeProtected, "This would %s %s, which this node's policy protects (docker.protect), so the node refuses it.", doing, strings.Join(slices.Compact(hit), ", "))
	}
	return s, committed, cs, nil
}

// after is the stack's state once a verb is done.
func (m *Module) after(ctx context.Context, name string) stackproto.LifecycleResult {
	res := stackproto.LifecycleResult{Name: name, Services: []stackproto.Service{}}
	cs, ok := m.containers(ctx, name)
	res.Status, _, _ = status(cs)
	if !ok {
		res.Status = stackproto.StatusUnknown
	}
	slices.SortFunc(cs, func(a, b Container) int { return strings.Compare(a.Service+a.Name, b.Service+b.Name) })
	for _, c := range cs {
		res.Services = append(res.Services, stackproto.Service{Name: c.Service, Container: strings.TrimPrefix(c.Name, "/"), Image: c.Image, State: c.State, Health: healthOf(c), Restarts: c.RestartCount})
	}
	return res
}

// volumes are the project's named volumes; a Docker failure reads as none.
func (m *Module) volumes(ctx context.Context, name string) []string {
	vs, err := m.docker.Volumes(ctx, name)
	if err != nil {
		m.log.Warn("stacks: couldn't list volumes", "stack", name, "err", err)
		return []string{}
	}
	if vs == nil {
		vs = []string{}
	}
	return vs
}

// ── stack.stop, stack.start, stack.restart ───────────────────────────────

func (m *Module) stop(ctx context.Context, req stackproto.LifecycleRequest, em emitter) (stackproto.LifecycleResult, error) {
	unlock, err := m.lock(req.Name)
	if err != nil {
		return stackproto.LifecycleResult{}, err
	}
	defer unlock()
	if _, _, _, err := m.lifecycleTarget(ctx, req.Name, req.ExpectHead, "stop"); err != nil {
		return stackproto.LifecycleResult{}, err
	}
	em.step("Stop " + req.Name)
	if err := m.docker.Stop(ctx, req.Name, eventLine(em)); err != nil {
		return stackproto.LifecycleResult{}, fmt.Errorf("stop %s: %w", req.Name, err)
	}
	m.settle(ctx, req.Name, func(cs []Container) bool {
		return !slices.ContainsFunc(cs, func(c Container) bool { return c.State == "running" || c.State == "restarting" })
	})
	return m.after(ctx, req.Name), nil
}

// settleFor bounds how long a lifecycle verb waits for Docker's list to
// show what the verb did.
const settleFor = 30 * time.Second

// settle waits, up to settleFor, until the stack's containers as Docker
// lists them satisfy done: Compose can return while the engine still
// lists a container it's stopping (seen on a CI runner's engine), and the
// result should say what is, not what was.
func (m *Module) settle(ctx context.Context, name string, done func([]Container) bool) {
	start := m.now()
	for {
		cs, ok := m.containers(ctx, name)
		if !ok || done(cs) || m.now().Sub(start) >= settleFor {
			return
		}
		if m.sleep(ctx, m.poll) != nil {
			return
		}
	}
}

func (m *Module) start(ctx context.Context, req stackproto.LifecycleRequest, em emitter) (stackproto.LifecycleResult, error) {
	unlock, err := m.lock(req.Name)
	if err != nil {
		return stackproto.LifecycleResult{}, err
	}
	defer unlock()
	_, _, cs, err := m.lifecycleTarget(ctx, req.Name, req.ExpectHead, "start")
	if err != nil {
		return stackproto.LifecycleResult{}, err
	}
	if len(cs) == 0 {
		return stackproto.LifecycleResult{}, notDeployed(req.Name)
	}
	em.step("Start " + req.Name)
	if err := m.docker.Start(ctx, req.Name, eventLine(em)); err != nil {
		return stackproto.LifecycleResult{}, fmt.Errorf("start %s: %w", req.Name, err)
	}
	return m.after(ctx, req.Name), nil
}

func notDeployed(name string) error {
	return stackproto.Errorf(stackproto.CodeNotDeployed, "%s has no containers to start: it was never deployed, or it was taken down. Deploy it instead.", name)
}

func (m *Module) restart(ctx context.Context, req stackproto.LifecycleRequest, em emitter) (stackproto.LifecycleResult, error) {
	unlock, err := m.lock(req.Name)
	if err != nil {
		return stackproto.LifecycleResult{}, err
	}
	defer unlock()
	_, committed, cs, err := m.lifecycleTarget(ctx, req.Name, req.ExpectHead, "restart")
	if err != nil {
		return stackproto.LifecycleResult{}, err
	}
	if len(cs) == 0 {
		return stackproto.LifecycleResult{}, notDeployed(req.Name)
	}
	em.step("Restart " + req.Name)
	if err := m.docker.Restart(ctx, req.Name, eventLine(em)); err != nil {
		return stackproto.LifecycleResult{}, fmt.Errorf("restart %s: %w", req.Name, err)
	}
	meta := metaOf(committed)
	unhealthy := m.restartGate(ctx, req.Name, pick(meta.wait, m.wait), pick(meta.soak, m.soak), em)
	res := m.after(ctx, req.Name)
	healthy := len(unhealthy) == 0
	res.Healthy, res.Unhealthy = &healthy, unhealthy
	if healthy {
		em.step("Restarted and healthy")
	} else {
		em.step("Restarted, but " + strings.Join(unhealthy, ", ") + " failed the health gate. Nothing was rolled back.")
	}
	return res, nil
}

// restartGate is the deploy's health gate after a restart, reporting only:
// every container must come back running (and healthy, where it has a
// healthcheck) within wait, as up --wait asks of a deploy, then stay so
// for soak. It returns the services that failed, sorted; none is healthy.
func (m *Module) restartGate(ctx context.Context, name string, wait, soak time.Duration, em emitter) []string {
	em.step(fmt.Sprintf("Wait for the services (up to %s), then health gate: every container must stay up for %s", wait, soak))
	start := m.now()
	var readyAt time.Time
	restarts := map[string]int{}
	failed := map[string]bool{}
	report := func() []string {
		out := sortedKeys(failed)
		for _, s := range out {
			em.line("%s failed the health gate", s)
		}
		return out
	}
	for {
		cs, err := m.docker.Containers(ctx, name)
		if err != nil {
			em.line("Couldn't read the containers: %v", err)
			return []string{name}
		}
		ready := len(cs) > 0
		for _, c := range cs {
			switch {
			case c.State == "exited" || c.State == "dead" || c.State == "restarting" || c.Health == "unhealthy":
				failed[c.Service] = true
			case c.State != "running" || c.Health == "starting":
				ready = false
			}
			if first, seen := restarts[c.ID]; !seen {
				restarts[c.ID] = c.RestartCount
			} else if c.RestartCount > first {
				failed[c.Service] = true
			}
		}
		now := m.now()
		switch {
		case len(failed) > 0:
			return report()
		case readyAt.IsZero() && ready:
			readyAt = now
		case readyAt.IsZero() && now.Sub(start) >= wait:
			// Still not up: the services that aren't are the ones that failed.
			for _, c := range cs {
				if c.State != "running" || c.Health == "starting" {
					failed[c.Service] = true
				}
			}
			if len(failed) == 0 {
				failed[name] = true // no containers at all
			}
			return report()
		}
		if !readyAt.IsZero() && now.Sub(readyAt) >= soak {
			return nil
		}
		next := m.poll
		if !readyAt.IsZero() {
			next = min(next, soak-now.Sub(readyAt))
		}
		if err := m.sleep(ctx, max(next, time.Millisecond)); err != nil {
			em.line("The health gate was interrupted: %v", err)
			return []string{name}
		}
	}
}

// ── stack.down and stack.remove ──────────────────────────────────────────

func (m *Module) down(ctx context.Context, req stackproto.LifecycleRequest, em emitter) (stackproto.LifecycleResult, error) {
	unlock, err := m.lock(req.Name)
	if err != nil {
		return stackproto.LifecycleResult{}, err
	}
	defer unlock()
	if _, _, _, err := m.lifecycleTarget(ctx, req.Name, req.ExpectHead, "take down"); err != nil {
		return stackproto.LifecycleResult{}, err
	}
	em.step("Take " + req.Name + " down (its volumes stay)")
	if err := m.docker.Down(ctx, req.Name, eventLine(em)); err != nil {
		return stackproto.LifecycleResult{}, fmt.Errorf("take %s down: %w", req.Name, err)
	}
	m.settle(ctx, req.Name, func(cs []Container) bool { return len(cs) == 0 })
	res := m.after(ctx, req.Name)
	res.VolumesKept = m.volumes(ctx, req.Name)
	return res, nil
}

func (m *Module) remove(ctx context.Context, req stackproto.RemoveRequest, em emitter) (stackproto.LifecycleResult, error) {
	unlock, err := m.lock(req.Name)
	if err != nil {
		return stackproto.LifecycleResult{}, err
	}
	defer unlock()
	s, committed, _, err := m.lifecycleTarget(ctx, req.Name, req.ExpectHead, "remove")
	if err != nil {
		return stackproto.LifecycleResult{}, err
	}
	// Deleting the folder would lose edits git doesn't have. A file that's
	// gone already has nothing to lose.
	if disk, err := os.ReadFile(s.path()); err == nil && string(disk) != committed {
		return stackproto.LifecycleResult{}, stackproto.Errorf(stackproto.CodeDrift, "%s was edited outside Cha Dash since its last commit, and removing it would lose the edits. Accept them (adopt again) or put the file back first.", s.path())
	}

	em.step("Take " + s.name + " down")
	if err := m.docker.Down(ctx, s.name, eventLine(em)); err != nil {
		return stackproto.LifecycleResult{}, fmt.Errorf("take %s down: %w", s.name, err)
	}
	res := m.after(ctx, s.name)
	vols := m.volumes(ctx, s.name)
	if req.Volumes && len(vols) > 0 {
		em.step("Remove the volumes " + strings.Join(vols, ", "))
		if err := m.docker.RemoveVolumes(ctx, vols); err != nil {
			// The stack is down but still managed, so this can be retried.
			return stackproto.LifecycleResult{}, fmt.Errorf("%s is down, but removing its volumes failed, so it wasn't removed: %w", s.name, err)
		}
		res.VolumesRemoved = vols
	} else {
		res.VolumesKept = vols
	}

	c := change{writes: map[string][]byte{}}
	var what string
	if s.location == stackproto.LocationExternal {
		// Never delete the user's files outside the root: stop managing it,
		// and its shadow copy goes.
		reg, err := m.readRegistry()
		if err != nil {
			return res, err
		}
		delete(reg.Stacks, s.name)
		regBytes := encodeRegistry(reg)
		if err := m.writeRootFile(registryPath, regBytes); err != nil {
			return res, err
		}
		c.writes[registryPath] = regBytes
		c.drops = []string{s.tracked}
		_ = os.RemoveAll(filepath.Join(m.root, filepath.FromSlash(shadowDir), s.name))
		res.FilesKept = s.path()
		what = "no longer managed; its files stay at " + s.dir
	} else {
		tracked, err := m.repo.tracked()
		if err != nil {
			return res, err
		}
		for _, p := range sortedKeys(tracked) {
			if strings.HasPrefix(p, s.name+"/") {
				c.drops = append(c.drops, p)
			}
		}
		head, err := m.headOf(s)
		if err != nil {
			return res, err
		}
		kept, err := m.keepEnv(s.name, s.dir, head)
		if err != nil {
			return res, fmt.Errorf("%s is down, but keeping its env files failed, so it wasn't removed: %w", s.name, err)
		}
		if len(kept) > 0 {
			em.step("Keep " + strings.Join(kept, ", ") + " on this node for 30 days, for a restore")
			res.EnvKept = kept
		}
		m.pruneRemoved()
		em.step("Delete " + s.dir)
		if err := os.RemoveAll(s.dir); err != nil {
			return res, fmt.Errorf("%s is down, but deleting %s failed: %w", s.name, s.dir, err)
		}
		what = "its folder deleted"
	}
	if req.Volumes && len(res.VolumesRemoved) > 0 {
		what += ", volumes removed"
	} else if len(vols) > 0 {
		what += ", volumes kept"
	}
	h, err := m.repo.commit(c, fmt.Sprintf("Remove %s: down, %s%s", s.name, what, by(req.By, req.Via)),
		[][2]string{{trailerKind, stackproto.KindRemove}, {trailerStack, s.name}, {trailerActor, actorOf(req.By)}})
	if err != nil {
		return res, err
	}
	if co, err := m.repo.commitByHash(h.String()); err == nil {
		cm := toCommit(co)
		res.Commit = &cm
	}
	em.step("Removed " + s.name)
	return res, nil
}
