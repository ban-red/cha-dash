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
	"github.com/distribution/reference"
	"github.com/docker/compose/v5/pkg/compose"

	"dash.cha.sh/internal/node/registry"
	"dash.cha.sh/internal/node/stacks/dockerapi"
)

// Docker is a fake daemon. Containers behave by their image's name:
// "…unhealthy…" fails its healthcheck, "…crashloop…" restarts on every
// look, "…exits…" exits with code 1, "…missing…" doesn't exist in any
// registry (the dry run refuses it); anything else runs, healthy when it
// has a healthcheck. Config hashes are Compose's own (compose.ServiceHash),
// as the real daemon's labels would be.
//
// It has a registry too (dockerapi.Registry): each tag has a digest,
// Digest(image) until Push moves it. Pull brings a tag's digest to the
// engine, and Up recreates a service whose container runs another digest
// than the engine has for its image, as Compose does.
type Docker struct {
	mu       sync.Mutex
	projects map[string]*project
	// FailUp makes every Up fail from the nth call on (1-based; 0 never).
	FailUp int
	// Unchecked makes the dry run unable to reach a registry.
	Unchecked bool
	// OnRecreate runs at the start of each RecreateImage (self-update).
	OnRecreate func(image string)
	// NoRepoDigests makes ImageRefs find no registry digest, as for an
	// image built or loaded locally.
	NoRepoDigests bool
	// LeaveOld makes RecreateImage leave the old container behind, stopped
	// and renamed <name>-chadash-old-<n>, as a recreate does when removing
	// it fails.
	LeaveOld bool
	// FailPull makes every Pull fail with it.
	FailPull error
	// LoginNeeded lists registries ("ghcr.io") that want a login this node
	// hasn't got: Ask, Pull and the dry run say so.
	LoginNeeded []string
	remote      map[string]string // tag (normalised) → the registry's digest reference
	local       map[string]string // tag (normalised) → the engine's, after a pull
	digests     map[string]string // container ID → the digest reference it runs
	pushes      int
	behave      map[string]string // digest reference → "unhealthy" or "exits" (Push)
	asked       []string
	ups         int
	vols        map[string][]string // project → its named volumes, kept by Down
	stopped     map[string]string   // container ID → its health when Stop stopped it
	calls       []string
	envs        []map[string]map[string]string
	next        int
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
		img := p.Services[name].Image
		if strings.Contains(img, "missing") {
			return fmt.Errorf("pull access denied for %s: manifest unknown", img)
		}
		if d := f.loginNeeded(img); d != "" {
			return errors.New(registry.NeedsLogin(d))
		}
	}
	return nil
}

// loginNeeded is the registry of image when it's in LoginNeeded; f.mu is
// held.
func (f *Docker) loginNeeded(image string) string {
	n, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return ""
	}
	if d := reference.Domain(n); slices.Contains(f.LoginNeeded, d) {
		return d
	}
	return ""
}

// key is an image's tag as the fake registry files it:
// "docker.io/library/nginx:1.27". A pinned reference is its own key.
func key(image string) string {
	n, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return image
	}
	return reference.TagNameOnly(n).String()
}

// localDigest is the digest reference the engine has for image; f.mu is
// held. "" without repo digests.
func (f *Docker) localDigest(image string) string {
	if f.NoRepoDigests {
		return ""
	}
	if strings.Contains(image, "@") {
		return Digest(image)
	}
	if d, ok := f.local[key(image)]; ok {
		return d
	}
	return Digest(image)
}

// runs is the digest reference a container runs; f.mu is held.
func (f *Docker) runs(c *dockerapi.Container) string {
	if d, ok := f.digests[c.ID]; ok {
		return d
	}
	return Digest(c.Image)
}

