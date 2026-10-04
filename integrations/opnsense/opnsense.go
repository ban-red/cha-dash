// Package opnsense reads an OPNsense router's WAN: its link state and
// throughput, live over the traffic stream (SSE) while someone watches and
// from a light counter poll otherwise, and 30-day totals the hub
// accumulates from the counters. See README.md.
package opnsense

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "opnsense"

// The poll plan (spike 4.5): the counters every minute, watched or not
// (6 kB, about 0.1 s on the router), and the traffic stream at a 5 s
// interval only while someone watches.
const (
	pollInterval   = time.Minute
	pollTimeout    = 20 * time.Second
	streamInterval = 5 * time.Second
	streamPath     = "/api/diagnostics/traffic/stream/5"
	countersPath   = "/api/diagnostics/traffic/interface"
	menuPath       = "/api/core/menu/tree"
)

// Stream timing, from the captures (traffic_stream_5s_timing.json): the
// first event comes 0.1–0.2 s after connecting, then one every 5.00–5.05 s.
const (
	// firstEventWait bounds the wait for the first event (headers included).
	firstEventWait = 15 * time.Second
	// eventGapWait is three intervals: a stream that misses that many is
	// stalled, and is reopened.
	eventGapWait = 3 * streamInterval
	// streamWindow is how long one stream stays open before it's reopened,
	// so no response grows past the client's limits (about 0.7 MB an hour).
	streamWindow = 10 * time.Minute
	// liveFresh is how recent a stream sample must be for the counter poll
	// to leave the rate to the stream.
	liveFresh = 2*streamInterval + 5*time.Second
)

// CodeMissingPermission is the error code for a privilege the API user lacks.
const CodeMissingPermission = "missing_permission"

func init() { integration.Register(Integration{}) }

// Integration is the OPNsense integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	wan := integration.String("WAN interface").WithDefault("wan").
		Describe("The OPNsense interface to report as the WAN, by its identifier (wan, opt1, …).")
	wan.Pattern, wan.MaxLength = `^[a-z][a-z0-9_]*$`, intp(32)
	return integration.Manifest{
		ID: ID, Name: "OPNsense", Icon: "opnsense", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"net.router", "net.wan"},
		ConfigSchema: integration.Object(
			integration.URLProp(), integration.TLSProp(),
			integration.Prop("key", integration.Secret("API key")),
			integration.Prop("secret", integration.Secret("API secret")),
			integration.Prop("wan", wan),
		).Require("url", "key", "secret"),
		Poll: integration.Poll{Interval: pollInterval, Idle: pollInterval, Timeout: pollTimeout},
	}
}

func intp(n int) *int { return &n }

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if _, err := baseOf(cfg.String("url")); err != nil {
		return []integration.FieldError{{Path: "/url", Message: err.Error()}}
	}
	return nil
}

func baseOf(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("must be the router's http:// or https:// address")
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// Connect implements integration.Integration. It does no I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	base, err := baseOf(cfg.String("url"))
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "url " + err.Error()}
	}
	// The stream outlives the default request timeout; its own client lets
	// one window run, and the watchdog catches a stalled one sooner.
	stream, err := d.NewHTTPClient(integration.HTTPOptions{Timeout: streamWindow + time.Minute})
	if err != nil {
		return nil, err
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	in := &instance{
		d: d, name: cfg.Name, prefix: ID + ":" + key + "/", base: base.String(),
		key: cfg.String("key"), secret: cfg.String("secret"), wan: cfg.String("wan"),
		stream: stream, window: streamWindow, firstWait: firstEventWait, gapWait: eventGapWait,
	}
	if in.name == "" {
		in.name = "OPNsense"
	}
	if in.wan == "" {
		in.wan = "wan"
	}
	if ip := net.ParseIP(base.Hostname()); ip != nil {
		in.ip = ip.String()
	}
	return in, nil
}

