package stacks

import (
	"context"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/compose"

	"dash.cha.sh/internal/stackproto"
)

// downtimePerRecreate is roughly how long a recreated service is
// unreachable: Compose stops the old container, then starts the new one
// (1.5 s per service in spike 4.6).
const downtimePerRecreate = 1.5

// planServices predicts what `up` will do without asking Docker to try:
// Compose stamps each container with a hash of its service config
// (compose.ServiceHash matched the CLI's label exactly in spike 4.6), so an
// equal hash means the service is left alone. Images whose reference
// changed are pulled first.
func planServices(p *types.Project, running []Container) ([]stackproto.ServicePlan, error) {
	type cur struct {
		hash, image string
	}
	have := map[string]cur{}
	for _, c := range running {
		if c.Service == "" {
			continue
		}
		have[c.Service] = cur{c.ConfigHash, c.Image}
	}
	var out []stackproto.ServicePlan
	for _, name := range sortedKeys(p.Services) {
		svc := p.Services[name]
		h, err := compose.ServiceHash(svc)
		if err != nil {
			return nil, err
		}
		sp := stackproto.ServicePlan{Name: name, Image: svc.Image}
		c, ok := have[name]
		delete(have, name)
		switch {
		case !ok:
			sp.Action, sp.Reason, sp.Pull = stackproto.ActionCreate, "new service", true
		case c.hash == h:
			sp.Action = stackproto.ActionUnchanged
		default:
			sp.Action, sp.Reason = stackproto.ActionRecreate, "its config changed"
			if c.image != svc.Image {
				sp.Pull, sp.Reason = true, "its image changed"
			}
		}
		out = append(out, sp)
	}
	for _, name := range sortedKeys(have) {
		out = append(out, stackproto.ServicePlan{Name: name, Action: stackproto.ActionRemove, Image: have[name].image, Reason: "no longer in the file"})
	}
	return out, nil
}

// planOf summarises a service plan.
func planOf(head string, changed bool, services []stackproto.ServicePlan) stackproto.Plan {
	pl := stackproto.Plan{Head: head, Changed: changed, Services: services}
	recreated, kept := 0, 0
	for _, s := range services {
		switch s.Action {
		case stackproto.ActionRecreate:
			recreated++
		case stackproto.ActionUnchanged:
			kept++
		}
	}
	pl.DowntimeS = float64(recreated) * downtimePerRecreate
	// The file is the one running, yet nothing matches: a different Compose
	// version stamped the containers, so the first deploy recreates them.
	pl.RecreateAll = !changed && recreated > 0 && kept == 0
	return pl
}

// summary is a plan as a commit subject's tail: "recreate web, create db".
func summary(services []stackproto.ServicePlan) string {
	var parts []string
	for _, s := range services {
		if s.Action != stackproto.ActionUnchanged {
			parts = append(parts, s.Action+" "+s.Name)
		}
	}
	if len(parts) == 0 {
		return "no service changes"
	}
	return strings.Join(parts, ", ")
}

// pulls lists the services whose images a deploy pulls.
func pulls(services []stackproto.ServicePlan) []string {
	var out []string
	for _, s := range services {
		if s.Pull {
			out = append(out, s.Name)
		}
	}
	return out
}

// heldRef is what a held service is pinned to: the tag its container runs,
// with that image's registry digest ("syslog-ng:latest@sha256:…"), as a
// rollback pins. Keeping the tag lets the Update Center ask about it once
// the hold is released; a digest alone reads as "pinned by digest". An
// image without a tag, or from another repository, keeps the digest
// reference as it is.
func heldRef(image, digestRef string) string {
	_, digest, ok := strings.Cut(digestRef, "@")
	img, _, _ := strings.Cut(image, "@")
	if !ok || img == "" || stripTag(img) == img || !sameRepo(img, digestRef) {
		return digestRef
	}
	return img + "@" + digest
}

// keepHeld keeps each held service that the plan would recreate on the
// image it runs now: in p only, pinned to that image's registry digest
// (as a rollback pins), so the committed file stays as written. A held
// service the plan leaves alone isn't touched, and a pinned one isn't
// pulled. It returns the plan again, with the pinned services' reasons.
func (m *Module) keepHeld(ctx context.Context, stack string, p *types.Project, running []Container, hold []string, services []stackproto.ServicePlan) ([]stackproto.ServicePlan, error) {
	if len(hold) == 0 {
		return services, nil
	}
	held := map[string]bool{}
	for _, h := range hold {
		held[h] = true
	}
	var touched []string
	for _, sp := range services {
		if held[sp.Name] && (sp.Action == stackproto.ActionRecreate || sp.Pull) && sp.Action != stackproto.ActionCreate {
			touched = append(touched, sp.Name)
		}
	}
	if len(touched) == 0 {
		return services, nil
	}
	refs, err := m.docker.ImageRefs(ctx, stack)
	if err != nil {
		m.log.Warn("stacks: couldn't read the running images; held services aren't kept", "stack", stack, "err", err)
		refs = nil
	}
	runs := map[string]string{} // service → the image its container was made from
	for _, c := range running {
		if c.Service != "" {
			runs[c.Service] = c.Image
		}
	}
	pinned, unkept := map[string]bool{}, map[string]bool{}
	for _, name := range touched {
		svc, ok := p.Services[name]
		if ref := refs[name]; ok && ref != "" {
			svc.Image = heldRef(runs[name], ref)
			p.Services[name] = svc
			pinned[name] = true
		} else {
			unkept[name] = true
		}
	}
	if len(pinned) > 0 {
		if services, err = planServices(p, running); err != nil {
			return nil, err
		}
	}
	for i, sp := range services {
		switch {
		case pinned[sp.Name]:
			services[i].Pull = false
			if sp.Action != stackproto.ActionUnchanged {
				services[i].Reason = "held: kept on the image it runs"
			}
		case unkept[sp.Name]:
			services[i].Reason += "; it's held, but its image has no registry digest to keep it on"
		}
	}
	return services, nil
}