// Pull implements dockerapi.Docker.
func (f *Docker) Pull(_ context.Context, p *types.Project, services []string, progress func(dockerapi.Event)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "pull "+p.Name+" "+strings.Join(services, ","))
	if f.FailPull != nil {
		return f.FailPull
	}
	for _, s := range services {
		img := p.Services[s].Image
		if d := f.loginNeeded(img); d != "" {
			return errors.New(registry.NeedsLogin(d))
		}
		if r, ok := f.remote[key(img)]; ok && !strings.Contains(img, "@") {
			if f.local == nil {
				f.local = map[string]string{}
			}
			f.local[key(img)] = r
		}
		progress(dockerapi.Event{ID: img, Text: "Pulled", Status: "done"})
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
	for _, k := range sortedKeys(p.Volumes) {
		if v := p.Volumes[k]; !bool(v.External) && v.Name != "" && !slices.Contains(f.vols[p.Name], v.Name) {
			if f.vols == nil {
				f.vols = map[string][]string{}
			}
			f.vols[p.Name] = append(f.vols[p.Name], v.Name)
			slices.Sort(f.vols[p.Name])
		}
	}
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
		want := f.localDigest(svc.Image)
		if c := byService[name]; c != nil && c.ConfigHash == h && (f.NoRepoDigests || f.runs(c) == want) {
			next = append(next, c)
			continue
		}
		if svc.CustomLabels["com.docker.compose.project"] != p.Name {
			return fmt.Errorf("service %s lacks the compose project label", name)
		}
		f.next++
		c := &dockerapi.Container{ID: fmt.Sprintf("c%d", f.next), Name: p.Name + "-" + name + "-1", Service: name, Image: svc.Image, State: "running", ConfigHash: h}
		if want != "" {
			if f.digests == nil {
				f.digests = map[string]string{}
			}
			f.digests[c.ID] = want
		}
		switch b := f.behave[want]; {
		case strings.Contains(svc.Image, "unhealthy"), b == "unhealthy":
			c.Health = "unhealthy"
		case strings.Contains(svc.Image, "exits"), b == "exits":
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

// Stop implements dockerapi.Docker: every container exits with code 0.
func (f *Docker) Stop(_ context.Context, name string, _ func(dockerapi.Event)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop "+name)
	if p := f.projects[name]; p != nil {
		if f.stopped == nil {
			f.stopped = map[string]string{}
		}
		for _, c := range p.containers {
			if c.State == "running" {
				f.stopped[c.ID] = c.Health
			}
			c.State, c.Health, c.ExitCode = "exited", "", 0
		}
	}
	return nil
}

// Start implements dockerapi.Docker: every container runs again, and
// behaves by its image's name as Up's do.
func (f *Docker) Start(_ context.Context, name string, _ func(dockerapi.Event)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "start "+name)
	f.run(name)
	return nil
}

// Restart implements dockerapi.Docker, as Start.
func (f *Docker) Restart(_ context.Context, name string, _ func(dockerapi.Event)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "restart "+name)
	f.run(name)
	return nil
}

// run (re)starts a project's containers; f.mu is held. A container whose
// image has "unhealthy" in its name fails its healthcheck, "exits" exits.
func (f *Docker) run(name string) {
	p := f.projects[name]
	if p == nil {
		return
	}
	for _, c := range p.containers {
		if h, ok := f.stopped[c.ID]; ok {
			c.Health = h
			delete(f.stopped, c.ID)
		}
		c.State, c.ExitCode = "running", 0
		switch {
		case strings.Contains(c.Image, "unhealthy"):
			c.Health = "unhealthy"
		case strings.Contains(c.Image, "exits"):
			c.State, c.ExitCode = "exited", 1
		case c.Health != "":
			c.Health = "healthy"
		}
	}
}

// Volumes implements dockerapi.Docker: the named volumes Up created for
// the project, which Down keeps.
func (f *Docker) Volumes(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.vols[name]), nil
}

// RemoveVolumes implements dockerapi.Docker. A volume a container of its
// project still has is in use, as the engine would say.
func (f *Docker) RemoveVolumes(_ context.Context, names []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "rmvolumes "+strings.Join(names, ","))
	for project, vs := range f.vols {
		if p := f.projects[project]; p != nil && len(p.containers) > 0 && slices.ContainsFunc(names, func(n string) bool { return slices.Contains(vs, n) }) {
			return fmt.Errorf("volume is in use by %s", project)
		}
	}
	for project, vs := range f.vols {
		f.vols[project] = slices.DeleteFunc(vs, func(v string) bool { return slices.Contains(names, v) })
	}
	return nil
}

// ImageRefs implements dockerapi.Docker with Digest.
func (f *Docker) ImageRefs(_ context.Context, name string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	if p := f.projects[name]; p != nil && !f.NoRepoDigests {
		for _, c := range p.containers {
			out[c.Service] = f.runs(c)
		}
	}
	return out, nil
}

// ImageDigest implements dockerapi.Docker: Digest(image), or what the
// last pull of its tag brought.
func (f *Docker) ImageDigest(_ context.Context, image string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.localDigest(image), nil
}

// ── the registry (dockerapi.Registry) ────────────────────────────────────

var _ dockerapi.Registry = (*Docker)(nil)