type instance struct {
	d           integration.Deps
	name        string
	prefix      string // "opnsense:<instance id>/"
	base        string
	key, secret string
	wan         string // the WAN's interface identifier
	ip          string // the URL's host when it's an address
	stream      *http.Client

	// Stream timing; tests shorten it.
	window, firstWait, gapWait time.Duration

	live atomic.Int64 // hub time (Unix ns) of the last stream sample

	mu      sync.Mutex // Sync and Close
	prev    *counters  // the last counter read, for the polled rate
	totals  *totals    // nil until loaded
	savedAt time.Time
	dirty   bool
}

func (in *instance) id(path string) string { return in.prefix + path }

// get reads one API path into dst with HTTP basic auth (key and secret).
// A 403 is a missing privilege, named by need.
func (in *instance) get(ctx context.Context, path, need string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.base+path, nil)
	if err != nil {
		return &integration.ConfigError{Field: "url", Message: "the url can't be used: " + err.Error()}
	}
	req.SetBasicAuth(in.key, in.secret)
	req.Header.Set("Accept", "application/json")
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := in.check(resp, need); err != nil {
		return err
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return in.notOPNsense(path, "the answer isn't the JSON it expects")
	}
	return nil
}

// check maps a response's status: 401 is a rejected key or secret, 403 a
// privilege the API user lacks, 404 no OPNsense API.
func (in *instance) check(resp *http.Response, need string) error {
	switch resp.StatusCode {
	case http.StatusForbidden:
		return &integration.RepairError{Code: CodeMissingPermission, Field: "key",
			Message: fmt.Sprintf("The API key's user lacks the %s privilege. Add it in OPNsense under System → Access → Users (or the user's group), then Cha Dash reconnects on its own.", need)}
	case http.StatusNotFound:
		return in.notOPNsense(resp.Request.URL.Path, "404 Not Found")
	}
	return integration.CheckResponse(resp)
}

func (in *instance) notOPNsense(path, detail string) error {
	return &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
		Message: fmt.Sprintf("%s%s doesn't answer like OPNsense's API (%s).", in.base, path, detail)}
}

// Close implements integration.Instance: it saves the totals it hasn't yet.
func (in *instance) Close() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.dirty {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		in.save(ctx, in.d.Now())
	}
	return nil
}

// ifaceCounters is one interface of traffic/interface. Every value is a
// string, and the keys have spaces.
type ifaceCounters struct {
	Name     string `json:"name"` // the description, "WAN"
	Device   string `json:"device"`
	Link     string `json:"link state"` // "2" is up
	BytesIn  string `json:"bytes received"`
	BytesOut string `json:"bytes transmitted"`
}

type trafficInterface struct {
	Interfaces map[string]ifaceCounters `json:"interfaces"`
	Time       float64                  `json:"time"` // the router's clock, Unix seconds
}

// counters is one read of the WAN's byte counters (since the interface
// attached, which is about boot).
type counters struct {
	rx, tx uint64
	time   float64 // the router's clock
}

// Sync implements integration.Instance: read the counters, publish the
// router and the WAN, the rate when the stream isn't live, and the 30-day
// totals.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	var t trafficInterface
	if err := in.get(ctx, countersPath, "Reporting: Traffic", &t); err != nil {
		return err
	}
	w, ok := t.Interfaces[in.wan]
	if !ok {
		return &integration.ConfigError{Field: "wan", Message: fmt.Sprintf("OPNsense has no interface %q; it has %s",
			in.wan, strings.Join(slices.Sorted(maps.Keys(t.Interfaces)), ", "))}
	}
	cur, cerr := parseCounters(w, t.Time)
	now := in.d.Now()

	in.mu.Lock()
	defer in.mu.Unlock()
	in.load(ctx)
	rxRate, txRate := integration.Unknown(), integration.Unknown()
	if cerr == nil {
		if in.prev != nil {
			rxRate, txRate = rate(in.prev, cur)
		}
		in.prev = cur
		if in.totals != nil {
			in.totals.add(now, in.wan, cur)
			in.dirty = true
		}
	} else {
		in.d.Log.Debug("the WAN counters aren't numbers", "err", cerr)
	}

	if err := in.d.Emit.Replace("", in.facts(len(t.Interfaces), w, now)); err != nil {
		return err
	}
	// While the stream is live it owns the rate; the minute's average
	// would only blur it.
	if now.Sub(time.Unix(0, in.live.Load())) > liveFresh {
		if err := in.d.Emit.Replace(scopeRate, integration.Facts{Metrics: in.rates(rxRate, txRate)}); err != nil {
			return err
		}
	}
	if in.dirty && (now.Sub(in.savedAt) >= saveEvery || dayOf(now) != dayOf(in.savedAt)) {
		in.save(ctx, now)
	}
	return nil
}

