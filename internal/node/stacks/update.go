package stacks

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"

	"dash.cha.sh/internal/stackproto"
)

// Updating a stack (plan 16, wave 32b): new images for a managed stack,
// its file unchanged. stack.update.check asks each tag's registry with a
// HEAD and pulls nothing. stack.update goes through the deploy's steps:
// validate, plan, the dry run, then it pulls the tags, and only the
// services whose digest moved are recreated, held to the gate, and rolled
// back to the digests they ran on failure. Its commit changes no file
// (Chadash-Kind: update) and records each moved service's old digest
// (Chadash-Previous-Image), so a rollback to the commit before it brings
// them back. Nothing moved: no commit.

// updateTarget checks a managed stack can be asked about: a valid name,
// managed, its file as committed, not Cha Dash's own. It returns the stack,
// its committed file and its containers.
func (m *Module) updateTarget(ctx context.Context, name string) (stackRef, string, []Container, error) {
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
	if disk, err := os.ReadFile(s.path()); err != nil || string(disk) != committed {
		return s, "", nil, updateDrift(s)
	}
	return s, committed, cs, nil
}

func updateDrift(s stackRef) error {
	return stackproto.Errorf(stackproto.CodeDrift, "%s was edited outside Cha Dash since its last commit, and an update runs the committed file. Accept the edits (adopt again) or put the file back first.", s.path())
}

// ── stack.update.check ───────────────────────────────────────────────────

func (m *Module) updateCheck(ctx context.Context, req stackproto.UpdateCheckRequest, _ emitter) (stackproto.UpdateCheck, error) {
	s, committed, cs, err := m.updateTarget(ctx, req.Name)
	if err != nil {
		return stackproto.UpdateCheck{}, err
	}
	head, err := m.headOf(s)
	if err != nil {
		return stackproto.UpdateCheck{}, err
	}
	// Placeholders for secrets: only the images matter here.
	p, err := load(ctx, s, committed, loadOptions{})
	if err != nil {
		return stackproto.UpdateCheck{}, stackproto.Errorf(stackproto.CodeInvalid, "%s", loadMessage(err))
	}
	refs, err := m.docker.ImageRefs(ctx, s.name)
	if err != nil {
		return stackproto.UpdateCheck{}, fmt.Errorf("read %s's images: %w", s.name, err)
	}
	runs := byService(cs)
	out := stackproto.UpdateCheck{Name: s.name, Head: head, Services: []stackproto.ImageMove{}}
	for _, name := range sortedKeys(p.Services) {
		svc := p.Services[name]
		mv := stackproto.ImageMove{Service: name, Image: svc.Image, Current: digestPart(refs[name])}
		_, running := runs[name]
		switch {
		case svc.Image == "":
			mv.Reason = "It's built from the file, so no registry has it."
		case strings.Contains(svc.Image, "@"):
			mv.Reason = "It's pinned by digest in the file, so there's no tag to move."
		case !running:
			mv.Reason = "It has no container, so nothing runs to compare with."
		case mv.Current == "":
			mv.Reason = "It was built or loaded on this host, so no registry has it."
		default:
			tagged, why := tagOf(svc.Image)
			if why != "" {
				mv.Reason = why
				break
			}
			a := m.images.Ask(ctx, tagged, req.Fresh)
			mv.Latest, mv.Login = a.Digest, a.Login
			if a.Digest == "" {
				mv.Reason = a.Reason
			} else {
				mv.Moved = a.Digest != mv.Current
			}
		}
		out.Services = append(out.Services, mv)
	}
	return out, nil
}

// held reports whether a service's container is kept on the image it runs
// (a deploy's hold pins it by digest in memory) while its file names a tag.
func held(c Container, svc types.ServiceConfig) bool {
	return strings.Contains(c.Image, "@") && c.Image != svc.Image
}

// byService is each service's first container.
func byService(cs []Container) map[string]Container {
	out := map[string]Container{}
	for _, c := range cs {
		if _, ok := out[c.Service]; !ok && c.Service != "" {
			out[c.Service] = c
		}
	}
	return out
}

// tagOf reads an image reference as the tag to ask a registry about
// ("nginx" is docker.io/library/nginx:latest). why says why there's none.
func tagOf(image string) (reference.NamedTagged, string) {
	n, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return nil, "Its image isn't a name Docker can look up."
	}
	if _, ok := n.(reference.Digested); ok {
		return nil, "It's pinned by digest in the file, so there's no tag to move."
	}
	t, ok := reference.TagNameOnly(n).(reference.NamedTagged)
	if !ok {
		return nil, "Its image isn't a name Docker can look up."
	}
	return t, ""
}

