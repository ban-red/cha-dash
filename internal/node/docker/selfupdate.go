package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/moby/client"

	"dash.cha.sh/internal/nodeproto"
)

// PullImage and RecreateImage are what Cha Dash updating itself needs from
// Docker (the stacks module's SelfUpdater, behind the stacks.selfupdate
// permission). They are not verbs: they skip the protection every Docker
// verb applies, because the hub's container is the one they change. The
// stacks module picks the container (the hub's, never the node's own).

// PullImage pulls ref and returns the digest reference it resolved to, from
// the pulled image's RepoDigests.
func (m *Module) PullImage(ctx context.Context, ref string, progress func(string)) (string, error) {
	pr, err := m.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return "", mapErr(err)
	}
	defer pr.Close()
	for msg, err := range pr.JSONMessages(ctx) {
		if err != nil {
			return "", mapErr(err)
		}
		if msg.Error != nil {
			return "", fmt.Errorf("pulling %s: %s", ref, msg.Error.Message)
		}
		if msg.Status != "" && msg.Progress == nil {
			progress(strings.TrimSpace(msg.ID + " " + msg.Status))
		}
	}
	img, err := m.cli.ImageInspect(ctx, ref)
	if err != nil {
		return "", mapErr(err)
	}
	repo, _, _ := strings.Cut(ref, "@")
	if i := strings.LastIndexByte(repo, ':'); i > strings.LastIndexByte(repo, '/') {
		repo = repo[:i]
	}
	for _, rd := range img.RepoDigests {
		if r, _, _ := strings.Cut(rd, "@"); r == repo {
			return rd, nil
		}
	}
	return "", fmt.Errorf("%s has no registry digest for %s", ref, repo)
}

// RecreateImage replaces the container id with one from its own config
// running image, and returns the new container's ID. The node's own
// container is refused.
func (m *Module) RecreateImage(ctx context.Context, id, image string, progress func(string)) (string, error) {
	res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return "", mapErr(err)
	}
	old := res.Container
	if m.self != "" && strings.HasPrefix(old.ID, m.self) {
		return "", nodeproto.Errorf(nodeproto.CodeForbidden, "%s is this node's own container; the node never recreates itself", strings.TrimPrefix(old.Name, "/"))
	}
	if old.Config == nil || old.HostConfig == nil {
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "Docker's inspect of %s is incomplete", strings.TrimPrefix(old.Name, "/"))
	}
	return m.replace(ctx, old, image, 0, progress)
}
