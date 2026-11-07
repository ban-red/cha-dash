package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/client"

	"dash.cha.sh/internal/node/registry"
	"dash.cha.sh/internal/nodeproto"
)

// The registry answers image.check keeps, as internal/node/registry has
// them.
const (
	CheckEvery = registry.CheckEvery
	checkRetry = registry.CheckRetry
	freshAfter = registry.FreshAfter
)

// Registry is the module's registry checker, for the stacks module to
// share: one cache of answers per node, and Docker's lookup for a registry
// only the daemon can reach.
func (m *Module) Registry() *registry.Checker { return m.check }

// newChecker asks registries for image.check: at m.registry's origin (tests
// point it elsewhere), on m.now's clock, with the node's logins, and
// through Docker for a registry the node can't reach itself.
func (m *Module) newChecker() *registry.Checker {
	return &registry.Checker{
		Base: func(d string) string { return m.registry(d) }, HTTP: registry.NewHTTPClient(), Logins: m.logins,
		Now: func() time.Time { return m.now() },
		Fallback: func(ctx context.Context, ref string) (string, error) {
			opts := client.DistributionInspectOptions{}
			if n, err := reference.ParseNormalizedNamed(ref); err == nil {
				opts.EncodedRegistryAuth = m.logins.EncodedAuth(reference.Domain(n))
			}
			res, err := m.cli.DistributionInspect(ctx, ref, opts)
			if err != nil {
				return "", err
			}
			return res.Descriptor.Digest.String(), nil
		},
	}
}

// pullOptions carries the node's login for ref's registry, when the
// policy allows one.
func (m *Module) pullOptions(ref string) client.ImagePullOptions {
	n, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return client.ImagePullOptions{}
	}
	return client.ImagePullOptions{RegistryAuth: m.logins.EncodedAuth(reference.Domain(n))}
}

// pullError says a refused pull in the registry sentence when the registry
// wants a login, and passes anything else through.
func (m *Module) pullError(ctx context.Context, ref string, err error) error {
	if err == nil || !registry.IsAuthError(err) {
		return err
	}
	n, perr := reference.ParseNormalizedNamed(ref)
	if perr != nil {
		return err
	}
	domain := reference.Domain(n)
	_, have := m.logins.For(domain)
	le := m.logins.Describe(registry.LoginErrorFor(err, domain, have))
	// Docker's containerd image store words a registry's 401 as "repository
	// does not exist or may require 'docker login'", the same as a missing
	// image on Docker Hub. image.check's own question to the registry tells
	// them apart, so ask it (its answer is kept, so this is rarely a request).
	if le.Maybe && !have {
		if tagged, ok := n.(reference.NamedTagged); ok && m.check.Ask(ctx, tagged, false).Login == registry.LoginNeeded {
			le.Maybe = false
		}
	}
	return nodeproto.Errorf(nodeproto.CodeFailed, "%s", fmt.Sprint(le))
}
