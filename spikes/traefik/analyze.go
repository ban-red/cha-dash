package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Shapes of the Traefik API (v3), only the fields the correlator needs.
type router struct {
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Status      string   `json:"status"`
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	EntryPoints []string `json:"entryPoints"`
	Middlewares []string `json:"middlewares"`
	Priority    int      `json:"priority"`
	TLS         *struct {
		CertResolver string `json:"certResolver"`
		Passthrough  bool   `json:"passthrough"`
	} `json:"tls"`
}

type service struct {
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	Type         string `json:"type"`
	LoadBalancer *struct {
		Servers []struct {
			URL     string `json:"url"`
			Address string `json:"address"`
		} `json:"servers"`
	} `json:"loadBalancer"`
	ServerStatus map[string]string `json:"serverStatus"`
	UsedBy       []string          `json:"usedBy"`
}

type middleware struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	UsedBy []string `json:"usedBy"`
}

type entrypoint struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Docker list shape (only what correlation needs).
type container struct {
	Names  []string
	Image  string
	State  string
	Labels map[string]string
	Ports  []struct {
		IP          string
		PrivatePort int
		PublicPort  int
		Type        string
	}
	HostConfig struct{ NetworkMode string }
}

var (
	reHost   = regexp.MustCompile("Host\\(`([^`]+)`\\)")
	reRegexp = regexp.MustCompile("HostRegexp\\(`([^`]+)`\\)")
	rePath   = regexp.MustCompile("(PathPrefix|Path)\\(`([^`]+)`\\)")
)

const jupiterIP = "10.0.11.120" // the Unraid host in every sanitized fixture

