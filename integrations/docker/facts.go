package docker

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// labelPrefixes is the label allowlist: only these reach the graph.
// Anything else (traefik.* basic-auth labels carry password hashes, images
// carry maintainers' emails) is never copied.
var labelPrefixes = []string{"homepage.", "chadash.", "com.docker.compose.", "net.unraid.docker."}

// secretLabel matches the last segment of a label that holds a credential
// (homepage.widget.key, homepage.widget.password). Those are dropped even
// under an allowed prefix.
var secretLabel = regexp.MustCompile(`(?i)(key|pass|secret|token|auth|cookie|credential)`)

// publicLabels is the allowlisted part of a container's labels, and the keys
// it dropped because they look like secrets.
func publicLabels(all map[string]string) (map[string]string, []string) {
	var out map[string]string
	var secret []string
	for k, v := range all {
		if !slices.ContainsFunc(labelPrefixes, func(p string) bool { return strings.HasPrefix(k, p) }) {
			continue
		}
		last := k[strings.LastIndexByte(k, '.')+1:]
		if secretLabel.MatchString(last) || hasUserinfo(v) {
			secret = append(secret, k)
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	slices.Sort(secret)
	return out, secret
}

// hasUserinfo reports a URL with credentials in it (http://user:pass@host).
func hasUserinfo(v string) bool {
	if !strings.Contains(v, "@") || !strings.Contains(v, "://") {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && u.User != nil
}

// publish turns the engine's containers into facts, then raises or resolves
// the hints.
func (in *instance) publish(list []summary) error {
	self := in.d.Emit.Self()
	eng := in.rid("engine")
	sort.Slice(list, func(i, j int) bool { return list[i].name() < list[j].name() })

	var f integration.Facts
	f.Relations = append(f.Relations, integration.Relation{From: self, Type: "monitors", To: eng})
	stacks := map[string]*stack{}
	var secretHolders []string
	running, managed := 0, ""
	for _, s := range list {
		c := in.cache[s.ID]
		r, secret := in.container(s, c)
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, integration.Relation{From: r.ID, Type: "runs_on", To: eng})
		if len(secret) > 0 {
			secretHolders = append(secretHolders, s.name())
		}
		if s.State == "running" {
			running++
		}
		if s.Labels["net.unraid.docker.managed"] != "" {
			managed = "unraid"
		}
		if p := s.Labels["com.docker.compose.project"]; p != "" {
			st := stacks[p]
			if st == nil {
				st = &stack{name: p}
				stacks[p] = st
			}
			st.add(s, r)
			f.Relations = append(f.Relations, integration.Relation{From: r.ID, Type: "part_of", To: in.rid("stack/" + p)})
		}
	}
	names := make([]string, 0, len(stacks))
	for p := range stacks {
		names = append(names, p)
	}
	slices.Sort(names)
	for _, p := range names {
		r := stacks[p].resource(in.rid("stack/" + p))
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, integration.Relation{From: r.ID, Type: "runs_on", To: eng})
	}
	if managed == "" && len(stacks) > 0 {
		managed = "compose"
	}
	f.Resources = append(f.Resources, in.engine(len(list), running, managed))
	if p := in.proxyContainer(list); p != nil {
		f.Relations = append(f.Relations, integration.Relation{From: self, Type: "depends_on", To: in.rid("container/" + p.name())})
	}
	if err := in.d.Emit.Replace("", f); err != nil {
		return err
	}
	return in.hints(eng, secretHolders)
}

// engine is the engine resource, the instance's service resource.
func (in *instance) engine(total, running int, manager string) integration.Resource {
	attrs := map[string]any{
		"endpoint": in.ep.raw, "via": "socket-proxy", "access": in.acc.tier(),
		"containers": total, "running": running,
	}
	switch {
	case in.ep.unix && in.acc.raw:
		attrs["via"] = "socket"
	case in.acc.raw:
		attrs["via"] = "tcp"
	}
	if !in.ep.unix {
		attrs["host"] = in.ep.host
		if net.ParseIP(in.ep.host) != nil {
			attrs["ip"] = in.ep.host
		}
	}
	if manager != "" {
		attrs["manager"] = manager
	}
	v := in.version
	for k, x := range map[string]string{"version": v.Version, "api_version": strings.TrimPrefix(in.api, "/v"), "os": v.Os, "arch": v.Arch, "kernel": v.KernelVersion} {
		if x != "" {
			attrs[k] = x
		}
	}
	if i := in.info; i != nil {
		if i.Name != "" {
			attrs["hostname"] = i.Name
		}
		if i.OperatingSystem != "" {
			attrs["os_name"] = i.OperatingSystem
		}
		if i.NCPU > 0 {
			attrs["cores"] = i.NCPU
		}
		if i.MemTotal > 0 {
			attrs["mem_gb"] = float64(i.MemTotal*10/(1<<30)) / 10
		}
	}
	return integration.Resource{
		ID: in.rid("engine"), Kind: "engine", Name: in.name, State: "running",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs, Caps: []string{"containers"},
	}
}

// container maps one container. c is its cached inspect, or nil when
// inspects are off or it hasn't been inspected yet.
func (in *instance) container(s summary, c *inspected) (integration.Resource, []string) {
	labels, secret := publicLabels(s.Labels)
	r := integration.Resource{
		ID: in.rid("container/" + s.name()), Kind: "container", Name: s.name(), Labels: labels,
		Attrs: map[string]any{"container_id": short(s.ID), "image": s.Image},
	}
	var ins *inspect
	if c != nil {
		ins = &c.ins
	}
	if ports := containerPorts(s, ins); len(ports) > 0 {
		r.Attrs["ports"] = ports
	}
	if pub := publishedPorts(s, ins); len(pub) > 0 {
		r.Attrs["published"] = pub
	}
	if m := s.HostConfig.NetworkMode; m != "" {
		r.Attrs["network_mode"] = m
	}
	var nets, ips []string
	for n, e := range s.NetworkSettings.Networks {
		nets = append(nets, n)
		if e.IPAddress != "" {
			ips = append(ips, e.IPAddress)
		}
	}
	slices.Sort(nets)
	slices.Sort(ips)
	if len(nets) > 0 {
		r.Attrs["networks"] = nets
	}
	if len(ips) > 0 {
		r.Attrs["ips"] = ips
	}
	hs := s.Health
	if ins != nil {
		r.Attrs["restart_count"] = ins.RestartCount
		r.Attrs["restart_policy"] = cmpStr(ins.HostConfig.RestartPolicy.Name, "no")
		if ins.HostConfig.Memory > 0 {
			r.Attrs["mem_limit_mib"] = ins.HostConfig.Memory >> 20
		}
		if hs == nil {
			hs = ins.State.Health
		}
	}
	// An exited container keeps its last health status: stale, so left out.
	if hs != nil && hs.Status != "" && hs.Status != "none" && s.State == "running" {
		r.Attrs["health_check"] = hs.Status
		if hs.FailingStreak > 0 {
			r.Attrs["failing_streak"] = hs.FailingStreak
		}
	}
	r.State, r.Desired, r.Health = containerHealth(s, ins, hs, r.Attrs)
	r.Links = in.links(s, ins, labels)
	return r, secret
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// exitedRE reads the exit code from a summary's Status when there's no
// inspect: "Exited (137) 4 months ago".
var exitedRE = regexp.MustCompile(`^Exited \((-?\d+)\)`)

// containerHealth maps Docker's state to the graph's state, desired state
// and health. Docker's own health check only counts while the container
// runs: an exited container keeps its last, stale status.
func containerHealth(s summary, ins *inspect, hs *healthSummary, attrs map[string]any) (string, string, integration.Health) {
	state := s.State
	desired := ""
	policy := ""
	var started, finished time.Time
	exit, oom := -1, false
	if ins != nil {
		policy = ins.HostConfig.RestartPolicy.Name
		started = dockerTime(ins.State.StartedAt)
		finished = dockerTime(ins.State.FinishedAt)
		exit, oom = ins.State.ExitCode, ins.State.OOMKilled
	} else if m := exitedRE.FindStringSubmatch(s.Status); m != nil {
		exit, _ = strconv.Atoi(m[1])
	}
	// Docker restarts always and unless-stopped containers, so they should
	// run. With "no" or on-failure (Unraid's templates use "no"; its own
	// autostart list decides) Docker can't tell a stop on purpose from an
	// outage, and the Unraid integration supplies the desired state.
	if policy == "always" || policy == "unless-stopped" {
		desired = "running"
	}
	h := integration.Health{Status: integration.HealthOK}
	switch state {
	case "running":
		if !started.IsZero() {
			attrs["started_at"] = started
		}
		status, streak := "", 0
		if hs != nil {
			status, streak = hs.Status, hs.FailingStreak
		}
		switch {
		case status == "unhealthy":
			h = integration.Health{Status: integration.HealthDown, Reason: fmt.Sprintf("health check failing (%d in a row)", streak)}
		case status == "starting":
			h = integration.Health{Status: integration.HealthAttention, Reason: "starting; health check pending"}
		case streak > 0:
			h = integration.Health{Status: integration.HealthAttention, Reason: fmt.Sprintf("health check failed %d %s in a row", streak, plural(streak, "time"))}
		default:
			h.Since = started
		}
	case "exited", "dead":
		if exit >= 0 {
			attrs["exit_code"] = exit
		}
		if oom {
			attrs["oom_killed"] = true
		}
		if !finished.IsZero() {
			attrs["finished_at"] = finished
		}
		h.Since = finished
		switch {
		case state == "dead":
			h.Status, h.Reason = integration.HealthDown, "dead: Docker couldn't remove it"
		case oom && desired == "running":
			h.Status, h.Reason = integration.HealthDown, "OOM-killed"
		case oom:
			h.Status, h.Reason = integration.HealthAttention, "last exit was an OOM kill"
		case desired == "running":
			h.Status, h.Reason = integration.HealthAttention, fmt.Sprintf("exited (%d); its restart policy (%s) says it should run", exit, policy)
		default:
			h.Reason = fmt.Sprintf("stopped (exited %d)", exit)
		}
	case "restarting":
		h = integration.Health{Status: integration.HealthAttention, Reason: "restarting"}
	case "paused":
		h = integration.Health{Status: integration.HealthMaintenance, Reason: "paused"}
	case "removing":
		h = integration.Health{Status: integration.HealthMaintenance, Reason: "being removed"}
	case "created":
		h.Reason = "created, never started"
	default:
		h = integration.Health{Status: integration.HealthUnknown}
	}
	return state, desired, h
}

func plural(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}

// dockerTime parses an inspect timestamp; Docker's zero time is
// "0001-01-01T00:00:00Z".
func dockerTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 2000 {
		return time.Time{}
	}
	return t.UTC().Truncate(time.Second)
}

// containerPorts are the container's ports, "8989/tcp", the way the Demo
// Home lists them: exposed and bound ones from the inspect (which covers
// stopped containers), else the summary's.
func containerPorts(s summary, ins *inspect) []string {
	seen := map[string]bool{}
	if ins != nil {
		for p := range ins.Config.ExposedPorts {
			seen[p] = true
		}
		for p := range ins.HostConfig.PortBindings {
			seen[p] = true
		}
	}
	if len(seen) == 0 {
		for _, p := range s.Ports {
			seen[fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)] = true
		}
	}
	return sortPorts(seen)
}

