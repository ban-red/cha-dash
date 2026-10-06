// Package dockerapi is the seam between the stacks module and the Docker
// daemon: the Docker interface the module calls and the shapes it uses.
// The stacks package implements it with the Compose SDK (stacks.SDK);
// stackstest has a fake for tests that need no daemon.
package dockerapi

import (
	"context"
	"errors"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
)

// Docker is everything the stacks module asks of the Docker daemon,
// through the Compose SDK. stacks.SDK implements it; tests use
// stackstest.Docker, so the whole pipeline (plan, preflight, commit, gate,
// rollback) runs without a daemon.
type Docker interface {
	// Projects lists the compose projects Docker knows about, from their
	// containers' labels (compose ls --all).
	Projects(ctx context.Context) ([]Project, error)
	// Containers lists a project's containers, stopped ones included, with
	// their restart counts (compose ps --all, plus an inspect each).
	Containers(ctx context.Context, project string) ([]Container, error)
	// DryRun runs `up` through Compose's reconciler without applying it.
	// It resolves every image against its registry, so a missing tag fails
	// here. Wrap ErrUnchecked when the registry couldn't be reached.
	DryRun(ctx context.Context, p *types.Project) error
	// Pull pulls the named services' images.
	Pull(ctx context.Context, p *types.Project, services []string, progress func(Event)) error
	// Up applies the project, removing orphans, and waits up to wait for
	// every container to run (and be healthy, where it has a healthcheck).
	Up(ctx context.Context, p *types.Project, wait time.Duration, progress func(Event)) error
	// Down removes the project's containers and networks (never volumes).
	Down(ctx context.Context, project string, progress func(Event)) error
	// ImageRefs is, per service, the image its container runs as a
	// pullable digest reference ("nginx@sha256:…"), for pinning rollbacks.
	// Services whose image has no registry digest are left out.
	ImageRefs(ctx context.Context, project string) (map[string]string, error)
}

// ErrUnchecked marks a preflight that couldn't reach a registry: "couldn't
// check" is not "would fail", and the message says which.
var ErrUnchecked = errors.New("couldn't reach the registry to check the images")

// Project is a compose project as Docker's labels describe it.
type Project struct {
	Name        string
	Status      string   // Compose's own summary: "running(2)", "exited(1)"
	ConfigFiles []string // absolute paths
	WorkingDir  string
}

// Container is one container of a project.
type Container struct {
	ID           string
	Name         string
	Service      string
	Image        string // as configured: "nginx:1.28"
	State        string // running, exited, restarting, created, paused, dead
	Health       string // healthy, unhealthy, starting, "" without a healthcheck
	ExitCode     int
	RestartCount int
	ConfigHash   string // com.docker.compose.config-hash
}

// Event is one Compose progress event: "web  Recreated".
type Event struct {
	ID     string
	Text   string
	Status string // working, done, warning, error
}
