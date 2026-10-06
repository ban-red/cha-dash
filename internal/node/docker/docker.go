// Package docker is chadash-node's Docker module: capability verbs over the
// Docker Engine API, each gated by the local policy (internal/node). It is
// not a pass-through: the hub can ask for exactly these verbs and nothing
// else, and every write is checked against docker.protect.
//
//	verb                 permission         request                         reply
//	engine.version       docker.read        {}                              Docker's /version: Version, ApiVersion, MinAPIVersion, Os, Arch
//	engine.info          docker.read        {}                              part of /info: Name, OperatingSystem, NCPU, MemTotal, …
//	engine.events        docker.read        {filters?, since?, until?}      a stream of events (no actor attributes but name and image); ends at until
//	container.list       docker.read        {all?}                          Docker's /containers/json
//	container.inspect    docker.read        {id}                            Docker's /containers/{id}/json; env values "[redacted]" under redact_env
//	container.stats      docker.read        {id}                            one stats sample
//	container.can        docker.read        {id, permission}                {allowed, reason}: would a write be allowed (policy, protect)?
//	container.logs       docker.logs        {id, tail?, since?, follow?, timestamps?}   items {stream, line}; reply {lines}
//	container.start      docker.lifecycle   {id}                            {id, name, state}
//	container.stop       docker.lifecycle   {id, timeout?}                  {id, name, state}
//	container.restart    docker.lifecycle   {id, timeout?}                  {id, name, state}
//	container.pull       docker.update      {id}                            items {status, id?, progress?}; reply {image, before, after, changed}
//	container.recreate   docker.update      {id, timeout?}                  items {step}; reply {id, name, state, old_id}
//	container.remove     docker.remove      {id, volumes?, force?}          {id, name, kept, removed}
//
// IDs may be a container ID or name. Errors are the node's codes:
// not_found (no such container), conflict (already started, say),
// forbidden (protected), unavailable (Docker didn't answer).
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"dash.cha.sh/internal/node"
	"dash.cha.sh/internal/nodeproto"
)

// DefaultHost is the Docker socket the node mounts.
const DefaultHost = "unix:///var/run/docker.sock"

// Options configure the module.
type Options struct {
	// Host is the Docker endpoint (DefaultHost when empty).
	Host string
	// HTTPClient overrides the client's transport (tests).
	HTTPClient *http.Client
	// Policy is the node's policy: protect and redact_env are read here;
	// the dispatcher checks the permissions.
	Policy node.Policy
	// SelfID is the node's own container ID or its prefix (the container's
	// default hostname), always protected. Empty: the hostname is used when
	// it looks like a container ID.
	SelfID string
	Log    *slog.Logger
}

// Module is the docker module.
type Module struct {
	cli    *client.Client
	policy node.Policy
	self   string
	log    *slog.Logger
}

