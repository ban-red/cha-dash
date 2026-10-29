// Package stackstest has a fake Docker daemon for the stacks module, so the
// deploy pipeline (plan, preflight, commit, gate, rollback) runs in tests
// without one: the module's own tests and the hub's end-to-end ones.
package stackstest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/compose"

	"dash.cha.sh/internal/node/stacks/dockerapi"
)

// Docker is a fake daemon. Containers behave by their image's name:
// "…unhealthy…" fails its healthcheck, "…crashloop…" restarts on every
// look, "…exits…" exits with code 1, "…missing…" doesn't exist in any
// registry (the dry run refuses it); anything else runs, healthy when it
// has a healthcheck. Config hashes are Compose's own (compose.ServiceHash),
// as the real daemon's labels would be.
type Docker struct {
	mu       sync.Mutex
	projects map[string]*project
	// FailUp makes every Up fail from the nth call on (1-based; 0 never).
	FailUp int
	// Unchecked makes the dry run unable to reach a registry.
	Unchecked bool
	ups       int
	calls     []string
	envs      []map[string]map[string]string
	next      int
}

type project struct {
	files      []string
	workdir    string
	containers []*dockerapi.Container
}

var _ dockerapi.Docker = (*Docker)(nil)

// New returns a daemon running nothing.
func New() *Docker { return &Docker{projects: map[string]*project{}} }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Projects implements dockerapi.Docker.
func (f *Docker) Projects(context.Context) ([]dockerapi.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []dockerapi.Project
	for _, name := range sortedKeys(f.projects) {
		p := f.projects[name]
		out = append(out, dockerapi.Project{Name: name, Status: "running", ConfigFiles: p.files, WorkingDir: p.workdir})
	}
	return out, nil
}

// Containers implements dockerapi.Docker.
func (f *Docker) Containers(_ context.Context, name string) ([]dockerapi.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.projects[name]
	if p == nil {
		return nil, nil
	}
	var out []dockerapi.Container
	for _, c := range p.containers {
		if strings.Contains(c.Image, "crashloop") {
			c.RestartCount++
		}
		out = append(out, *c)
	}
	return out, nil
}

// DryRun implements dockerapi.Docker.
func (f *Docker) DryRun(_ context.Context, p *types.Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "dryrun "+p.Name)
	if f.Unchecked {
		return fmt.Errorf("%w: dial tcp: lookup registry.example: no such host", dockerapi.ErrUnchecked)
	}
	for _, name := range sortedKeys(p.Services) {
		if img := p.Services[name].Image; strings.Contains(img, "missing") {
			return fmt.Errorf("pull access denied for %s: manifest unknown", img)
		}
	}
	return nil
}

// Pull implements dockerapi.Docker.
func (f *Docker) Pull(_ context.Context, p *types.Project, services []string, progress func(dockerapi.Event)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "pull "+p.Name+" "+strings.Join(services, ","))
	for _, s := range services {
		progress(dockerapi.Event{ID: p.Services[s].Image, Text: "Pulled", Status: "done"})
	}
	return nil
}

// Up implements dockerapi.Docker: services whose hash changed get a new
// container; orphans go.
func (f *Docker) Up(_ context.Context, p *types.Project, _ time.Duration, progress func(dockerapi.Event)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ups++
	f.calls = append(f.calls, "up "+p.Name)
	env := map[string]map[string]string{}
	for name, svc := range p.Services {
		env[name] = map[string]string{}
		for k, v := range svc.Environment {
			if v != nil {
				env[name][k] = *v
			}
		}
	}
	f.envs = append(f.envs, env)
	if f.FailUp > 0 && f.ups >= f.FailUp {
		return errors.New("the daemon refused: no space left on device")
	}
	cur := f.projects[p.Name]
	if cur == nil {
		cur = &project{}
		f.projects[p.Name] = cur
	}
	cur.workdir, cur.files = p.WorkingDir, p.ComposeFiles
	byService := map[string]*dockerapi.Container{}
	for _, c := range cur.containers {
		byService[c.Service] = c
	}
	var next []*dockerapi.Container
	for _, name := range sortedKeys(p.Services) {
		svc := p.Services[name]
		h, err := compose.ServiceHash(svc)
		if err != nil {
			return err
		}
		if c := byService[name]; c != nil && c.ConfigHash == h {
			next = append(next, c)
			continue
		}
		if svc.CustomLabels["com.docker.compose.project"] != p.Name {
			return fmt.Errorf("service %s lacks the compose project label", name)
		}
		f.next++
		c := &dockerapi.Container{ID: fmt.Sprintf("c%d", f.next), Name: p.Name + "-" + name + "-1", Service: name, Image: svc.Image, State: "running", ConfigHash: h}
		switch {
		case strings.Contains(svc.Image, "unhealthy"):
			c.Health = "unhealthy"
		case strings.Contains(svc.Image, "exits"):
			c.State, c.ExitCode = "exited", 1
		case svc.HealthCheck != nil:
			c.Health = "healthy"
		}
		progress(dockerapi.Event{ID: c.Name, Text: "Recreated", Status: "done"})
		next = append(next, c)
	}
	cur.containers = next
	return nil
}

// Down implements dockerapi.Docker.
func (f *Docker) Down(_ context.Context, name string, _ func(dockerapi.Event)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "down "+name)
	delete(f.projects, name)
	return nil
}

// ImageRefs implements dockerapi.Docker with Digest.
func (f *Docker) ImageRefs(_ context.Context, name string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	if p := f.projects[name]; p != nil {
		for _, c := range p.containers {
			out[c.Service] = Digest(c.Image)
		}
	}
	return out, nil
}

// Digest is the fake registry's digest reference for an image:
// "nginx:1.27" → "nginx@sha256:<sha256 of the reference>". An image
// already pinned keeps its digest.
func Digest(image string) string {
	ref, d, pinned := strings.Cut(image, "@")
	repo := ref
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		repo = ref[:i]
	}
	if pinned {
		return repo + "@" + d
	}
	sum := sha256.Sum256([]byte(image))
	return repo + "@sha256:" + hex.EncodeToString(sum[:])
}

// Called counts the calls that start with prefix ("up media", "pull ").
func (f *Docker) Called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// Running is the image each service of a project runs.
func (f *Docker) Running(name string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	if p := f.projects[name]; p != nil {
		for _, c := range p.containers {
			out[c.Service] = c.Image
		}
	}
	return out
}

// LastEnv is the environment the last Up gave each service, resolved.
func (f *Docker) LastEnv() map[string]map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.envs) == 0 {
		return nil
	}
	return f.envs[len(f.envs)-1]
}

// Reset forgets the calls so far (after setting a scene up).
func (f *Docker) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ups, f.calls, f.envs = 0, nil, nil
}

// SetHash overwrites a running service's config-hash label, as a Compose
// version that hashed differently would have stamped it.
func (f *Docker) SetHash(name, service, hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.projects[name]; p != nil {
		for _, c := range p.containers {
			if c.Service == service {
				c.ConfigHash = hash
			}
		}
	}
}

// SetFiles gives a running project other compose files, as an override
// does, keeping its containers.
func (f *Docker) SetFiles(name string, files []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.projects[name]; p != nil {
		p.files = files
	}
}

// AddProject registers a project that runs no container the fake models,
// such as one with override files.
func (f *Docker) AddProject(name string, files []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects[name] = &project{files: files}
}
