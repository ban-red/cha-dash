package unifi

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// flex is a number UniFi sends as a number or as a string ("11.8").
type flex struct {
	v  float64
	ok bool
}

func (f *flex) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil // an odd value is unknown, not a failed sync
	}
	f.v, f.ok = v, true
	return nil
}

func (f flex) value() float64 {
	if !f.ok {
		return integration.Unknown()
	}
	return f.v
}

// device is one entry of stat/device.
type device struct {
	MAC         string `json:"mac"`
	IP          string `json:"ip"`
	Name        string `json:"name"`
	Model       string `json:"model"`
	Type        string `json:"type"` // udm, ugw, uxg, usw, uap, …
	Version     string `json:"version"`
	Adopted     *bool  `json:"adopted"`
	State       int    `json:"state"`
	Upgradable  bool   `json:"upgradable"`
	UpgradeTo   string `json:"upgrade_to_firmware"`
	Uptime      flex   `json:"uptime"`
	LastSeen    flex   `json:"last_seen"`
	NumSta      flex   `json:"num_sta"`
	SystemStats struct {
		CPU flex `json:"cpu"`
		Mem flex `json:"mem"`
	} `json:"system-stats"`
	Uplink struct {
		MAC string `json:"uplink_mac"`
	} `json:"uplink"`
}

// subsystem is one entry of stat/health: wlan, wan, www, lan, vpn.
type subsystem struct {
	Subsystem string `json:"subsystem"`
	Status    string `json:"status"` // ok, warning, error, unknown
	NumUser   flex   `json:"num_user"`
	NumGuest  flex   `json:"num_guest"`
	NumGW     flex   `json:"num_gw"`
	GWMAC     string `json:"gw_mac"`
	GWName    string `json:"gw_name"`
	ISP       string `json:"isp_name"`
	RxRate    flex   `json:"rx_bytes-r"` // bytes per second
	TxRate    flex   `json:"tx_bytes-r"`
	Latency   flex   `json:"latency"` // ms, on www
}

// kindOf maps UniFi's device type to a Home Graph kind. Gateways are
// routers, so the self-aware T4 and the network widgets treat them so.
func kindOf(t string) string {
	switch t {
	case "udm", "ugw", "uxg":
		return "router"
	case "usw":
		return "switch"
	case "uap":
		return "access_point"
	}
	return "network_device"
}

// stateOf maps stat/device's state code (the values aiounifi and the
// controller's UI use) to a state and a health.
func stateOf(code int) (string, integration.Health) {
	h := func(status, reason string) integration.Health {
		return integration.Health{Status: status, Reason: reason}
	}
	switch code {
	case 1:
		return "online", h(integration.HealthOK, "")
	case 0:
		return "offline", h(integration.HealthDown, "Disconnected from UniFi Network")
	case 4:
		return "updating", h(integration.HealthMaintenance, "Updating its firmware")
	case 5:
		return "provisioning", h(integration.HealthMaintenance, "Provisioning")
	case 6:
		return "heartbeat_missed", h(integration.HealthAttention, "Missed its heartbeat")
	case 2, 7:
		return "adopting", h(integration.HealthMaintenance, "Being adopted")
	case 3:
		return "firmware_mismatch", h(integration.HealthAttention, "Its firmware doesn't match the site's")
	case 8:
		return "deleting", h(integration.HealthMaintenance, "Being removed")
	case 9:
		return "inform_error", h(integration.HealthAttention, "Can't reach UniFi Network's inform address")
	case 10:
		return "adoption_failed", h(integration.HealthAttention, "Adoption failed")
	case 11:
		return "isolated", h(integration.HealthAttention, "Isolated: lost its uplink")
	}
	return "unknown", h(integration.HealthUnknown, fmt.Sprintf("UniFi reports state %d", code))
}

func slug(mac string) string { return strings.ReplaceAll(strings.ToLower(mac), ":", "") }

