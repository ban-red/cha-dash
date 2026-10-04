package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/spikes/traefik/labkit"
)

// Meta is what capture learned about one endpoint, without identifying values.
type Meta struct {
	Name         string            `json:"name"`
	CapturedAt   time.Time         `json:"capturedAt"`
	Reachable    bool              `json:"reachable"`
	Error        string            `json:"error,omitempty"` // error kind only
	Scheme       string            `json:"scheme"`          // tcp:// → plain http
	APIVersion   string            `json:"apiVersion,omitempty"`
	PingHeaders  map[string]string `json:"pingHeaders,omitempty"`
	Statuses     map[string]int    `json:"statuses"` // path template → status
	Containers   int               `json:"containers"`
	Inspected    int               `json:"inspected"`
	EventsWindow string            `json:"eventsWindow,omitempty"`
	LiveEvents   int               `json:"liveEvents"`
	PastEvents   int               `json:"pastEvents"`
	Durations    map[string]string `json:"durations"` // path template → latency
}

// listEntry is the part of /containers/json the capture needs.
type listEntry struct {
	ID      string `json:"Id"`
	State   string
	Image   string
	ImageID string
}

type endpoint struct {
	name   string
	base   *url.URL
	client *http.Client // short requests
	stream *http.Client // events (no client-wide timeout; per-request context)
	meta   *Meta
	prefix string // /v1.xx
}

func capture(envPath, rawDir string, window time.Duration, only string) error {
	if window > 60*time.Second {
		return errors.New("events window is capped at 60s")
	}
	env, err := labkit.LoadEnv(envPath)
	if err != nil {
		return err
	}
	if err := env.Require("DOCKER_ENDPOINTS"); err != nil {
		return err
	}
	for _, name := range strings.Split(env["DOCKER_ENDPOINTS"], ",") {
		name = strings.TrimSpace(name)
		if name == "" || (only != "" && name != only) {
			continue
		}
		key := "DOCKER_" + strings.ToUpper(name) + "_URL"
		if err := env.Require(key); err != nil {
			return err
		}
		dir := filepath.Join(rawDir, name)
		if err := os.MkdirAll(filepath.Join(dir, "inspect"), 0o700); err != nil {
			return err
		}
		ep, err := newEndpoint(name, env[key])
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		logf("== %s", name)
		if err := ep.run(dir, window); err != nil {
			logf("%s: %v", name, err)
		}
		if err := writeJSON(filepath.Join(dir, "meta.json"), ep.meta); err != nil {
			return err
		}
	}
	return nil
}

func newEndpoint(name, raw string) (*endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("does not parse as tcp://host:port")
	}
	scheme := u.Scheme
	switch u.Scheme {
	case "tcp", "http":
		u.Scheme = "http"
	case "https":
	default:
		return nil, errors.New("only tcp://, http:// and https:// are supported (no unix sockets on this Mac)")
	}
	u.Path, u.RawQuery = "", ""
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 20 * time.Second
	return &endpoint{
		name:   name,
		base:   u,
		client: labkit.NewReadOnlyClient(tr, dockerGuard, 250*time.Millisecond, 25*time.Second),
		stream: labkit.NewReadOnlyClient(tr, dockerGuard, 250*time.Millisecond, 0),
		meta: &Meta{Name: name, CapturedAt: time.Now().UTC().Truncate(time.Second), Scheme: scheme,
			Statuses: map[string]int{}, Durations: map[string]string{}},
	}, nil
}

// get sends one GET/HEAD; tmpl is the path with ids replaced by {id} etc., the
// only form that is logged or recorded.
func (e *endpoint) get(method, path, tmpl string, timeout time.Duration) (*http.Response, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ref, err := url.Parse(path)
	if err != nil {
		return nil, nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, method, e.base.ResolveReference(ref).String(), nil)
	start := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, nil, errors.New(labkit.SafeErr(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	e.meta.Statuses[tmpl] = resp.StatusCode
	e.meta.Durations[tmpl] = time.Since(start).Round(time.Millisecond).String()
	logf("%s %s → %d (%d bytes, %s)", method, tmpl, resp.StatusCode, len(body), time.Since(start).Round(time.Millisecond))
	return resp, body, err
}

func (e *endpoint) getJSON(path, tmpl string, dst string) ([]byte, error) {
	resp, body, err := e.get(http.MethodGet, path, tmpl, 25*time.Second)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: status %d", tmpl, resp.StatusCode)
	}
	if dst != "" {
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fmt.Errorf("%s: not JSON", tmpl)
		}
		if err := writeJSON(dst, v); err != nil {
			return nil, err
		}
	}
	return body, nil
}

