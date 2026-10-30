// Package unraid reads an Unraid server through the Unraid API (GraphQL):
// the host, the array with its disks, the pools, the shares, and the Docker
// containers Unraid manages with their autostart settings. It needs API
// 4.37.4 or later, and it never sends the `disks` query, which wakes
// spun-down drives. See README.md.
package unraid

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "unraid"

// MinAPI is the oldest Unraid API the integration talks to.
var MinAPI = version{major: 4, minor: 37, patch: 4}

// Refresh rates beyond the coordinator's interval (Manifest.Poll).
const (
	infoEvery       = time.Hour        // host identity, versions, boot time
	networkEvery    = time.Hour        // the LAN address; the read takes 1.5–2.6 s
	sharesEvery     = 5 * time.Minute  // shares change rarely
	dockerIdleEvery = 15 * time.Minute // containers while nobody watches
	syncInterval    = time.Minute      // array, metrics, and containers while watched
	syncTimeout     = 30 * time.Second
)

// Optional reads (their warnings are keyed by part) and the scopes they
// publish into.
const (
	partContainers  = "containers"
	partShares      = "shares"
	partMetrics     = "metrics"
	partVMs         = "vms"
	scopeShares     = "shares"
	scopeContainers = "containers"
	scopeVMs        = "vms"
)

// Modes: what the owner chose at onboarding. The key's grants are checked
// against it, never trusted from it.
const (
	ModeReadOnly = "read_only"
	ModeControl  = "control"
)

const defaultName = "Unraid"

func init() { integration.Register(Integration{}) }

// Integration is the Unraid integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Unraid", Icon: "unraid", Version: "0.2.0", Tier: integration.Silver,
		Category:     integration.CatHosts,
		Capabilities: []integration.Capability{"host.metrics", "storage.pools", "storage.disks", "storage.shares", "containers", "compute.guests"},
		ConfigSchema: integration.Object(
			integration.URLProp(), integration.TLSProp(),
			integration.Prop("mode", integration.Enum("Read-only, or let Cha Dash control Unraid?", ModeReadOnly, ModeControl).
				Titled("Read-only", "Control: start, stop and update containers and VMs").WithDefault(ModeReadOnly).
				Describe("Control needs a key with Docker and VM update scopes; the recipe's consent link asks for the scopes the choice needs. You can switch later without deleting the instance.")),
			integration.Prop("parity_control", integration.Boolean("Also let Cha Dash run parity checks").
				Describe("Control mode only. Starts, pauses, resumes and cancels parity checks. It needs a key with Array: Update, which Unraid also lets add and remove disks: use the recipe's parity link, and Cha Dash never sends anything but the four parity mutations.")),
			integration.Prop("api_key", integration.Secret("API key").Describe("A key from the consent link below, or from "+keysPage+".")),
		).Require("url", "api_key"),
		Poll: integration.Poll{Interval: syncInterval, Timeout: syncTimeout},
		// One key (decision 6): in control mode it acts with the grants Test
		// reports as docker and vms, so those aren't a "write grant" warning.
		// A read-only instance offers no actions at all.
		Control: &integration.Control{Mode: "mode", ModeOn: ModeControl, Grants: []string{"docker", "vms", "array"}},
	}
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if _, err := endpointOf(cfg.String("url")); err != nil {
		return []integration.FieldError{{Path: "/url", Message: err.Error()}}
	}
	return nil
}

// endpointOf returns the API's URL for the webGUI URL the user gave (a
// trailing /graphql is accepted too).
func endpointOf(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("must be the server's http:// or https:// address")
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/graphql") + "/graphql"
	return u, nil
}

