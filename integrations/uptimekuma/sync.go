package uptimekuma

import (
	"context"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// Uptime Kuma's monitor statuses.
const (
	statusDown        = 0
	statusUp          = 1
	statusPending     = 2
	statusMaintenance = 3
)

// statusPage is GET /api/status-page/<slug>.
type statusPage struct {
	Config struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
	} `json:"config"`
	PublicGroupList []struct {
		Name        string `json:"name"`
		MonitorList []struct {
			ID        int      `json:"id"`
			Name      string   `json:"name"`
			Type      string   `json:"type"`
			URL       string   `json:"url"` // only when the page shows it (sendUrl)
			CertDays  *float64 `json:"certExpiryDaysRemaining"`
			ValidCert *bool    `json:"validCert"`
		} `json:"monitorList"`
	} `json:"publicGroupList"`
}

// heartbeats is GET /api/status-page/heartbeat/<slug>: up to 100 beats per
// monitor, oldest first, and the 24-hour uptime as a ratio.
type heartbeats struct {
	HeartbeatList map[string][]beat  `json:"heartbeatList"`
	UptimeList    map[string]float64 `json:"uptimeList"`
}

type beat struct {
	Status int      `json:"status"`
	Time   string   `json:"time"` // "2026-10-03 14:04:02.111", UTC
	Msg    string   `json:"msg"`  // empty since Uptime Kuma 2
	Ping   *float64 `json:"ping"`
}

func (b beat) at() time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05.999", time.RFC3339Nano, "2006-01-02T15:04:05.999"} {
		if t, err := time.ParseInLocation(layout, b.Time, time.UTC); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// monitor is one monitor on the status page, merged from all sources.
type monitor struct {
	id, name, typ, group string
	url, host, port      string
	status               int // -1: no data
	pingMS               float64
	msg                  string
	since                time.Time // start of the current down streak
	uptime               float64   // 24 h ratio; NaN when absent
	certDays             float64   // NaN when absent
	certValid            *bool
}

// Sync implements integration.Instance: the status page, its heartbeats,
// and /metrics when there's a key. All three are small, so every sync
// reads them all.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	mons, page, err := in.read(ctx)
	if err != nil {
		return err
	}
	return in.d.Emit.Replace("", in.facts(page, mons))
}

// read fetches and merges the sources.
func (in *instance) read(ctx context.Context) ([]*monitor, statusPage, error) {
	var page statusPage
	if err := in.statusPageJSON(ctx, &page, "api", "status-page", in.slug); err != nil {
		return nil, page, err
	}
	if page.PublicGroupList == nil && page.Config.Slug == "" {
		return nil, page, &integration.RepairError{Code: integration.CodeAPIChanged, Message: "the status page API answered without a config or monitors: this isn't an API this integration knows"}
	}
	var hb heartbeats
	if err := in.statusPageJSON(ctx, &hb, "api", "status-page", "heartbeat", in.slug); err != nil {
		return nil, page, err
	}
	var prom promMetrics
	if in.key != "" {
		var err error
		if prom, err = in.fetchMetrics(ctx); err != nil {
			return nil, page, err
		}
	}
	var out []*monitor
	seen := map[string]bool{}
	for _, g := range page.PublicGroupList {
		for _, sm := range g.MonitorList {
			id := strconv.Itoa(sm.ID)
			if seen[id] {
				continue
			}
			seen[id] = true
			m := &monitor{id: id, name: sm.Name, typ: sm.Type, group: g.Name, url: clean(sm.URL), status: -1,
				pingMS: math.NaN(), uptime: math.NaN(), certDays: math.NaN(), certValid: sm.ValidCert}
			if sm.CertDays != nil && *sm.CertDays >= 0 {
				m.certDays = *sm.CertDays
			}
			if u, ok := hb.UptimeList[id+"_24"]; ok {
				m.uptime = u
			}
			m.fromBeats(hb.HeartbeatList[id])
			if pm := prom.lookup(id, sm.Name); pm != nil {
				m.fromMetrics(pm)
			}
			out = append(out, m)
		}
	}
	return out, page, nil
}

