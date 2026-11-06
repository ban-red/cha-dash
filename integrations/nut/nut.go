// Package nut reads Network UPS Tools: each UPS that upsd serves, with its
// status, battery charge, runtime, load and input voltage, over NUT's own
// line protocol on TCP (port 3493). Read only. A low battery raises an
// incident at attention. See README.md.
package nut

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "nut"

// DefaultPort is upsd's port.
const DefaultPort = 3493

func init() { integration.Register(Integration{}) }

// Integration is the NUT integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	host := (&integration.Schema{Type: "string", Title: "Host", Format: "hostname"}).
		Describe("The machine running upsd, e.g. nas.home.arpa or 192.168.1.10.")
	user := integration.String("Username").Describe("Optional: a user from upsd.users. upsd lets anyone who can connect read, so most setups leave this empty.")
	pass := integration.Secret("Password").Describe("That user's password.")
	return integration.Manifest{
		ID: ID, Name: "Network UPS Tools", Icon: "nut", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatMonitoring,
		Capabilities: []integration.Capability{"power.ups"},
		ConfigSchema: integration.Object(
			integration.Prop("host", host),
			integration.Prop("port", integration.Integer("Port", 1, 65535).WithDefault(DefaultPort)),
			integration.Prop("username", user),
			integration.Prop("password", pass),
		).Require("host"),
		// A low battery leaves minutes, so the inbox hears of it whether or
		// not anyone watches. A sync is one short connection.
		Poll: integration.Poll{Interval: 15 * time.Second, Background: true, Timeout: 15 * time.Second},
	}
}

type config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ValidateConfig implements integration.ConfigValidator: a password needs a
// username, and neither may break a protocol line.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return []integration.FieldError{{Message: err.Error()}}
	}
	var errs []integration.FieldError
	if c.Password != "" && c.Username == "" {
		errs = append(errs, integration.FieldError{Path: "/username", Message: "is required with a password"})
	}
	if strings.ContainsAny(c.Username, "\r\n\x00") {
		errs = append(errs, integration.FieldError{Path: "/username", Message: "must be one line"})
	}
	if strings.ContainsAny(c.Password, "\r\n\x00") {
		errs = append(errs, integration.FieldError{Path: "/password", Message: "must be one line"})
	}
	return errs
}

// Connect implements integration.Integration. No I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return nil, &integration.ConfigError{Message: err.Error()}
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if strings.ContainsAny(c.Username+c.Password, "\r\n\x00") {
		return nil, &integration.ConfigError{Field: "username", Message: "the username and password must be one line each"}
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return &instance{d: d, cfg: c, key: key, addr: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), low: map[string]lowBattery{}}, nil
}

type instance struct {
	d    integration.Deps
	cfg  config
	key  string
	addr string

	// Guarded by the single Sync goroutine.
	version string
	low     map[string]lowBattery // by resource ID: the open low-battery incidents
}

