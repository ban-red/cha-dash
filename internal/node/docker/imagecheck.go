package docker

import (
	"context"
	"encoding/json"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"dash.cha.sh/internal/node"
)

// The update states image.check answers (integration.ImageUpdate's).
const (
	updateCurrent   = "current"
	updateAvailable = "available"
	updateCantCheck = "cant_check"
)

// imageUpdate is one container's answer, integration.ImageUpdate plus the
// container and its image reference.
type imageUpdate struct {
	ID        string    `json:"id"`
	Image     string    `json:"image"`
	State     string    `json:"state"`
	Current   string    `json:"current,omitempty"`
	Latest    string    `json:"latest,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Reason    string    `json:"reason,omitempty"`
}

// imageCheck answers whether each container's image tag has moved in its
// registry (images.check). It sends the registry a HEAD for the tag's
// manifest (registry.Client.Head), which changes nothing and which Docker
// Hub doesn't count as a pull, and asks Docker only for a registry it can't
// reach itself. It logs in only where the policy's images.registries lists
// the registry and the node's Docker config has a login for it; a registry
// that needs one otherwise answers cant_check, saying so. With fresh
// (Check now), answers older than freshAfter are asked again.
func (m *Module) imageCheck(ctx context.Context, req json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	var r struct {
		IDs   []string `json:"ids"`
		Fresh bool     `json:"fresh"`
	}
	if len(req) > 0 && string(req) != "null" {
		if err := decode(req, &r); err != nil {
			return nil, err
		}
	}
	if len(r.IDs) > 100 {
		return nil, node.Invalid("at most 100 ids")
	}
	for _, id := range r.IDs {
		if !idRE.MatchString(id) {
			return nil, node.Invalid("ids must be container IDs or names")
		}
	}
	ids := r.IDs
	if len(ids) == 0 {
		res, err := m.cli.ContainerList(ctx, client.ContainerListOptions{})
		if err != nil {
			return nil, mapErr(err)
		}
		for _, c := range res.Items {
			ids = append(ids, c.ID)
		}
	}
	out := make([]imageUpdate, 0, len(ids))
	for _, id := range ids {
		res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			if cerrdefs.IsNotFound(err) && len(r.IDs) == 0 {
				continue // gone since the list
			}
			return nil, mapErr(err)
		}
		out = append(out, m.checkOne(ctx, res.Container, r.Fresh))
	}
	return reply(map[string]any{"images": out})
}

// checkOne is one container's answer.
func (m *Module) checkOne(ctx context.Context, c container.InspectResponse, fresh bool) imageUpdate {
	u := imageUpdate{ID: c.ID, State: updateCantCheck}
	if c.Config != nil {
		u.Image = c.Config.Image
	}
	tagged, why := tagOf(u.Image)
	if why != "" {
		u.Reason = why
		return u
	}
	img, err := m.cli.ImageInspect(ctx, c.Image)
	if err != nil {
		u.Reason = "Docker couldn't read its image: " + mapErr(err).Error()
		return u
	}
	var have []string
	for _, rd := range img.RepoDigests {
		if d, err := reference.ParseNormalizedNamed(rd); err == nil && d.Name() == tagged.Name() {
			if dg, ok := d.(reference.Digested); ok {
				have = append(have, dg.Digest().String())
			}
		}
	}
	if len(have) == 0 {
		u.Reason = "It was built or loaded on this host, so no registry has it."
		return u
	}
	a := m.check.Ask(ctx, tagged, fresh)
	u.CheckedAt = a.At
	if a.Digest == "" {
		u.Reason = a.Reason
		return u
	}
	u.Latest = a.Digest
	for _, d := range have {
		if d == a.Digest {
			u.State, u.Current = updateCurrent, d
			return u
		}
	}
	u.State, u.Current = updateAvailable, have[0]
	return u
}

// tagOf reads a container's image reference as the tag to ask a registry
// about ("nginx" is docker.io/library/nginx:latest). A reference with a
// tag and a digest, as an update leaves a container
// ("example/app:1@sha256:…"), still has its tag. why says why there's no
// tag: a digest alone, or no name Docker can look up. reference.Familiar
// gives the short form ("nginx:1.27") for pulls and sentences.
func tagOf(image string) (tag reference.NamedTagged, why string) {
	n, err := reference.ParseNormalizedNamed(image)
	if image == "" || err != nil {
		return nil, "Its image isn't a name Docker can look up."
	}
	name := reference.TrimNamed(n)
	if t, ok := n.(reference.Tagged); ok {
		tag, _ = reference.WithTag(name, t.Tag())
		return tag, ""
	}
	if _, ok := n.(reference.Digested); ok {
		return nil, "It's pinned by digest, so there's no tag to move."
	}
	tag, _ = reference.WithTag(name, "latest")
	return tag, ""
}
