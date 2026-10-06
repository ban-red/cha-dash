// Package docker reads one Docker engine, through a socket proxy, the
// socket itself, or chadash-node (node://<name>, v0.2): the engine, its containers and their compose stacks, with
// live updates from the event stream while someone watches. Containers keep
// an allowlist of their labels, so homepage.* labels arrive ready for label
// discovery. A read-only probe classifies the endpoint (raw API, broad proxy,
// filtered proxy) and reports what it grants; a 403 from a proxy turns a
// feature off instead of failing. See README.md.
package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "docker"

// Polling and API limits.
const (
	// watchedInterval is the sync interval while someone watches and the
	// event stream isn't connected; liveInterval is the reconcile interval
	// while it is (events catch the changes, the re-list catches what they
	// miss, such as a health check's failing streak).
	watchedInterval = 30 * time.Second
	liveInterval    = 60 * time.Second
	// probeEvery re-runs the endpoint probe, so a proxy reconfigured in
	// place is noticed without restarting the instance.
	probeEvery = time.Hour
	// inspectParallel bounds concurrent inspects (the spike: 2–4).
	inspectParallel = 4
	// API versions: the newest one this integration was written against,
	// and the oldest it accepts (Docker 19.03).
	maxAPI = "1.54"
	minAPI = "1.40"
)

func init() { integration.Register(Integration{}) }

// Integration is the Docker integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	// No default mode: tls is set only for an endpoint that speaks TLS, and
	// its presence is what switches the client to https.
	tls := integration.TLSSchema()
	tls.Property("mode").Default = nil
	tls.Describe("Only for an endpoint that speaks TLS (usually :2376). Leave it out for a socket proxy on plain TCP.")
	return integration.Manifest{
		ID: ID, Name: "Docker", Icon: "docker", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"containers"},
		ConfigSchema: integration.Object(
			integration.Prop("endpoint", integration.URL("Endpoint").Describe(
				"tcp://host:port of a read-only socket proxy, unix:///var/run/docker.sock, or node://local for the hub's own host through chadash-node.")),
			integration.Prop("tls", tls),
		).Require("endpoint"),
		Poll: integration.Poll{Interval: watchedInterval, Idle: 5 * time.Minute, Timeout: 45 * time.Second},
	}
}

// nodeRE is a node's name or ID in a node:// endpoint.
var nodeRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// endpoint is a parsed endpoint config value.
type endpoint struct {
	raw    string
	unix   bool
	node   string // node: the chadash-node's name or ID
	socket string // unix: the socket path
	host   string // tcp: host and port
	port   string
	base   string // the HTTP base URL requests go to
}

// parseEndpoint reads tcp://host[:port] or unix:///path. withTLS (the
// config has a tls key) makes a TCP endpoint https.
func parseEndpoint(s string, withTLS bool) (endpoint, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return endpoint{}, errors.New("must be tcp://host:port, unix:///path/to/docker.sock or node://<node name>")
	}
	ep := endpoint{raw: s}
	switch u.Scheme {
	case "node":
		name, _ := nodeName(s)
		if !nodeRE.MatchString(name) {
			return endpoint{}, errors.New("a node endpoint is node://<node name>, such as node://local")
		}
		if withTLS {
			return endpoint{}, errors.New("the node channel has its own encryption; leave tls out")
		}
		ep.node, ep.base = name, "http://node"
	case "unix":
		if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
			return endpoint{}, errors.New("a socket needs an absolute path: unix:///var/run/docker.sock (three slashes)")
		}
		if withTLS {
			return endpoint{}, errors.New("a Unix socket has no TLS; leave tls out")
		}
		ep.unix, ep.socket, ep.base = true, u.Path, "http://docker"
	case "tcp":
		if u.Hostname() == "" {
			return endpoint{}, errors.New("needs a host: tcp://host:port")
		}
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
			return endpoint{}, errors.New("is a host and port only: tcp://host:port")
		}
		ep.host, ep.port = u.Hostname(), u.Port()
		scheme := "http"
		if withTLS {
			scheme = "https"
		}
		if ep.port == "" {
			ep.port = "2375"
			if withTLS {
				ep.port = "2376"
			}
		}
		ep.base = scheme + "://" + net.JoinHostPort(ep.host, ep.port)
	default:
		return endpoint{}, fmt.Errorf("must start with tcp://, unix:// or node://, not %s://", u.Scheme)
	}
	return ep, nil
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if _, err := parseEndpoint(cfg.String("endpoint"), cfg.Values["tls"] != nil); err != nil {
		path := "/endpoint"
		if strings.Contains(err.Error(), "tls") {
			path = "/tls"
		}
		return []integration.FieldError{{Path: path, Message: err.Error()}}
	}
	return nil
}

