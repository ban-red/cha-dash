package correlate

import (
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/internal/graph"
)

// home builds a synthetic graph the way integrations publish one: every
// resource has its instance as source, and relations come from the same
// instance unless said otherwise.
type home struct {
	t   *testing.T
	doc graph.Doc
}

func newHome(t *testing.T) *home {
	return &home{t: t, doc: graph.Doc{Resources: map[graph.RID]*graph.Resource{}, Relations: map[string]*graph.Relation{}}}
}

// add adds a resource. kv are attrs, except keys starting with "label:".
func (h *home) add(id, kind, name string, kv ...any) *home {
	h.t.Helper()
	r := &graph.Resource{ID: graph.RID(id), Kind: graph.Kind(kind), Name: name, Health: graph.HealthOK, Attrs: map[string]any{}}
	if integ, rest, ok := strings.Cut(id, ":"); ok && integ != "integration" {
		inst, _, _ := strings.Cut(rest, "/")
		r.Source = "integration:" + inst
	}
	for i := 0; i+1 < len(kv); i += 2 {
		k := kv[i].(string)
		if l, ok := strings.CutPrefix(k, "label:"); ok {
			if r.Labels == nil {
				r.Labels = map[string]string{}
			}
			r.Labels[l] = kv[i+1].(string)
			continue
		}
		switch v := kv[i+1].(type) {
		case []string:
			s := make([]any, len(v))
			for i, x := range v {
				s[i] = x
			}
			r.Attrs[k] = s
		case int:
			r.Attrs[k] = float64(v)
		default:
			r.Attrs[k] = v
		}
	}
	h.doc.Resources[r.ID] = r
	return h
}

func (h *home) rel(from, typ, to string) *home {
	src := h.doc.Resources[graph.RID(from)].Source
	rel := &graph.Relation{From: graph.RID(from), Type: graph.RelType(typ), To: graph.RID(to), Source: src}
	h.doc.Relations[rel.Key()] = rel
	return h
}

func (h *home) run(decisions ...Decision) Result { return Correlate(h.doc, decisions) }

func hasLink(r Result, from string, typ graph.RelType, to string) (Link, bool) {
	for _, l := range r.Links {
		if l.From == graph.RID(from) && l.Type == typ && l.To == graph.RID(to) {
			return l, true
		}
	}
	return Link{}, false
}

func hasSuggestion(r Result, from string, typ graph.RelType, to string) bool {
	return slices.ContainsFunc(r.Suggestions, func(l Link) bool {
		return l.From == graph.RID(from) && l.Type == typ && l.To == graph.RID(to)
	})
}

func mustLink(t *testing.T, r Result, from string, typ graph.RelType, to string, conf Confidence, rule string) {
	t.Helper()
	l, ok := hasLink(r, from, typ, to)
	if !ok {
		t.Fatalf("no link %s %s %s; links:\n%s", from, typ, to, dump(r.Links))
	}
	if l.Confidence != conf || l.Rule != rule {
		t.Fatalf("%s %s %s: %s by %s, want %s by %s", from, typ, to, l.Confidence, l.Rule, conf, rule)
	}
	if l.Evidence == "" {
		t.Fatalf("%s %s %s has no evidence", from, typ, to)
	}
}

func noLink(t *testing.T, r Result, from string, typ graph.RelType, to string) {
	t.Helper()
	if l, ok := hasLink(r, from, typ, to); ok {
		t.Fatalf("unexpected link %s %s %s (%s: %s)", from, typ, to, l.Rule, l.Evidence)
	}
}

func dump(ls []Link) string {
	var b strings.Builder
	for _, l := range ls {
		b.WriteString("  " + l.Key() + " [" + string(l.Confidence) + " " + l.Rule + "] " + l.Evidence + "\n")
	}
	return b.String()
}

const fullID = "aa2bd00fa7c9e3b1c0de5f00d1e2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4"