// shortID is what a container's default hostname looks like.
var shortID = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// New builds the module. It doesn't contact Docker: the first verb does,
// negotiating the API version then.
func New(o Options) (*Module, error) {
	if o.Host == "" {
		o.Host = DefaultHost
	}
	opts := []client.Opt{client.WithHost(o.Host), client.WithUserAgent("chadash-node")}
	if o.HTTPClient != nil {
		opts = append(opts, client.WithHTTPClient(o.HTTPClient))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	self := o.SelfID
	if self == "" {
		if h, err := os.Hostname(); err == nil && shortID.MatchString(h) {
			self = h
		}
	}
	return &Module{cli: cli, policy: o.Policy, self: self, log: o.Log}, nil
}

// SelfProject is the compose project the node's own container belongs to
// (its com.docker.compose.project label), for modules that must never
// touch the stack running Cha Dash (the stacks module's Protected list).
// It's "" when the node can't tell: not in a container, a hostname that
// isn't the container ID, or no compose label.
func (m *Module) SelfProject(ctx context.Context) (string, error) {
	if m.self == "" {
		return "", nil
	}
	res, err := m.cli.ContainerInspect(ctx, m.self, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", mapErr(err)
	}
	if res.Container.Config == nil {
		return "", nil
	}
	return res.Container.Config.Labels["com.docker.compose.project"], nil
}

// Close releases the client's connections.
func (m *Module) Close() error { return m.cli.Close() }

// Name implements node.Module.
func (m *Module) Name() string { return "docker" }

// Verbs implements node.Module.
func (m *Module) Verbs() []node.Verb {
	return []node.Verb{
		{Name: "engine.version", Policy: "docker.read", Handle: m.version},
		{Name: "engine.info", Policy: "docker.read", Handle: m.info},
		{Name: "engine.events", Policy: "docker.read", Handle: m.events},
		{Name: "container.list", Policy: "docker.read", Handle: m.list},
		{Name: "container.inspect", Policy: "docker.read", Handle: m.inspect},
		{Name: "container.stats", Policy: "docker.read", Handle: m.stats},
		{Name: "container.can", Policy: "docker.read", Handle: m.can},
		{Name: "container.logs", Policy: "docker.logs", Handle: m.logs},
		{Name: "container.start", Policy: "docker.lifecycle", Handle: m.start},
		{Name: "container.stop", Policy: "docker.lifecycle", Handle: m.stop},
		{Name: "container.restart", Policy: "docker.lifecycle", Handle: m.restart},
		{Name: "container.pull", Policy: "docker.update", Handle: m.pull},
		{Name: "container.recreate", Policy: "docker.update", Handle: m.recreate},
		{Name: "container.remove", Policy: "docker.remove", Handle: m.remove},
	}
}

// target is a request naming one container.
type target struct {
	ID string `json:"id"`
	// Timeout is seconds to wait before killing, for stop, restart and
	// recreate (Docker's default when 0).
	Timeout int `json:"timeout,omitempty"`
}

// idRE is what a container reference may look like: an ID, or a name.
var idRE = regexp.MustCompile(`^/?[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)

func decode(req json.RawMessage, v any) error {
	if err := json.Unmarshal(req, v); err != nil {
		return node.Invalid("the request doesn't parse: %v", err)
	}
	return nil
}

func decodeTarget(req json.RawMessage) (target, error) {
	var t target
	if err := decode(req, &t); err != nil {
		return t, err
	}
	if !idRE.MatchString(t.ID) {
		return t, node.Invalid("id must be a container ID or name")
	}
	if t.Timeout < 0 || t.Timeout > 600 {
		return t, node.Invalid("timeout must be 0–600 seconds")
	}
	t.ID = strings.TrimPrefix(t.ID, "/")
	return t, nil
}

// mapErr turns a Docker client error into the node's codes. Docker's own
// messages name the container and say what's wrong; they carry no secrets.
func mapErr(err error) error {
	var pe *nodeproto.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pe):
		return err
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case cerrdefs.IsNotFound(err):
		return nodeproto.Errorf(nodeproto.CodeNotFound, "%s", dockerMessage(err))
	case cerrdefs.IsConflict(err), cerrdefs.IsNotModified(err):
		return nodeproto.Errorf(nodeproto.CodeConflict, "%s", dockerMessage(err))
	case cerrdefs.IsInvalidArgument(err):
		return nodeproto.Errorf(nodeproto.CodeInvalid, "%s", dockerMessage(err))
	case client.IsErrConnectionFailed(err):
		return nodeproto.Errorf(nodeproto.CodeUnavailable, "the node can't reach Docker: is the socket mounted?")
	}
	return nodeproto.Errorf(nodeproto.CodeFailed, "Docker: %s", dockerMessage(err))
}

func dockerMessage(err error) string {
	return strings.TrimPrefix(err.Error(), "Error response from daemon: ")
}

func reply(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	return b, err
}

func (m *Module) version(ctx context.Context, _ json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	v, err := m.cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	return reply(map[string]string{
		"Version": v.Version, "ApiVersion": v.APIVersion, "MinAPIVersion": v.MinAPIVersion,
		"Os": v.Os, "Arch": v.Arch, "Platform": v.Platform.Name,
	})
}

func (m *Module) info(ctx context.Context, _ json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	r, err := m.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	i := r.Info
	// A subset: /info also carries registry mirrors, proxy settings and
	// labels the hub has no use for.
	return reply(map[string]any{
		"ID": i.ID, "Name": i.Name, "OperatingSystem": i.OperatingSystem, "OSType": i.OSType,
		"Architecture": i.Architecture, "KernelVersion": i.KernelVersion, "ServerVersion": i.ServerVersion,
		"NCPU": i.NCPU, "MemTotal": i.MemTotal, "Containers": i.Containers,
		"ContainersRunning": i.ContainersRunning, "ContainersStopped": i.ContainersStopped,
		"Images": i.Images, "Driver": i.Driver,
	})
}

func (m *Module) list(ctx context.Context, req json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	var r struct {
		All bool `json:"all"`
	}
	if err := decode(req, &r); err != nil {
		return nil, err
	}
	res, err := m.cli.ContainerList(ctx, client.ContainerListOptions{All: r.All})
	if err != nil {
		return nil, mapErr(err)
	}
	return reply(res.Items)
}

// inspectRaw returns a container's inspect as Docker sent it, with the
// environment's values redacted when the policy says so.
func (m *Module) inspectRaw(ctx context.Context, id string) (client.ContainerInspectResult, json.RawMessage, error) {
	res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return res, nil, mapErr(err)
	}
	if !m.policy.RedactEnv() {
		return res, res.Raw, nil
	}
	out, err := redactEnv(res.Raw)
	return res, out, err
}

func (m *Module) inspect(ctx context.Context, req json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	t, err := decodeTarget(req)
	if err != nil {
		return nil, err
	}
	_, raw, err := m.inspectRaw(ctx, t.ID)
	return raw, err
}

// Redacted replaces an environment value that never leaves the node.
const Redacted = "[redacted]"

// redactEnv keeps each Config.Env entry's name and drops its value.
func redactEnv(raw json.RawMessage) (json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "Docker's inspect didn't parse")
	}
	var cfg map[string]json.RawMessage
	if c, ok := doc["Config"]; ok && json.Unmarshal(c, &cfg) == nil && cfg != nil {
		var env []string
		if e, ok := cfg["Env"]; ok && json.Unmarshal(e, &env) == nil {
			for i, kv := range env {
				k, _, _ := strings.Cut(kv, "=")
				env[i] = k + "=" + Redacted
			}
			cfg["Env"], _ = json.Marshal(env)
			doc["Config"], _ = json.Marshal(cfg)
		}
	}
	return json.Marshal(doc)
}

func (m *Module) stats(ctx context.Context, req json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	t, err := decodeTarget(req)
	if err != nil {
		return nil, err
	}
	res, err := m.cli.ContainerStats(ctx, t.ID, client.ContainerStatsOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	defer res.Body.Close()
	var v json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "reading stats: %v", err)
	}
	return v, nil
}

// writable inspects a container a write verb is about to touch and refuses
// protected ones: the node itself, the hub, and docker.protect.
func (m *Module) writable(ctx context.Context, id string) (client.ContainerInspectResult, error) {
	res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return res, mapErr(err)
	}
	c := res.Container
	name := strings.TrimPrefix(c.Name, "/")
	service := ""
	if c.Config != nil {
		service = c.Config.Labels["com.docker.compose.service"]
	}
	if m.self != "" && strings.HasPrefix(c.ID, m.self) {
		return res, node.Forbidden("%s is this node's own container; the node never acts on itself", name)
	}
	if m.policy.Protected(name, service) {
		return res, node.Forbidden("%s is protected by this node's policy", name)
	}
	return res, nil
}

// can answers whether a write would be allowed on a container, before the
// hub plans it: {id, permission} → {allowed, reason}. The hub shows the
// reason as the action's "unavailable" sentence instead of failing later.
func (m *Module) can(ctx context.Context, req json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	var r struct {
		ID         string `json:"id"`
		Permission string `json:"permission"`
	}
	if err := decode(req, &r); err != nil {
		return nil, err
	}
	if !idRE.MatchString(r.ID) || !node.ValidPermission(r.Permission) {
		return nil, node.Invalid("can needs a container id and a permission such as docker.lifecycle")
	}
	answer := func(ok bool, reason string) (json.RawMessage, error) {
		return reply(map[string]any{"allowed": ok, "reason": reason})
	}
	if !m.policy.Allows(r.Permission) {
		return answer(false, fmt.Sprintf("this node's policy doesn't allow %s", r.Permission))
	}
	if !node.IsWrite(r.Permission) {
		if _, err := m.cli.ContainerInspect(ctx, r.ID, client.ContainerInspectOptions{}); err != nil {
			return nil, mapErr(err)
		}
		return answer(true, "")
	}
	if _, err := m.writable(ctx, r.ID); err != nil {
		if nodeproto.CodeOf(err) == nodeproto.CodeForbidden {
			return answer(false, err.Error())
		}
		return nil, err
	}
	return answer(true, "")
}

// stateOf reads a container's state after a change.
func (m *Module) stateOf(ctx context.Context, id string) (map[string]string, error) {
	res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	c := res.Container
	state := ""
	if c.State != nil {
		state = string(c.State.Status)
	}
	return map[string]string{"id": c.ID, "name": strings.TrimPrefix(c.Name, "/"), "state": state}, nil
}

func timeoutPtr(s int) *int {
	if s <= 0 {
		return nil
	}
	return &s
}

func (m *Module) start(ctx context.Context, req json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	t, err := decodeTarget(req)
	if err != nil {
		return nil, err
	}
	res, err := m.writable(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if res.Container.State != nil && res.Container.State.Running {
		return nil, nodeproto.Errorf(nodeproto.CodeConflict, "%s is already running", strings.TrimPrefix(res.Container.Name, "/"))
	}
	if _, err := m.cli.ContainerStart(ctx, res.Container.ID, client.ContainerStartOptions{}); err != nil {
		return nil, mapErr(err)
	}
	st, err := m.stateOf(ctx, res.Container.ID)
	if err != nil {
		return nil, err
	}
	return reply(st)
}

func (m *Module) stop(ctx context.Context, req json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	t, err := decodeTarget(req)
	if err != nil {
		return nil, err
	}
	res, err := m.writable(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if res.Container.State == nil || !res.Container.State.Running {
		return nil, nodeproto.Errorf(nodeproto.CodeConflict, "%s isn't running", strings.TrimPrefix(res.Container.Name, "/"))
	}
	if _, err := m.cli.ContainerStop(ctx, res.Container.ID, client.ContainerStopOptions{Timeout: timeoutPtr(t.Timeout)}); err != nil {
		return nil, mapErr(err)
	}
	st, err := m.stateOf(ctx, res.Container.ID)
	if err != nil {
		return nil, err
	}
	return reply(st)
}

func (m *Module) restart(ctx context.Context, req json.RawMessage, _ func(json.RawMessage)) (json.RawMessage, error) {
	t, err := decodeTarget(req)
	if err != nil {
		return nil, err
	}
	res, err := m.writable(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if _, err := m.cli.ContainerRestart(ctx, res.Container.ID, client.ContainerRestartOptions{Timeout: timeoutPtr(t.Timeout)}); err != nil {
		return nil, mapErr(err)
	}
	st, err := m.stateOf(ctx, res.Container.ID)
	if err != nil {
		return nil, err
	}
	return reply(st)
}

// waitCtx gives a write a grace period after the caller's context ends, so
// a stop half-way through a recreate is followed by the rollback.
func waitCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
}