func analyze(fx, dockerDir string) error {
	var routers []router
	var services []service
	var mws []middleware
	var eps []entrypoint
	for f, dst := range map[string]any{"http-routers.json": &routers, "http-services.json": &services,
		"http-middlewares.json": &mws, "entrypoints.json": &eps} {
		if err := readJSON(filepath.Join(fx, f), dst); err != nil {
			return err
		}
	}
	svc := map[string]service{}
	for _, s := range services {
		svc[s.Name] = s
	}
	mwType := map[string]string{}
	for _, m := range mws {
		mwType[m.Name] = m.Type
	}
	epAddr := map[string]string{}
	for _, e := range eps {
		epAddr[e.Name] = e.Address
	}

	// Host ports on jupiter → container, from the Docker fixture's inspect
	// files: HostConfig.PortBindings covers stopped containers too (the list's
	// Ports is empty for them), and host-network containers bind their
	// exposed ports directly. A running container wins a port conflict.
	byPort := map[int]string{}
	running := map[int]bool{}
	images := map[string]string{}
	if dockerDir != "" {
		files, _ := filepath.Glob(filepath.Join(dockerDir, "inspect", "*.json"))
		for _, f := range files {
			var c struct {
				Name   string
				Config struct {
					Image        string
					ExposedPorts map[string]any
				}
				State      struct{ Status string }
				HostConfig struct {
					NetworkMode  string
					PortBindings map[string][]struct{ HostPort string }
				}
			}
			if err := readJSON(f, &c); err != nil {
				return err
			}
			name := strings.TrimPrefix(c.Name, "/")
			images[name] = c.Config.Image
			label := name
			if c.State.Status != "running" {
				label += " [" + c.State.Status + "]"
			}
			add := func(port int, l string) {
				if running[port] {
					return
				}
				byPort[port] = l
				running[port] = c.State.Status == "running"
			}
			for _, bs := range c.HostConfig.PortBindings {
				for _, b := range bs {
					if n, err := strconv.Atoi(b.HostPort); err == nil {
						add(n, label)
					}
				}
			}
			if c.HostConfig.NetworkMode == "host" {
				for p := range c.Config.ExposedPorts {
					if n, err := strconv.Atoi(strings.SplitN(p, "/", 2)[0]); err == nil {
						add(n, label+" (host network)")
					}
				}
			}
		}
	}

	type row struct {
		router, openURL, mws, backend, resolves, kind string
		path                                          bool
	}
	var rows []row
	counts := map[string]int{}
	for _, r := range routers {
		hosts := reHost.FindAllStringSubmatch(r.Rule, -1)
		paths := rePath.FindAllStringSubmatch(r.Rule, -1)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		open := ""
		if len(hosts) > 0 {
			open = scheme + "://" + hosts[0][1]
			if port := epPort(epAddr, r.EntryPoints, scheme); port != "" {
				open += ":" + port
			}
			if len(paths) > 0 && paths[0][2] != "/" {
				open += paths[0][2]
			}
		} else if m := reRegexp.FindStringSubmatch(r.Rule); m != nil {
			open = "(HostRegexp: no single URL)"
		}
		var mw []string
		for _, m := range r.Middlewares {
			t := mwType[m]
			switch t {
			case "basicauth", "forwardauth", "digestauth":
				mw = append(mw, "auth:"+strings.SplitN(m, "@", 2)[0])
			case "redirectscheme":
				mw = append(mw, "→https")
			case "stripprefix":
				mw = append(mw, "strip:"+strings.SplitN(m, "@", 2)[0])
			default:
				mw = append(mw, t)
			}
		}
		// A router names its service without a provider suffix when both
		// live in the same provider; qualify it the way Traefik does.
		if !strings.Contains(r.Service, "@") {
			r.Service += "@" + r.Provider
		}
		s := svc[r.Service]
		backend, resolves, kind := "", "", ""
		switch {
		case strings.HasSuffix(r.Service, "@internal"):
			backend, kind = r.Service, "internal"
		case s.LoadBalancer == nil || len(s.LoadBalancer.Servers) == 0:
			backend, kind = "(no servers)", "none"
		default:
			raw := s.LoadBalancer.Servers[0].URL
			backend = raw
			u, _ := url.Parse(raw)
			host, port := u.Hostname(), u.Port()
			if port == "" {
				port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
			}
			switch {
			case dockerBridge(host):
				kind, resolves = "docker", "container on Traefik's own Docker host (pvedocker), "+r.Provider+" provider"
			case host == jupiterIP:
				kind = "jupiter"
				n, _ := strconv.Atoi(port)
				if c, ok := byPort[n]; ok {
					resolves = "jupiter → " + c
					if img := images[strings.Fields(c)[0]]; img != "" {
						resolves += " (" + img + ")"
					}
				} else if n == 80 || n == 443 {
					resolves = "jupiter itself (Unraid web UI)"
				} else {
					resolves = "jupiter :" + port + " (no container binds it: a VM, or gone)"
				}
				if u.Path != "" && u.Path != "/" {
					resolves += ", backend path " + u.Path
				}
			case strings.HasPrefix(host, "10.0."):
				kind, resolves = "lan", "LAN host ."+host[strings.LastIndex(host, ".")+1:]
			default:
				kind, resolves = "name", "by name: "+host
			}
		}
		counts["provider:"+r.Provider]++
		counts["backend:"+kind]++
		if len(paths) > 0 && paths[0][2] != "/" {
			counts["path-based"]++
		}
		for _, m := range mw {
			if strings.HasPrefix(m, "auth:") {
				counts["with auth middleware"]++
			}
		}
		rows = append(rows, row{router: r.Name, openURL: open, mws: strings.Join(mw, " "), backend: backend, resolves: resolves, kind: kind,
			path: len(paths) > 0 && paths[0][2] != "/"})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].kind != rows[j].kind {
			return rows[i].kind < rows[j].kind
		}
		return rows[i].router < rows[j].router
	})

	fmt.Println("## Routers → services → backends")
	fmt.Println()
	fmt.Println("| Router | Open URL | Middlewares | Backend | Resolves to |")
	fmt.Println("|---|---|---|---|---|")
	for _, r := range rows {
		fmt.Printf("| `%s` | %s | %s | `%s` | %s |\n", r.router, r.openURL, r.mws, r.backend, r.resolves)
	}
	fmt.Println()
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("## Counts")
	for _, k := range keys {
		fmt.Printf("- %s: %d\n", k, counts[k])
	}
	var unusedSvc, unusedMw []string
	for _, s := range services {
		if len(s.UsedBy) == 0 && !strings.HasSuffix(s.Name, "@internal") {
			unusedSvc = append(unusedSvc, s.Name)
		}
	}
	for _, m := range mws {
		if len(m.UsedBy) == 0 {
			unusedMw = append(unusedMw, m.Name)
		}
	}
	fmt.Println("- services with no router:", unusedSvc)
	fmt.Println("- middlewares with no router:", unusedMw)
	return nil
}

// dockerBridge reports whether host is in the sanitizer's Docker bridge range
// (real 172.16.0.0/12 → 10.100.0.0–10.115.255.255).
func dockerBridge(host string) bool {
	a, err := netip.ParseAddr(host)
	if err != nil || !a.Is4() {
		return false
	}
	b := a.As4()
	return b[0] == 10 && b[1] >= 100 && b[1] <= 115
}

// epPort returns the port to put in an open URL when the router's entrypoint
// isn't the scheme's default port.
func epPort(addr map[string]string, eps []string, scheme string) string {
	for _, e := range eps {
		a := addr[e]
		_, p, _ := strings.Cut(a, ":")
		if p == "" || (scheme == "https" && p == "443") || (scheme == "http" && p == "80") {
			return ""
		}
		if e == "web" || e == "websecure" {
			return p
		}
	}
	return ""
}

func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
