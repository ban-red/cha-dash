package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"dash.cha.sh/internal/integration"
)

// CodeCapability is the repair code for an endpoint the proxy refuses but
// the integration can't do without (listing containers).
const CodeCapability = "capability_missing"

// versionInfo is GET /version.
type versionInfo struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	Os            string `json:"Os"`
	Arch          string `json:"Arch"`
	KernelVersion string `json:"KernelVersion"`
}

// engineInfo is the part of GET /info the engine resource shows.
type engineInfo struct {
	Name            string `json:"Name"`
	OperatingSystem string `json:"OperatingSystem"`
	NCPU            int    `json:"NCPU"`
	MemTotal        int64  `json:"MemTotal"`
}

// summary is one entry of GET /containers/json?all=1.
type summary struct {
	ID         string            `json:"Id"`
	Names      []string          `json:"Names"`
	Image      string            `json:"Image"`
	Labels     map[string]string `json:"Labels"`
	State      string            `json:"State"`
	Status     string            `json:"Status"`
	Health     *healthSummary    `json:"Health"` // API 1.52+; older engines need the inspect
	Ports      []portSummary     `json:"Ports"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type healthSummary struct {
	Status        string `json:"Status"`
	FailingStreak int    `json:"FailingStreak"`
}

type portSummary struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// name is the container's own name: Names holds it with a leading slash,
// plus legacy link aliases ("/web/db") that have more.
func (s summary) name() string {
	for _, n := range s.Names {
		if n = strings.TrimPrefix(n, "/"); n != "" && !strings.Contains(n, "/") {
			return n
		}
	}
	if len(s.ID) >= 12 {
		return s.ID[:12]
	}
	return s.ID
}

// key changes whenever the summary says something an inspect would show
// differently. Status ("Up 3 minutes") moves on a restart, so restarts
// between polls are caught without events.
func (s summary) key() string {
	h := ""
	if s.Health != nil {
		h = s.Health.Status
	}
	return s.State + "|" + s.Status + "|" + h
}

// inspect is the part of GET /containers/{id}/json the facts use. The
// environment and command line are not kept: flagEnv and flagArgs drop
// everything but a socket proxy's own switches while decoding.
type inspect struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
		Status     string         `json:"Status"`
		OOMKilled  bool           `json:"OOMKilled"`
		ExitCode   int            `json:"ExitCode"`
		StartedAt  string         `json:"StartedAt"`
		FinishedAt string         `json:"FinishedAt"`
		Health     *healthSummary `json:"Health"` // its Log (check output) isn't decoded
	} `json:"State"`
	Config struct {
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		Env          flagEnv             `json:"Env"`
		Cmd          flagArgs            `json:"Cmd"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
		Memory int64 `json:"Memory"`
	} `json:"HostConfig"`
}

// inspected is a cached inspect and the summary key it was taken at.
type inspected struct {
	key string
	ins inspect
}

// deniedError is a request the proxy refused: a capability not granted,
// not a failure of the source.
type deniedError struct {
	path   string
	status int
}

func (e *deniedError) Error() string {
	return fmt.Sprintf("the endpoint refused GET %s (%d)", e.path, e.status)
}

func isDenied(err error) bool {
	var de *deniedError
	return errors.As(err, &de)
}

// denied reports whether a status is a proxy's refusal. Docker itself
// answers GETs with 404 for anything unknown; 401, 403 and 405 come from a
// proxy (or an authorization plugin) in front of it.
func denied(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden ||
		status == http.StatusMethodNotAllowed || status == http.StatusProxyAuthRequired
}

