// Package checks is the built-in uptime checks integration: HTTP and TCP
// probes that need no credentials. Each target becomes a check resource with
// an up/down health and a latency series, and a target that fails
// fail_after times in a row (3 by default) raises a problem in the inbox,
// resolved when it answers again. See README.md.
package checks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "checks"

// Limits and defaults.
const (
	DefaultInterval  = 60 * time.Second
	MinInterval      = 10 * time.Second
	DefaultFailAfter = 3
	MaxTargets       = 100
	maxCheckTimeout  = 10 * time.Second
	parallel         = 16
)

func init() { integration.Register(Integration{}) }

// Integration is the checks integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	tls := integration.TLSSchema().Describe("Certificate check for this target; defaults to the instance's.")
	target := integration.Object(
		integration.Prop("name", &integration.Schema{Type: "string", Title: "Name", MinLength: new(1), MaxLength: new(64)}),
		integration.Prop("kind", integration.Enum("Check", "http", "tcp").WithDefault("http")),
		integration.Prop("url", integration.URL("URL").Describe("http checks: the page to fetch.")),
		integration.Prop("host", &integration.Schema{Type: "string", Title: "Host", Format: "hostname", Description: "tcp checks: the host to connect to."}),
		integration.Prop("port", integration.Integer("Port", 1, 65535).Describe("tcp checks: the port to connect to.")),
		integration.Prop("expect_status", integration.Integer("Expected status", 100, 599).Describe("http checks: the status that means up. Default: any 2xx or 3xx.")),
		integration.Prop("interval_s", integration.Integer("Interval (seconds)", MinInterval.Seconds(), 86400).WithDefault(int(DefaultInterval.Seconds()))),
		integration.Prop("fail_after", integration.Integer("Failures before a problem", 1, 100).WithDefault(DefaultFailAfter)),
		integration.Prop("tls", tls),
	).Require("name", "kind")
	targets := integration.Array("Targets", target)
	targets.MinItems, targets.MaxItems = new(1), new(MaxTargets)
	return integration.Manifest{
		ID: ID, Name: "Checks", Icon: "checks", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"checks.uptime"},
		ConfigSchema: integration.Object(integration.Prop("targets", targets), integration.TLSProp()).Require("targets"),
		Poll:         integration.Poll{Interval: DefaultInterval, Background: true, Timeout: 90 * time.Second},
	}
}

// target is one entry of the targets config key.
type target struct {
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	URL          string         `json:"url"`
	Host         string         `json:"host"`
	Port         int            `json:"port"`
	ExpectStatus int            `json:"expect_status"`
	IntervalS    int            `json:"interval_s"`
	FailAfter    int            `json:"fail_after"`
	TLS          map[string]any `json:"tls"`
}

type config struct {
	Targets []target `json:"targets"`
}

// ValidateConfig implements integration.ConfigValidator: what the schema
// can't say about targets.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return []integration.FieldError{{Path: "/targets", Message: "must be a list of targets"}}
	}
	var errs []integration.FieldError
	seen := map[string]bool{}
	for i, t := range c.Targets {
		at := func(field, msg string) {
			errs = append(errs, integration.FieldError{Path: fmt.Sprintf("/targets/%d/%s", i, field), Message: msg})
		}
		switch t.Kind {
		case "http":
			switch {
			case t.URL == "":
				at("url", "is required for an http check")
			case !strings.HasPrefix(t.URL, "http://") && !strings.HasPrefix(t.URL, "https://"):
				at("url", "must start with http:// or https://")
			}
		case "tcp":
			if t.Host == "" {
				at("host", "is required for a tcp check")
			}
			if t.Port == 0 {
				at("port", "is required for a tcp check")
			}
		}
		if slug(t.Name) == "" {
			at("name", "needs a letter or a digit")
		}
		if seen[t.Name] {
			at("name", "is already used by another target")
		}
		seen[t.Name] = true
		if t.TLS != nil {
			if _, err := integration.ParseTLS(t.TLS); err != nil {
				at("tls", err.Error())
			}
		}
	}
	return errs
}

// slug turns a target name into the last part of its resource ID.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// Connect implements integration.Integration.
func (Integration) Connect(ctx context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return nil, &integration.ConfigError{Message: "targets: " + err.Error()}
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	in := &instance{d: d}
	slugs := map[string]bool{}
	for _, t := range c.Targets {
		// Names are unique, slugs might not be ("Sonarr" and "sonarr!"): the
		// later one gets a number.
		s := slug(t.Name)
		for n := 2; slugs[s]; n++ {
			s = slug(t.Name) + "-" + strconv.Itoa(n)
		}
		slugs[s] = true
		p := &probe{target: t, slug: s}
		p.rid = "check:" + key + "/" + p.slug
		p.interval = DefaultInterval
		if t.IntervalS > 0 {
			p.interval = max(time.Duration(t.IntervalS)*time.Second, MinInterval)
		}
		if p.FailAfter <= 0 {
			p.FailAfter = DefaultFailAfter
		}
		p.timeout = min(maxCheckTimeout, p.interval)
		if t.Kind == "http" {
			client, err := in.client(cfg, t, p.timeout)
			if err != nil {
				return nil, err
			}
			p.client = client
		}
		in.probes = append(in.probes, p)
	}
	return in, nil
}

