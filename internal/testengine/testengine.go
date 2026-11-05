// Package testengine runs tests against a real Docker Engine: layer 3 in
// docs/plan/17-test-automation.md. Tests that use it carry the `engine`
// build tag and run with `bun run test:engine`.
//
// Everything a test starts is throwaway and removed in cleanup, even after
// a failure: containers, networks and volumes carry the label
// chadash.test=<run>, and compose projects are named cte-<run>-<name>
// (Project). A leak check (LeakCheck, run by CI after the suite) fails on
// anything left behind.
//
// Images are never built. A canary image (Push) is a pinned busybox with
// a different config, committed through the Engine API and pushed to a
// throwaway registry (Registry) on the engine's loopback. That registry is
// reachable from the engine and from this process, so the engine must be
// local (a socket, OrbStack, Docker Desktop, a CI runner).
//
// With no engine the tests skip, saying why; with CHADASH_REQUIRE_ENGINE=1
// (CI) they fail instead.
package testengine

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"golang.org/x/crypto/bcrypt"
)

// The images the harness pulls, pinned to their multi-arch index digests.
const (
	Busybox       = "busybox:1.37@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"
	RegistryImage = "registry:3@sha256:d4032d4cc323c1731c707931ecada07a53076eac38e3b61ae57533f170f2c6c3"
)

// Label marks everything a run starts; its value is the run's ID.
const Label = "chadash.test"

// ProjectPrefix starts every compose project a test deploys.
const ProjectPrefix = "cte-"

// Engine is one test's connection to the engine, and what it must clean up.
type Engine struct {
	t   testing.TB
	Cli *client.Client
	// Host is the engine's address, for the node's modules (docker.Options.Host,
	// stacks.NewSDK).
	Host string
	// Run is this test's ID: 8 hex characters.
	Run string

	mu       sync.Mutex
	projects []string
}

// New connects to the engine named by the environment (DOCKER_HOST, or the
// default socket), and registers the cleanup. It skips the test when there
// is no engine, unless CHADASH_REQUIRE_ENGINE=1.
func New(t testing.TB) *Engine {
	t.Helper()
	if testing.Short() {
		t.Skip("engine tests don't run with -short")
	}
	cli, err := client.New(client.FromEnv, client.WithUserAgent("chadash-testengine"))
	if err != nil {
		unavailable(t, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		cli.Close()
		unavailable(t, err)
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	e := &Engine{t: t, Cli: cli, Host: cli.DaemonHost(), Run: hex.EncodeToString(b[:])}
	t.Cleanup(func() {
		e.cleanup()
		cli.Close()
	})
	return e
}

func unavailable(t testing.TB, err error) {
	t.Helper()
	if os.Getenv("CHADASH_REQUIRE_ENGINE") == "1" {
		t.Fatalf("no Docker Engine, and CHADASH_REQUIRE_ENGINE=1: %v", err)
	}
	t.Skipf("no Docker Engine (%v): engine tests skipped", err)
}

// Labels are the labels every container, network and volume of this run carries.
func (e *Engine) Labels() map[string]string { return map[string]string{Label: e.Run} }

// Project is a compose project name for this run, registered for cleanup.
func (e *Engine) Project(name string) string {
	p := ProjectPrefix + e.Run + "-" + name
	e.mu.Lock()
	e.projects = append(e.projects, p)
	e.mu.Unlock()
	return p
}

// Ctx is a context for one engine call: the test's, bounded.
func (e *Engine) Ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

// Pull pulls an image unless the engine already has it.
func (e *Engine) Pull(ref string) {
	e.t.Helper()
	ctx, cancel := e.Ctx()
	defer cancel()
	if _, err := e.Cli.ImageInspect(ctx, ref); err == nil {
		return
	}
	r, err := e.Cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		e.t.Fatalf("pull %s: %v", ref, err)
	}
	if err := r.Wait(ctx); err != nil {
		e.t.Fatalf("pull %s: %v", ref, err)
	}
}

// Start starts a container from image with this run's labels, and returns
// its ID. extra labels are added.
func (e *Engine) Start(name, image string, extra map[string]string) string {
	e.t.Helper()
	ctx, cancel := e.Ctx()
	defer cancel()
	labels := e.Labels()
	maps.Copy(labels, extra)
	res, err := e.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   ProjectPrefix + e.Run + "-" + name,
		Config: &container.Config{Image: image, Labels: labels},
	})
	if err != nil {
		e.t.Fatalf("create %s: %v", name, err)
	}
	if _, err := e.Cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		e.t.Fatalf("start %s: %v", name, err)
	}
	return res.ID
}