// Connect implements integration.Integration. It does no I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	ep, err := endpointOf(cfg.String("url"))
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "url " + err.Error()}
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	web := *ep
	web.Path = strings.TrimSuffix(ep.Path, "graphql")
	in := &instance{
		d: d, name: cfg.Name, prefix: ID + ":" + key + "/", instanceID: cfg.InstanceID,
		endpoint: ep.String(), web: web.String(), apiKey: cfg.String("api_key"),
		control:  cfg.String("mode") == ModeControl,
		parity:   cfg.String("mode") == ModeControl && cfg.Values["parity_control"] == true,
		warnings: map[string]integration.Warning{},
		resolved: map[string]resolvedTarget{},
	}
	if in.name == "" {
		in.name = defaultName
	}
	if ip := net.ParseIP(ep.Hostname()); ip != nil {
		in.urlIP = ip.String()
	}
	return in, nil
}

type instance struct {
	d        integration.Deps
	name     string // the instance's name
	prefix   string // "unraid:<instance id>/"
	endpoint string // .../graphql
	web      string // the webGUI, for links
	apiKey   string
	urlIP    string // the URL's host when it's an IP address
	// control is the instance's mode: true when the owner chose to let Cha
	// Dash control Unraid. Read-only instances have no actions.
	control bool
	// parity is control with parity checks switched on: it adds the array
	// scope (and the four parity actions) to what the key must grant.
	parity     bool
	instanceID string // for the recipe an unavailable action points to

	// apiMu guards api, which Sync and Test write and actions read.
	apiMu sync.Mutex
	api   string // the API version last read

	// Actions run beside Sync (actions.go): their own reads, the key's
	// grants for a short while, and the HTTP client for mutations.
	actMu    sync.Mutex
	me       *meData // nil: not read yet, or the key can't read ME
	meAt     time.Time
	resolved map[string]resolvedTarget // by target ID, as last resolved
	longHTTP *http.Client

	// Sync state; Sync runs on one goroutine.
	info       *infoData
	infoAt     time.Time
	versionOK  bool
	netIP      string
	netAt      time.Time
	arrayDisks map[string]bool // names of the array's data and parity disks
	pools      map[string]bool // names of the pools
	shares     map[string]bool // names of the shares last read
	sharesAt   time.Time
	dockerAt   time.Time
	vmsAt      time.Time
	warnings   map[string]integration.Warning // by part
}

func (in *instance) id(path string) string { return in.prefix + path }

func (in *instance) setAPI(v string) {
	in.apiMu.Lock()
	defer in.apiMu.Unlock()
	in.api = v
}

func (in *instance) apiVersion() string {
	in.apiMu.Lock()
	defer in.apiMu.Unlock()
	return in.api
}

// Close implements integration.Instance. The runtime owns the HTTP client.
func (in *instance) Close() error { return nil }

// Test implements integration.Instance: the version, then the key's grants
// from `me`.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	var v struct {
		Info infoData `json:"info"`
	}
	if err := in.query(ctx, "ChadashVersion", qVersion, &v); err != nil {
		if fe, ok := errors.AsType[*forbiddenError](err); ok {
			err = fe.repair()
		}
		return integration.Failed(err)
	}
	in.setAPI(v.Info.Versions.Core.API)
	res := integration.TestResult{OK: true, Version: v.Info.version()}
	var me struct {
		Me meData `json:"me"`
	}
	err := in.query(ctx, "ChadashMe", qMe, &me)
	var fe *forbiddenError
	switch {
	case errors.As(err, &fe):
		res.Warnings = append(res.Warnings, integration.Warning{Code: "grants_unknown", Severity: integration.WarnInfo,
			Message: "The API key can't read its own permissions (ME: Read), so Cha Dash can't check what it may do."})
	case err != nil:
		failed := integration.Failed(err)
		failed.Version = res.Version
		return failed
	default:
		res.Grants = me.Me.grants()
		warnings, missing := me.Me.check(in.control, in.parity)
		res.Warnings = append(res.Warnings, warnings...)
		if missing != "" {
			res.OK, res.ErrorCode, res.ErrorMessage = false, CodeMissingPermission, missing
		}
	}
	if err := checkVersion(in.apiVersion()); err != nil {
		code, _, msg := integration.Classify(err)
		res.OK, res.ErrorCode, res.ErrorMessage = false, code, msg
	}
	return res
}