// client builds the http client for one target: its own tls policy, or the
// instance's. Redirects aren't followed: a 302 to a login page means up.
// With tofu and no pin, the pin is recorded on the target, so targets with
// different certificates each get their own.
func (in *instance) client(cfg integration.Config, t target, timeout time.Duration) (*http.Client, error) {
	pol := cfg.TLS()
	if t.TLS != nil {
		p, err := integration.ParseTLS(t.TLS)
		if err != nil {
			return nil, &integration.ConfigError{Message: t.Name + ": " + err.Error()}
		}
		pol = p
	}
	opts := integration.HTTPOptions{TLS: pol, Redirects: integration.NoRedirects, Timeout: timeout}
	if pol.Mode == integration.TLSTOFU && pol.Pin == "" {
		name := t.Name
		opts.OnPin = func(pin string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := in.d.UpdateConfig(ctx, func(c map[string]any) { setTargetPin(c, name, pin) })
			if err != nil {
				in.d.Log.Warn("couldn't save the pinned certificate", "target", name, "err", err)
			}
		}
	}
	return in.d.NewHTTPClient(opts)
}

// setTargetPin records a TOFU pin on the named target of a stored config.
func setTargetPin(c map[string]any, name, pin string) {
	targets, _ := c["targets"].([]any)
	for _, x := range targets {
		t, ok := x.(map[string]any)
		if !ok || t["name"] != name {
			continue
		}
		tls, _ := t["tls"].(map[string]any)
		if tls == nil {
			tls = map[string]any{"mode": string(integration.TLSTOFU)}
		}
		tls["pin"] = pin
		t["tls"] = tls
		return
	}
}

type instance struct {
	d      integration.Deps
	probes []*probe
}

// probe is one target and its running state.
type probe struct {
	target
	slug     string
	rid      string
	interval time.Duration
	timeout  time.Duration
	client   *http.Client

	// Guarded by the single Sync goroutine; Test uses fresh probes.
	next      time.Time
	checked   bool
	failures  int
	firstFail time.Time
	last      result
	raised    bool
}

// result is one check.
type result struct {
	ok      bool
	status  int
	latency time.Duration
	code    string
	err     string
}

// Interval implements integration.Intervaler: the fastest target's.
func (in *instance) Interval() time.Duration {
	iv := time.Duration(0)
	for _, p := range in.probes {
		if iv == 0 || p.interval < iv {
			iv = p.interval
		}
	}
	return iv
}

// Close implements integration.Instance.
func (in *instance) Close() error {
	for _, p := range in.probes {
		if p.client != nil {
			p.client.CloseIdleConnections()
		}
	}
	return nil
}

// Test implements integration.Instance: every target once.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	start := time.Now()
	results := in.run(ctx, in.probes)
	var failed []string
	code := ""
	for i, r := range results {
		if !r.ok {
			failed = append(failed, in.probes[i].Name+": "+r.err)
			if code == "" {
				code = r.code
			}
		}
	}
	res := integration.TestResult{OK: len(failed) == 0, Latency: time.Since(start)}
	if len(failed) > 0 {
		res.ErrorCode = code
		res.ErrorMessage = fmt.Sprintf("%d of %d %s failed: %s", len(failed), len(results), plural(len(results), "check"), strings.Join(failed, "; "))
	}
	return res
}

func plural(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}

// Sync implements integration.Instance: run the targets that are due, then
// publish every target. A failing target isn't a failed sync.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	now := in.d.Now()
	var due []*probe
	for _, p := range in.probes {
		// A tenth of the interval of slack absorbs the coordinator's jitter.
		if p.next.IsZero() || !now.Before(p.next.Add(-p.interval/10)) {
			due = append(due, p)
		}
	}
	results := in.run(ctx, due)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for i, p := range due {
		p.apply(results[i], now)
	}
	return in.publish()
}

// run checks probes concurrently and returns their results in order.
func (in *instance) run(ctx context.Context, probes []*probe) []result {
	out := make([]result, len(probes))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = p.check(ctx)
		})
	}
	wg.Wait()
	return out
}

func (p *probe) check(ctx context.Context) result {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	start := time.Now()
	if p.Kind == "tcp" {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
		if err != nil {
			return failure(err)
		}
		latency := time.Since(start)
		conn.Close()
		return result{ok: true, latency: latency}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return failure(&integration.ConfigError{Message: "the url can't be fetched: " + err.Error()})
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return failure(err)
	}
	latency := time.Since(start)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	r := result{status: resp.StatusCode, latency: latency}
	switch {
	case p.ExpectStatus != 0 && resp.StatusCode != p.ExpectStatus:
		r.code, r.err = integration.CodeUpstream, fmt.Sprintf("answered %s (expected %d)", statusText(resp.StatusCode), p.ExpectStatus)
	case p.ExpectStatus == 0 && resp.StatusCode >= 400:
		r.code, r.err = integration.CodeUpstream, "answered "+statusText(resp.StatusCode)
	default:
		r.ok = true
	}
	return r
}