// model is what one sync read, ready to publish.
type model struct {
	in       *instance
	site     string // the site resource's ID
	wan      string // the WAN resource's ID; "" when the site has no gateway
	gateway  string // the gateway's resource ID, when known
	devices  []integration.Resource
	metrics  []integration.Metric
	rels     []integration.Relation
	pending  int
	offline  []string
	upgrades int

	health  map[string]subsystem
	wanRes  integration.Resource
	wanDown bool
}

func (in *instance) model(devices []device, health []subsystem) *model {
	m := &model{in: in, site: in.id("site"), health: map[string]subsystem{}}
	for _, s := range health {
		m.health[s.Subsystem] = s
	}
	self := in.d.Emit.Self()
	m.rels = append(m.rels, integration.Relation{From: self, Type: "monitors", To: m.site})
	byMAC := map[string]string{}
	for _, d := range devices {
		if d.MAC != "" {
			byMAC[strings.ToLower(d.MAC)] = in.id("device/" + slug(d.MAC))
		}
	}
	for _, d := range devices {
		if d.Adopted != nil && !*d.Adopted {
			m.pending++ // waiting for adoption: not this site's yet
			continue
		}
		if d.MAC == "" {
			in.d.Log.Debug("skipped a device without a MAC address", "model", d.Model)
			continue
		}
		id := byMAC[strings.ToLower(d.MAC)]
		r := m.device(id, d)
		m.devices = append(m.devices, r)
		m.rels = append(m.rels,
			integration.Relation{From: self, Type: "monitors", To: id},
			integration.Relation{From: id, Type: "part_of", To: m.site})
		if up := byMAC[strings.ToLower(d.Uplink.MAC)]; up != "" && up != id {
			m.rels = append(m.rels, integration.Relation{From: up, Type: "provides_network_for", To: id})
		}
		if r.Kind == "router" && m.gateway == "" {
			m.gateway = id
		}
	}
	if wan, ok := m.health["wan"]; ok {
		if gw := byMAC[strings.ToLower(wan.GWMAC)]; gw != "" {
			m.gateway = gw
		}
		if wan.NumGW.v > 0 || wan.GWMAC != "" || (wan.Status != "unknown" && wan.Status != "") {
			m.wanFacts(wan, m.health["www"])
		}
	}
	return m
}

func (m *model) device(id string, d device) integration.Resource {
	state, health := stateOf(d.State)
	name := d.Name
	if name == "" {
		name = d.Model + " " + d.MAC
	}
	r := integration.Resource{ID: id, Kind: kindOf(d.Type), Name: name, State: state, Health: health,
		Attrs: map[string]any{"platform": "unifi", "model": d.Model, "mac": strings.ToLower(d.MAC), "upgradable": d.Upgradable},
		Links: []integration.Link{{Title: "UniFi Network", URL: m.in.base}},
	}
	if d.Type != "" {
		r.Attrs["unifi_type"] = d.Type
	}
	if d.IP != "" {
		r.Attrs["ip"] = d.IP
	}
	if d.Version != "" {
		r.Attrs["version"] = d.Version
	}
	if d.Upgradable {
		m.upgrades++
		if d.UpgradeTo != "" {
			r.Attrs["upgrade_to"] = d.UpgradeTo
		}
	}
	// Boot time from the device's own report (last_seen − uptime), so it
	// doesn't drift between syncs the way now − uptime would.
	online := health.Status != integration.HealthDown
	if online && d.Uptime.ok && d.Uptime.v > 0 && d.LastSeen.ok && d.LastSeen.v > d.Uptime.v {
		r.Attrs["boot_at"] = time.Unix(int64(d.LastSeen.v-d.Uptime.v), 0).UTC().Truncate(time.Minute).Format(time.RFC3339)
	}
	if !online {
		m.offline = append(m.offline, name)
		if d.LastSeen.ok && d.LastSeen.v > 0 {
			r.Health.Since = time.Unix(int64(d.LastSeen.v), 0).UTC()
		}
	}
	if r.Kind == "router" {
		r.Labels = map[string]string{"chadash.role": "network-core"}
		r.Caps = []string{"net.router"}
	}
	cpu, mem := d.SystemStats.CPU.value(), d.SystemStats.Mem.value()
	if !online {
		cpu, mem = integration.Unknown(), integration.Unknown()
	}
	m.metrics = append(m.metrics,
		integration.Metric{Resource: id, Key: "cpu.pct", Unit: "%", Min: 0, Max: 100, Band: [2]float64{0, 60}, Value: cpu},
		integration.Metric{Resource: id, Key: "mem.pct", Unit: "%", Min: 0, Max: 100, Band: [2]float64{0, 80}, Warn: 90, Crit: 98, Value: mem})
	if r.Kind == "switch" || r.Kind == "access_point" {
		n := d.NumSta.value()
		if !online {
			n = integration.Unknown()
		}
		m.metrics = append(m.metrics, integration.Metric{Resource: id, Key: "clients.count", Unit: "count", Min: 0, Max: 200, Band: [2]float64{0, 40}, Value: n})
	}
	return r
}