// Sync implements integration.Instance. Every sync reads the array and the
// metrics (both cheap); system information hourly; shares every five
// minutes; containers every sync while someone watches, else every
// fifteen minutes. A read the key isn't granted is a Repair for the array
// and system information, and a warning for the rest.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	due := func(last time.Time, every time.Duration) bool {
		return last.IsZero() || now.Sub(last) >= every-every/10
	}

	if in.info == nil || !in.versionOK || due(in.infoAt, infoEvery) {
		var r struct {
			Info infoData `json:"info"`
		}
		if err := in.query(ctx, "ChadashInfo", qInfo, &r); err != nil {
			return required(err)
		}
		in.info, in.infoAt = &r.Info, now
		in.setAPI(r.Info.Versions.Core.API)
		in.d.Emit.SetVersion(r.Info.version())
		if err := checkVersion(in.apiVersion()); err != nil {
			in.versionOK = false
			return err
		}
		in.versionOK = true
	}
	if in.urlIP == "" && due(in.netAt, networkEvery) {
		in.netAt = now
		var r struct {
			Info struct {
				Interfaces []netInterface `json:"networkInterfaces"`
			} `json:"info"`
		}
		if err := in.query(ctx, "ChadashNetwork", qNetwork, &r); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			in.d.Log.Debug("couldn't read the network interfaces", "err", err)
		} else {
			in.netIP = pickIP(r.Info.Interfaces)
		}
	}

	var arr struct {
		Array arrayData `json:"array"`
	}
	if err := in.query(ctx, "ChadashArray", qArray, &arr); err != nil {
		return required(err)
	}
	var met struct {
		Metrics *metricsData `json:"metrics"`
	}
	if err := in.query(ctx, "ChadashMetrics", qMetrics, &met); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		in.partFailed(partMetrics, err)
		met.Metrics = nil
	} else {
		delete(in.warnings, partMetrics)
	}
	in.arrayDisks, in.pools = arr.Array.diskNames(), arr.Array.poolNames()
	if err := in.d.Emit.Replace("", in.coreFacts(&arr.Array, met.Metrics)); err != nil {
		return err
	}

	if due(in.sharesAt, sharesEvery) {
		var r struct {
			Shares []share `json:"shares"`
		}
		if err := in.query(ctx, "ChadashShares", qShares, &r); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			in.partFailed(partShares, err)
		} else {
			in.sharesAt = now
			delete(in.warnings, partShares)
			in.shares = map[string]bool{}
			for _, s := range r.Shares {
				in.shares[s.Name] = true
			}
			if err := in.d.Emit.Replace(scopeShares, in.shareFacts(r.Shares)); err != nil {
				return err
			}
		}
	}

	dockerEvery := dockerIdleEvery
	if want.Watched && want.WantsKind("container") {
		dockerEvery = 0
	}
	if due(in.dockerAt, dockerEvery) {
		var r struct {
			Docker struct {
				Containers []container `json:"containers"`
			} `json:"docker"`
		}
		if err := in.query(ctx, "ChadashDocker", qDocker, &r); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			in.partFailed(partContainers, err)
		} else {
			in.dockerAt = now
			delete(in.warnings, partContainers)
			if err := in.d.Emit.Replace(scopeContainers, in.containerFacts(r.Docker.Containers)); err != nil {
				return err
			}
		}
	}

	vmsEvery := dockerIdleEvery
	if want.Watched && want.WantsKind("vm") {
		vmsEvery = 0
	}
	if due(in.vmsAt, vmsEvery) {
		if err := in.syncVMs(ctx, now); err != nil {
			return err
		}
	}

	ws := make([]integration.Warning, 0, len(in.warnings))
	for _, k := range slices.Sorted(maps.Keys(in.warnings)) {
		ws = append(ws, in.warnings[k])
	}
	in.d.Emit.SetWarnings(ws)
	return nil
}

