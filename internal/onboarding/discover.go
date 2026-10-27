package onboarding

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
)

// Discovery limits. A request names at most a handful of targets, so it can
// never become a scan of a network.
const (
	MaxHosts        = 8
	MaxDocker       = 8
	probeTimeout    = 3 * time.Second  // one GET
	discoverTimeout = 20 * time.Second // the whole request
	probeParallel   = 8
	probeBodyBytes  = 64 << 10
)

// DiscoverRequest says what to look at. Every target is one the owner
// confirmed in the wizard, except the sidecar, which is the shipped
// compose.yaml's own socket proxy.
type DiscoverRequest struct {
	// Sidecar probes the compose sidecar's socket proxy (SidecarEndpoint).
	Sidecar bool `json:"sidecar"`
	// Docker endpoints to test: tcp://host:port or unix:///path.
	Docker []string `json:"docker"`
	// Hosts to check on the well-known ports of the integrations with a
	// discover hint in their recipe, and Docker's 2375. Names or addresses,
	// one machine each: no ranges, no subnets.
	Hosts []string `json:"hosts"`
}

// Candidate is something that answered, with the config to add it.
type Candidate struct {
	Integration string         `json:"integration"`
	Name        string         `json:"name"` // a suggested instance name
	Config      map[string]any `json:"config"`
	// Source is sidecar, docker or host; Target is what was checked.
	Source string `json:"source"`
	Target string `json:"target"`
	// Ready means it needs no credential and can be added as it is.
	Ready   bool   `json:"ready"`
	Version string `json:"version,omitempty"`
	// Added is the instance that already reads it, if one does.
	Added    string                `json:"added,omitempty"`
	Grants   []integration.Grant   `json:"grants"`
	Warnings []integration.Warning `json:"warnings"`
}

// Checked is one target and what came of it.
type Checked struct {
	Target string `json:"target"`
	Source string `json:"source"`
	Found  int    `json:"found"`
	// Error says, in words, why a Docker endpoint didn't answer.
	Error string `json:"error,omitempty"`
}

// DiscoverResult is POST /api/onboarding/discover's answer.
type DiscoverResult struct {
	Found   []Candidate `json:"found"`
	Checked []Checked   `json:"checked"`
}

var hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// validHost accepts one machine: a DNS name or an IP address, without a
// port, a scheme, a zone or a prefix length.
func validHost(h string) bool {
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Zone() == ""
	}
	// A name's last label has a letter (no top-level domain is all digits),
	// so "198.51.100.1-50" reads as the range it is, not as a name.
	last := h[strings.LastIndexByte(h, '.')+1:]
	return len(h) <= 253 && hostRE.MatchString(h) && strings.ContainsFunc(last, func(r rune) bool { return r < 128 && unicode.IsLetter(r) })
}

// clean trims, drops blanks and duplicates, and checks the limits.
func (req DiscoverRequest) clean() (DiscoverRequest, error) {
	norm := func(list []string, max int, what string, ok func(string) bool) ([]string, error) {
		var out []string
		for _, v := range list {
			v = strings.TrimSpace(v)
			if v == "" || slices.Contains(out, v) {
				continue
			}
			if !ok(v) {
				return nil, fmt.Errorf("%w: %q isn't %s", ErrInvalid, v, what)
			}
			out = append(out, v)
		}
		if len(out) > max {
			return nil, fmt.Errorf("%w: at most %d at a time", ErrInvalid, max)
		}
		return out, nil
	}
	var err error
	out := DiscoverRequest{Sidecar: req.Sidecar}
	if out.Hosts, err = norm(req.Hosts, MaxHosts, "a host name or an address", validHost); err != nil {
		return out, err
	}
	out.Docker, err = norm(req.Docker, MaxDocker, "a Docker endpoint (tcp://host:port or unix:///path)", func(v string) bool {
		return strings.HasPrefix(v, "tcp://") || strings.HasPrefix(v, "unix:///")
	})
	return out, err
}

