package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dash.cha.sh/internal/stackproto"
)

// Cha Dash updates itself (stack.selfupdate): the node beside the hub, in
// the hub's compose project, pulls the hub's image at a tag, pins its
// digest, recreates the hub's container with it and holds it to the
// container's healthcheck. If the hub doesn't come up healthy, the node
// puts the previous digest back and gates again, as a stack rollback does.
//
// Only the container whose image is ghcr.io/ban-red/chadash changes. The
// node never touches its own (chadash-node) or any other service: it
// recreates that one container from the config it runs with, so it doesn't
// need the hub's compose files either.
//
// The hub goes away mid-update, so the work never depends on the caller:
// it runs to the end whatever happens to the request, and the last result
// is kept in the root (.chadash/selfupdate.json) for
// stack.selfupdate.result to answer once the hub is back. The outcome is
// committed to the stacks repo with Chadash-Kind: self-update, under the
// compose project's name, so it shows in that stack's history.

// HubImage is the hub's image repository: the only image self-update
// changes.
const HubImage = "ghcr.io/ban-red/chadash"

// SelfUpdateGate is how long the hub has to report healthy after it's
// recreated (and again after a rollback).
const SelfUpdateGate = 120 * time.Second

const selfUpdateFile = metaDir + "/selfupdate.json"

// selfUpdateLock is the lock name one self-update at a time holds. It
// can't be a stack's name (they start with a letter or digit).
const selfUpdateLock = ".selfupdate"

var tagRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// hubImageRef reads the requested image: a tag ("0.3.0") or the hub's
// repository with one ("ghcr.io/ban-red/chadash:0.3.0"). Anything else,
// another repository (the node's own image among them) or a digest, is
// refused.
func hubImageRef(image string) (string, error) {
	image = strings.TrimSpace(image)
	tag := image
	if strings.Contains(image, "/") || strings.Contains(image, "@") {
		repo, t, ok := strings.Cut(image, "@")
		if ok {
			return "", stackproto.Errorf(stackproto.CodeInvalid, "Give a tag, not a digest (%s): the node pins the digest the tag points at when it pulls.", t)
		}
		i := strings.LastIndexByte(repo, ':')
		if i < 0 || i < strings.LastIndexByte(repo, '/') {
			return "", stackproto.Errorf(stackproto.CodeInvalid, "%s has no tag. Give one, such as %s:0.3.0.", image, HubImage)
		}
		if normalRepo(repo[:i]) != normalRepo(HubImage) {
			return "", stackproto.Errorf(stackproto.CodeInvalid, "Self-update only changes the hub's image, %s; %s isn't it.", HubImage, repo[:i])
		}
		tag = repo[i+1:]
	}
	if !tagRE.MatchString(tag) {
		return "", stackproto.Errorf(stackproto.CodeInvalid, "%q isn't an image tag.", tag)
	}
	return HubImage + ":" + tag, nil
}

// imageRepo is an image reference's repository, normalised, without tag or
// digest.
func imageRepo(image string) string {
	ref, _, _ := strings.Cut(image, "@")
	return normalRepo(stripTag(ref))
}

// hubContainer finds the hub's container: in the node's own compose
// project, the one whose image is HubImage. why says why there's none to
// update.
func (m *Module) hubContainer(ctx context.Context) (project string, hub Container, why string, err error) {
	if m.selfUpdater == nil {
		return "", Container{}, "This node can't recreate containers itself, so it can't update the hub.", nil
	}
	project = m.selfProjectName()
	if project == "" {
		return "", Container{}, "This node doesn't run in a compose project beside the hub, so it can't update it. Self-update runs on the hub's own host, from the node in its compose project.", nil
	}
	cs, err := m.docker.Containers(ctx, project)
	if err != nil {
		return project, Container{}, "", fmt.Errorf("list %s's containers: %w", project, err)
	}
	// Only a running container is the hub (it's the one asking). A stopped
	// one is a leftover: a recreate that couldn't remove the old container
	// leaves it under <name>-chadash-old-<time>, still in the project.
	var hubs []Container
	for _, c := range cs {
		if imageRepo(c.Image) == normalRepo(HubImage) && c.State == "running" && !strings.Contains(c.Name, "-chadash-old-") {
			hubs = append(hubs, c)
		}
	}
	switch len(hubs) {
	case 0:
		return project, Container{}, fmt.Sprintf("There's no hub container (%s) in the compose project %s.", HubImage, project), nil
	case 1:
		return project, hubs[0], "", nil
	}
	return project, Container{}, fmt.Sprintf("The compose project %s runs %d containers of %s, so which one is the hub isn't clear.", project, len(hubs), HubImage), nil
}