// wanFacts builds the WAN from the wan subsystem (the gateway's uplink) and
// www (whether the internet answers through it).
func (m *model) wanFacts(wan, www subsystem) {
	m.wan = m.in.id("wan")
	r := integration.Resource{ID: m.wan, Kind: "wan", Name: "WAN", Attrs: map[string]any{"platform": "unifi"},
		Links: []integration.Link{{Title: "UniFi Network", URL: m.in.base}}, Caps: []string{"net.wan"}}
	if wan.ISP != "" {
		r.Attrs["isp"] = wan.ISP
	}
	if wan.GWName != "" {
		r.Attrs["gateway"] = wan.GWName
	}
	switch {
	case wan.Status == "error":
		r.State, r.Health = "error", integration.Health{Status: integration.HealthDown, Reason: "The WAN is down"}
		m.wanDown = true
	case www.Status == "error":
		r.State, r.Health = "error", integration.Health{Status: integration.HealthDown, Reason: "The WAN is up, but the internet doesn't answer"}
		m.wanDown = true
	case wan.Status == "warning" || www.Status == "warning":
		r.State, r.Health = "online", integration.Health{Status: integration.HealthAttention, Reason: "UniFi reports the internet connection degraded"}
	case wan.Status == "ok":
		r.State, r.Health = "online", integration.Health{Status: integration.HealthOK}
	default:
		r.State, r.Health = "unknown", integration.Health{Status: integration.HealthUnknown, Reason: "UniFi doesn't know the WAN's state"}
	}
	m.wanRes = r
	mbps := func(f flex) float64 {
		if !f.ok {
			return integration.Unknown()
		}
		return math.Round(f.v*8/1e6*100) / 100
	}
	rx, tx := wan.RxRate, wan.TxRate
	if !rx.ok {
		rx, tx = www.RxRate, www.TxRate
	}
	latency := www.Latency.value()
	if m.wanDown {
		latency = integration.Unknown()
	}
	m.metrics = append(m.metrics,
		integration.Metric{Resource: m.wan, Key: "rx.mbps", Unit: "Mb/s", Min: 0, Max: 1000, Band: [2]float64{2, 60}, Value: mbps(rx)},
		integration.Metric{Resource: m.wan, Key: "tx.mbps", Unit: "Mb/s", Min: 0, Max: 1000, Band: [2]float64{1, 20}, Value: mbps(tx)},
		integration.Metric{Resource: m.wan, Key: "latency.ms", Unit: "ms", Min: 0, Max: 1000, Band: [2]float64{1, 40}, Warn: 150, Crit: 500, Value: latency})
}