// Discover looks at what req names and reports what answered.
func (s *Service) Discover(ctx context.Context, req DiscoverRequest) (DiscoverResult, error) {
	req, err := req.clean()
	if err != nil {
		return DiscoverResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()

	var (
		mu  sync.Mutex
		res = DiscoverResult{Found: []Candidate{}, Checked: []Checked{}}
		wg  sync.WaitGroup
		sem = make(chan struct{}, probeParallel)
	)
	record := func(c Checked, found ...Candidate) {
		mu.Lock()
		defer mu.Unlock()
		c.Found = len(found)
		res.Checked = append(res.Checked, c)
		res.Found = append(res.Found, found...)
	}
	docker := func(source, endpoint, target string) {
		defer wg.Done()
		sem <- struct{}{}
		defer func() { <-sem }()
		c, why := s.testDocker(ctx, endpoint)
		if c == nil {
			record(Checked{Target: target, Source: source, Error: why})
			return
		}
		c.Source, c.Target = source, target
		record(Checked{Target: target, Source: source}, *c)
	}
	if req.Sidecar {
		wg.Add(1)
		go docker("sidecar", s.o.Sidecar, s.o.Sidecar)
	}
	for _, ep := range req.Docker {
		wg.Add(1)
		go docker("docker", ep, ep)
	}
	for _, h := range req.Hosts {
		wg.Go(func() {
			var found []Candidate
			var fmu sync.Mutex
			var hw sync.WaitGroup
			for _, p := range s.probes(h) {
				hw.Go(func() {
					sem <- struct{}{}
					defer func() { <-sem }()
					if s.matches(ctx, p.url, p.hint.Match) {
						fmu.Lock()
						found = append(found, p.candidate)
						fmu.Unlock()
					}
				})
			}
			// Docker's raw API, or a socket proxy, on its usual port.
			hw.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				if c, _ := s.testDocker(ctx, "tcp://"+net.JoinHostPort(h, "2375")); c != nil {
					fmu.Lock()
					found = append(found, *c)
					fmu.Unlock()
				}
			})
			hw.Wait()
			for i := range found {
				found[i].Source, found[i].Target = "host", h
			}
			record(Checked{Target: h, Source: "host"}, found...)
		})
	}
	wg.Wait()

	s.annotate(res.Found)
	slices.SortStableFunc(res.Found, func(a, b Candidate) int {
		return cmpKeys(sourceRank(a.Source), sourceRank(b.Source), a.Target, b.Target, a.Integration, b.Integration, configURL(a.Config), configURL(b.Config))
	})
	slices.SortStableFunc(res.Checked, func(a, b Checked) int {
		return cmpKeys(sourceRank(a.Source), sourceRank(b.Source), a.Target, b.Target, "", "", "", "")
	})
	return res, nil
}

func sourceRank(s string) string {
	switch s {
	case "sidecar":
		return "0"
	case "docker":
		return "1"
	}
	return "2"
}

func cmpKeys(a1, b1, a2, b2, a3, b3, a4, b4 string) int {
	for _, p := range [][2]string{{a1, b1}, {a2, b2}, {a3, b3}, {a4, b4}} {
		if c := strings.Compare(p[0], p[1]); c != 0 {
			return c
		}
	}
	return 0
}

// testDocker runs the Docker integration's own connection test against an
// endpoint: GET requests only (the version, the three-tier probe, the
// container list). It returns a candidate, or why there's none.
func (s *Service) testDocker(ctx context.Context, endpoint string) (*Candidate, string) {
	res, err := s.o.Integrations.Test(ctx, runtime.TestRequest{Integration: "docker", Config: map[string]any{"endpoint": endpoint}})
	var ve *runtime.ValidationError
	switch {
	case errors.As(err, &ve):
		return nil, ve.Error()
	case errors.Is(err, runtime.ErrUnknownIntegration):
		return nil, "this hub has no Docker integration"
	case err != nil:
		return nil, "the test couldn't run"
	case !res.OK:
		switch res.ErrorCode {
		case integration.CodeUnreachable:
			return nil, "nothing answered there"
		case integration.CodeTimeout:
			return nil, "no answer in time"
		}
		return nil, res.ErrorMessage
	}
	c := &Candidate{
		Integration: "docker", Name: "Docker", Config: map[string]any{"endpoint": endpoint},
		Ready: true, Version: res.Version, Grants: res.Grants, Warnings: res.Warnings,
	}
	if c.Grants == nil {
		c.Grants = []integration.Grant{}
	}
	if c.Warnings == nil {
		c.Warnings = []integration.Warning{}
	}
	if u, err := url.Parse(endpoint); err == nil && u.Hostname() != "" && endpoint != s.o.Sidecar {
		c.Name = "Docker on " + u.Hostname()
	}
	return c, ""
}