// shortDigest is "sha256:abc1234" for a digest or a digest reference.
func shortDigest(d string) string {
	if strings.Contains(d, "@") {
		d = digestPart(d)
	}
	if len(d) > len("sha256:")+7 {
		return d[:len("sha256:")+7]
	}
	return d
}

// ── stack.update ─────────────────────────────────────────────────────────

func (m *Module) update(ctx context.Context, req stackproto.UpdateRequest, em emitter) (stackproto.DeployResult, error) {
	unlock, err := m.lock(req.Name)
	if err != nil {
		return stackproto.DeployResult{}, err
	}
	defer unlock()
	if _, _, _, err := m.updateTarget(ctx, req.Name); err != nil {
		return stackproto.DeployResult{}, err
	}
	a, err := m.prepare(ctx, req.Name, req.ExpectHead, req.Secrets)
	if err != nil {
		return stackproto.DeployResult{}, err
	}
	if len(a.running) == 0 {
		return stackproto.DeployResult{}, stackproto.Errorf(stackproto.CodeNotDeployed, "%s has no containers to update: it was never deployed, or it was taken down. Deploy it instead.", req.Name)
	}
	a.content, a.kind, a.by, a.via, a.ref, a.hold = a.prev, stackproto.KindUpdate, req.By, req.Via, req.Ref, req.Hold
	return m.runUpdate(ctx, a, req, em.withSecrets(a.secrets))
}

