package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"dash.cha.sh/internal/integration"
)

// access is what the endpoint lets the hub read, from the probe. Every
// probe request is a GET; nothing here ever writes.
type access struct {
	raw     bool // swarm and plugin endpoints answer like Docker: the raw API
	broad   bool // logs pass: a proxy that lets every GET under /containers/ through
	inspect bool
	events  bool
	stats   bool
	info    bool
	images  bool
}

// tier is the endpoint's class for the engine's attrs and the hints (spike
// 4.4's three tiers).
func (a access) tier() string {
	switch {
	case a.raw:
		return "raw"
	case a.broad:
		return "broad-proxy"
	}
	return "filtered-proxy"
}

// probeID names a container no request is meant to find. The probe asks
// about it, so a request a proxy lets through gets Docker's 400 or 404 and
// never a real container's data: logs with stdout=0&stderr=0 are refused
// before Docker even looks the container up.
const probeID = "chadash-probe-0"

// rawPaths are read-only endpoints a filtering proxy has no reason to
// allow. When every one of them answers like Docker, nothing is filtering.
var rawPaths = []string{"/swarm", "/secrets", "/configs", "/nodes", "/plugins"}

// probe classifies the endpoint and finds which optional reads it allows.
func (in *instance) probe(ctx context.Context) error {
	var a access
	ok := func(path string, dst any) (bool, error) { return in.answers(ctx, in.api+path, dst) }
	var info engineInfo
	steps := []struct {
		path string
		dst  any
		set  *bool
	}{
		{"/info", &info, &a.info},
		{"/images/" + probeID + "/json", nil, &a.images},
		{"/containers/" + probeID + "/json", nil, &a.inspect},
		{"/containers/" + probeID + "/stats?stream=false&one-shot=true", nil, &a.stats},
		{"/containers/" + probeID + "/logs?stdout=0&stderr=0&tail=0", nil, &a.broad},
		{"/events?since=1&until=1", nil, &a.events}, // a window in 1970: returns at once
	}
	for _, s := range steps {
		v, err := ok(s.path, s.dst)
		if err != nil {
			return err
		}
		*s.set = v
	}
	a.raw = true
	for _, p := range rawPaths {
		v, err := ok(p, nil)
		if err != nil {
			return err
		}
		a.raw = a.raw && v
	}
	in.acc, in.probedAt, in.info = a, in.d.Now(), nil
	if a.info {
		in.info = &info
	}
	return nil
}

// answers reports whether Docker itself answered a GET: not refused, and
// JSON (Docker's errors are JSON; a proxy's refusals and a web server's
// pages aren't). A 200 is decoded into dst when it's set.
func (in *instance) answers(ctx context.Context, path string, dst any) (bool, error) {
	resp, err := in.get(ctx, in.http, path)
	if err != nil {
		return false, err
	}
	defer drain(resp)
	if denied(resp.StatusCode) || !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return false, nil
	}
	if dst != nil && resp.StatusCode == 200 {
		_ = json.NewDecoder(resp.Body).Decode(dst)
	}
	return true, nil
}

// writes is whether the endpoint forwards requests that change things.
type writes struct {
	known bool
	yes   bool
	why   string
}

// writes reads it without ever sending a write: the raw API always can; a
// socket proxy says so in its own config, when the engine lists it and the
// hub may inspect it.
func (in *instance) writes(list []summary) writes {
	if in.acc.raw {
		return writes{known: true, yes: true, why: "the raw Docker API"}
	}
	p := in.proxyContainer(list)
	if p == nil {
		return writes{}
	}
	c := in.cache[p.ID]
	if c == nil {
		return writes{}
	}
	return proxyWrites(p.Image, c.ins)
}

// proxyContainer finds the socket proxy serving this endpoint among the
// engine's containers: a socket-proxy image named like the endpoint's host
// (a compose service) or publishing its port. Ambiguity finds nothing.
func (in *instance) proxyContainer(list []summary) *summary {
	if in.ep.unix {
		return nil
	}
	var found *summary
	for i := range list {
		s := &list[i]
		if !isSocketProxy(s.Image) {
			continue
		}
		match := s.name() == in.ep.host || s.Labels["com.docker.compose.service"] == in.ep.host
		for _, p := range s.Ports {
			match = match || fmt.Sprint(p.PublicPort) == in.ep.port
		}
		if !match {
			continue
		}
		if found != nil {
			return nil
		}
		found = s
	}
	return found
}

func isSocketProxy(image string) bool {
	return strings.Contains(strings.ToLower(image), "socket-proxy")
}