// publishedPorts are the host bindings, "18989->8989/tcp", with the host IP
// when it isn't a wildcard. A natural key: routes and checks reach
// containers by host port.
func publishedPorts(s summary, ins *inspect) []string {
	seen := map[string]bool{}
	bind := func(ip, host, cont string) {
		if host == "" || host == "0" {
			return
		}
		b := host + "->" + cont
		if ip != "" && ip != "0.0.0.0" && ip != "::" {
			b = net.JoinHostPort(ip, host) + "->" + cont
		}
		seen[b] = true
	}
	if ins != nil && len(ins.HostConfig.PortBindings) > 0 {
		for cont, bs := range ins.HostConfig.PortBindings {
			for _, b := range bs {
				bind(b.HostIP, b.HostPort, cont)
			}
		}
	} else {
		for _, p := range s.Ports {
			if p.PublicPort != 0 {
				bind(p.IP, strconv.Itoa(p.PublicPort), fmt.Sprintf("%d/%s", p.PrivatePort, p.Type))
			}
		}
	}
	return sortPorts(seen)
}

// sortPorts orders "8989/tcp" and "18989->8989/tcp" numerically.
func sortPorts(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	num := func(p string) int {
		p = p[strings.LastIndexByte(p, ':')+1:]
		if d := strings.FieldsFunc(p, func(r rune) bool { return r < '0' || r > '9' }); len(d) > 0 {
			n, _ := strconv.Atoi(d[0])
			return n
		}
		return 0
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := num(out[i]), num(out[j]); a != b {
			return a < b
		}
		return out[i] < out[j]
	})
	return out
}