// jupiter is an Unraid server whose Docker engine both integrations read.
func jupiter(t *testing.T) *home {
	return newHome(t).
		add("unraid:i_u/host", "host", "jupiter", "platform", "unraid", "ip", "10.0.11.120").
		add("unraid:i_u/array", "array", "array").rel("unraid:i_u/array", "part_of", "unraid:i_u/host").
		add("unraid:i_u/container/sonarr", "container", "sonarr", "container_id", fullID, "autostart", true).
		rel("unraid:i_u/container/sonarr", "runs_on", "unraid:i_u/host").
		add("unraid:i_u/container/radarr", "container", "radarr", "container_id", "bb"+fullID[2:]).
		rel("unraid:i_u/container/radarr", "runs_on", "unraid:i_u/host").
		add("docker:i_d/engine", "engine", "Jupiter", "host", "dockerproxy", "manager", "unraid").
		add("docker:i_d/container/sonarr", "container", "sonarr", "container_id", fullID[:12],
			"published", []string{"18989->8989/tcp"}, "ports", []string{"8989/tcp"}, "ips", []string{"10.101.0.24"}).
		rel("docker:i_d/container/sonarr", "runs_on", "docker:i_d/engine").
		add("docker:i_d/container/radarr", "container", "radarr", "container_id", "bb"+fullID[2:12],
			"published", []string{"17878->7878/tcp"}, "ports", []string{"7878/tcp"}).
		rel("docker:i_d/container/radarr", "runs_on", "docker:i_d/engine")
}

func TestContainersMergeByID(t *testing.T) {
	r := jupiter(t).run()
	mustLink(t, r, "unraid:i_u/container/sonarr", SameAs, "docker:i_d/container/sonarr", Exact, "container_id")
	mustLink(t, r, "unraid:i_u/container/radarr", SameAs, "docker:i_d/container/radarr", Exact, "container_id")
	noLink(t, r, "unraid:i_u/container/radarr", SameAs, "docker:i_d/container/sonarr")
	// The engine's containers are the ones Unraid reports on jupiter, so it
	// runs there, though its endpoint is a compose name.
	mustLink(t, r, "docker:i_d/engine", RunsOn, "unraid:i_u/host", Exact, "containers")
	// Unraid's Docker stops with the array.
	mustLink(t, r, "docker:i_d/engine", DependsOn, "unraid:i_u/array", Strong, "unraid")
	if len(r.Suggestions) != 0 {
		t.Fatalf("suggestions: %s", dump(r.Suggestions))
	}
}

func TestShortIDsMatchOnlyByPrefix(t *testing.T) {
	h := newHome(t).
		add("docker:i_a/container/x", "container", "x", "container_id", "0123456789ab").
		add("docker:i_b/container/x", "container", "x", "container_id", "0123456789ab").
		add("unraid:i_u/container/y", "container", "y", "container_id", "0123456789ac"+strings.Repeat("0", 52)).
		add("docker:i_a/container/z", "container", "z", "container_id", "not-hex-at-all")
	r := h.run()
	mustLink(t, r, "docker:i_b/container/x", SameAs, "docker:i_a/container/x", Exact, "container_id")
	if len(r.Links) != 1 {
		t.Fatalf("links:\n%s", dump(r.Links))
	}
}