func (m *Module) selfUpdate(ctx context.Context, req stackproto.SelfUpdateRequest, em emitter) (stackproto.SelfUpdateResult, error) {
	ref, err := hubImageRef(req.Image)
	if err != nil {
		return stackproto.SelfUpdateResult{}, err
	}
	unlock, err := m.lock(selfUpdateLock)
	if err != nil {
		return stackproto.SelfUpdateResult{}, stackproto.Errorf(stackproto.CodeBusy, "An update of Cha Dash is already running.")
	}
	defer unlock()
	project, hub, why, err := m.hubContainer(ctx)
	if err != nil {
		return stackproto.SelfUpdateResult{}, err
	}
	if why != "" {
		return stackproto.SelfUpdateResult{}, stackproto.Errorf(stackproto.CodeForbidden, "%s", why)
	}
	// From here the hub, which asked, goes away: finish whatever happens to
	// the request.
	ctx = context.WithoutCancel(ctx)
	line := func(s string) { em.line("%s", s) }
	start := m.now().UTC()
	// What to put back if the new one fails: a reference no pull can move,
	// the digest the hub runs now with its tag kept for reading
	// (ghcr.io/ban-red/chadash:0.2.0@sha256:…). Never the bare tag: the
	// pull below would move it to the image being tried. An image with no
	// registry digest (built or loaded locally) is refused rather than
	// rolled back by image ID: a hub running "sha256:…" would no longer
	// read as ghcr.io/ban-red/chadash, so it would lose its protection and
	// the next update couldn't find it.
	prev := ""
	if strings.Contains(hub.Image, "@") {
		prev = hub.Image
	} else if refs, err := m.docker.ImageRefs(ctx, project); err != nil {
		return stackproto.SelfUpdateResult{}, fmt.Errorf("read the hub's image digest: %w", err)
	} else if _, sum, ok := strings.Cut(refs[hub.Service], "@"); ok {
		prev = hub.Image + "@" + sum
	}
	if prev == "" {
		return stackproto.SelfUpdateResult{}, stackproto.Errorf(stackproto.CodeConflict,
			"The hub's image, %s, has no registry digest (it was built or loaded on this host), so there'd be no fixed image to roll back to. Nothing was changed; update it with docker compose instead.", hub.Image)
	}
	res := stackproto.SelfUpdateResult{Outcome: stackproto.OutcomeRunning, Previous: prev, Current: prev, Ref: req.Ref, Image: ref, StartedAt: &start}
	if err := m.saveSelfUpdate(res); err != nil {
		return res, err
	}
	finish := func(outcome, reason string) (stackproto.SelfUpdateResult, error) {
		end := m.now().UTC()
		res.Outcome, res.Reason, res.FinishedAt = outcome, reason, &end
		subject := fmt.Sprintf("Update Cha Dash to %s: %s", ref, outcome)
		if reason != "" {
			subject += " (" + strings.TrimSuffix(reason, ".") + ")"
		}
		// The history gets the result first (staged straight into the
		// index), then the file .result reads, once, with the commit: the
		// hub may be reading it already.
		b, _ := json.MarshalIndent(res, "", "  ")
		h, err := m.repo.commit(change{writes: map[string][]byte{selfUpdateFile: append(b, '\n')}}, subject+by(req.By, req.Via),
			[][2]string{{trailerKind, stackproto.KindSelfUpdate}, {trailerStack, project}, {trailerActor, actorOf(req.By)}})
		if err != nil {
			m.log.Error("stacks: couldn't record the self-update in the stacks repo", "err", err)
		} else {
			res.Commit = h.String()
		}
		if err := m.saveSelfUpdate(res); err != nil {
			m.log.Error("stacks: couldn't keep the self-update's result", "err", err)
		}
		return res, nil
	}

	em.step("Pull " + ref)
	digest, err := m.selfUpdater.PullImage(ctx, ref, line)
	if err != nil {
		return finish(stackproto.OutcomeBlocked, "Pulling "+ref+" failed, so nothing was changed: "+err.Error())
	}
	_, sum, ok := strings.Cut(digest, "@")
	if !ok || !strings.HasPrefix(sum, "sha256:") {
		return finish(stackproto.OutcomeBlocked, "The registry gave no digest for "+ref+", so nothing was changed.")
	}
	pinned := ref + "@" + sum
	res.Current = pinned
	if _, was, _ := strings.Cut(prev, "@"); was == sum {
		res.Current = prev
		return finish(stackproto.OutcomeUnchanged, "The hub already runs "+ref+".")
	}

	// The gate and the rollback follow the container RecreateImage made, by
	// its ID: a service lookup could find a stopped leftover instead.
	em.step("Recreate the hub with " + pinned)
	newID, err := m.selfUpdater.RecreateImage(ctx, hub.ID, pinned, line)
	if err != nil {
		res.Current = prev
		return finish(stackproto.OutcomeBlocked, "Recreating the hub failed, so the old container was put back: "+err.Error())
	}
	gateErr := m.hubGate(ctx, project, newID, em)
	if gateErr == nil {
		em.step("Updated and healthy")
		return finish(stackproto.OutcomeDeployed, "")
	}
	reason := gateErr.Error()
	em.line("Health gate failed: %s", reason)

	// Roll back: the image it ran before, through the same gate.
	em.step("Roll back to " + prev)
	res.Current = prev
	backID, err := m.selfUpdater.RecreateImage(ctx, newID, prev, line)
	if err != nil {
		res.Current = pinned
		return finish(stackproto.OutcomeStuck, fmt.Sprintf("%s; and recreating it with %s failed: %v", reason, prev, err))
	}
	if err := m.hubGate(ctx, project, backID, em); err != nil {
		em.step("Stuck: the rollback failed too")
		return finish(stackproto.OutcomeStuck, fmt.Sprintf("%s; and the rollback to %s failed too: %v", reason, prev, err))
	}
	em.step("Rolled back and healthy")
	return finish(stackproto.OutcomeRolledBack, fmt.Sprintf("%s. Rolled back to %s on its own, and it's healthy again.", strings.TrimSuffix(reason, "."), prev))
}