// Inspect inspects a container.
func (e *Engine) Inspect(id string) container.InspectResponse {
	e.t.Helper()
	ctx, cancel := e.Ctx()
	defer cancel()
	res, err := e.Cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		e.t.Fatalf("inspect %s: %v", id, err)
	}
	return res.Container
}

// WaitHealthy waits until the container's health check passes.
func (e *Engine) WaitHealthy(id string, within time.Duration) {
	e.t.Helper()
	deadline := time.Now().Add(within)
	for {
		c := e.Inspect(id)
		if c.State != nil && c.State.Health != nil && c.State.Health.Status == container.Healthy {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("%s isn't healthy after %s: %+v", c.Name, within, c.State)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ── registry ──

// Registry is a throwaway registry on the engine's loopback.
type Registry struct {
	// Host is host:port, as image references name it.
	Host string
	// User and Password are set when the registry needs a login.
	User, Password string
}

// Ref is an image reference in the registry.
func (r *Registry) Ref(repo, tag string) string { return r.Host + "/" + repo + ":" + tag }

// Auth is the X-Registry-Auth value for the registry's login. Without one
// it's an empty login ({}): older engines refuse a push with no header at
// all ("missing X-Registry-Auth"), which newer ones allow.
func (r *Registry) Auth() string {
	if r.User == "" {
		return base64.URLEncoding.EncodeToString([]byte("{}"))
	}
	b, _ := json.Marshal(map[string]string{"username": r.User, "password": r.Password, "serveraddress": r.Host})
	return base64.URLEncoding.EncodeToString(b)
}

// RegistryOption configures Registry.
type RegistryOption func(*registryConfig)

type registryConfig struct{ user, password string }

// WithLogin makes the registry ask for this user and password (htpasswd).
func WithLogin(user, password string) RegistryOption {
	return func(c *registryConfig) { c.user, c.password = user, password }
}

// Registry starts a registry on 127.0.0.1 at a free port and waits until it
// answers.
func (e *Engine) Registry(opts ...RegistryOption) *Registry {
	e.t.Helper()
	var cfg registryConfig
	for _, o := range opts {
		o(&cfg)
	}
	e.Pull(RegistryImage)
	ctx, cancel := e.Ctx()
	defer cancel()
	env := []string{"REGISTRY_LOG_LEVEL=warn", "OTEL_TRACES_EXPORTER=none"}
	if cfg.user != "" {
		env = append(env, "REGISTRY_AUTH=htpasswd", "REGISTRY_AUTH_HTPASSWD_REALM=chadash-test", "REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd")
	}
	port := network.MustParsePort("5000/tcp")
	res, err := e.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   ProjectPrefix + e.Run + "-registry-" + randHex(2),
		Config: &container.Config{Image: RegistryImage, Env: env, Labels: e.Labels(), ExposedPorts: network.PortSet{port: {}}},
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{
			port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}},
		}},
	})
	if err != nil {
		e.t.Fatalf("registry: %v", err)
	}
	if cfg.user != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(cfg.password), bcrypt.MinCost)
		if err != nil {
			e.t.Fatal(err)
		}
		content := tarOf("htpasswd", []byte(cfg.user+":"+string(hash)+"\n"))
		if _, err := e.Cli.CopyToContainer(ctx, res.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: dirTar("auth", content)}); err != nil {
			e.t.Fatalf("registry login: %v", err)
		}
	}
	if _, err := e.Cli.ContainerStart(ctx, res.ID, client.ContainerStartOptions{}); err != nil {
		e.t.Fatalf("registry: %v", err)
	}
	c := e.Inspect(res.ID)
	var host string
	for _, b := range c.NetworkSettings.Ports[port] {
		if b.HostPort != "" {
			host = "127.0.0.1:" + b.HostPort
		}
	}
	if host == "" {
		e.t.Fatalf("registry: no published port: %+v", c.NetworkSettings.Ports)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/v2/", nil)
		r, err := http.DefaultClient.Do(req)
		if err == nil {
			r.Body.Close()
			if r.StatusCode == http.StatusOK || r.StatusCode == http.StatusUnauthorized {
				break
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("registry at %s doesn't answer: %v", host, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &Registry{Host: host, User: cfg.user, Password: cfg.password}
}

// ── canary images ──

// Canary is how a canary image behaves when it runs.
type Canary int

const (
	// Healthy serves HTTP on 8080 and its health check passes.
	Healthy Canary = iota
	// ExitsOne exits with status 1 at once.
	ExitsOne
	// NeverHealthy runs, but its health check never passes.
	NeverHealthy
)

func (c Canary) String() string {
	return [...]string{"healthy", "exits-one", "never-healthy"}[c]
}

// serve is a canary's web server; probe its health check.
const serve = "mkdir -p /w && echo ok > /w/index.html && exec httpd -f -p 8080 -h /w"

func probe(port string) *container.HealthConfig {
	return &container.HealthConfig{
		Test:     []string{"CMD", "wget", "-q", "-O-", "http://127.0.0.1:" + port + "/"},
		Interval: time.Second, Timeout: time.Second, Retries: 2,
	}
}

// Push makes a canary image of the given kind, tags it as ref (in reg),
// pushes it and returns its pinned reference, repo@sha256:…. version tells
// canaries of the same kind apart, so pushing a new version moves the tag.
func (e *Engine) Push(reg *Registry, ref string, kind Canary, version string) string {
	e.t.Helper()
	e.Pull(Busybox)
	ctx, cancel := e.Ctx()
	defer cancel()
	cfg := &container.Config{Labels: map[string]string{Label: e.Run, Label + ".canary": kind.String() + "/" + version}}
	switch kind {
	case Healthy:
		cfg.Entrypoint, cfg.Healthcheck = []string{"sh", "-c", serve}, probe("8080")
	case ExitsOne:
		cfg.Entrypoint = []string{"sh", "-c", "exit 1"}
	case NeverHealthy:
		cfg.Entrypoint, cfg.Healthcheck = []string{"sh", "-c", serve}, probe("9")
	}
	cfg.Cmd = []string{}
	base, err := e.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: ProjectPrefix + e.Run + "-canary-" + randHex(3), Config: &container.Config{Image: Busybox, Labels: e.Labels()},
	})
	if err != nil {
		e.t.Fatalf("canary: %v", err)
	}
	commit, err := e.Cli.ContainerCommit(ctx, base.ID, client.ContainerCommitOptions{Config: cfg})
	_, _ = e.Cli.ContainerRemove(ctx, base.ID, client.ContainerRemoveOptions{Force: true})
	if err != nil {
		e.t.Fatalf("canary commit: %v", err)
	}
	if _, err := e.Cli.ImageTag(ctx, client.ImageTagOptions{Source: commit.ID, Target: ref}); err != nil {
		e.t.Fatalf("canary tag %s: %v", ref, err)
	}
	push, err := e.Cli.ImagePush(ctx, ref, client.ImagePushOptions{RegistryAuth: reg.Auth()})
	if err != nil {
		e.t.Fatalf("push %s: %v", ref, err)
	}
	if err := push.Wait(ctx); err != nil {
		e.t.Fatalf("push %s: %v", ref, err)
	}
	img, err := e.Cli.ImageInspect(ctx, commit.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	repo, _, _ := strings.Cut(strings.TrimPrefix(ref, reg.Host+"/"), ":")
	for _, d := range img.RepoDigests {
		if strings.HasPrefix(d, reg.Host+"/"+repo+"@") {
			return d
		}
	}
	e.t.Fatalf("push %s: no digest in %v", ref, img.RepoDigests)
	return ""
}

// ── cleanup and the leak check ──

func (e *Engine) mine(labels map[string]string) bool {
	if labels[Label] == e.Run {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Contains(e.projects, labels["com.docker.compose.project"])
}

// cleanup removes what this run started: containers, networks, volumes,
// then images. It logs, never fails: a failure here is what LeakCheck finds.
func (e *Engine) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if cs, err := e.Cli.ContainerList(ctx, client.ContainerListOptions{All: true}); err == nil {
		for _, c := range cs.Items {
			if e.mine(c.Labels) {
				if _, err := e.Cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
					e.t.Logf("cleanup: container %s: %v", c.ID[:12], err)
				}
			}
		}
	}
	if ns, err := e.Cli.NetworkList(ctx, client.NetworkListOptions{}); err == nil {
		for _, n := range ns.Items {
			if e.mine(n.Labels) {
				if _, err := e.Cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil {
					e.t.Logf("cleanup: network %s: %v", n.Name, err)
				}
			}
		}
	}
	if vs, err := e.Cli.VolumeList(ctx, client.VolumeListOptions{}); err == nil {
		for _, v := range vs.Items {
			if e.mine(v.Labels) {
				if _, err := e.Cli.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{Force: true}); err != nil {
					e.t.Logf("cleanup: volume %s: %v", v.Name, err)
				}
			}
		}
	}
	if is, err := e.Cli.ImageList(ctx, client.ImageListOptions{All: true}); err == nil {
		for _, i := range is.Items {
			if i.Labels[Label] == e.Run {
				if _, err := e.Cli.ImageRemove(ctx, i.ID, client.ImageRemoveOptions{Force: true, PruneChildren: true}); err != nil {
					e.t.Logf("cleanup: image %s: %v", i.ID, err)
				}
			}
		}
	}
}