func statusText(code int) string {
	if t := http.StatusText(code); t != "" {
		return strconv.Itoa(code) + " " + t
	}
	return strconv.Itoa(code)
}

func failure(err error) result {
	code, _, msg := integration.Classify(err)
	if errors.Is(err, context.DeadlineExceeded) {
		code, msg = integration.CodeTimeout, "didn't answer in time"
	}
	return result{code: code, err: msg}
}

func (p *probe) apply(r result, now time.Time) {
	p.checked = true
	p.next = now.Add(p.interval)
	p.last = r
	if r.ok {
		p.failures = 0
		p.firstFail = time.Time{}
		return
	}
	if p.failures == 0 {
		p.firstFail = now
	}
	p.failures++
}

func (p *probe) down() bool { return p.failures >= p.FailAfter }

func (p *probe) where() string {
	if p.Kind == "tcp" {
		return net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	}
	return p.URL
}

// publish emits every target's facts and raises or resolves its problem.
func (in *instance) publish() error {
	self := in.d.Emit.Self()
	var f integration.Facts
	for _, p := range in.probes {
		r := integration.Resource{
			ID: p.rid, Kind: "check", Name: p.Name, Caps: []string{"checks.uptime"},
			Attrs: map[string]any{"check": p.Kind, "target": p.where()},
		}
		if p.Kind == "http" {
			r.Links = []integration.Link{{Title: p.Name, URL: p.URL}}
			if p.last.status != 0 {
				r.Attrs["status_code"] = p.last.status
			}
		}
		switch {
		case !p.checked:
			r.State, r.Health.Status = "unknown", integration.HealthUnknown
		case p.failures == 0:
			r.State, r.Health.Status = "online", integration.HealthOK
		case !p.down():
			r.State, r.Health.Status = "error", integration.HealthAttention
			r.Health.Reason = p.last.err + "; checking again"
		default:
			r.State, r.Health.Status, r.Health.Reason = "error", integration.HealthDown, p.last.err
			r.Health.Since = p.firstFail
		}
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations, integration.Relation{From: self, Type: "monitors", To: p.rid})
		m := integration.Metric{Resource: p.rid, Unit: "ms", Min: 1, Max: 5000, Value: integration.Unknown()}
		if p.Kind == "tcp" {
			m.Key, m.Band, m.Warn, m.Crit = "tcp.ms", [2]float64{1, 50}, 200, 1000
		} else {
			m.Key, m.Band, m.Warn, m.Crit = "http.ms", [2]float64{1, 300}, 800, 2000
		}
		if p.checked && p.last.ok {
			m.Value = float64(p.last.latency.Microseconds()) / 1000
		}
		f.Metrics = append(f.Metrics, m)
	}
	if err := in.d.Emit.Replace("", f); err != nil {
		return err
	}
	for _, p := range in.probes {
		switch {
		case p.down():
			if err := in.d.Emit.Raise(p.incident(self)); err != nil {
				return err
			}
			p.raised = true
		case p.raised:
			msg := fmt.Sprintf("%s answered again at %s.", p.Name, hhmm(in.d.Now()))
			if err := in.d.Emit.Resolve("check."+p.slug, msg); err != nil {
				return err
			}
			p.raised = false
		}
	}
	return nil
}

func hhmm(t time.Time) string { return t.Local().Format("15:04") }

// incident is the problem for a target that's down. Its fields stay the same
// while it's down, so raising it again on each check publishes nothing.
func (p *probe) incident(self string) integration.Incident {
	kind := strings.ToUpper(p.Kind)
	facts := map[string]any{"check": p.Kind, "target": p.where(), "error": p.last.err}
	if p.last.status != 0 {
		facts["status_code"] = p.last.status
	}
	in := integration.Incident{
		ID: "check." + p.slug, Scenario: "check-failed", Class: integration.ClassProblem,
		Severity: integration.SeverityDown,
		Title:    p.Name + " isn't answering",
		Summary:  fmt.Sprintf("%s has failed its %s check since %s: %s.", p.Name, kind, hhmm(p.firstFail), p.last.err),
		Target:   p.rid, OpenedAt: p.firstFail, Facts: facts,
		Causes:       []integration.Cause{{Text: p.last.err, Resource: p.rid, At: p.firstFail.UTC().Truncate(time.Second)}},
		ResolvesWhen: fmt.Sprintf("Resolves when %s answers again.", p.Name),
	}
	if p.Kind == "http" {
		in.Suggested = integration.Action{ID: "link.open", Label: "Open " + p.Name, Tier: "T0", Target: p.rid}
		in.Alternatives = []integration.Action{{ID: "integration.settings", Label: "Review the check", Tier: "T0", Target: self}}
	} else {
		in.Suggested = integration.Action{ID: "integration.settings", Label: "Review the check", Tier: "T0", Target: self}
	}
	return in
}