// Ask implements dockerapi.Registry: the tag's digest, Digest(image) until
// Push moves it. "…missing…" has no such tag; a registry in LoginNeeded
// wants a login.
func (f *Docker) Ask(_ context.Context, tagged reference.NamedTagged, _ bool) registry.Answer {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := tagged.String()
	f.asked = append(f.asked, k)
	if d := reference.Domain(tagged); slices.Contains(f.LoginNeeded, d) {
		return registry.Answer{Reason: registry.NeedsLogin(d), Login: registry.LoginNeeded}
	}
	if strings.Contains(k, "missing") {
		return registry.Answer{Reason: "Its registry doesn't have that tag any more."}
	}
	r, ok := f.remote[k]
	if !ok {
		r = Digest(reference.FamiliarString(tagged))
	}
	_, d, _ := strings.Cut(r, "@")
	return registry.Answer{Digest: d}
}

// Push moves image's tag in the registry to a new digest, as pushing a new
// build would, and returns its digest reference. The engine keeps the old
// one until a pull. behaves, when given, is how a container of the new
// digest behaves, as an image name would say: "unhealthy" or "exits".
func (f *Docker) Push(image string, behaves ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes++
	if f.remote == nil {
		f.remote = map[string]string{}
	}
	ref := Digest(fmt.Sprintf("%s#%d", image, f.pushes)) // the tag goes, "#n" with it
	f.remote[key(image)] = ref
	if len(behaves) > 0 {
		if f.behave == nil {
			f.behave = map[string]string{}
		}
		f.behave[ref] = behaves[0]
	}
	return ref
}

// Asked is the tags Ask was asked about, normalised.
func (f *Docker) Asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
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

// AddContainer runs a container in a project (made if need be), as
// something outside Cha Dash would: Cha Dash's own hub and node, say. An
// empty ID gets one.
func (f *Docker) AddContainer(name string, c dockerapi.Container) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.projects[name]
	if p == nil {
		p = &project{}
		f.projects[name] = p
	}
	if c.ID == "" {
		f.next++
		c.ID = fmt.Sprintf("c%d", f.next)
	}
	p.containers = append(p.containers, &c)
}

// ── self-update (dockerapi.SelfUpdater) ──────────────────────────────────

var _ dockerapi.SelfUpdater = (*Docker)(nil)

// PullImage implements dockerapi.SelfUpdater: "…missing…" isn't in any
// registry; anything else resolves to Digest(ref).
func (f *Docker) PullImage(_ context.Context, ref string, progress func(string)) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "pullimage "+ref)
	f.mu.Unlock()
	if strings.Contains(ref, "missing") {
		return "", fmt.Errorf("pull access denied for %s: manifest unknown", ref)
	}
	progress("Pulled " + ref)
	return Digest(ref), nil
}

// RollbackImage implements dockerapi.SelfUpdater: RecreateImage, whose
// containers start anyway.
func (f *Docker) RollbackImage(ctx context.Context, id, image string, progress func(string)) (string, error) {
	return f.RecreateImage(ctx, id, image, progress)
}

// RecreateImage implements dockerapi.SelfUpdater: the container gets a new
// ID and the image, and behaves by its name as Up's containers do (a
// healthcheck is assumed, as the hub's image has one). OnRecreate, when
// set, runs first, outside the lock: a test can hold the update there.
func (f *Docker) RecreateImage(_ context.Context, id, image string, progress func(string)) (string, error) {
	f.mu.Lock()
	hook := f.OnRecreate
	f.mu.Unlock()
	if hook != nil {
		hook(image)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.projects {
		for _, c := range p.containers {
			if c.ID != id {
				continue
			}
			f.calls = append(f.calls, "recreate "+c.Service+" "+image)
			f.next++
			if f.LeaveOld {
				// The old one stays, stopped and renamed, ahead of the new.
				old := *c
				old.Name, old.State, old.Health = fmt.Sprintf("%s-chadash-old-%d", c.Name, f.next), "exited", ""
				nc := *c
				p.containers = append(p.containers, &nc)
				*c = old
				c = &nc
			}
			c.ID, c.Image, c.State, c.Health, c.ExitCode, c.RestartCount = fmt.Sprintf("c%d", f.next), image, "running", "healthy", 0, 0
			switch {
			case strings.Contains(image, "unhealthy"):
				c.Health = "unhealthy"
			case strings.Contains(image, "exits"):
				c.State, c.ExitCode = "exited", 1
			}
			progress("Recreated " + c.Name)
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("no such container: %s", id)
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

// SetConfigFiles changes the compose files Docker says started a project,
// such as "-" for one started from stdin.
func (f *Docker) SetConfigFiles(name string, files ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.projects[name]; p != nil {
		p.files = files
	}
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