// clients are the site's counts from stat/health: lan and wlan, users and
// guests. A missing subsystem is unknown.
func (m *model) clients() []integration.Metric {
	count := func(ok bool, fs ...flex) float64 {
		if !ok {
			return integration.Unknown()
		}
		n := 0.0
		for _, f := range fs {
			if !f.ok {
				return integration.Unknown()
			}
			n += f.v
		}
		return n
	}
	lan, lok := m.health["lan"]
	wlan, wok := m.health["wlan"]
	wired := count(lok, lan.NumUser, lan.NumGuest)
	wifi := count(wok, wlan.NumUser, wlan.NumGuest)
	guests := count(lok && wok, lan.NumGuest, wlan.NumGuest)
	total := wired + wifi // NaN when either is unknown
	metric := func(key string, v float64) integration.Metric {
		return integration.Metric{Resource: m.site, Key: key, Unit: "count", Min: 0, Max: 500, Band: [2]float64{0, 100}, Value: v}
	}
	return []integration.Metric{metric("clients.count", total), metric("clients.wifi", wifi), metric("clients.wired", wired), metric("clients.guests", guests)}
}

func (m *model) facts() integration.Facts {
	in := m.in
	site := integration.Resource{ID: m.site, Kind: "network", Name: in.name, State: "running",
		Health: integration.Health{Status: integration.HealthOK},
		Attrs: map[string]any{"platform": "unifi", "unifi_platform": in.platform, "site": in.site,
			"devices": len(m.devices), "devices_offline": len(m.offline), "upgradable": m.upgrades, "pending_adoption": m.pending},
		Links: []integration.Link{{Title: "UniFi Network", URL: in.base}},
		Caps:  []string{"net.devices", "net.clients"},
	}
	if in.version != "" {
		site.Attrs["version"] = in.version
	}
	if n := len(m.offline); n > 0 {
		names := slices.Clone(m.offline)
		slices.Sort(names)
		word := "device is"
		if n > 1 {
			word = "devices are"
		}
		site.Health = integration.Health{Status: integration.HealthAttention, Reason: fmt.Sprintf("%d %s offline: %s", n, word, strings.Join(names, ", "))}
	}
	f := integration.Facts{Resources: append([]integration.Resource{site}, m.devices...), Relations: m.rels,
		Metrics: append(m.clients(), m.metrics...)}
	if m.wan != "" {
		f.Resources = append(f.Resources, m.wanRes)
		parent := m.site
		if m.gateway != "" {
			parent = m.gateway
		}
		f.Relations = append(f.Relations, integration.Relation{From: m.wan, Type: "part_of", To: parent})
	}
	return f
}

// incidentWAN is the WAN-down incident's ID, local to the instance.
const incidentWAN = "wan.down"

// wanIncident raises the WAN-down incident while UniFi reports the WAN or
// the internet down, and resolves it otherwise (resolving one that isn't
// open does nothing).
func (in *instance) wanIncident(m *model) error {
	if !m.wanDown {
		return in.d.Emit.Resolve(incidentWAN, "UniFi reports the WAN up again (seen at "+in.d.Now().Local().Format("15:04")+").")
	}
	r := m.wanRes
	cause := r.ID
	if m.gateway != "" {
		cause = m.gateway
	}
	inc := integration.Incident{
		ID: incidentWAN, Scenario: "wan-down", Class: integration.ClassProblem, Severity: integration.SeverityDown,
		Title:   "The internet is down at " + in.name,
		Summary: fmt.Sprintf("UniFi Network reports: %s.", strings.ToLower(r.Health.Reason[:1])+r.Health.Reason[1:]),
		Target:  r.ID,
		Facts:   map[string]any{"wan": m.health["wan"].Status, "www": m.health["www"].Status},
		Causes:  []integration.Cause{{Text: r.Health.Reason, Resource: cause}},
		BlastRadius: []integration.Impact{
			{Resource: m.site, Via: "part_of", Depth: 1},
		},
		Suggested:    integration.Action{ID: "link.open", Label: "Open UniFi Network", Tier: "T0", Target: r.ID},
		ResolvesWhen: "Resolves when UniFi reports the WAN up and the internet answering.",
	}
	if isp, _ := r.Attrs["isp"].(string); isp != "" {
		inc.Facts["isp"] = isp
	}
	return in.d.Emit.Raise(inc)
}