// webuiRE is one placeholder of Unraid's web UI template:
// http://[IP]:[PORT:8989]/.
var webuiRE = regexp.MustCompile(`\[(IP|PORT:(\d+))\]`)

// links are the container's open URLs: its homepage.href, and Unraid's web
// UI template filled in with the engine's address and the host port bound to
// the named container port. The template needs an endpoint host the browser
// can reach (an IP or a dotted name, not a compose service name).
func (in *instance) links(s summary, ins *inspect, labels map[string]string) []integration.Link {
	var out []integration.Link
	if href := labels["homepage.href"]; isWebURL(href) {
		out = append(out, integration.Link{Title: cmpStr(labels["homepage.name"], s.name()), URL: href})
	}
	tmpl := labels["net.unraid.docker.webui"]
	if tmpl == "" || in.ep.unix || (net.ParseIP(in.ep.host) == nil && !strings.Contains(in.ep.host, ".")) {
		return out
	}
	ok := true
	u := webuiRE.ReplaceAllStringFunc(tmpl, func(m string) string {
		sub := webuiRE.FindStringSubmatch(m)
		if sub[1] == "IP" {
			if strings.Contains(in.ep.host, ":") {
				return "[" + in.ep.host + "]"
			}
			return in.ep.host
		}
		if p := hostPort(s, ins, sub[2]); p != "" {
			return p
		}
		ok = false
		return m
	})
	if ok && isWebURL(u) {
		out = append(out, integration.Link{Title: "Web UI", URL: u})
	}
	return out
}