// fromBeats takes the newest beat's status and ping, and when it's down,
// when the streak of down beats began.
func (m *monitor) fromBeats(beats []beat) {
	if len(beats) == 0 {
		return
	}
	newest := len(beats) - 1
	for i, b := range beats {
		if b.at().After(beats[newest].at()) {
			newest = i
		}
	}
	b := beats[newest]
	m.status, m.msg = b.Status, strings.TrimSpace(b.Msg)
	if b.Ping != nil {
		m.pingMS = *b.Ping
	}
	if b.Status == statusDown && newest == len(beats)-1 {
		m.since = b.at()
		for i := newest - 1; i >= 0 && beats[i].Status == statusDown; i-- {
			m.since = beats[i].at()
		}
	}
}

// fromMetrics prefers /metrics' live status and response time over the
// heartbeat endpoint, which Uptime Kuma caches for a minute.
func (m *monitor) fromMetrics(pm *promMonitor) {
	if !math.IsNaN(pm.status) {
		if s := int(pm.status); s != m.status {
			m.status, m.msg, m.since = s, "", time.Time{}
		}
	}
	if !math.IsNaN(pm.responseMS) {
		m.pingMS = pm.responseMS
	}
	if !math.IsNaN(pm.certDays) && pm.certDays >= 0 {
		m.certDays = pm.certDays
	}
	if !math.IsNaN(pm.certValid) {
		v := pm.certValid == 1
		m.certValid = &v
	}
	if m.url == "" {
		m.url = pm.url
	}
	m.host, m.port = pm.host, pm.port
}

// health is the monitor's state and health.
func (m *monitor) health() (string, integration.Health) {
	reason := func(def string) string {
		if m.msg != "" {
			return m.msg
		}
		return def
	}
	switch m.status {
	case statusUp:
		return "online", integration.Health{Status: integration.HealthOK}
	case statusDown:
		return "error", integration.Health{Status: integration.HealthDown, Reason: reason("Uptime Kuma reports it down"), Since: m.since}
	case statusPending:
		return "error", integration.Health{Status: integration.HealthAttention, Reason: reason("Failing; Uptime Kuma is retrying")}
	case statusMaintenance:
		return "maintenance", integration.Health{Status: integration.HealthMaintenance, Reason: "In a maintenance window"}
	}
	return "unknown", integration.Health{Status: integration.HealthUnknown}
}

// latencyKey matches the built-in checks' keys where the kind of probe is
// the same: http.ms for HTTP monitors, tcp.ms for ports.
func (m *monitor) latencyKey() (key string, band [2]float64, warn, crit float64) {
	switch m.typ {
	case "http", "keyword", "json-query", "real-browser", "grpc-keyword":
		return "http.ms", [2]float64{1, 300}, 800, 2000
	case "port":
		return "tcp.ms", [2]float64{1, 50}, 200, 1000
	case "ping":
		return "ping.ms", [2]float64{1, 50}, 200, 1000
	}
	return "response.ms", [2]float64{1, 300}, 800, 2000
}

// target is what the monitor probes, when it's known.
func (m *monitor) target() string {
	switch {
	case m.url != "":
		return m.url
	case m.host != "" && m.port != "":
		return net.JoinHostPort(m.host, m.port)
	}
	return m.host
}

func (in *instance) appID() string              { return in.prefix + "/app" }
func (in *instance) monitorID(id string) string { return in.prefix + "/monitor/" + id }