// get sends a GET to the endpoint. The caller closes the body.
func (in *instance) get(ctx context.Context, c *http.Client, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.ep.base+path, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// getJSON GETs path and decodes a 2xx answer into dst. A refusal is a
// *deniedError, never CheckResponse's auth error: Docker has no credential
// to reject. Other statuses are CheckResponse's errors.
func (in *instance) getJSON(ctx context.Context, path string, dst any) error {
	resp, err := in.get(ctx, in.http, path)
	if err != nil {
		return err
	}
	defer drain(resp)
	if denied(resp.StatusCode) {
		return &deniedError{path: stripQuery(path), status: resp.StatusCode}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("GET %s: %w", stripQuery(path), err)
	}
	return nil
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

func stripQuery(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return p[:i]
	}
	return p
}

// negotiate pings the endpoint and picks the API version to speak.
func (in *instance) negotiate(ctx context.Context) error {
	resp, err := in.get(ctx, in.http, "/_ping")
	if err != nil {
		return err
	}
	pingAPI := resp.Header.Get("Api-Version")
	status := resp.StatusCode
	drain(resp)
	if status != http.StatusOK && !denied(status) {
		return &integration.ConfigError{Field: "endpoint",
			Message: fmt.Sprintf("%s doesn't look like a Docker API: GET /_ping answered %d", in.ep.raw, status)}
	}
	var v versionInfo
	if err := in.getJSON(ctx, "/version", &v); err != nil {
		if !isDenied(err) || pingAPI == "" {
			if isDenied(err) {
				return &integration.RepairError{Code: CodeCapability, Field: "endpoint",
					Message: "the socket proxy refuses both /_ping and /version; allow them (PING=1 and VERSION=1 on Tecnativa's proxy)"}
			}
			return err
		}
		v.APIVersion = pingAPI
	}
	api, err := pickAPI(v.APIVersion, v.MinAPIVersion)
	if err != nil {
		return err
	}
	in.api, in.version = "/v"+api, v
	if v.Version != "" {
		in.d.Emit.SetVersion(v.Version)
	}
	return nil
}

// pickAPI speaks the engine's API version, capped at maxAPI so response
// shapes stay the ones this integration knows.
func pickAPI(server, serverMin string) (string, error) {
	if server == "" {
		return "", &integration.RepairError{Code: integration.CodeAPIChanged, Message: "the engine didn't say which API version it speaks"}
	}
	if cmpVersion(server, minAPI) < 0 {
		return "", &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("Docker API %s is too old; Cha Dash needs %s or later (Docker 19.03+)", server, minAPI)}
	}
	api := server
	if cmpVersion(api, maxAPI) > 0 {
		api = maxAPI
	}
	if serverMin != "" && cmpVersion(api, serverMin) < 0 {
		api = serverMin
	}
	return api, nil
}

// cmpVersion compares dotted API versions ("1.54" > "1.9").
func cmpVersion(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// list reads every container. The integration can't work without it, so a
// refusal is a repair.
func (in *instance) list(ctx context.Context) ([]summary, error) {
	var out []summary
	err := in.getJSON(ctx, in.api+"/containers/json?all=1", &out)
	if isDenied(err) {
		return nil, &integration.RepairError{Code: CodeCapability, Field: "endpoint",
			Message: "the socket proxy refuses to list containers; allow GET /containers/json (CONTAINERS=1 on Tecnativa's proxy)"}
	}
	return out, err
}

// inspectAll inspects, a few at a time, every listed container whose
// summary changed since its cached inspect, plus those in force, and drops
// the cache of containers that are gone. A container that vanished between
// the list and its inspect is skipped; a refusal turns inspects off.
func (in *instance) inspectAll(ctx context.Context, list []summary, force map[string]bool) error {
	listed := map[string]bool{}
	var due []summary
	for _, s := range list {
		listed[s.ID] = true
		if c := in.cache[s.ID]; c == nil || c.key != s.key() || force[s.ID] {
			due = append(due, s)
		}
	}
	for id := range in.cache {
		if !listed[id] {
			delete(in.cache, id)
		}
	}
	if !in.acc.inspect || len(due) == 0 {
		return nil
	}
	errs := make([]error, len(due))
	sem := make(chan struct{}, inspectParallel)
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for i, s := range due {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			c, err := in.inspect(ctx, s)
			if c != nil {
				mu.Lock()
				in.cache[s.ID] = c
				mu.Unlock()
			}
			errs[i] = err
		})
	}
	wg.Wait()
	for i, s := range due {
		var se *integration.StatusError
		switch err := errs[i]; {
		case err == nil:
		case isDenied(err):
			in.acc.inspect = false
			return nil
		case errors.As(err, &se) && se.Code == http.StatusNotFound:
			delete(in.cache, s.ID) // removed since the list
		default:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			in.d.Log.Debug("inspect failed", "container", s.name(), "err", err)
		}
	}
	return nil
}

// inspect reads one container.
func (in *instance) inspect(ctx context.Context, s summary) (*inspected, error) {
	var ins inspect
	if err := in.getJSON(ctx, in.api+"/containers/"+s.ID+"/json", &ins); err != nil {
		return nil, err
	}
	return &inspected{key: s.key(), ins: ins}, nil
}