// hostPort is the host port a container port is reached on: its binding,
// or the port itself on the host network.
func hostPort(s summary, ins *inspect, port string) string {
	if s.HostConfig.NetworkMode == "host" {
		return port
	}
	if ins != nil {
		for _, proto := range []string{"/tcp", "/udp"} {
			for _, b := range ins.HostConfig.PortBindings[port+proto] {
				if b.HostPort != "" {
					return b.HostPort
				}
			}
		}
	}
	for _, p := range s.Ports {
		if strconv.Itoa(p.PrivatePort) == port && p.PublicPort != 0 {
			return strconv.Itoa(p.PublicPort)
		}
	}
	return ""
}

func isWebURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

// stack is a compose project as its containers describe it.
type stack struct {
	name           string
	total, running int
	managed, dir   string
	files          []string
	worst          integration.Health
	worstName      string
}

var healthRank = map[string]int{integration.HealthOK: 0, integration.HealthMaintenance: 1, integration.HealthUnknown: 1,
	integration.HealthAttention: 2, integration.HealthDown: 3}

func (st *stack) add(s summary, r integration.Resource) {
	st.total++
	if s.State == "running" {
		st.running++
	}
	if m := s.Labels["net.unraid.docker.managed"]; m != "" {
		st.managed = m
	}
	if d := s.Labels["com.docker.compose.project.working_dir"]; d != "" {
		st.dir = d
	}
	if f := s.Labels["com.docker.compose.project.config_files"]; f != "" && st.files == nil {
		st.files = strings.Split(f, ",")
	}
	if st.worstName == "" || healthRank[r.Health.Status] > healthRank[st.worst.Status] {
		st.worst, st.worstName = r.Health, s.name()
	}
}