func (e *endpoint) run(dir string, window time.Duration) error {
	// Reachability: one short ping.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.base.ResolveReference(&url.URL{Path: "/_ping"}).String(), nil)
	resp, err := e.client.Do(req)
	cancel()
	if err != nil {
		e.meta.Error = labkit.SafeErr(err)
		logf("GET /_ping → unreachable (%s)", e.meta.Error)
		return nil
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	e.meta.Reachable = true
	e.meta.Statuses["GET /_ping"] = resp.StatusCode
	e.meta.PingHeaders = map[string]string{}
	for _, h := range []string{"Api-Version", "Server", "Ostype", "Docker-Experimental", "Builder-Version", "Swarm", "Content-Type"} {
		if v := resp.Header.Get(h); v != "" {
			e.meta.PingHeaders[h] = v
		}
	}
	logf("GET /_ping → %d", resp.StatusCode)

	// Version decides the API prefix.
	body, err := e.getJSON("/version", "GET /version", filepath.Join(dir, "version.json"))
	if err != nil {
		return err
	}
	var ver struct{ ApiVersion string }
	_ = json.Unmarshal(body, &ver)
	if ver.ApiVersion == "" {
		return errors.New("no ApiVersion in /version")
	}
	e.meta.APIVersion = ver.ApiVersion
	e.prefix = "/v" + ver.ApiVersion
	p := e.prefix

	if _, err := e.getJSON(p+"/info", "GET /v{api}/info", filepath.Join(dir, "info.json")); err != nil {
		logf("info: %v", err)
	}
	body, err = e.getJSON(p+"/containers/json?all=1", "GET /v{api}/containers/json?all=1", filepath.Join(dir, "containers.json"))
	if err != nil {
		return err
	}
	var list []listEntry
	_ = json.Unmarshal(body, &list)
	e.meta.Containers = len(list)
	volume := ""
	for _, c := range list {
		b, err := e.getJSON(p+"/containers/"+c.ID+"/json", "GET /v{api}/containers/{id}/json", filepath.Join(dir, "inspect", c.ID+".json"))
		if err != nil {
			logf("inspect: %v", err)
			continue
		}
		e.meta.Inspected++
		var in struct{ Mounts []struct{ Type, Name string } }
		if volume == "" && json.Unmarshal(b, &in) == nil {
			for _, m := range in.Mounts {
				if m.Type == "volume" && m.Name != "" {
					volume = m.Name
					break
				}
			}
		}
	}
	imgBody, _ := e.getJSON(p+"/images/json", "GET /v{api}/images/json", filepath.Join(dir, "images.json"))
	netBody, _ := e.getJSON(p+"/networks", "GET /v{api}/networks", filepath.Join(dir, "networks.json"))
	volBody, _ := e.getJSON(p+"/volumes", "GET /v{api}/volumes", filepath.Join(dir, "volumes.json"))

	// Past events still in the engine's buffer (returns at once: until is now).
	now := time.Now()
	past, err := e.events(now.Add(-24*time.Hour), now, 20*time.Second)
	if err != nil {
		logf("past events: %v", err)
	}
	e.meta.PastEvents = len(past)
	writeJSON(filepath.Join(dir, "events-past.json"), past)

	// The proxy-vs-raw probe (GET/HEAD only, status codes recorded).
	probe := e.probe(list, volume, imgBody, netBody, volBody)
	writeJSON(filepath.Join(dir, "probe.json"), probe)

	// Live events for a bounded window, then close.
	start := time.Now()
	live, err := e.events(start, start.Add(window), window+5*time.Second)
	if err != nil {
		logf("live events: %v", err)
	}
	e.meta.EventsWindow = window.String()
	e.meta.LiveEvents = len(live)
	writeJSON(filepath.Join(dir, "events-live.json"), live)
	logf("events: %d past (24h buffer), %d live in %s", len(past), len(live), window)
	return nil
}

// events reads /events between since and until (both required by the guard)
// and returns the decoded messages. The context deadline closes the stream.
func (e *endpoint) events(since, until time.Time, max time.Duration) ([]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), max)
	defer cancel()
	q := url.Values{"since": {strconv.FormatInt(since.Unix(), 10)}, "until": {strconv.FormatInt(until.Unix(), 10)}}
	u := e.base.ResolveReference(&url.URL{Path: e.prefix + "/events", RawQuery: q.Encode()})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := e.stream.Do(req)
	if err != nil {
		return nil, errors.New(labkit.SafeErr(err))
	}
	defer resp.Body.Close()
	e.meta.Statuses["GET /v{api}/events?since&until"] = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("events: status %d", resp.StatusCode)
	}
	var out []json.RawMessage
	dec := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var m json.RawMessage
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return out, nil
			}
			return out, errors.New(labkit.SafeErr(err))
		}
		out = append(out, m)
	}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