// Connect implements integration.Integration. It does no I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	ep, err := parseEndpoint(cfg.String("endpoint"), cfg.Values["tls"] != nil)
	if err != nil {
		return nil, &integration.ConfigError{Field: "endpoint", Message: "endpoint " + err.Error()}
	}
	in := &instance{d: d, ep: ep, key: cfg.InstanceID, name: cfg.Name, http: d.HTTP, cache: map[string]*inspected{}, raised: map[string]bool{}}
	if in.key == "" {
		in.key = "test"
	}
	if in.name == "" {
		in.name = cmpStr(ep.host, "docker")
	}
	switch {
	case ep.unix:
		c, err := d.NewHTTPClient(integration.HTTPOptions{UnixSocket: ep.socket})
		if err != nil {
			return nil, err
		}
		in.http = c
	case ep.node != "":
		if d.Nodes == nil {
			return nil, &integration.ConfigError{Field: "endpoint",
				Message: "this hub has no chadash-node to read through; add the node service to the compose file (deploy/README.md), or use a socket proxy"}
		}
		in.http = &http.Client{Transport: &nodeTransport{nodes: d.Nodes, node: ep.node}, Timeout: 30 * time.Second}
		if in.name == "" || in.name == "docker" {
			in.name = ep.node
		}
	}
	// The event stream outlives any per-request timeout: the same transport
	// (TLS policy, socket, response cap) without the client's deadline.
	// Cancelling the context ends it.
	in.stream = &http.Client{Transport: in.http.Transport, CheckRedirect: in.http.CheckRedirect}
	return in, nil
}

func cmpStr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

type instance struct {
	d      integration.Deps
	ep     endpoint
	key    string // the instance ID in resource IDs
	name   string
	http   *http.Client
	stream *http.Client
	live   atomic.Bool // the event stream is connected

	// mu serialises refreshes: Sync and Subscribe share everything below.
	mu       sync.Mutex
	api      string // "/v1.54", once negotiated
	version  versionInfo
	acc      access
	probedAt time.Time
	info     *engineInfo           // from /info, when the proxy allows it
	cache    map[string]*inspected // by container ID
	raised   map[string]bool       // open hints, by incident ID
	hubID    string                // node:// only: the hub's container, as the node names it
	selfAt   time.Time             // when the node was last asked
}

// rid is a resource ID of this instance.
func (in *instance) rid(path string) string { return ID + ":" + in.key + "/" + path }

// Interval implements integration.Intervaler: slower while events arrive.
func (in *instance) Interval() time.Duration {
	if in.live.Load() {
		return liveInterval
	}
	return watchedInterval
}

// Close implements integration.Instance.
func (in *instance) Close() error {
	in.http.CloseIdleConnections()
	return nil
}

// Test implements integration.Instance: negotiate, probe, list.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	start := time.Now()
	in.mu.Lock()
	defer in.mu.Unlock()
	if err := in.negotiate(ctx); err != nil {
		return integration.Failed(err)
	}
	if err := in.probe(ctx); err != nil {
		return integration.Failed(err)
	}
	list, err := in.list(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	// The proxy's own flags say whether it forwards writes.
	if p := in.proxyContainer(list); p != nil && in.acc.inspect {
		switch c, err := in.inspect(ctx, *p); {
		case err == nil:
			in.cache[p.ID] = c
		case isDenied(err):
			in.acc.inspect = false
		}
	}
	w := in.writes(list)
	return integration.TestResult{
		OK: true, Version: in.version.Version, Latency: time.Since(start),
		Grants: in.acc.grants(in.ep, w), Warnings: in.acc.warnings(in.ep),
	}
}

// Sync implements integration.Instance: list (and inspect what changed),
// then publish everything.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.refresh(ctx, nil)
}

// refresh reads the engine and publishes it. force lists container IDs to
// inspect again whatever their summary says (an event named them).
func (in *instance) refresh(ctx context.Context, force map[string]bool) error {
	if in.api == "" {
		if err := in.negotiate(ctx); err != nil {
			return err
		}
	}
	if in.probedAt.IsZero() || in.d.Now().Sub(in.probedAt) >= probeEvery {
		if err := in.probe(ctx); err != nil {
			return err
		}
	}
	list, err := in.list(ctx)
	if err != nil {
		return err
	}
	if err := in.inspectAll(ctx, list, force); err != nil {
		return err
	}
	// Through chadash-node, ask which container is the hub: hourly, or at
	// once when the one it named is gone (recreated by an update).
	if in.ep.node != "" && (in.selfAt.IsZero() || in.d.Now().Sub(in.selfAt) >= probeEvery ||
		in.hubID != "" && !slices.ContainsFunc(list, func(s summary) bool { return s.ID == in.hubID })) {
		in.hubID, in.selfAt = in.nodeSelf(ctx), in.d.Now()
	}
	return in.publish(list)
}
