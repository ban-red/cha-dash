package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"

	"dash.cha.sh/internal/stackproto"
)

// The deploy pipeline (03 · Architecture → Stacks, proven in spike 4.6):
//
//  1. Validate the file (the loader, secret references, x-chadash).
//  2. Plan from config hashes against the running containers.
//  3. Preflight: a dry run through Compose's reconciler, which resolves
//     every image, so a bad tag is blocked before anything stops.
//  4. Commit the attempt, recording the images running now.
//  5. Pull what changed, then up --wait, streaming progress.
//  6. Health gate: a soak that fails on any restart, exit or unhealthy
//     state (--wait alone passes crash loops).
//  7. Keep it, or roll back: the previous file as a new commit, pinned to
//     the digests it last ran, through the same gate with a shorter soak.
//     If that fails too, the stack is stuck.

// attempt is one pass through the pipeline: a deploy, or a rollback the
// user asked for (which is a deploy of an older file).
type attempt struct {
	s        stackRef
	content  string            // what to deploy
	prev     string            // what it replaces: the file at HEAD
	hadPrev  bool              // false for a brand-new stack
	head     string            // HEAD's hash for this stack, "" for none
	secrets  map[string]string // every reference's value
	kind     string            // stackproto.KindDeploy or KindRollback
	subject  string            // the commit subject, without the "· by" tail
	by, via  string
	ref      string // the hub's task ID
	running  []Container
	prevRefs map[string]string // service → digest reference running now
}

func (m *Module) deploy(ctx context.Context, req stackproto.DeployRequest, em emitter) (stackproto.DeployResult, error) {
	unlock, err := m.lock(req.Stack)
	if err != nil {
		return stackproto.DeployResult{}, err
	}
	defer unlock()
	a, err := m.prepare(ctx, req.Stack, req.ExpectHead, req.Secrets)
	if err != nil {
		return stackproto.DeployResult{}, err
	}
	a.content, a.kind, a.by, a.via, a.ref = req.Content, stackproto.KindDeploy, req.By, req.Via, req.Ref
	a.subject = "Deploy " + a.s.name
	return m.run(ctx, a, em.withSecrets(a.secrets))
}

func (m *Module) rollback(ctx context.Context, req stackproto.RollbackRequest, em emitter) (stackproto.DeployResult, error) {
	unlock, err := m.lock(req.Stack)
	if err != nil {
		return stackproto.DeployResult{}, err
	}
	defer unlock()
	a, err := m.prepare(ctx, req.Stack, req.ExpectHead, req.Secrets)
	if err != nil {
		return stackproto.DeployResult{}, err
	}
	if !a.hadPrev {
		return stackproto.DeployResult{}, stackproto.Errorf(stackproto.CodeNotManaged, "%s has no history to roll back to.", a.s.name)
	}
	target, err := m.repo.commitByHash(req.Commit)
	if err != nil {
		return stackproto.DeployResult{}, err
	}
	old, ok, err := m.repo.file(target, a.s.tracked)
	if err != nil {
		return stackproto.DeployResult{}, err
	}
	if !ok {
		return stackproto.DeployResult{}, stackproto.Errorf(stackproto.CodeNotFound, "%s didn't exist at %s.", a.s.name, target.Hash.String()[:7])
	}
	pins, err := m.repo.pinsAfter(a.s.name, target.Hash)
	if err != nil {
		return stackproto.DeployResult{}, err
	}
	content, pinned := pinImages(old, pins)
	a.content, a.kind, a.by, a.via, a.ref = content, stackproto.KindRollback, req.By, req.Via, req.Ref
	a.subject = fmt.Sprintf("Roll back %s to %s", a.s.name, target.Hash.String()[:7])
	if len(pinned) > 0 {
		a.subject += " (pinned " + strings.Join(pinned, ", ") + ")"
	}
	return m.run(ctx, a, em.withSecrets(a.secrets))
}