func TestRoutesAppsAndAddresses(t *testing.T) {
	h := jupiter(t).
		add("traefik:i_t/app", "app", "Traefik", "type", "traefik").
		add("traefik:i_t/route/sonarr", "route", "sonarr", "url", "https://sonarr.lab.example", "upstream", "http://10.0.11.120:18989",
			"upstream_addr", "10.0.11.120:18989").
		add("traefik:i_t/route/radarr", "route", "radarr", "url", "https://radarr.lab.example", "upstream_addr", "10.101.0.30:7878").
		add("sonarr:i_s/app", "app", "Sonarr", "type", "sonarr", "url", "https://sonarr.lab.example").
		add("radarr:i_r/app", "app", "Radarr", "type", "radarr", "url", "http://10.0.11.120:17878").
		add("check:i_c/sonarr", "check", "Sonarr", "target", "https://sonarr.lab.example/").
		add("check:i_c/radarr-port", "check", "Radarr port", "target", "10.0.11.120:17878")
	r := h.run()
	// Published ports answer on the address of the host the engine runs on.
	mustLink(t, r, "traefik:i_t/route/sonarr", RoutesTo, "docker:i_d/container/sonarr", Strong, "backend")
	mustLink(t, r, "traefik:i_t/route/sonarr", RoutesTo, "sonarr:i_s/app", Strong, "url")
	mustLink(t, r, "sonarr:i_s/app", RunsOn, "docker:i_d/container/sonarr", Strong, "route")
	mustLink(t, r, "radarr:i_r/app", RunsOn, "docker:i_d/container/radarr", Strong, "address")
	// Nothing answers at radarr's route's backend.
	noLink(t, r, "traefik:i_t/route/radarr", RoutesTo, "docker:i_d/container/radarr")
	mustLink(t, r, "check:i_c/sonarr", Monitors, "sonarr:i_s/app", Strong, "target")
	mustLink(t, r, "check:i_c/sonarr", Monitors, "traefik:i_t/route/sonarr", Strong, "target")
	mustLink(t, r, "check:i_c/radarr-port", Monitors, "docker:i_d/container/radarr", Strong, "address")
	mustLink(t, r, "check:i_c/radarr-port", Monitors, "radarr:i_r/app", Strong, "target")
	// Relations go to the primary: the Docker container, never Unraid's.
	for _, l := range r.Links {
		if l.Type != SameAs && strings.HasPrefix(string(l.To), "unraid:i_u/container/") {
			t.Fatalf("a link to an alias: %s", l.Key())
		}
	}
}

func TestBridgeAddresses(t *testing.T) {
	h := jupiter(t).
		add("traefik:i_t/route/sonarr", "route", "sonarr", "url", "https://sonarr.lab.example", "upstream_addr", "10.101.0.24:8989")
	mustLink(t, h.run(), "traefik:i_t/route/sonarr", RoutesTo, "docker:i_d/container/sonarr", Strong, "backend")
}

// A backend with a path is another proxy (the nginx-proxy hop): the route
// reaches the proxy's container, and the app behind it is only suggested,
// by name.
func TestProxyHopSuggestsByName(t *testing.T) {
	h := jupiter(t).
		add("docker:i_d/container/nginx-proxy", "container", "nginx-proxy", "container_id", "cc"+fullID[2:12], "published", []string{"7780->80/tcp"}).
		rel("docker:i_d/container/nginx-proxy", "runs_on", "docker:i_d/engine").
		add("traefik:i_t/route/sonarr", "route", "sonarr", "url", "https://jupiter.lab.example/sonarr",
			"upstream", "http://10.0.11.120:7780/sonarr", "upstream_addr", "10.0.11.120:7780", "upstream_path", "/sonarr").
		add("sonarr:i_s/app", "app", "Sonarr", "type", "sonarr", "url", "https://jupiter.lab.example/sonarr")
	r := h.run()
	mustLink(t, r, "traefik:i_t/route/sonarr", RoutesTo, "docker:i_d/container/nginx-proxy", Strong, "backend")
	mustLink(t, r, "traefik:i_t/route/sonarr", RoutesTo, "sonarr:i_s/app", Strong, "url")
	noLink(t, r, "sonarr:i_s/app", RunsOn, "docker:i_d/container/nginx-proxy")
	if !hasSuggestion(r, "sonarr:i_s/app", RunsOn, "docker:i_d/container/sonarr") || len(r.Suggestions) != 1 {
		t.Fatalf("suggestions:\n%s", dump(r.Suggestions))
	}

	// The owner confirms: the link is published, the suggestion goes.
	r = h.run(Decision{From: "sonarr:i_s/app", Type: RunsOn, To: "docker:i_d/container/sonarr", Verdict: Confirm, Rule: "name"})
	mustLink(t, r, "sonarr:i_s/app", RunsOn, "docker:i_d/container/sonarr", Confirmed, "name")
	if len(r.Suggestions) != 0 {
		t.Fatalf("suggestions after confirming:\n%s", dump(r.Suggestions))
	}
	// Or rejects: neither link nor suggestion, ever.
	r = h.run(Decision{From: "sonarr:i_s/app", Type: RunsOn, To: "docker:i_d/container/sonarr", Verdict: Reject})
	noLink(t, r, "sonarr:i_s/app", RunsOn, "docker:i_d/container/sonarr")
	if len(r.Suggestions) != 0 {
		t.Fatalf("suggestions after rejecting:\n%s", dump(r.Suggestions))
	}
}

