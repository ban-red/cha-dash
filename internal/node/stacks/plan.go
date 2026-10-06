package stacks

import (
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