// hubByID is the project's container with the ID id.
func (m *Module) hubByID(ctx context.Context, project, id string) (Container, error) {
	cs, err := m.docker.Containers(ctx, project)
	if err != nil {
		return Container{}, err
	}
	for _, c := range cs {
		if c.ID == id {
			return c, nil
		}
	}
	return Container{}, fmt.Errorf("%s has no container %s", project, id)
}

// hubGate waits up to SelfUpdateGate for the hub's container, the one with
// the ID id, to report healthy. It fails at once if it exits, restarts or
// turns unhealthy. A container without a healthcheck passes once it runs.
func (m *Module) hubGate(ctx context.Context, project, id string, em emitter) error {
	em.step(fmt.Sprintf("Health gate: the hub must report healthy within %s", SelfUpdateGate))
	start := m.now()
	for {
		c, err := m.hubByID(ctx, project, id)
		if err == nil {
			switch {
			case c.State == "exited" || c.State == "dead":
				return fmt.Errorf("%s exited with code %d", c.Name, c.ExitCode)
			case c.State == "restarting":
				return fmt.Errorf("%s is restarting", c.Name)
			case c.Health == "unhealthy":
				return fmt.Errorf("container %s is unhealthy", c.Name)
			case c.State == "running" && (c.Health == "healthy" || c.Health == ""):
				return nil
			}
		}
		elapsed := m.now().Sub(start)
		if elapsed >= SelfUpdateGate {
			if err != nil {
				return fmt.Errorf("the hub's container didn't come back within %s: %v", SelfUpdateGate, err)
			}
			return fmt.Errorf("the hub didn't report healthy within %s", SelfUpdateGate)
		}
		em.fraction(float64(elapsed) / float64(SelfUpdateGate))
		if err := m.sleep(ctx, min(m.poll, SelfUpdateGate-elapsed)); err != nil {
			return err
		}
	}
}

func (m *Module) selfUpdateResult(ctx context.Context, _ stackproto.SelfUpdateResultRequest, _ emitter) (stackproto.SelfUpdateResult, error) {
	res, err := m.lastSelfUpdate()
	if err != nil {
		return res, err
	}
	if res.Outcome == stackproto.OutcomeRunning {
		// A running record no update holds: the node stopped mid-update.
		if unlock, err := m.lock(selfUpdateLock); err == nil {
			unlock()
			res.Outcome = stackproto.OutcomeStuck
			res.Reason = "The node stopped while it was updating the hub, so how the update ended isn't known. Check the hub's container."
		}
	}
	_, hub, why, err := m.hubContainer(ctx)
	switch {
	case err != nil:
		res.Unavailable = "Docker couldn't be asked about the hub's container: " + err.Error()
	case why != "":
		res.Unavailable = why
	default:
		res.Available, res.Running = true, hub.Image
	}
	return res, nil
}

func (m *Module) lastSelfUpdate() (stackproto.SelfUpdateResult, error) {
	var res stackproto.SelfUpdateResult
	b, err := os.ReadFile(filepath.Join(m.root, filepath.FromSlash(selfUpdateFile)))
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return stackproto.SelfUpdateResult{}, fmt.Errorf("read %s: %w", selfUpdateFile, err)
	}
	res.Available, res.Unavailable, res.Running = false, "", ""
	return res, nil
}

func (m *Module) saveSelfUpdate(res stackproto.SelfUpdateResult) error {
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return m.writeRootFile(selfUpdateFile, append(b, '\n'))
}