// Two engines both have a container at 172.17.0.2 (Docker's default
// bridge): a route to it is ambiguous, so it's a question, not a link.
func TestAmbiguousBackendIsSuggested(t *testing.T) {
	h := newHome(t)
	for _, e := range []string{"i_a", "i_b"} {
		h.add("docker:"+e+"/engine", "engine", e).
			add("docker:"+e+"/container/web", "container", "web", "container_id", e[2:]+"00000000000",
				"ports", []string{"80/tcp"}, "ips", []string{"172.17.0.2"}).
			rel("docker:"+e+"/container/web", "runs_on", "docker:"+e+"/engine")
	}
	h.add("traefik:i_t/route/web", "route", "web", "url", "https://web.lab.example", "upstream_addr", "172.17.0.2:80")
	r := h.run()
	noLink(t, r, "traefik:i_t/route/web", RoutesTo, "docker:i_a/container/web")
	noLink(t, r, "traefik:i_t/route/web", RoutesTo, "docker:i_b/container/web")
	if !hasSuggestion(r, "traefik:i_t/route/web", RoutesTo, "docker:i_a/container/web") ||
		!hasSuggestion(r, "traefik:i_t/route/web", RoutesTo, "docker:i_b/container/web") {
		t.Fatalf("suggestions:\n%s", dump(r.Suggestions))
	}
	for _, s := range r.Suggestions {
		if s.Confidence != Weak || !strings.Contains(s.Evidence, "one of 2") {
			t.Fatalf("suggestion %+v", s)
		}
	}
}

// Rejecting a merge keeps the pair apart, even when the IDs match exactly
// and even through a third member.
func TestRejectedMergeHolds(t *testing.T) {
	h := jupiter(t).
		add("docker:i_e/container/sonarr", "container", "sonarr", "container_id", fullID[:12])
	r := h.run()
	if l, _ := hasLink(r, "docker:i_e/container/sonarr", SameAs, "docker:i_d/container/sonarr"); l.Rule != "container_id" {
		t.Fatalf("links:\n%s", dump(r.Links))
	}
	r = h.run(Decision{From: "docker:i_d/container/sonarr", Type: SameAs, To: "unraid:i_u/container/sonarr", Verdict: Reject})
	same := map[graph.RID]graph.RID{}
	for _, l := range r.Links {
		if l.Type == SameAs {
			same[l.From] = l.To
		}
	}
	canon := func(id graph.RID) graph.RID {
		if p, ok := same[id]; ok {
			return p
		}
		return id
	}
	if canon("docker:i_d/container/sonarr") == canon("unraid:i_u/container/sonarr") {
		t.Fatalf("a rejected pair was merged:\n%s", dump(r.Links))
	}
	// The pair that wasn't rejected still merges.
	if canon("docker:i_e/container/sonarr") != canon("docker:i_d/container/sonarr") && canon("docker:i_e/container/sonarr") != canon("unraid:i_u/container/sonarr") {
		t.Fatalf("links:\n%s", dump(r.Links))
	}
}

func TestConfirmedMerge(t *testing.T) {
	h := newHome(t).
		add("sonarr:i_a/app", "app", "Sonarr", "type", "sonarr", "url", "http://10.0.11.120:8989", "version", "4.0.15").
		add("sonarr:i_b/app", "app", "Sonarr (proxy)", "type", "sonarr", "url", "https://sonarr.lab.example", "version", "4.0.15")
	r := h.run()
	if !hasSuggestion(r, "sonarr:i_b/app", SameAs, "sonarr:i_a/app") || len(r.Links) != 0 {
		t.Fatalf("links:\n%s\nsuggestions:\n%s", dump(r.Links), dump(r.Suggestions))
	}
	r = h.run(Decision{From: "sonarr:i_a/app", Type: SameAs, To: "sonarr:i_b/app", Verdict: Confirm, Rule: "version"})
	mustLink(t, r, "sonarr:i_b/app", SameAs, "sonarr:i_a/app", Confirmed, "version")
	if len(r.Suggestions) != 0 {
		t.Fatalf("suggestions:\n%s", dump(r.Suggestions))
	}
}