// prepare checks the stack can be deployed at all: it's managed (or new),
// nobody deployed since the plan, nobody edited it outside Cha Dash, and
// it isn't Cha Dash's own.
func (m *Module) prepare(ctx context.Context, name, expectHead string, secrets stackproto.Secrets) (*attempt, error) {
	s, err := m.resolve(name)
	if err != nil {
		return nil, err
	}
	if s.exists && !s.managed {
		return nil, stackproto.Errorf(stackproto.CodeNotManaged, "%s isn't managed by Cha Dash yet. Adopt it first.", name)
	}
	a := &attempt{s: s, secrets: secretsOrEmpty(secrets)}
	if a.prev, a.hadPrev, err = m.repo.file(nil, s.tracked); err != nil {
		return nil, err
	}
	if a.head, err = m.headOf(s); err != nil {
		return nil, err
	}
	if expectHead != "" && expectHead != a.head {
		return nil, stackproto.Errorf(stackproto.CodeConflict, "%s changed since this was planned (now at %s). Plan it again.", name, short(a.head))
	}
	if a.hadPrev {
		disk, err := os.ReadFile(s.path())
		if err != nil || string(disk) != a.prev {
			return nil, stackproto.Errorf(stackproto.CodeDrift, "%s was edited outside Cha Dash since its last commit. Accept the edits (adopt again) or put the file back before deploying.", s.path())
		}
	}
	cs, err := m.docker.Containers(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("list %s's containers: %w", name, err)
	}
	a.running = cs
	return a, nil
}

func short(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	if h == "" {
		return "nothing"
	}
	return h
}