// probe is one GET that recognises a service on a host.
type probe struct {
	url       string
	hint      integration.DiscoverHint
	candidate Candidate
}

// probes lists the GETs for a host: one per discover hint in the
// registered recipes.
func (s *Service) probes(host string) []probe {
	var out []probe
	for _, m := range s.o.Registry.Manifests() {
		r, ok := s.o.Registry.Recipe(m.ID)
		if !ok {
			continue
		}
		for _, h := range r.Discover {
			origin := originOf(h.Scheme, host, h.Port)
			cfg := map[string]any{"url": origin}
			if h.Scheme == "https" {
				cfg["tls"] = map[string]any{"mode": string(integration.TLSTOFU)}
			}
			out = append(out, probe{url: origin + h.Path, hint: h, candidate: Candidate{
				Integration: m.ID, Name: m.Name, Config: cfg,
				Ready:  len(m.SecretFields()) == 0 && m.ConfigSchema.Property("url") != nil && !requiresMore(m),
				Grants: []integration.Grant{}, Warnings: []integration.Warning{},
			}})
		}
	}
	return out
}

// requiresMore reports whether the config needs more than url and tls.
func requiresMore(m integration.Manifest) bool {
	for _, k := range m.ConfigSchema.Required {
		if k != "url" && k != "tls" {
			return true
		}
	}
	return false
}

// originOf is scheme://host[:port], leaving out the scheme's default port.
func originOf(scheme, host string, port int) string {
	hp := net.JoinHostPort(host, strconv.Itoa(port))
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		hp = host
		if strings.Contains(host, ":") {
			hp = "[" + host + "]"
		}
	}
	return scheme + "://" + hp
}

// matches GETs u, without credentials, and reports whether the start of the
// body contains match (case-insensitively), whatever the status.
func (s *Service) matches(ctx context.Context, u, match string) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", runtime.UserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, probeBodyBytes))
	return strings.Contains(strings.ToLower(string(b)), strings.ToLower(match))
}

// probeClient is the host probes' client: short timeouts, no proxy from
// the environment, no keep-alives, redirects followed only within the
// origin. Certificates aren't verified: self-signed is the norm on a LAN
// (Proxmox's 8006), the request carries no credential, and the page it
// reads is only compared with a product name. The instance it proposes uses
// TLS mode tofu.
func probeClient(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Client {
	if dial == nil {
		dial = (&net.Dialer{Timeout: probeTimeout}).DialContext
	}
	tr := &http.Transport{
		DialContext:            dial,
		TLSClientConfig:        &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // G402: see above
		TLSHandshakeTimeout:    probeTimeout,
		ResponseHeaderTimeout:  probeTimeout,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   probeTimeout + time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// configURL is a candidate's url or endpoint, for comparing and sorting.
func configURL(cfg map[string]any) string {
	for _, k := range []string{"url", "endpoint"} {
		if v, ok := cfg[k].(string); ok {
			return v
		}
	}
	return ""
}

// sameTarget compares two URLs or endpoints, ignoring case in the scheme
// and host, a trailing slash, and a default port.
func sameTarget(a, b string) bool {
	norm := func(s string) string {
		u, err := url.Parse(strings.TrimRight(strings.TrimSpace(s), "/"))
		if err != nil {
			return s
		}
		host, port := strings.ToLower(u.Hostname()), u.Port()
		switch {
		case u.Scheme == "http" && port == "80", u.Scheme == "https" && port == "443", u.Scheme == "tcp" && port == "2375":
			port = ""
		}
		return strings.ToLower(u.Scheme) + "://" + host + ":" + port + u.Path
	}
	return norm(a) == norm(b)
}

// annotate marks candidates an instance already reads, and keeps suggested
// names clear of the instances' names.
func (s *Service) annotate(found []Candidate) {
	insts := s.o.Integrations.List()
	for i := range found {
		c := &found[i]
		names := map[string]bool{}
		for _, in := range insts {
			if in.Integration != c.Integration {
				continue
			}
			names[in.Name] = true
			if sameTarget(configURL(in.Config), configURL(c.Config)) {
				c.Added = in.ID
			}
		}
		if names[c.Name] && c.Source == "host" {
			c.Name += " on " + c.Target
		}
		for n := 2; names[c.Name]; n++ {
			c.Name = strings.TrimSuffix(c.Name, " "+strconv.Itoa(n-1)) + " " + strconv.Itoa(n)
		}
	}
}