// Two Proxmox instances reading one cluster: the nodes merge by address,
// then the guests by VMID on the merged node. Two standalone hosts that
// both have a VMID 100 stay apart.
func TestGuestsMergeOnTheSameNode(t *testing.T) {
	h := newHome(t)
	for _, inst := range []string{"i_p", "i_q"} {
		h.add("proxmox:"+inst+"/node/mars-1", "host", "mars-1", "platform", "pve", "ip", "10.0.11.190").
			add("proxmox:"+inst+"/lxc/110", "lxc", "pvedocker", "vmid", 110, "ip", "10.0.11.187").
			rel("proxmox:"+inst+"/lxc/110", "runs_on", "proxmox:"+inst+"/node/mars-1")
	}
	h.add("proxmox:i_x/node/pve", "host", "pve", "platform", "pve", "ip", "10.0.11.44").
		add("proxmox:i_x/qemu/100", "vm", "a", "vmid", 100).rel("proxmox:i_x/qemu/100", "runs_on", "proxmox:i_x/node/pve").
		add("proxmox:i_y/node/pve", "host", "pve", "platform", "pve", "ip", "10.0.11.45").
		add("proxmox:i_y/qemu/100", "vm", "b", "vmid", 100).rel("proxmox:i_y/qemu/100", "runs_on", "proxmox:i_y/node/pve").
		add("docker:i_d/engine", "engine", "pvedocker", "ip", "10.0.11.187")
	r := h.run()
	mustLink(t, r, "proxmox:i_q/node/mars-1", SameAs, "proxmox:i_p/node/mars-1", Strong, "ip")
	mustLink(t, r, "proxmox:i_q/lxc/110", SameAs, "proxmox:i_p/lxc/110", Exact, "vmid")
	noLink(t, r, "proxmox:i_y/qemu/100", SameAs, "proxmox:i_x/qemu/100")
	noLink(t, r, "proxmox:i_y/node/pve", SameAs, "proxmox:i_x/node/pve")
	// The engine at the LXC's address runs in it (the primary LXC).
	mustLink(t, r, "docker:i_d/engine", RunsOn, "proxmox:i_p/lxc/110", Strong, "ip")
}

func TestOtherIdentities(t *testing.T) {
	h := newHome(t).
		add("unraid:i_a/disk/disk1", "disk", "disk1", "serial", "WD-FAKE0001").
		add("unraid:i_b/disk/disk1", "disk", "disk1", "serial", "WD-FAKE0001").
		add("check:i_c/sonarr", "check", "Sonarr", "target", "https://sonarr.lab.example").
		add("uptimekuma:i_k/monitor/7", "check", "Sonarr", "target", "https://sonarr.lab.example/").
		add("plex:i_a/app", "app", "Plex", "type", "plex", "url", "https://plex.lab.example", "machine_identifier", "fake-machine").
		add("plex:i_b/app", "app", "Plex direct", "type", "plex", "url", "https://10.0.11.120:32400", "machine_identifier", "fake-machine").
		add("radarr:i_a/app", "app", "Radarr", "type", "radarr", "url", "https://radarr.lab.example/").
		add("radarr:i_b/app", "app", "Radarr again", "type", "radarr", "url", "https://RADARR.lab.example").
		add("docker:i_d/container/x", "container", "x", "label:chadash.id", "thing").
		add("docker:i_e/container/y", "container", "y", "label:chadash.id", "thing")
	r := h.run()
	mustLink(t, r, "unraid:i_b/disk/disk1", SameAs, "unraid:i_a/disk/disk1", Exact, "serial")
	mustLink(t, r, "uptimekuma:i_k/monitor/7", SameAs, "check:i_c/sonarr", Strong, "target")
	mustLink(t, r, "plex:i_b/app", SameAs, "plex:i_a/app", Exact, "machine_identifier")
	mustLink(t, r, "radarr:i_b/app", SameAs, "radarr:i_a/app", Strong, "url")
	mustLink(t, r, "docker:i_e/container/y", SameAs, "docker:i_d/container/x", Exact, "chadash.id")
}