// lowBattery is one open incident, kept as first raised so raising it again
// publishes nothing.
type lowBattery struct {
	incident integration.Incident
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// ups is one UPS as read.
type ups struct {
	upsEntry
	vars map[string]string
	err  *upsdError // LIST VAR refused: DATA-STALE, DRIVER-NOT-CONNECTED, …
}

// read is one session: credentials, VER (once), LIST UPS, LIST VAR for each.
func (in *instance) read(ctx context.Context, wantVersion bool) ([]ups, error) {
	s, err := dial(ctx, in.addr)
	if err != nil {
		return nil, err
	}
	defer s.close()
	out, err := in.readOn(s, wantVersion)
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, in.classify(err)
}

func (in *instance) readOn(s *session, wantVersion bool) ([]ups, error) {
	if err := s.auth(in.cfg.Username, in.cfg.Password); err != nil {
		return nil, err
	}
	if wantVersion {
		v, err := s.version()
		if err != nil {
			return nil, err
		}
		in.version = v
	}
	list, err := s.listUPS()
	if err != nil {
		return nil, err
	}
	out := make([]ups, 0, len(list))
	for _, e := range list {
		vars, err := s.listVar(e.Name)
		var ue *upsdError
		switch {
		case errors.As(err, &ue) && !authCode(ue.Code):
			if ue.Code == "UNKNOWN-UPS" {
				continue // removed between the two lists
			}
			out = append(out, ups{upsEntry: e, err: ue})
		case err != nil:
			return nil, err
		default:
			out = append(out, ups{upsEntry: e, vars: vars})
		}
	}
	return out, nil
}

func authCode(code string) bool {
	switch code {
	case "ACCESS-DENIED", "INVALID-PASSWORD", "PASSWORD-REQUIRED", "INVALID-USERNAME", "USERNAME-REQUIRED":
		return true
	}
	return false
}

// classify turns upsd's refusals into the framework's errors. Messages name
// the command and the code, never a credential.
func (in *instance) classify(err error) error {
	var ue *upsdError
	if !errors.As(err, &ue) {
		return err
	}
	switch ue.Code {
	case "INVALID-USERNAME", "USERNAME-REQUIRED":
		return &integration.AuthError{Field: "username", Message: fmt.Sprintf("upsd refused the username (ERR %s)", ue.Code)}
	case "INVALID-PASSWORD", "PASSWORD-REQUIRED":
		return &integration.AuthError{Field: "password", Message: fmt.Sprintf("upsd refused the password (ERR %s)", ue.Code)}
	case "ACCESS-DENIED":
		if in.cfg.Username == "" {
			return &integration.AuthError{Field: "username",
				Message: fmt.Sprintf("upsd refused %s (ERR ACCESS-DENIED): enter a username and password from upsd.users, or let this host connect", ue.Cmd)}
		}
		return &integration.AuthError{Field: "password",
			Message: fmt.Sprintf("upsd refused %s (ERR ACCESS-DENIED): check the username and password, and that upsd lets this host connect", ue.Cmd)}
	case "UNKNOWN-COMMAND":
		return notUpsd(ue.Cmd, "ERR UNKNOWN-COMMAND")
	}
	return err
}

// Test implements integration.Instance.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	start := time.Now()
	list, err := in.read(ctx, true)
	if err != nil {
		r := integration.Failed(err)
		r.Version = in.version
		return r
	}
	res := integration.TestResult{OK: true, Version: in.version, Latency: time.Since(start)}
	if in.cfg.Password != "" {
		res.Warnings = append(res.Warnings, integration.Warning{Code: "plain_tcp", Severity: integration.WarnWarning,
			Message: "upsd's protocol sends the password in clear over TCP, and reading needs none: leave the username and password empty unless upsd asks for them."})
	}
	if len(list) == 0 {
		res.Warnings = append(res.Warnings, integration.Warning{Code: "no_ups", Severity: integration.WarnWarning,
			Message: "upsd answered but serves no UPS: check ups.conf."})
	}
	for _, u := range list {
		if u.err != nil {
			res.Warnings = append(res.Warnings, integration.Warning{Code: "ups_unreadable", Severity: integration.WarnWarning,
				Message: fmt.Sprintf("%s: %s", u.Name, driverProblem(u.err.Code))})
		}
	}
	return res
}

// Sync implements integration.Instance: one session, then every UPS.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	needVersion := in.version == ""
	list, err := in.read(ctx, needVersion)
	if err != nil {
		return err
	}
	if needVersion && in.version != "" {
		in.d.Emit.SetVersion(in.version)
	}
	return in.publish(list)
}

// publish emits every UPS and raises or resolves its low-battery incident.
func (in *instance) publish(list []ups) error {
	self := in.d.Emit.Self()
	now := in.d.Now()
	var f integration.Facts
	var warnings []integration.Warning
	if len(list) == 0 {
		warnings = append(warnings, integration.Warning{Code: "no_ups", Severity: integration.WarnWarning,
			Message: "upsd serves no UPS: check ups.conf."})
	}
	type lowNow struct {
		rid, slug string
		u         ups
		a         assessment
	}
	var lows []lowNow
	slugs, unread := map[string]bool{}, map[string]bool{}
	for _, u := range list {
		s := slug(u.Name)
		base := s
		for n := 2; slugs[s]; n++ {
			s = base + "-" + strconv.Itoa(n)
		}
		slugs[s] = true
		rid := ID + ":" + in.key + "/ups/" + s
		a := assess(u)
		name := u.Desc
		if name == "" {
			name = u.Name
		}
		attrs := map[string]any{"type": "ups", "provider": ID, "ups": u.Name, "server": in.addr}
		if u.Desc != "" {
			attrs["description"] = u.Desc
		}
		for attr, keys := range map[string][]string{
			"manufacturer": {"device.mfr", "ups.mfr"}, "model": {"device.model", "ups.model"},
			"driver": {"driver.name"}, "battery_type": {"battery.type"}, "test_result": {"ups.test.result"},
			"alarm": {"ups.alarm"}, "beeper": {"ups.beeper.status"},
		} {
			if v := first(u.vars, keys...); v != "" {
				attrs[attr] = v
			}
		}
		if u.vars != nil {
			attrs["status"] = u.vars["ups.status"]
			attrs["flags"] = a.flags
			attrs["on_battery"] = a.has("OB")
			attrs["low_battery"] = a.has("LB")
			attrs["charging"] = a.has("CHRG")
			attrs["replace_battery"] = a.has("RB")
		}
		if u.err != nil {
			attrs["error"] = u.err.Code
		}
		f.Resources = append(f.Resources, integration.Resource{
			ID: rid, Kind: "ups", Name: name, State: a.state, Health: a.health, Attrs: attrs,
			Caps: []string{"power.ups"},
		})
		f.Relations = append(f.Relations, integration.Relation{From: self, Type: "monitors", To: rid})
		f.Metrics = append(f.Metrics, metrics(rid, u.vars)...)
		switch {
		case a.has("LB"):
			lows = append(lows, lowNow{rid, s, u, a})
		case u.err != nil:
			unread[rid] = true // unknown: an open incident stays open
		}
	}
	in.d.Emit.SetWarnings(warnings)
	if err := in.d.Emit.Replace("", f); err != nil {
		return err
	}
	open := map[string]bool{}
	for _, l := range lows {
		open[l.rid] = true
		lb, ok := in.low[l.rid]
		if !ok {
			lb = lowBattery{incident: lowIncident(l.rid, l.slug, l.u, l.a, now, self)}
			in.low[l.rid] = lb
		}
		if err := in.d.Emit.Raise(lb.incident); err != nil {
			return err
		}
	}
	for _, rid := range sortedKeys(in.low) {
		if open[rid] || unread[rid] {
			continue
		}
		lb := in.low[rid]
		msg := fmt.Sprintf("%s no longer reports a low battery (%s).", lb.incident.Facts["name"], now.Local().Format("15:04"))
		if err := in.d.Emit.Resolve(lb.incident.ID, msg); err != nil {
			return err
		}
		delete(in.low, rid)
	}
	return nil
}