// syncVMs reads the VMs. Read-only keys made before v0.2 have no VMS scope,
// so on a read-only instance a refusal is quiet; on a controllable one it's
// a warning, as the VM actions need it. Any other failure is logged only:
// with Unraid's VM manager off the read fails, and that isn't a problem.
func (in *instance) syncVMs(ctx context.Context, now time.Time) error {
	var r struct {
		VMs struct {
			Domains []vmDomain `json:"domains"`
		} `json:"vms"`
	}
	err := in.query(ctx, "ChadashVMs", qVMs, &r)
	var fe *forbiddenError
	switch {
	case err == nil:
		in.vmsAt = now
		delete(in.warnings, partVMs)
		return in.d.Emit.Replace(scopeVMs, in.vmFacts(r.VMs.Domains))
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &fe) && in.control:
		in.partFailed(partVMs, err)
	default:
		in.vmsAt = now
		delete(in.warnings, partVMs)
		in.d.Log.Debug("couldn't read the VMs", "err", err)
	}
	return nil
}

// required makes a refused read the integration can't do without a Repair.
func required(err error) error {
	if fe, ok := errors.AsType[*forbiddenError](err); ok {
		return fe.repair()
	}
	return err
}

// partFailed records a warning for an optional read that failed; what it
// published before stays.
func (in *instance) partFailed(part string, err error) {
	w := integration.Warning{Code: "read_failed", Severity: integration.WarnWarning, Message: "Couldn't read " + reads[opOf[part]].what + ": " + err.Error()}
	if fe, ok := errors.AsType[*forbiddenError](err); ok {
		w = integration.Warning{Code: CodeMissingPermission, Severity: integration.WarnWarning,
			Message: "The API key can't read " + reads[fe.op].what + ", so they're missing. Give it " + reads[fe.op].resource + ": Read in Unraid (" + keysPage + ")."}
	}
	in.warnings[part] = w
	in.d.Log.Debug("an optional read failed", "part", part, "err", err)
}

var opOf = map[string]string{partMetrics: "ChadashMetrics", partShares: "ChadashShares", partContainers: "ChadashDocker", partVMs: "ChadashVMs"}

// netInterface is one of info.networkInterfaces.
type netInterface struct {
	Name      string `json:"name"`
	IPAddress string `json:"ipAddress"`
	Virtual   bool   `json:"virtual"`
	Operstate string `json:"operstate"`
}

// pickIP returns the server's LAN address: br0's (Unraid's default bridge),
// else the first physical interface that's up with an IPv4 address.
func pickIP(ifs []netInterface) string {
	ok := func(i netInterface) bool {
		ip := net.ParseIP(i.IPAddress)
		return ip != nil && ip.To4() != nil && !ip.IsLoopback()
	}
	for _, name := range []string{"br0", "bond0", "eth0"} {
		for _, i := range ifs {
			if i.Name == name && ok(i) {
				return i.IPAddress
			}
		}
	}
	for _, i := range ifs {
		skip := i.Virtual || i.Operstate != "up" || i.Name == "lo" || i.Name == "docker0"
		for _, p := range []string{"br-", "veth", "vnet", "virbr", "tailscale", "wg"} {
			skip = skip || strings.HasPrefix(i.Name, p)
		}
		if !skip && ok(i) {
			return i.IPAddress
		}
	}
	return ""
}

// meData is what `me` says about the key.
type meData struct {
	Name        string   `json:"name"`
	Roles       []string `json:"roles"`
	Permissions []struct {
		Resource string   `json:"resource"`
		Actions  []string `json:"actions"`
	} `json:"permissions"`
}

// readAction reports whether an action only reads. Unraid writes actions as
// READ_ANY (4.37) or read:any; anything that isn't a read counts as a write.
func readAction(a string) bool {
	a = strings.ToUpper(strings.ReplaceAll(a, ":", "_"))
	return a == "READ_ANY" || a == "READ_OWN"
}