func TestLabelsPlaceApps(t *testing.T) {
	h := newHome(t).
		add("docker:i_d/engine", "engine", "e").
		add("docker:i_d/container/immich_server", "container", "immich_server", "label:homepage.href", "https://photos.family.example").
		rel("docker:i_d/container/immich_server", "runs_on", "docker:i_d/engine").
		add("docker:i_d/container/mealie", "container", "mealie", "label:chadash.id", "i_m").
		rel("docker:i_d/container/mealie", "runs_on", "docker:i_d/engine").
		add("immich:i_i/app", "app", "Immich", "type", "immich", "url", "https://photos.family.example/").
		add("mealie:i_m/app", "app", "Mealie", "type", "mealie", "url", "https://mealie.lab.example").
		add("traefik:i_t/route/photos", "route", "photos", "url", "https://photos.family.example")
	r := h.run()
	mustLink(t, r, "immich:i_i/app", RunsOn, "docker:i_d/container/immich_server", Strong, "homepage.href")
	mustLink(t, r, "mealie:i_m/app", RunsOn, "docker:i_d/container/mealie", Exact, "chadash.id")
	mustLink(t, r, "traefik:i_t/route/photos", RoutesTo, "docker:i_d/container/immich_server", Strong, "homepage.href")
	mustLink(t, r, "traefik:i_t/route/photos", RoutesTo, "immich:i_i/app", Strong, "url")
}

func TestPlacementByAddress(t *testing.T) {
	h := newHome(t).
		add("proxmox:i_p/node/minig5", "host", "minig5", "platform", "pve", "ip", "10.0.11.200").
		add("proxmox:i_p/qemu/100", "vm", "homeassistant", "vmid", 100, "ip", "10.0.11.20").
		add("proxmox:i_p/qemu/103", "vm", "OPNsense", "vmid", 103, "ip", "10.0.11.1").
		add("homeassistant:i_h/app", "app", "Home Assistant", "type", "homeassistant", "url", "http://10.0.11.20:8123").
		add("frigate:i_f/app", "app", "Frigate", "type", "frigate", "url", "http://127.0.0.1:5000").
		add("opnsense:i_o/router", "router", "OPNsense", "ip", "10.0.11.1")
	r := h.run()
	mustLink(t, r, "homeassistant:i_h/app", RunsOn, "proxmox:i_p/qemu/100", Strong, "ip")
	mustLink(t, r, "opnsense:i_o/router", RunsOn, "proxmox:i_p/qemu/103", Strong, "ip")
	// Loopback is every machine: it never places anything.
	for _, l := range r.Links {
		if l.From == "frigate:i_f/app" {
			t.Fatalf("loopback placed frigate: %s", l.Key())
		}
	}
}