// facts is everything the instance publishes.
func (in *instance) facts(page statusPage, mons []*monitor) integration.Facts {
	self := in.d.Emit.Self()
	app := in.appID()
	up, down := 0, 0
	var f integration.Facts
	for _, m := range mons {
		id := in.monitorID(m.id)
		state, health := m.health()
		attrs := map[string]any{"check": m.typ, "monitor_id": m.id}
		if m.group != "" {
			attrs["group"] = m.group
		}
		if t := m.target(); t != "" {
			attrs["target"] = t
		}
		if m.host != "" && net.ParseIP(m.host) != nil {
			attrs["ip"] = m.host
		}
		if !math.IsNaN(m.uptime) {
			attrs["uptime_24h"] = math.Round(m.uptime*1000) / 10 // percent, one decimal
		}
		if !math.IsNaN(m.certDays) && m.certValid != nil && *m.certValid {
			attrs["cert_days"] = int(m.certDays)
		}
		r := integration.Resource{ID: id, Kind: "check", Name: m.name, State: state, Health: health, Attrs: attrs, Caps: []string{"checks.uptime"}}
		if strings.HasPrefix(m.url, "http://") || strings.HasPrefix(m.url, "https://") {
			r.Links = []integration.Link{{Title: m.name, URL: m.url}}
		}
		f.Resources = append(f.Resources, r)
		f.Relations = append(f.Relations,
			integration.Relation{From: id, Type: "part_of", To: app},
			integration.Relation{From: self, Type: "monitors", To: id})
		key, band, warn, crit := m.latencyKey()
		lat := integration.Metric{Resource: id, Key: key, Unit: "ms", Min: 1, Max: 5000, Band: band, Warn: warn, Crit: crit, Value: integration.Unknown()}
		if m.status == statusUp && m.pingMS > 0 {
			lat.Value = m.pingMS
		}
		f.Metrics = append(f.Metrics, lat)
		switch m.status {
		case statusUp:
			up++
		case statusDown:
			down++
		}
	}
	total := float64(len(mons))
	attrs := map[string]any{"type": ID, "status_page": in.slug, "monitors": len(mons), "url": in.link}
	if page.Config.Title != "" {
		attrs["title"] = page.Config.Title
	}
	f.Resources = append([]integration.Resource{{
		ID: app, Kind: "app", Name: in.name, State: "running", Desired: "running",
		Health: integration.Health{Status: integration.HealthOK}, Attrs: attrs, Caps: []string{"checks.uptime"},
		Links: []integration.Link{{Title: in.name, URL: in.link}, {Title: "Status page", URL: in.base.JoinPath("status", in.slug).String()}},
	}}, f.Resources...)
	f.Relations = append(f.Relations, integration.Relation{From: self, Type: "monitors", To: app})
	f.Metrics = append(f.Metrics,
		integration.Metric{Resource: app, Key: "monitors.up", Unit: "count", Min: 0, Max: total, Band: [2]float64{total, total}, Value: float64(up)},
		integration.Metric{Resource: app, Key: "monitors.down", Unit: "count", Min: 0, Max: total, Band: [2]float64{0, 0}, Value: float64(down)},
	)
	return f
}

// Test implements integration.Instance: the status page and its heartbeats
// answer, and so does /metrics with the key. There's no version: none of
// these endpoints says it.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	mons, page, err := in.read(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	res := integration.TestResult{OK: true, Grants: []integration.Grant{{
		Name: "status page " + in.slug, Access: integration.AccessRead,
		Detail: fmt.Sprintf("%q, %d %s; public: anyone who can reach Uptime Kuma can read it", page.Config.Title, len(mons), plural(len(mons), "monitor")),
	}}}
	if in.key != "" {
		res.Grants = append(res.Grants, integration.Grant{Name: "metrics", Access: integration.AccessRead,
			Detail: "An Uptime Kuma API key only opens /metrics; it can't sign in or change anything"})
	}
	if len(mons) == 0 {
		res.Warnings = append(res.Warnings, integration.Warning{Code: "kuma_no_monitors", Severity: integration.WarnInfo,
			Message: fmt.Sprintf("The status page %q shows no monitors, so there's nothing to show.", in.slug)})
	}
	return res
}

func plural(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}