func sortedKeys(m map[string]lowBattery) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func first(vars map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(vars[k]); v != "" {
			return v
		}
	}
	return ""
}

// assessment is a UPS's state and health from ups.status.
type assessment struct {
	state  string
	health integration.Health
	flags  []string
}

func (a assessment) has(flag string) bool { return slices.Contains(a.flags, flag) }

// Health from ups.status, worst first:
//
//	OFF, FSD             down       the load has no power, or is being shut down
//	LB                   attention  (and the incident)
//	OB, RB, OVER, BYPASS attention
//	ALARM                attention  with ups.alarm
//	CAL                  maintenance
//	OL                   ok
//
// A UPS whose driver upsd can't reach is attention, state error: nothing
// would warn of a power cut on it.
func assess(u ups) assessment {
	if u.err != nil {
		return assessment{state: "error", health: integration.Health{Status: integration.HealthAttention, Reason: driverProblem(u.err.Code)}}
	}
	flags := strings.Fields(u.vars["ups.status"])
	a := assessment{flags: flags}
	if len(flags) == 0 {
		a.state, a.health = "unknown", integration.Health{Status: integration.HealthUnknown, Reason: "upsd reports no ups.status"}
		return a
	}
	switch {
	case a.has("OFF"):
		a.state = "off"
	case a.has("OB"):
		a.state = "on_battery"
	case a.has("OL"):
		a.state = "online"
	default:
		a.state = "unknown"
	}
	var down, attention []string
	if a.has("OFF") {
		down = append(down, "the UPS's output is off")
	}
	if a.has("FSD") {
		down = append(down, "upsd is shutting the systems it powers down (forced shutdown)")
	}
	switch {
	case a.has("LB") && a.has("OB"):
		attention = append(attention, "on battery, and the battery is low"+runtimeNote(u.vars))
	case a.has("LB"):
		attention = append(attention, "the battery is low"+runtimeNote(u.vars))
	case a.has("OB"):
		attention = append(attention, "on battery: the mains power is out"+runtimeNote(u.vars))
	}
	if a.has("RB") {
		attention = append(attention, "the battery needs replacing")
	}
	if a.has("OVER") {
		attention = append(attention, "overloaded")
	}
	if a.has("BYPASS") {
		attention = append(attention, "on bypass: the battery isn't protecting the load")
	}
	if a.has("ALARM") {
		msg := "the UPS reports an alarm"
		if al := strings.TrimSpace(u.vars["ups.alarm"]); al != "" {
			msg += ": " + al
		}
		attention = append(attention, msg)
	}
	switch {
	case len(down) > 0:
		a.health = integration.Health{Status: integration.HealthDown, Reason: strings.Join(append(down, attention...), "; ")}
	case len(attention) > 0:
		a.health = integration.Health{Status: integration.HealthAttention, Reason: strings.Join(attention, "; ")}
	case a.has("CAL"):
		a.health = integration.Health{Status: integration.HealthMaintenance, Reason: "calibrating the battery"}
	case a.state == "online":
		a.health = integration.Health{Status: integration.HealthOK}
	default:
		a.health = integration.Health{Status: integration.HealthUnknown, Reason: "ups.status is " + u.vars["ups.status"]}
	}
	return a
}

