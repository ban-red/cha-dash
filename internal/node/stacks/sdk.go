package stacks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/client"
)

// SDK is Docker through the Compose SDK, in process: no docker CLI, the
// same reconciler and config hashes as `docker compose` (ADR-0020). It
// needs a daemon, so it's exercised by the maintainer's live run, not by
// `go test` (see README.md → Running it for real).
type SDK struct {
	cli command.Cli
}

var _ Docker = (*SDK)(nil)

// NewSDK connects to host (unix:///var/run/docker.sock, tcp://…), or the
// usual way when it's empty (DOCKER_HOST, or the default socket). The node
// passes the host its Docker module uses, so both reach the same daemon.
func NewSDK(host string) (*SDK, error) {
	cli, err := command.NewDockerCli()
	if err != nil {
		return nil, err
	}
	opts := &flags.ClientOptions{}
	if host != "" {
		opts.Hosts = []string{host}
	}
	if err := cli.Initialize(opts); err != nil {
		return nil, err
	}
	return &SDK{cli: cli}, nil
}

// service is a Compose service whose events go to progress. Each operation
// gets its own, so concurrent stacks don't mix their progress.
func (d *SDK) service(progress func(Event), dryRun bool) (api.Compose, error) {
	opts := []compose.Option{compose.WithPrompt(compose.AlwaysOkPrompt()), compose.WithEventProcessor(events{progress})}
	if dryRun {
		opts = append(opts, compose.WithDryRun)
	}
	return compose.NewComposeService(d.cli, opts...)
}

// Projects implements Docker.
func (d *SDK) Projects(ctx context.Context) ([]Project, error) {
	svc, err := d.service(nil, false)
	if err != nil {
		return nil, err
	}
	list, err := svc.List(ctx, api.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(list))
	for _, l := range list {
		p := Project{Name: l.Name, Status: l.Status}
		for f := range strings.SplitSeq(l.ConfigFiles, ",") {
			if f = strings.TrimSpace(f); f != "" {
				p.ConfigFiles = append(p.ConfigFiles, f)
			}
		}
		if cs, err := svc.Ps(ctx, l.Name, api.PsOptions{All: true}); err == nil && len(cs) > 0 {
			p.WorkingDir = cs[0].Labels[api.WorkingDirLabel]
		}
		out = append(out, p)
	}
	return out, nil
}

// Containers implements Docker.
func (d *SDK) Containers(ctx context.Context, project string) ([]Container, error) {
	svc, err := d.service(nil, false)
	if err != nil {
		return nil, err
	}
	cs, err := svc.Ps(ctx, project, api.PsOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(cs))
	for _, c := range cs {
		ct := Container{
			ID: c.ID, Name: c.Name, Service: c.Service, Image: c.Image,
			State: string(c.State), Health: string(c.Health), ExitCode: c.ExitCode,
			ConfigHash: c.Labels[api.ConfigHashLabel],
		}
		info, err := d.cli.Client().ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		ct.RestartCount = info.Container.RestartCount
		// Compose's list can leave Health empty, say while a container
		// restarts, and empty reads as "no healthcheck". Inspect knows.
		if st := info.Container.State; st != nil && st.Health != nil {
			ct.Health = string(st.Health.Status)
		}
		out = append(out, ct)
	}
	return out, nil
}

func upOptions(p *types.Project, wait time.Duration) api.UpOptions {
	return api.UpOptions{
		Create: api.CreateOptions{
			Recreate:             api.RecreateDiverged,
			RecreateDependencies: api.RecreateDiverged,
			RemoveOrphans:        true, // for an owned stack, the file is the whole truth
			QuietPull:            true,
		},
		Start: api.StartOptions{Project: p, Wait: wait > 0, WaitTimeout: wait},
	}
}

// DryRun implements Docker.
func (d *SDK) DryRun(ctx context.Context, p *types.Project) error {
	svc, err := d.service(nil, true)
	if err != nil {
		return err
	}
	if err := svc.Up(ctx, p, upOptions(p, 0)); err != nil {
		if unreachable(err) {
			return fmt.Errorf("%w: %v", ErrUnchecked, err)
		}
		return err
	}
	return nil
}

// unreachable tells "couldn't ask the registry" from "the registry said no".
func unreachable(err error) bool {
	s := strings.ToLower(err.Error())
	for _, w := range []string{"dial tcp", "no such host", "i/o timeout", "tls handshake timeout", "connection refused", "network is unreachable", "context deadline exceeded"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// Pull implements Docker.
func (d *SDK) Pull(ctx context.Context, p *types.Project, services []string, progress func(Event)) error {
	sub, err := p.WithSelectedServices(services)
	if err != nil {
		return err
	}
	svc, err := d.service(progress, false)
	if err != nil {
		return err
	}
	return svc.Pull(ctx, sub, api.PullOptions{Quiet: true})
}

// Up implements Docker.
func (d *SDK) Up(ctx context.Context, p *types.Project, wait time.Duration, progress func(Event)) error {
	svc, err := d.service(progress, false)
	if err != nil {
		return err
	}
	return svc.Up(ctx, p, upOptions(p, wait))
}

// Down implements Docker.
func (d *SDK) Down(ctx context.Context, project string, progress func(Event)) error {
	svc, err := d.service(progress, false)
	if err != nil {
		return err
	}
	return svc.Down(ctx, project, api.DownOptions{RemoveOrphans: true})
}

// ImageRefs implements Docker: per service, the registry digest of the
// image its container runs, from the image's RepoDigests.
func (d *SDK) ImageRefs(ctx context.Context, project string) (map[string]string, error) {
	svc, err := d.service(nil, false)
	if err != nil {
		return nil, err
	}
	cs, err := svc.Ps(ctx, project, api.PsOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, c := range cs {
		info, err := d.cli.Client().ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			return nil, err
		}
		img, err := d.cli.Client().ImageInspect(ctx, info.Container.Image)
		if err != nil {
			continue // removed since, or never pulled from a registry
		}
		for _, rd := range img.RepoDigests {
			if sameRepo(c.Image, rd) {
				out[c.Service] = rd
				break
			}
		}
	}
	return out, nil
}

// events adapts Compose's event processor to a progress callback.
type events struct{ fn func(Event) }

func (e events) Start(context.Context, string) {}
func (e events) Done(string, bool)             {}
func (e events) On(rs ...api.Resource) {
	if e.fn == nil {
		return
	}
	for _, r := range rs {
		st := "done"
		switch r.Status {
		case api.Working:
			st = "working"
		case api.Warning:
			st = "warning"
		case api.Error:
			st = "error"
		}
		e.fn(Event{ID: r.ID, Text: strings.TrimSpace(r.Text + " " + r.Details), Status: st})
	}
}