const scopeRate = "rate"

func parseCounters(w ifaceCounters, at float64) (*counters, error) {
	var c counters
	if _, err := fmt.Sscan(w.BytesIn, &c.rx); err != nil {
		return nil, fmt.Errorf("bytes received %q: %w", w.BytesIn, err)
	}
	if _, err := fmt.Sscan(w.BytesOut, &c.tx); err != nil {
		return nil, fmt.Errorf("bytes transmitted %q: %w", w.BytesOut, err)
	}
	c.time = at
	return &c, nil
}

// rate is the average throughput between two counter reads, in Mb/s, by the
// router's clock. A counter that went down (a reboot) or a gap of more than
// ten minutes gives unknown.
func rate(a, b *counters) (rx, tx float64) {
	dt := b.time - a.time
	if dt <= 0 || dt > 600 || b.rx < a.rx || b.tx < a.tx {
		return integration.Unknown(), integration.Unknown()
	}
	return mbps(float64(b.rx-a.rx), dt), mbps(float64(b.tx-a.tx), dt)
}

func mbps(bytes, seconds float64) float64 {
	return roundTo(bytes*8/seconds/1e6, 2)
}

// facts are the router, the WAN, and the WAN's 30-day totals.
func (in *instance) facts(interfaces int, w ifaceCounters, now time.Time) integration.Facts {
	router, wan := in.id("router"), in.id("wan")
	r := integration.Resource{ID: router, Kind: "router", Name: in.name, State: "running",
		Health: integration.Health{Status: integration.HealthOK},
		Labels: map[string]string{"chadash.role": "network-core"},
		Attrs:  map[string]any{"platform": "opnsense", "interfaces": interfaces},
		Links:  []integration.Link{{Title: "OPNsense", URL: in.base}},
		Caps:   []string{"net.router", "net.wan"},
	}
	if in.ip != "" {
		r.Attrs["ip"] = in.ip
	}
	wr := integration.Resource{ID: wan, Kind: "wan", Name: w.Name, Attrs: map[string]any{"interface": in.wan}}
	if wr.Name == "" {
		wr.Name = "WAN"
	}
	if w.Device != "" {
		wr.Attrs["device"] = w.Device
	}
	switch w.Link {
	case "2":
		wr.State, wr.Health = "online", integration.Health{Status: integration.HealthOK}
	case "1":
		wr.State, wr.Health = "error", integration.Health{Status: integration.HealthDown, Reason: "The WAN link is down"}
	default:
		wr.State, wr.Health = "unknown", integration.Health{Status: integration.HealthUnknown, Reason: "OPNsense doesn't know the WAN's link state"}
	}
	rx30, tx30 := integration.Unknown(), integration.Unknown()
	if t := in.totals; t != nil && t.counting() {
		rx, tx := t.sum(now)
		rx30, tx30 = roundTo(rx/1e9, 2), roundTo(tx/1e9, 2)
		if now.Sub(t.Since) < totalsWindow {
			wr.Attrs["totals_since"] = t.Since.UTC().Format(time.RFC3339)
		}
	}
	return integration.Facts{
		Resources: []integration.Resource{r, wr},
		Relations: []integration.Relation{
			{From: in.d.Emit.Self(), Type: "monitors", To: router},
			{From: wan, Type: "part_of", To: router},
		},
		Metrics: []integration.Metric{
			{Resource: wan, Key: "rx.gb_30d", Unit: "GB", Min: 0, Max: 100000, Band: [2]float64{600, 1100}, Value: rx30},
			{Resource: wan, Key: "tx.gb_30d", Unit: "GB", Min: 0, Max: 100000, Band: [2]float64{300, 600}, Value: tx30},
		},
	}
}