// resource is the stack's resource: running when all its containers run,
// stopped when none does, partial between; its health is its worst
// container's.
func (st *stack) resource(id string) integration.Resource {
	attrs := map[string]any{"project": st.name, "containers": st.total, "running": st.running}
	if st.dir != "" {
		attrs["working_dir"] = st.dir
	}
	if len(st.files) > 0 {
		attrs["config_files"] = st.files
	}
	if st.managed != "" {
		// composeman: Unraid's Compose Manager owns the files on its flash.
		attrs["managed_by"] = st.managed
	}
	state := "partial"
	switch st.running {
	case st.total:
		state = "running"
	case 0:
		state = "stopped"
	}
	h := integration.Health{Status: integration.HealthOK}
	if healthRank[st.worst.Status] >= healthRank[integration.HealthAttention] {
		h = integration.Health{Status: integration.HealthAttention, Reason: st.worstName + ": " + st.worst.Reason}
		if st.worst.Status == integration.HealthDown && st.running == 0 {
			h.Status = integration.HealthDown
		}
	}
	return integration.Resource{ID: id, Kind: "stack", Name: st.name, State: state, Health: h, Attrs: attrs}
}

// Hints, by incident ID (local to the instance).
const (
	hintEndpoint     = "hint.docker_endpoint"
	hintLabelSecrets = "hint.label_secrets"
)

// hints raises the credential-hygiene hints that apply and resolves the
// ones that no longer do.
func (in *instance) hints(eng string, secretHolders []string) error {
	want := map[string]integration.Incident{}
	base := func(id, title, summary, resolves string) integration.Incident {
		return integration.Incident{ID: id, Scenario: "credential-hygiene", Class: integration.ClassProblem,
			Severity: integration.SeverityAttention, Category: integration.CategorySecurity, Title: title, Summary: summary, Target: eng, ResolvesWhen: resolves,
			Facts: map[string]any{"endpoint": in.ep.raw, "access": in.acc.tier()}}
	}
	switch {
	case in.acc.raw && in.ep.unix:
		want[hintEndpoint] = base(hintEndpoint, "Cha Dash has full Docker access on "+in.name,
			"This instance reads the Docker socket itself, which is full control of the host. Cha Dash only reads: point it at a read-only socket proxy (the compose preset has one).",
			"Resolves when the instance reads through a filtering proxy.")
	case in.acc.raw:
		want[hintEndpoint] = base(hintEndpoint, in.name+" exposes the raw Docker API",
			fmt.Sprintf("%s answers like a raw Docker API: anyone who can reach it has root on the host. Put a read-only socket proxy in front of it and point this instance at the proxy.", in.ep.raw),
			"Resolves when the endpoint filters what it allows.")
	case in.acc.broad:
		// Reads only, so a hint (info) rather than a problem that needs you;
		// the raw API and the socket above hand out root and stay attention.
		inc := base(hintEndpoint, in.name+"'s socket proxy is too broad",
			fmt.Sprintf("The socket proxy at %s lets every GET under /containers/ through, so anyone who can reach it can read every container's environment, logs and files. Restrict who can connect (a firewall, or wollomatic's -allowfrom), or switch to a path-anchored allowlist like Cha Dash's compose preset.", in.ep.raw),
			"Resolves when the proxy refuses container logs.")
		inc.Severity = integration.SeverityInfo
		want[hintEndpoint] = inc
	}
	if len(secretHolders) > 0 {
		inc := base(hintLabelSecrets, "Secrets in container labels on "+in.name,
			fmt.Sprintf("%s %s secret-looking labels (keys, passwords or tokens, such as homepage.widget.key). Anyone who can list containers can read them; Cha Dash doesn't copy them. Keep the secret in Cha Dash's vault instead and remove the label.",
				joinNames(secretHolders), map[bool]string{true: "carries", false: "carry"}[len(secretHolders) == 1]),
			"Resolves when no container carries a secret-looking label.")
		inc.Facts["containers"] = secretHolders
		want[hintLabelSecrets] = inc
	}
	for _, id := range []string{hintEndpoint, hintLabelSecrets} {
		inc, ok := want[id]
		switch {
		case ok:
			if err := in.d.Emit.Raise(inc); err != nil {
				return err
			}
			in.raised[id] = true
		case in.raised[id]:
			if err := in.d.Emit.Resolve(id, "No longer applies."); err != nil {
				return err
			}
			delete(in.raised, id)
		}
	}
	return nil
}

// joinNames lists names in a sentence, at most five.
func joinNames(names []string) string {
	if len(names) > 5 {
		return strings.Join(names[:5], ", ") + fmt.Sprintf(" and %d more", len(names)-5)
	}
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