// run takes an attempt through the seven steps.
func (m *Module) run(ctx context.Context, a *attempt, em emitter) (stackproto.DeployResult, error) {
	start := m.now()
	res := stackproto.DeployResult{}
	done := func() (stackproto.DeployResult, error) {
		res.DurationMS = m.now().Sub(start).Milliseconds()
		res.Reason = scrub(res.Reason, a.secrets)
		return res, nil
	}
	if why := m.protectedReason(a.s.name, []string{a.content, a.prev}, a.running); why != "" {
		return res, stackproto.Errorf(stackproto.CodeProtected, "%s", why)
	}

	// 1. Validate.
	em.step("Validate the file")
	v := validate(ctx, a.s, a.content)
	if !v.OK {
		for _, msg := range v.Messages {
			if msg.Severity == stackproto.SeverityError {
				return res, stackproto.Errorf(stackproto.CodeInvalid, "%s", msg.Message)
			}
		}
	}
	for _, name := range v.Secrets {
		if _, ok := a.secrets[name]; !ok {
			return res, stackproto.Errorf(stackproto.CodeInvalid, "The secret %q wasn't sent with the deploy.", name)
		}
	}
	p, err := load(ctx, a.s, a.content, loadOptions{secrets: a.secrets})
	if err != nil {
		return res, stackproto.Errorf(stackproto.CodeInvalid, "%s", scrub(loadMessage(err), a.secrets))
	}
	meta := metaOf(a.content)

	// 2. Plan.
	em.step("Plan from the running containers")
	services, err := planServices(p, a.running)
	if err != nil {
		return res, err
	}
	res.Plan = planOf(a.head, a.content != a.prev || !a.hadPrev, services)
	if a.hadPrev && a.content == a.prev {
		res.Outcome = stackproto.OutcomeUnchanged
		res.Reason = "The file is the same as the last commit, so there's nothing to deploy."
		return done()
	}

	// 3. Preflight.
	em.step("Check every image can be pulled (dry run)")
	if err := m.docker.DryRun(ctx, p); err != nil {
		res.Outcome = stackproto.OutcomeBlocked
		if errors.Is(err, ErrUnchecked) {
			res.Reason = "Couldn't reach the registry to check the images, so nothing was changed: " + err.Error()
		} else {
			res.Reason = "The dry run refused it, so nothing was changed: " + err.Error()
		}
		em.line("%s", res.Reason)
		return done()
	}

	// 4. Commit the attempt.
	em.step("Commit the attempt")
	if refs, err := m.docker.ImageRefs(ctx, a.s.name); err != nil {
		m.log.Warn("stacks: couldn't read the running images; a rollback won't be pinned", "stack", a.s.name, "err", err)
	} else {
		a.prevRefs = matchingRefs(refs, a.running, imagesOf(a.prev))
	}
	trailers := [][2]string{{trailerKind, a.kind}, {trailerStack, a.s.name}, {trailerActor, actorOf(a.by)}}
	for _, svc := range sortedKeys(a.prevRefs) {
		trailers = append(trailers, [2]string{trailerPrevious, svc + "=" + a.prevRefs[svc]})
	}
	subject := a.subject + ": " + summary(services) + by(a.by, a.via)
	if a.ref != "" {
		subject += " · " + oneLine(a.ref)
	}
	commit, err := m.writeAndCommit(a.s, a.content, subject, trailers)
	if err != nil {
		return res, err
	}
	res.Commit = commit
	em.line("Committed %s", short(commit))

	// 5 and 6. Pull and up, then the gate.
	wait, soak := pick(meta.wait, m.wait), pick(meta.soak, m.soak)
	gateErr := m.upAndGate(ctx, a.s.name, p, pulls(services), wait, soak, em)
	if gateErr == nil {
		res.Outcome = stackproto.OutcomeDeployed
		em.step("Deployed and healthy")
		return done()
	}

	// 7. Roll back, even if the request was cancelled meanwhile.
	ctx = context.WithoutCancel(ctx)
	reason := scrub(gateErr.Error(), a.secrets)
	em.line("Health gate failed: %s", reason)
	res.Reason = reason
	if !a.hadPrev {
		m.removeNew(ctx, a, reason, &res, em)
		return done()
	}
	em.step("Roll back to " + short(a.head))
	back, pinned := pinImages(a.prev, a.prevRefs)
	rbSubject := fmt.Sprintf("Roll back %s: %s", a.s.name, reason)
	if len(pinned) > 0 {
		rbSubject += " (pinned " + strings.Join(pinned, ", ") + ")"
	}
	rbCommit, err := m.writeAndCommit(a.s, back, rbSubject+by(a.by, a.via),
		[][2]string{{trailerKind, stackproto.KindRollback}, {trailerStack, a.s.name}, {trailerActor, "chadash-node"}})
	if err != nil {
		res.Outcome, res.Reason = stackproto.OutcomeStuck, fmt.Sprintf("%s; and the rollback couldn't be committed: %v", reason, err)
		return done()
	}
	res.RollbackCommit = rbCommit
	bp, err := load(ctx, a.s, back, loadOptions{secrets: a.secrets})
	if err != nil {
		res.Outcome, res.Reason = stackproto.OutcomeStuck, fmt.Sprintf("%s; and the previous file no longer loads: %s", reason, loadMessage(err))
		return done()
	}
	prevMeta := metaOf(a.prev)
	rbSoak := pick(prevMeta.rollbackSoak, m.rollbackSoak)
	if err := m.upAndGate(ctx, a.s.name, bp, nil, pick(prevMeta.wait, m.wait), rbSoak, em); err != nil {
		res.Outcome = stackproto.OutcomeStuck
		res.Reason = fmt.Sprintf("%s; and the rollback to %s failed too: %v", reason, short(a.head), err)
		em.step("Stuck: the rollback failed too")
		return done()
	}
	res.Outcome = stackproto.OutcomeRolledBack
	res.Reason = fmt.Sprintf("%s. Rolled back to %s on its own, and it's healthy again.", strings.TrimSuffix(reason, "."), short(a.head))
	em.step("Rolled back and healthy")
	return done()
}

// removeNew undoes a brand-new stack's failed first deploy: there's no
// previous version, so its containers go and its file leaves the repo.
// The hub keeps the draft.
func (m *Module) removeNew(ctx context.Context, a *attempt, reason string, res *stackproto.DeployResult, em emitter) {
	em.step("Remove the new stack: it never ran healthy")
	if err := m.docker.Down(ctx, a.s.name, eventLine(em)); err != nil {
		res.Outcome, res.Reason = stackproto.OutcomeStuck, fmt.Sprintf("%s; and removing it failed: %v", reason, err)
		return
	}
	_ = os.Remove(a.s.path())
	_ = os.Remove(a.s.dir) // only if empty
	h, err := m.repo.commit(change{drops: []string{a.s.tracked}},
		fmt.Sprintf("Roll back %s: %s (removed; it never ran healthy)%s", a.s.name, reason, by(a.by, a.via)),
		[][2]string{{trailerKind, stackproto.KindRollback}, {trailerStack, a.s.name}, {trailerActor, "chadash-node"}})
	if err != nil {
		res.Outcome, res.Reason = stackproto.OutcomeStuck, fmt.Sprintf("%s; and the removal couldn't be committed: %v", reason, err)
		return
	}
	res.RollbackCommit = h.String()
	res.Outcome = stackproto.OutcomeRolledBack
	res.Reason = strings.TrimSuffix(reason, ".") + ". It was new, so Cha Dash removed it again; the draft is kept."
}