// An engine reached through the node has no address. Its host name places
// it only with the guest's own cores and memory, and only when one guest
// has that name; otherwise it stays a suggestion.
func TestEnginePlacedByNameAndSize(t *testing.T) {
	guests := func() *home {
		return newHome(t).
			add("proxmox:i_p/lxc/3696", "lxc", "chadash-sandbox", "vmid", 3696, "cores", 2, "mem_gb", 2.0).
			add("proxmox:i_p/qemu/102", "vm", "media", "vmid", 102, "cores", 4, "mem_gb", 8.0)
	}
	r := guests().
		add("docker:i_d/engine", "engine", "Docker", "endpoint", "node://local", "hostname", "chadash-sandbox", "cores", 2, "mem_gb", 2.0).
		add("docker:i_v/engine", "engine", "Docker", "endpoint", "node://media", "hostname", "media", "cores", 4, "mem_gb", 7.6).
		run()
	mustLink(t, r, "docker:i_d/engine", RunsOn, "proxmox:i_p/lxc/3696", Strong, "hostname")
	mustLink(t, r, "docker:i_v/engine", RunsOn, "proxmox:i_p/qemu/102", Strong, "hostname") // a VM's kernel keeps some memory

	// The name alone, or a different size, is only a suggestion.
	r = guests().add("docker:i_d/engine", "engine", "Docker", "endpoint", "node://local", "hostname", "chadash-sandbox", "cores", 8, "mem_gb", 2.0).run()
	noLink(t, r, "docker:i_d/engine", RunsOn, "proxmox:i_p/lxc/3696")
	if !hasSuggestion(r, "docker:i_d/engine", RunsOn, "proxmox:i_p/lxc/3696") {
		t.Errorf("a name with another size should be suggested: %s", dump(r.Links))
	}
	r = guests().add("docker:i_d/engine", "engine", "Docker", "endpoint", "node://local", "hostname", "chadash-sandbox").run()
	noLink(t, r, "docker:i_d/engine", RunsOn, "proxmox:i_p/lxc/3696")

	// Two guests with the name and size: neither is sure.
	r = guests().
		add("proxmox:i_q/lxc/200", "lxc", "chadash-sandbox", "vmid", 200, "cores", 2, "mem_gb", 2.0).
		add("docker:i_d/engine", "engine", "Docker", "endpoint", "node://local", "hostname", "chadash-sandbox", "cores", 2, "mem_gb", 2.0).
		run()
	noLink(t, r, "docker:i_d/engine", RunsOn, "proxmox:i_p/lxc/3696")
	noLink(t, r, "docker:i_d/engine", RunsOn, "proxmox:i_q/lxc/200")
}

func TestStoragesJoinDatastores(t *testing.T) {
	h := newHome(t).
		add("pbs:i_b/app", "app", "PBS", "type", "pbs", "links", []any{map[string]any{"title": "PBS", "url": "https://10.0.11.120:8007/"}}).
		add("pbs:i_b/datastore/mars", "backup_target", "mars", "store", "mars").rel("pbs:i_b/datastore/mars", "part_of", "pbs:i_b/app").
		add("pbs:i_b/datastore/pluto", "backup_target", "pluto", "store", "pluto").rel("pbs:i_b/datastore/pluto", "part_of", "pbs:i_b/app").
		add("proxmox:i_m/storage/pbs-mars", "storage", "pbs-mars", "pbs.server", "10.0.11.120", "pbs.datastore", "mars").
		add("proxmox:i_p/storage/pbs-pluto", "storage", "pbs-pluto", "pbs.server", "jupiter.lab.example", "pbs.datastore", "pluto").
		add("proxmox:i_p/storage/pbs-gone", "storage", "pbs-gone", "pbs.server", "10.0.11.120", "pbs.datastore", "nope")
	r := h.run()
	mustLink(t, r, "proxmox:i_m/storage/pbs-mars", StoresOn, "pbs:i_b/datastore/mars", Exact, "pbs")
	// A server name the PBS instance isn't configured with: a suggestion.
	noLink(t, r, "proxmox:i_p/storage/pbs-pluto", StoresOn, "pbs:i_b/datastore/pluto")
	if !hasSuggestion(r, "proxmox:i_p/storage/pbs-pluto", StoresOn, "pbs:i_b/datastore/pluto") || len(r.Suggestions) != 1 {
		t.Fatalf("suggestions:\n%s", dump(r.Suggestions))
	}
}