// proxyWrites reads a proxy's write switches: Tecnativa's (and
// LinuxServer's) 0/1 environment flags, or wollomatic's -allowPOST style
// method allowlists. Values it can't read (a redacted fixture) leave it
// unknown.
func proxyWrites(image string, ins inspect) writes {
	if strings.Contains(strings.ToLower(image), "wollomatic") {
		for _, a := range ins.Config.Cmd {
			if k, v, _ := strings.Cut(strings.TrimLeft(a, "-"), "="); v != "" {
				return writes{known: true, yes: true, why: fmt.Sprintf("the proxy allows %s requests (-%s)", strings.TrimPrefix(k, "allow"), k)}
			}
		}
		for _, k := range wollomaticEnv {
			if v := ins.Config.Env[k]; v != "" {
				return writes{known: true, yes: true, why: fmt.Sprintf("the proxy allows %s requests (%s)", strings.TrimPrefix(k, "SP_ALLOW_"), k)}
			}
		}
		return writes{known: true}
	}
	unknown := false
	for _, k := range tecnativaEnv {
		v, ok := ins.Config.Env[k]
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return writes{known: true, yes: true, why: fmt.Sprintf("the proxy allows writes (%s=%s)", k, v)}
		case "0", "false", "no", "off", "":
		default:
			unknown = true
		}
	}
	return writes{known: !unknown}
}

// The only environment variables and arguments ever kept from an inspect:
// socket proxies' write switches. Everything else is dropped while decoding.
var (
	tecnativaEnv  = []string{"POST", "ALLOW_START", "ALLOW_STOP", "ALLOW_RESTARTS"}
	wollomaticEnv = []string{"SP_ALLOW_POST", "SP_ALLOW_PUT", "SP_ALLOW_PATCH", "SP_ALLOW_DELETE"}
	wollomaticArg = []string{"allowPOST", "allowPUT", "allowPATCH", "allowDELETE"}
)

// flagEnv is Config.Env reduced to the proxy switches above.
type flagEnv map[string]string

func (f *flagEnv) UnmarshalJSON(b []byte) error {
	var all []string
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	m := flagEnv{}
	for _, kv := range all {
		k, v, _ := strings.Cut(kv, "=")
		if slices.Contains(tecnativaEnv, k) || slices.Contains(wollomaticEnv, k) {
			m[k] = v
		}
	}
	*f = m
	return nil
}

// flagArgs is Config.Cmd reduced to wollomatic's write allowlists.
type flagArgs []string

func (f *flagArgs) UnmarshalJSON(b []byte) error {
	var all []string
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	var out flagArgs
	for _, a := range all {
		k, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && slices.Contains(wollomaticArg, k) {
			out = append(out, a)
		}
	}
	*f = out
	return nil
}

// grants is what Test reports the endpoint allows.
func (a access) grants(ep endpoint, w writes) []integration.Grant {
	g := []integration.Grant{{Name: "containers", Detail: "list containers, their state and labels"}}
	add := func(on bool, name, detail string) {
		if on {
			g = append(g, integration.Grant{Name: name, Detail: detail})
		}
	}
	add(a.inspect, "inspect", "each container's full config, its environment included (Cha Dash drops the environment)")
	add(a.events, "events", "the live event stream")
	add(a.stats, "stats", "per-container CPU and memory")
	add(a.info, "info", "the engine's host name and totals")
	add(a.images, "images", "image details")
	add(a.broad && !a.raw, "logs and files", "every GET under /containers/: logs, file archives and whole filesystems")
	switch {
	case a.raw && ep.unix:
		g = append(g, integration.Grant{Name: "engine", Access: integration.AccessAdmin, Write: true,
			Detail: "the Docker socket itself: full control of the engine, and root on the host"})
	case a.raw:
		g = append(g, integration.Grant{Name: "engine", Access: integration.AccessAdmin, Write: true,
			Detail: "the raw Docker API: full control of the engine, and root on the host"})
	case w.yes:
		g = append(g, integration.Grant{Name: "writes", Access: integration.AccessWrite, Write: true, Detail: w.why})
	}
	return g
}

// warnings is what Test says about the endpoint: its tier, and the reads it
// refuses that cost a feature.
func (a access) warnings(ep endpoint) []integration.Warning {
	var out []integration.Warning
	switch {
	case a.raw && ep.unix:
		out = append(out, integration.Warning{Code: "docker_raw_socket", Severity: integration.WarnWarning,
			Message: "Cha Dash reads the Docker socket itself, which is full control of the host. It only needs to read: point it at a read-only socket proxy (the compose preset has one)."})
	case a.raw:
		out = append(out, integration.Warning{Code: "docker_raw_api", Severity: integration.WarnCritical,
			Message: ep.raw + " is the raw Docker API: anyone who can reach it has root on the host. Put a read-only socket proxy in front of it."})
	case a.broad:
		out = append(out, integration.Warning{Code: "docker_broad_proxy", Severity: integration.WarnWarning,
			Message: "This socket proxy lets every GET under /containers/ through: anyone who can reach it can read every container's environment, logs and files."})
	}
	var off []string
	if !a.events {
		off = append(off, "events (live updates; Cha Dash polls instead)")
	}
	if !a.inspect {
		off = append(off, "inspect (restart counts and policies, ports of stopped containers)")
	}
	if !a.info {
		off = append(off, "info (the engine's host name)")
	}
	if len(off) > 0 {
		out = append(out, integration.Warning{Code: "capability_missing", Severity: integration.WarnInfo,
			Message: "The endpoint doesn't allow " + strings.Join(off, ", ") + "."})
	}
	return out
}