func pick(override, def time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return def
}

// matchingRefs keeps the digest references of services whose running image
// is the one the previous file names, so a rollback pins what actually ran.
func matchingRefs(refs map[string]string, running []Container, prevImages map[string]string) map[string]string {
	out := map[string]string{}
	for _, c := range running {
		ref, ok := refs[c.Service]
		if !ok {
			continue
		}
		img := prevImages[c.Service]
		if img != "" && (c.Image == img || strings.HasPrefix(c.Image, img+"@")) {
			out[c.Service] = ref
		}
	}
	return out
}

// writeAndCommit writes the stack's real file (and its shadow copy, for a
// stack outside the root) and commits the versioned copy.
func (m *Module) writeAndCommit(s stackRef, content, subject string, trailers [][2]string) (string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(s.path(), []byte(content)); err != nil {
		return "", err
	}
	if s.location == stackproto.LocationExternal {
		if err := m.writeRootFile(s.tracked, []byte(content)); err != nil {
			return "", err
		}
	}
	h, err := m.repo.commit(change{writes: map[string][]byte{s.tracked: []byte(content)}}, subject, trailers)
	if err != nil {
		return "", err
	}
	return h.String(), nil
}

// writeFileAtomic replaces path by renaming a sibling temp file over it,
// keeping the old file's mode.
func writeFileAtomic(path string, b []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".chadash-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func eventLine(em emitter) func(Event) {
	return func(e Event) {
		if e.Status == "working" {
			return
		}
		em.line("%s", strings.TrimSpace(e.ID+"  "+e.Text))
	}
}

// upAndGate pulls, applies, and holds the result to the gate.
func (m *Module) upAndGate(ctx context.Context, name string, p *types.Project, pull []string, wait, soak time.Duration, em emitter) error {
	if len(pull) > 0 {
		em.step("Pull " + strings.Join(pull, ", "))
		if err := m.docker.Pull(ctx, p, pull, eventLine(em)); err != nil {
			return fmt.Errorf("pulling failed: %w", err)
		}
	}
	em.step(fmt.Sprintf("Start the services and wait for them (up to %s)", wait))
	if err := m.docker.Up(ctx, p, wait, eventLine(em)); err != nil {
		return fmt.Errorf("up --wait failed: %w", err)
	}
	return m.soakCheck(ctx, name, soak, em)
}

// soakCheck fails if any container stops running, turns unhealthy, or
// restarts within d. Polling the state alone misses crash loops that spend
// most of their time running; the restart count doesn't (spike 4.6).
func (m *Module) soakCheck(ctx context.Context, name string, d time.Duration, em emitter) error {
	em.step(fmt.Sprintf("Health gate: every container must stay up for %s", d))
	start := m.now()
	restarts := map[string]int{}
	for {
		cs, err := m.docker.Containers(ctx, name)
		if err != nil {
			return fmt.Errorf("couldn't read the containers: %w", err)
		}
		if len(cs) == 0 {
			return errors.New("no containers are running")
		}
		for _, c := range cs {
			switch {
			case c.State == "exited" || c.State == "dead":
				return fmt.Errorf("%s exited with code %d", c.Name, c.ExitCode)
			case c.State == "restarting":
				return fmt.Errorf("%s is restarting", c.Name)
			case c.State != "running":
				return fmt.Errorf("%s is %s", c.Name, c.State)
			case c.Health == "unhealthy":
				return fmt.Errorf("container %s is unhealthy", c.Name)
			}
			if first, seen := restarts[c.ID]; !seen {
				restarts[c.ID] = c.RestartCount
			} else if c.RestartCount > first {
				return fmt.Errorf("%s restarted %d× during the soak", c.Name, c.RestartCount-first)
			}
		}
		elapsed := m.now().Sub(start)
		if elapsed >= d {
			return nil
		}
		if d > 0 {
			em.fraction(float64(elapsed) / float64(d))
		}
		if err := m.sleep(ctx, min(m.poll, d-elapsed)); err != nil {
			return err
		}
	}
}
