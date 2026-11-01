package docker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"dash.cha.sh/internal/node"
)

// CheckEvery is how long the node keeps a registry's answer for a tag: a
// container's tag is asked about at most this often.
const CheckEvery = 6 * time.Hour

// checkRetry is how long a failed answer is kept: a registry that was down
// is asked again sooner than one that answered.
const checkRetry = 15 * time.Minute

// freshAfter is how old an answer must be for a fresh check (Check now) to
// ask again, so pressing it twice doesn't ask twice.
const freshAfter = time.Minute

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

// tagAnswer is what a registry said about a tag, when, and for how long
// the node keeps it.
type tagAnswer struct {
	digest string // the tag's manifest digest, "sha256:…"
	reason string // why there's no digest
	at     time.Time
	keep   time.Duration
}

// tagCache keeps registries' answers, so a tag is asked about at most once
// every CheckEvery whoever asks.
type tagCache struct {
	mu   sync.Mutex
	tags map[string]tagAnswer
}

// imageCheck answers whether each container's image tag has moved in its
// registry (images.check). It sends the registry a HEAD for the tag's
// manifest (headTag), which changes nothing and which Docker Hub doesn't
// count as a pull, and asks Docker only for a registry it can't reach
// itself. It sends no credentials, so a registry that needs a login
// answers cant_check. With fresh (Check now), answers older than
// freshAfter are asked again.
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
	a := m.askRegistry(ctx, tagged, fresh)
	u.CheckedAt = a.at
	if a.digest == "" {
		u.Reason = a.reason
		return u
	}
	u.Latest = a.digest
	for _, d := range have {
		if d == a.digest {
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

// askRegistry is the registry's answer for the tag, from the cache while
// it's fresh: for keep, or with fresh for freshAfter. It asks the registry
// with a HEAD (headTag), and Docker only when the registry can't be reached
// directly.
func (m *Module) askRegistry(ctx context.Context, tagged reference.NamedTagged, fresh bool) tagAnswer {
	ref := tagged.String()
	now := m.now()
	m.tags.mu.Lock()
	a, ok := m.tags.tags[ref]
	m.tags.mu.Unlock()
	keep := a.keep
	if fresh {
		keep = min(keep, freshAfter)
	}
	if ok && now.Sub(a.at) < keep {
		return a
	}
	a = tagAnswer{at: now, keep: CheckEvery}
	d, err := m.headTag(ctx, tagged)
	if err != nil && ctx.Err() == nil && !errors.Is(err, errRegistryLogin) && !errors.Is(err, errRegistryNotFound) && !errors.Is(err, errRegistryAnswered) {
		// Not reachable from here (plain http, a registry only the
		// daemon's config knows): Docker's lookup, which may count as a
		// pull, but only for such registries. A registry that answered
		// (even a 429) is never asked again this way.
		ictx, cancel := context.WithTimeout(ctx, registryTimeout)
		res, ierr := m.cli.DistributionInspect(ictx, ref, client.DistributionInspectOptions{})
		cancel()
		if err = ierr; err == nil {
			d = res.Descriptor.Digest.String()
		}
	}
	if err != nil && ctx.Err() != nil {
		// The caller gave up (a sync's budget): say so, but keep nothing,
		// so the next check asks the registry instead of repeating this.
		a.reason = "The check was cut short before its registry answered."
		return a
	}
	switch {
	case err == nil:
		a.digest = d
	case errors.Is(err, errRegistryLogin) || cerrdefs.IsUnauthorized(err) || cerrdefs.IsPermissionDenied(err) || strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "denied"):
		// A login won't appear by itself: kept as long as an answer.
		a.reason = "Its registry needs a login, and the node doesn't send one."
	case errors.Is(err, errRegistryNotFound) || cerrdefs.IsNotFound(err):
		a.reason, a.keep = "Its registry doesn't have that tag any more.", checkRetry
	default:
		a.reason, a.keep = "Its registry didn't answer: "+err.Error(), checkRetry
	}
	m.tags.mu.Lock()
	if m.tags.tags == nil {
		m.tags.tags = map[string]tagAnswer{}
	}
	m.tags.tags[ref] = a
	m.tags.mu.Unlock()
	return a
}