// Leaks lists what any run left behind: containers, networks, volumes and
// images labelled chadash.test, and compose projects named cte-…. CI runs
// it after the suite (cmd/testengine-leaks).
func Leaks(ctx context.Context, cli *client.Client) ([]string, error) {
	ours := func(labels map[string]string) bool {
		_, tagged := labels[Label]
		return tagged || strings.HasPrefix(labels["com.docker.compose.project"], ProjectPrefix)
	}
	var out []string
	cs, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	for _, c := range cs.Items {
		if ours(c.Labels) {
			out = append(out, fmt.Sprintf("container %s %v", c.ID[:12], c.Names))
		}
	}
	ns, err := cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	for _, n := range ns.Items {
		if ours(n.Labels) {
			out = append(out, "network "+n.Name)
		}
	}
	vs, err := cli.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, err
	}
	for _, v := range vs.Items {
		if ours(v.Labels) {
			out = append(out, "volume "+v.Name)
		}
	}
	is, err := cli.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		return nil, err
	}
	for _, i := range is.Items {
		if _, ok := i.Labels[Label]; ok {
			out = append(out, fmt.Sprintf("image %s %v", i.ID, i.RepoTags))
		}
	}
	return out, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// tarOf is a tar stream of one file; dirTar puts a tar's files in a directory.
func tarOf(name string, content []byte) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	_ = w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))})
	_, _ = w.Write(content)
	_ = w.Close()
	return buf.Bytes()
}

func dirTar(dir string, inner []byte) *bytes.Reader {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	_ = w.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	r := tar.NewReader(bytes.NewReader(inner))
	for {
		h, err := r.Next()
		if err != nil {
			break
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		h.Name = dir + "/" + h.Name
		_ = w.WriteHeader(h)
		_, _ = w.Write(b.Bytes())
	}
	_ = w.Close()
	return bytes.NewReader(buf.Bytes())
}