// grants lists a role or a resource per grant. Anything beyond reading is
// Write: a create, update or delete action, the admin role, or a role Cha
// Dash doesn't know.
func (m meData) grants() []integration.Grant {
	var out []integration.Grant
	for _, r := range m.Roles {
		g := integration.Grant{Name: "role:" + strings.ToLower(r), Detail: "role"}
		switch strings.ToUpper(r) {
		case "ADMIN":
			g.Access, g.Write = integration.AccessAdmin, true
		case "VIEWER", "GUEST":
			g.Access = integration.AccessRead
		default:
			g.Access, g.Write = integration.AccessWrite, true
		}
		out = append(out, g)
	}
	for _, p := range m.Permissions {
		reads, writes := 0, 0
		actions := make([]string, len(p.Actions))
		for i, a := range p.Actions {
			actions[i] = strings.ToLower(a)
			if readAction(a) {
				reads++
			} else {
				writes++
			}
		}
		g := integration.Grant{Name: strings.ToLower(p.Resource), Detail: strings.Join(actions, ", "), Access: integration.AccessRead}
		switch {
		case writes > 0 && reads > 0:
			g.Access, g.Write = integration.AccessReadWrite, true
		case writes > 0:
			g.Access, g.Write = integration.AccessWrite, true
		}
		out = append(out, g)
	}
	return out
}

// canRead reports whether the key may read an Unraid resource.
func (m meData) canRead(resource string) bool {
	for _, r := range m.Roles {
		if r := strings.ToUpper(r); r == "ADMIN" || r == "VIEWER" {
			return true
		}
	}
	for _, p := range m.Permissions {
		if strings.EqualFold(p.Resource, resource) && slices.ContainsFunc(p.Actions, readAction) {
			return true
		}
	}
	return false
}

// can reports whether the key may take an action on an Unraid resource:
// verb is UPDATE, CREATE or DELETE (any or own), and the admin role or a
// "*" action allows everything.
func (m meData) can(resource, verb string) bool {
	if slices.ContainsFunc(m.Roles, func(r string) bool { return strings.EqualFold(r, "ADMIN") }) {
		return true
	}
	for _, p := range m.Permissions {
		if !strings.EqualFold(p.Resource, resource) {
			continue
		}
		for _, a := range p.Actions {
			a = strings.ToUpper(strings.ReplaceAll(a, ":", "_"))
			if a == "*" || strings.HasPrefix(a, verb+"_") {
				return true
			}
		}
	}
	return false
}

// check compares the key's grants with what the instance does: the array
// read is required (missing says so), Docker and shares are warnings, and
// a controllable instance also wants to read VMs and to update Docker and
// VMs (each missing one is a warning naming the actions it stops).
func (m meData) check(control, parity bool) (warnings []integration.Warning, missing string) {
	if !m.canRead("ARRAY") {
		missing = "The API key can't read the array (it needs ARRAY: Read). Add it in Unraid under " + keysPage + "."
	}
	reads := []struct{ resource, what string }{{"DOCKER", "Docker containers"}, {"SHARE", "shares"}}
	if control {
		reads = append(reads, struct{ resource, what string }{"VMS", "VMs"})
	}
	for _, r := range reads {
		if !m.canRead(r.resource) {
			warnings = append(warnings, integration.Warning{Code: CodeMissingPermission, Severity: integration.WarnWarning,
				Message: "The API key can't read " + r.what + " (" + r.resource + ": Read), so they won't show."})
		}
	}
	if control {
		scopes := controlScopes
		if parity {
			scopes = append(slices.Clone(controlScopes), writeArray)
		}
		for _, s := range scopes {
			if !m.can(s.resource, "UPDATE") {
				warnings = append(warnings, integration.Warning{Code: CodeMissingPermission, Severity: integration.WarnWarning,
					Message: "Cha Dash is set to control Unraid, but the API key can't " + s.what + " (it needs " + s.resource + ": Update), so " + s.actions + " will say so. " + addScopeHelp(s)})
			}
		}
	}
	return warnings, missing
}