// runtimeNote is ", about 12 minutes left" when the runtime is known.
func runtimeNote(vars map[string]string) string {
	r := num(vars, "battery.runtime")
	if math.IsNaN(r) {
		return ""
	}
	m := math.Round(r / 60)
	if m == 1 {
		return ", about 1 minute left"
	}
	return fmt.Sprintf(", about %.0f minutes left", m)
}

func driverProblem(code string) string {
	switch code {
	case "DATA-STALE":
		return "upsd's data for this UPS is stale: its driver lost contact with the UPS"
	case "DRIVER-NOT-CONNECTED":
		return "upsd isn't connected to this UPS's driver: check that the driver runs"
	}
	return "upsd couldn't read this UPS (ERR " + code + ")"
}

// num is a variable as a number, or NaN.
func num(vars map[string]string, key string) float64 {
	v, ok := vars[key]
	if !ok {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsInf(f, 0) {
		return math.NaN()
	}
	return f
}

func round1(f float64) float64 {
	if math.IsNaN(f) {
		return f
	}
	return math.Round(f*10) / 10
}

// metrics are one UPS's series. Missing variables are unknown, never 0.
func metrics(rid string, vars map[string]string) []integration.Metric {
	runtime := num(vars, "battery.runtime") // seconds
	if !math.IsNaN(runtime) {
		runtime /= 60
	}
	load := num(vars, "ups.load")
	power := num(vars, "ups.realpower")
	nominal := num(vars, "ups.realpower.nominal")
	if math.IsNaN(power) && !math.IsNaN(load) && !math.IsNaN(nominal) {
		power = load * nominal / 100
	}
	powerMax := 2000.0
	if !math.IsNaN(nominal) && nominal > 0 {
		powerMax = nominal
	}
	volts := num(vars, "input.voltage")
	vMax, vBand := 260.0, [2]float64{207, 253}
	if vn := num(vars, "input.voltage.nominal"); !math.IsNaN(vn) && vn > 0 {
		vMax, vBand = math.Round(vn*1.3), [2]float64{math.Round(vn * 0.9), math.Round(vn * 1.1)}
	}
	return []integration.Metric{
		{Resource: rid, Key: "battery.pct", Unit: "%", Max: 100, Band: [2]float64{90, 100}, Value: round1(num(vars, "battery.charge"))},
		{Resource: rid, Key: "runtime.min", Unit: "min", Max: 240, Band: [2]float64{10, 240}, Value: round1(runtime)},
		{Resource: rid, Key: "load.pct", Unit: "%", Max: 100, Band: [2]float64{0, 60}, Warn: 80, Crit: 95, Value: round1(load)},
		{Resource: rid, Key: "input.v", Unit: "V", Max: vMax, Band: vBand, Value: round1(volts)},
		{Resource: rid, Key: "power.w", Unit: "W", Max: powerMax, Band: [2]float64{0, powerMax * 0.6}, Value: math.Round(power)},
	}
}

// lowIncident is the problem for a UPS reporting a low battery.
func lowIncident(rid, s string, u ups, a assessment, now time.Time, self string) integration.Incident {
	name := u.Desc
	if name == "" {
		name = u.Name
	}
	title := name + "'s battery is low"
	summary := fmt.Sprintf("%s reports a low battery (%s)", name, u.vars["ups.status"])
	if a.has("OB") {
		title = name + " is on battery, and the battery is low"
		summary = fmt.Sprintf("%s is running on battery and reports it low (%s)", name, u.vars["ups.status"])
	}
	var left []string
	if c := num(u.vars, "battery.charge"); !math.IsNaN(c) {
		left = append(left, fmt.Sprintf("%.0f%% charge", c))
	}
	if r := num(u.vars, "battery.runtime"); !math.IsNaN(r) {
		left = append(left, fmt.Sprintf("about %.0f minutes of runtime", math.Round(r/60)))
	}
	if len(left) > 0 {
		summary += " at " + now.Local().Format("15:04") + ": " + strings.Join(left, ", ")
	}
	summary += ". Systems that upsmon protects shut down soon."
	facts := map[string]any{"name": name, "ups": u.Name, "status": u.vars["ups.status"]}
	return integration.Incident{
		ID: "ups." + s + ".low_battery", Scenario: "ups-low-battery",
		Class: integration.ClassProblem, Severity: integration.SeverityAttention,
		Title: title, Summary: summary, Target: rid, OpenedAt: now, Facts: facts,
		Causes:       []integration.Cause{{Text: "ups.status " + u.vars["ups.status"], Resource: rid, At: now.UTC().Truncate(time.Second)}},
		Suggested:    integration.Action{ID: "integration.refresh", Label: "Check now", Tier: "T0", Target: self},
		ResolvesWhen: "Resolves when " + name + " no longer reports a low battery.",
	}
}

// slug is a UPS name as the last part of its resource ID.
func slug(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	s := b.String()
	if s == "" {
		return "ups"
	}
	return s
}