// runUpdate takes an update through the deploy's steps, the pull first:
// pulling changes no container, and what moved decides the plan.
func (m *Module) runUpdate(ctx context.Context, a *attempt, req stackproto.UpdateRequest, em emitter) (stackproto.DeployResult, error) {
	start := m.now()
	res := stackproto.DeployResult{}
	done := func() (stackproto.DeployResult, error) {
		res.DurationMS = m.now().Sub(start).Milliseconds()
		res.Reason = scrub(res.Reason, a.secrets)
		return res, nil
	}
	name := a.s.name

	// 1. Validate.
	p, flagged, err := m.loadAttempt(ctx, a, em)
	if err != nil {
		return res, err
	}
	meta := metaOf(a.content)
	for _, s := range req.Services {
		if _, ok := p.Services[s]; !ok {
			return res, stackproto.Errorf(stackproto.CodeInvalid, "%s has no service %q.", name, s)
		}
	}

	// 2. Plan: which tags to pull.
	em.step("Plan from the running containers")
	refs, err := m.docker.ImageRefs(ctx, name)
	if err != nil {
		return res, fmt.Errorf("read %s's images: %w", name, err)
	}
	runs := byService(a.running)
	wanted := func(s string) bool {
		return (len(req.Services) == 0 || slices.Contains(req.Services, s)) && !slices.Contains(req.Hold, s)
	}
	// updatable: a running service on a registry tag.
	updatable := func(s string) (Container, bool) {
		img := p.Services[s].Image
		c, ok := runs[s]
		return c, ok && img != "" && !strings.Contains(img, "@") && refs[s] != ""
	}
	var pull []string
	for _, s := range sortedKeys(p.Services) {
		if _, ok := updatable(s); ok && wanted(s) {
			pull = append(pull, s)
		}
	}
	res.Plan = planOf(a.head, false, nil)
	if len(pull) == 0 {
		res.Outcome = stackproto.OutcomeUnchanged
		res.Reason = "No service to update runs an image from a registry tag."
		return done()
	}

	// 3. Preflight.
	em.step("Check every image can be pulled (dry run)")
	if err := m.docker.DryRun(ctx, p); err != nil {
		res.Outcome = stackproto.OutcomeBlocked
		res.Reason = "The dry run refused it, so nothing was changed: " + err.Error()
		em.line("%s", scrub(res.Reason, a.secrets))
		return done()
	}

	// The pull, ahead of the commit: it changes no container.
	em.step("Pull " + strings.Join(pull, ", "))
	if err := m.docker.Pull(ctx, p, pull, eventLine(em)); err != nil {
		res.Outcome = stackproto.OutcomeBlocked
		res.Reason = "Pulling failed, so nothing was changed: " + err.Error()
		em.line("%s", scrub(res.Reason, a.secrets))
		return done()
	}
	moved := map[string][2]string{} // service → the digest references it ran and will run
	keepOn := map[string]string{}   // service → the image it stays on, in p only
	for _, s := range sortedKeys(p.Services) {
		c, ok := updatable(s)
		if !ok {
			continue
		}
		now, err := m.docker.ImageDigest(ctx, p.Services[s].Image)
		if err != nil {
			return res, fmt.Errorf("read %s's image: %w", s, err)
		}
		changed := now != "" && digestPart(now) != digestPart(refs[s])
		switch {
		case changed && wanted(s):
			moved[s] = [2]string{refs[s], now}
		case held(c, p.Services[s]):
			keepOn[s] = c.Image
		case changed:
			// Not in this update, but its tag moved anyway (another
			// service's pull of the same image, or one outside Cha Dash):
			// kept on the digest it runs.
			keepOn[s] = heldRef(c.Image, refs[s])
		}
	}
	for s, img := range keepOn {
		svc := p.Services[s]
		svc.Image = img
		p.Services[s] = svc
	}
	services, err := planServices(p, a.running)
	if err != nil {
		return res, err
	}
	var changes []string
	for i, sp := range services {
		if mv, ok := moved[sp.Name]; ok {
			sp.Action, sp.Pull, sp.Reason = stackproto.ActionRecreate, true, "its image moved"
			sp.From, sp.To = digestPart(mv[0]), digestPart(mv[1])
			if want := req.Expect[sp.Name]; want != "" && want != sp.To {
				sp.Reason += fmt.Sprintf("; the registry moved again since the check (planned %s)", shortDigest(want))
			}
			changes = append(changes, fmt.Sprintf("%s %s → %s", sp.Name, shortDigest(sp.From), shortDigest(sp.To)))
		} else if _, kept := keepOn[sp.Name]; kept && sp.Action != stackproto.ActionUnchanged {
			sp.Reason = "held: kept on the image it runs"
		}
		services[i] = sp
	}
	pl := planOf(a.head, false, services)
	pl.RecreateAll = false
	pl.Services = slices.DeleteFunc(slices.Clone(services), func(sp stackproto.ServicePlan) bool { return sp.Action == stackproto.ActionUnchanged })
	pl.RootEquivalent = scrubAll(flagged, a.secrets)
	res.Plan = pl
	if len(moved) == 0 {
		res.Outcome = stackproto.OutcomeUnchanged
		res.Reason = "Every service already runs the newest image its tag has, so there's nothing to update."
		em.line("%s", res.Reason)
		return done()
	}
	if why := m.protectedTouched(services, a.running); why != "" {
		return res, stackproto.Errorf(stackproto.CodeProtected, "%s", why)
	}

	// 4. Commit: no file changes, the old digests as trailers.
	em.step("Commit the update")
	a.prevRefs = map[string]string{}
	trailers := [][2]string{{trailerKind, stackproto.KindUpdate}, {trailerStack, name}, {trailerActor, actorOf(a.by)}}
	for _, s := range sortedKeys(moved) {
		a.prevRefs[s] = moved[s][0]
		trailers = append(trailers, [2]string{trailerPrevious, s + "=" + moved[s][0]})
	}
	subject := "Update " + name + ": " + strings.Join(changes, ", ") + by(a.by, a.via)
	if a.ref != "" {
		subject += " · " + oneLine(a.ref)
	}
	commit, err := m.writeAndCommit(a.s, a.content, subject, trailers)
	if err != nil {
		return res, err
	}
	res.Commit = commit
	em.line("Committed %s", short(commit))

	// 5 and 6. Up (what moved is recreated), then the gate.
	wait, soak := pick(meta.wait, m.wait), pick(meta.soak, m.soak)
	gateErr := m.upAndGate(ctx, name, p, nil, wait, soak, em)
	if gateErr == nil {
		res.Outcome = stackproto.OutcomeDeployed
		em.step("Updated and healthy")
		return done()
	}

	// 7. Roll back to the digests they ran, the others kept as they are.
	ctx = context.WithoutCancel(ctx)
	reason := scrub(gateErr.Error(), a.secrets)
	em.line("Health gate failed: %s", reason)
	res.Reason = reason
	m.rollBack(ctx, a, reason, &res, em, func(bp *types.Project) {
		for s, img := range keepOn {
			if svc, ok := bp.Services[s]; ok {
				svc.Image = img
				bp.Services[s] = svc
			}
		}
	})
	return done()
}