func TestComposeDependencies(t *testing.T) {
	h := newHome(t)
	for _, c := range []struct{ name, svc, dep string }{
		{"immich_server", "immich-server", "database:service_started:false,redis:service_started:false"},
		{"immich_postgres", "database", ""}, {"immich_redis", "redis", ""},
	} {
		h.add("docker:i_d/container/"+c.name, "container", c.name, "label:com.docker.compose.project", "immich",
			"label:com.docker.compose.service", c.svc, "label:com.docker.compose.depends_on", c.dep)
	}
	h.add("docker:i_e/container/other_redis", "container", "other_redis", "label:com.docker.compose.project", "immich",
		"label:com.docker.compose.service", "redis")
	r := h.run()
	mustLink(t, r, "docker:i_d/container/immich_server", DependsOn, "docker:i_d/container/immich_postgres", Exact, "compose")
	mustLink(t, r, "docker:i_d/container/immich_server", DependsOn, "docker:i_d/container/immich_redis", Exact, "compose")
	noLink(t, r, "docker:i_d/container/immich_server", DependsOn, "docker:i_e/container/other_redis")
}

func TestOwnRelationsAreIgnored(t *testing.T) {
	h := jupiter(t)
	first := h.run()
	for _, l := range first.Links {
		rel := l.relation()
		h.doc.Relations[rel.Key()] = &rel
	}
	// A stale link of ours that no rule supports any more.
	stale := graph.Relation{From: "docker:i_d/container/sonarr", Type: RunsOn, To: "unraid:i_u/host", Source: Source}
	h.doc.Relations[stale.Key()] = &stale
	if again := h.run(); !slices.Equal(again.Links, first.Links) {
		t.Fatalf("the result changed when fed its own links:\n%s\nvs\n%s", dump(again.Links), dump(first.Links))
	}
}

func TestReaders(t *testing.T) {
	h := jupiter(t)
	for _, l := range h.run().Links {
		rel := l.relation()
		h.doc.Relations[rel.Key()] = &rel
	}
	if got := Canonical(h.doc, "unraid:i_u/container/sonarr"); got != "docker:i_d/container/sonarr" {
		t.Fatalf("Canonical = %s", got)
	}
	if got := Members(h.doc, "unraid:i_u/container/sonarr"); !slices.Equal(got, []graph.RID{"docker:i_d/container/sonarr", "unraid:i_u/container/sonarr"}) {
		t.Fatalf("Members = %v", got)
	}
	var names []string
	for _, r := range Distinct(h.doc, "container") {
		names = append(names, string(r.ID))
	}
	if !slices.Equal(names, []string{"docker:i_d/container/radarr", "docker:i_d/container/sonarr"}) {
		t.Fatalf("Distinct = %v", names)
	}
}

func TestKeys(t *testing.T) {
	for in, want := range map[string]string{
		"https://Sonarr.LAB.example./": "sonarr.lab.example:443",
		"http://10.0.11.120:8989/x/":   "10.0.11.120:8989/x",
		"http://[fd00::1]/":            "[fd00::1]:80",
	} {
		u, ok := parseURL(in)
		if !ok || u.key() != want {
			t.Errorf("parseURL(%q) = %q, %v; want %q", in, u.key(), ok, want)
		}
	}
	for _, bad := range []string{"ftp://x", "http://user:pass@x", "x", "http://x:99999"} {
		if _, ok := parseURL(bad); ok {
			t.Errorf("parseURL(%q) accepted", bad)
		}
	}
	for in, want := range map[string]binding{
		"18989->8989/tcp":          {"", 18989, 8989, "tcp"},
		"10.0.11.120:8080->80/tcp": {"10.0.11.120", 8080, 80, "tcp"},
		"[::1]:53->53/udp":         {"::1", 53, 53, "udp"},
	} {
		if b, ok := parseBinding(in); !ok || b != want {
			t.Errorf("parseBinding(%q) = %+v, %v", in, b, ok)
		}
	}
	for host, want := range map[string]bool{"10.0.11.1": true, "127.0.0.1": false, "::1": false, "0.0.0.0": false, "169.254.1.1": false, "fd00::1": true, "jupiter": false} {
		if routable(host) != want {
			t.Errorf("routable(%q) = %v", host, !want)
		}
	}
	u, _ := parseURL("https://jupiter.lab.example/sonarr/api")
	p, _ := parseURL("https://jupiter.lab.example/sonarr")
	q, _ := parseURL("https://jupiter.lab.example/son")
	if !u.under(p) || u.under(q) {
		t.Error("under should respect path segments")
	}
}