// rates are the WAN's throughput series, the Demo Home's scales.
func (in *instance) rates(rx, tx float64) []integration.Metric {
	wan := in.id("wan")
	return []integration.Metric{
		{Resource: wan, Key: "rx.mbps", Unit: "Mb/s", Min: 0, Max: 1000, Band: [2]float64{2, 60}, Value: rx},
		{Resource: wan, Key: "tx.mbps", Unit: "Mb/s", Min: 0, Max: 1000, Band: [2]float64{1, 20}, Value: tx},
	}
}

// Test implements integration.Instance: the grants from the ACL-filtered
// menu, then the counters, which need Reporting: Traffic.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	var res integration.TestResult
	var menu []menuItem
	if err := in.get(ctx, menuPath, "", &menu); err != nil {
		var re *integration.RepairError
		if !errors.As(err, &re) || re.Code != CodeMissingPermission {
			return integration.Failed(err)
		}
		res.Warnings = append(res.Warnings, integration.Warning{Code: "grants_unknown", Severity: integration.WarnInfo,
			Message: "OPNsense refused the menu, so Cha Dash can't list this key's privileges."})
	} else {
		res.Grants = grants(menu)
	}
	var t trafficInterface
	if err := in.get(ctx, countersPath, "Reporting: Traffic", &t); err != nil {
		failed := integration.Failed(err)
		failed.Grants, failed.Warnings = res.Grants, res.Warnings
		return failed
	}
	if _, ok := t.Interfaces[in.wan]; !ok {
		res.ErrorCode = integration.CodeInvalidConfig
		res.ErrorMessage = fmt.Sprintf("OPNsense has no interface %q to report as the WAN.", in.wan)
		return res
	}
	res.OK = true
	return res
}

// menuItem is one entry of core/menu/tree, which OPNsense filters by the
// caller's privileges.
type menuItem struct {
	ID          string     `json:"Id"`
	VisibleName string     `json:"VisibleName"`
	URL         string     `json:"Url"`
	Children    []menuItem `json:"Children"`
}

// readOnly are the menu pages whose privilege only reads, by their menu IDs,
// with the privilege's name (OPNsense 26.7). Any other page counts as a
// write: most OPNsense pages are settings pages, and spike 4.5 found read
// privileges that also grant writes (Status: Services can restart them).
var readOnly = map[string]string{
	"Reporting/Traffic":           "Reporting: Traffic",
	"System/Diagnostics/Activity": "Diagnostics: System Activity",
	"Lobby/Dashboard":             "Lobby: Dashboard",
	"Lobby/License":               "Lobby: License",
}

// grants turns the menu's pages into grants. Logout is every user's.
func grants(items []menuItem) []integration.Grant {
	var out []integration.Grant
	var walk func(items []menuItem, ids, names []string)
	walk = func(items []menuItem, ids, names []string) {
		for _, it := range items {
			ids, names := append(slices.Clone(ids), it.ID), append(slices.Clone(names), it.VisibleName)
			if len(it.Children) > 0 {
				walk(it.Children, ids, names)
				continue
			}
			path := strings.Join(ids, "/")
			switch {
			case it.URL == "" || path == "Lobby/Logout":
			case readOnly[path] != "":
				out = append(out, integration.Grant{Name: readOnly[path], Access: integration.AccessRead, Detail: it.URL})
			default:
				out = append(out, integration.Grant{Name: strings.Join(names, " › "), Access: integration.AccessWrite, Write: true, Detail: it.URL})
			}
		}
	}
	walk(items, nil, nil)
	return out
}
