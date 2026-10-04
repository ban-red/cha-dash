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
	"net/url"
	"slices"
	"strings"
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
	scopeShares     = "shares"
	scopeContainers = "containers"
)

const defaultName = "Unraid"

func init() { integration.Register(Integration{}) }

// Integration is the Unraid integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "Unraid", Icon: "unraid", Version: "0.1.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"host.metrics", "storage.pools", "storage.disks", "storage.shares", "containers"},
		ConfigSchema: integration.Object(
			integration.URLProp(), integration.TLSProp(),
			integration.Prop("api_key", integration.Secret("API key").Describe("A read-only key from "+keysPage+".")),
		).Require("url", "api_key"),
		Poll: integration.Poll{Interval: syncInterval, Timeout: syncTimeout},
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
		d: d, name: cfg.Name, prefix: ID + ":" + key + "/",
		endpoint: ep.String(), web: web.String(), apiKey: cfg.String("api_key"),
		warnings: map[string]integration.Warning{},
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

	// Sync state; Sync runs on one goroutine.
	api        string // the API version last read
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
	warnings   map[string]integration.Warning // by part
}

func (in *instance) id(path string) string { return in.prefix + path }

// Close implements integration.Instance. The runtime owns the HTTP client.
func (in *instance) Close() error { return nil }

// Test implements integration.Instance: the version, then the key's grants
// from `me`.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	var v struct {
		Info infoData `json:"info"`
	}
	if err := in.query(ctx, "ChadashVersion", qVersion, &v); err != nil {
		var fe *forbiddenError
		if errors.As(err, &fe) {
			err = fe.repair()
		}
		return integration.Failed(err)
	}
	in.api = v.Info.Versions.Core.API
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
		warnings, missing := me.Me.check()
		res.Warnings = append(res.Warnings, warnings...)
		if missing != "" {
			res.OK, res.ErrorCode, res.ErrorMessage = false, CodeMissingPermission, missing
		}
	}
	if err := checkVersion(in.api); err != nil {
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
		in.info, in.infoAt, in.api = &r.Info, now, r.Info.Versions.Core.API
		in.d.Emit.SetVersion(r.Info.version())
		if err := checkVersion(in.api); err != nil {
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

	ws := make([]integration.Warning, 0, len(in.warnings))
	for _, k := range slices.Sorted(maps.Keys(in.warnings)) {
		ws = append(ws, in.warnings[k])
	}
	in.d.Emit.SetWarnings(ws)
	return nil
}

// required makes a refused read the integration can't do without a Repair.
func required(err error) error {
	var fe *forbiddenError
	if errors.As(err, &fe) {
		return fe.repair()
	}
	return err
}

// partFailed records a warning for an optional read that failed; what it
// published before stays.
func (in *instance) partFailed(part string, err error) {
	w := integration.Warning{Code: "read_failed", Severity: integration.WarnWarning, Message: "Couldn't read " + reads[opOf[part]].what + ": " + err.Error()}
	var fe *forbiddenError
	if errors.As(err, &fe) {
		w = integration.Warning{Code: CodeMissingPermission, Severity: integration.WarnWarning,
			Message: "The API key can't read " + reads[fe.op].what + ", so they're missing. Give it " + reads[fe.op].resource + ": Read in Unraid (" + keysPage + ")."}
	}
	in.warnings[part] = w
	in.d.Log.Debug("an optional read failed", "part", part, "err", err)
}

var opOf = map[string]string{partMetrics: "ChadashMetrics", partShares: "ChadashShares", partContainers: "ChadashDocker"}

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

// check compares the key's grants with the reads the integration makes:
// the array is required (missing says so), Docker and shares are warnings.
func (m meData) check() (warnings []integration.Warning, missing string) {
	if !m.canRead("ARRAY") {
		missing = "The API key can't read the array (it needs ARRAY: Read). Add it in Unraid under " + keysPage + "."
	}
	for _, r := range []struct{ resource, what string }{{"DOCKER", "Docker containers"}, {"SHARE", "shares"}} {
		if !m.canRead(r.resource) {
			warnings = append(warnings, integration.Warning{Code: CodeMissingPermission, Severity: integration.WarnWarning,
				Message: "The API key can't read " + r.what + " (" + r.resource + ": Read), so they won't show."})
		}
	}
	return warnings, missing
}
