// Package truenas is the TrueNAS integration (25.04 and later), read only:
// the system as a host, its pools and their health, datasets and their
// usage, disks with SMART and temperature, apps, periodic snapshot tasks
// and replication tasks, which feed backup coverage. It speaks TrueNAS's
// WebSocket JSON-RPC 2.0 API with an API key (the REST API is deprecated,
// W3). See README.md.
package truenas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "truenas"

const (
	liveInterval = time.Minute
	idleInterval = 5 * time.Minute
	syncTimeout  = 30 * time.Second
	minMajor     = 25 // 25.04 brought /api/current
	testedMajor  = 25 // 25.04 and 25.10
	stateKey     = "replication_ok"
)

func init() { integration.Register(Integration{}) }

// Integration is the TrueNAS integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		ID: ID, Name: "TrueNAS", Icon: "truenas", Version: "0.1.0", Tier: integration.Silver,
		Category:     integration.CatHosts,
		Capabilities: []integration.Capability{"storage.pools", "storage.disks", "storage.shares", "backups.targets"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Where TrueNAS's web interface answers, e.g. https://nas.lab.example. It must be https: TrueNAS revokes an API key that arrives over plain HTTP.")),
			integration.TLSProp(),
			integration.Prop("api_key", integration.Secret("API key").Describe("An API key of a user with the Read-only Administrator privilege.")),
		).Require("url", "api_key"),
		Poll: integration.Poll{Interval: liveInterval, Idle: idleInterval, Timeout: syncTimeout},
	}
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	u, err := url.Parse(cfg.String("url"))
	if err != nil || u.Host == "" {
		return []integration.FieldError{{Path: "/url", Message: "must be an absolute https:// URL"}}
	}
	if u.Scheme != "https" {
		return []integration.FieldError{{Path: "/url", Message: "must start with https://: TrueNAS revokes an API key used over plain HTTP"}}
	}
	return nil
}

// Connect implements integration.Integration. No I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	var c struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if err := cfg.Decode(&c); err != nil {
		return nil, &integration.ConfigError{Message: err.Error()}
	}
	u, err := url.Parse(strings.TrimRight(c.URL, "/"))
	if err != nil || u.Host == "" {
		return nil, &integration.ConfigError{Message: "the url needs a host", Field: "url"}
	}
	if u.Scheme != "https" {
		return nil, &integration.ConfigError{Message: "the url must be https: TrueNAS revokes an API key used over plain HTTP", Field: "url"}
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	web := strings.TrimRight(u.String(), "/")
	ws := *u
	ws.Scheme = "wss"
	ws.Path = strings.TrimRight(u.Path, "/") + apiPath
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return &instance{d: d, key: key, name: cfg.Name, web: web, host: u.Hostname(),
		cl:     &client{http: d.HTTP, url: ws.String(), key: c.APIKey},
		hidden: map[string]string{}, raised: map[string]bool{}}, nil
}

type instance struct {
	d    integration.Deps
	key  string // the instance ID
	name string
	web  string // the web interface
	host string // the URL's host, an address or a name
	cl   *client

	version string
	newer   bool

	info     systemInfo
	pools    []pool
	datasets []dataset
	disks    []disk
	temps    map[string]float64
	tempsAt  time.Time
	apps     []app
	snaps    []snapshotTask
	repls    []replication
	alerts   []alert
	have     map[string]bool // parts read at least once
	lastOK   map[string]int64
	okLoaded bool

	hidden map[string]string // part → why it isn't shown
	notes  []integration.Warning
	raised map[string]bool
}

// Close implements integration.Instance.
func (in *instance) Close() error {
	in.cl.close()
	in.d.HTTP.CloseIdleConnections()
	return nil
}

var versionRE = regexp.MustCompile(`\d+\.\d+(?:\.\d+)*`)

// checkVersion reads system.info's version: "25.10.0", "25.04.2.4".
func checkVersion(v string) (string, bool, error) {
	full := versionRE.FindString(v)
	major, err := strconv.Atoi(strings.SplitN(full, ".", 2)[0])
	if full == "" || err != nil {
		return v, false, &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: fmt.Sprintf("TrueNAS reported a version Cha Dash can't read (%q)", v)}
	}
	if major < minMajor {
		return full, false, &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("TrueNAS %s isn't supported: Cha Dash reads 25.04 and later", full)}
	}
	return full, major > testedMajor, nil
}

func newerWarning(v string) integration.Warning {
	return integration.Warning{Code: "version_untested", Severity: integration.WarnInfo,
		Message: fmt.Sprintf("TrueNAS %s is newer than this integration has been tested with (25.04 and 25.10).", v)}
}

// Test implements integration.Instance: the version, and the roles of the
// key's user.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	var info systemInfo
	if err := in.cl.call(ctx, "system.info", nil, &info); err != nil {
		return integration.Failed(in.classify(err))
	}
	v, newer, err := checkVersion(info.Version)
	if err != nil {
		return integration.Failed(err)
	}
	res := integration.TestResult{OK: true, Version: v}
	var who me
	if err := in.cl.call(ctx, "auth.me", nil, &who); err != nil {
		res.Warnings = append(res.Warnings, integration.Warning{Code: "roles_unknown", Severity: integration.WarnInfo,
			Message: "Cha Dash couldn't read the API key's roles (auth.me): " + err.Error()})
	} else {
		res.Grants = grants(who)
		if w := integration.WriteGrantWarning(res.Grants); w != nil {
			res.Warnings = append(res.Warnings, *w)
		}
	}
	if newer {
		res.Warnings = append(res.Warnings, newerWarning(v))
	}
	return res
}

// grants are the key's user's roles. READONLY_ADMIN and the *_READ roles
// only read; anything else can change something.
func grants(who me) []integration.Grant {
	var out []integration.Grant
	roles := slices.Clone([]string(who.Privilege.Roles))
	slices.Sort(roles)
	for _, r := range roles {
		g := integration.Grant{Name: r, Access: integration.AccessRead}
		switch {
		case r == "FULL_ADMIN":
			g.Access, g.Write, g.Detail = integration.AccessAdmin, true, "Every right on the system. A Read-only Administrator's key is enough for Cha Dash."
		case r == "READONLY_ADMIN" || strings.HasSuffix(r, "_READ"):
			g.Detail = "Reads only."
		default:
			g.Access, g.Write = integration.AccessReadWrite, true
		}
		out = append(out, g)
	}
	if who.Privilege.WebShell {
		out = append(out, integration.Grant{Name: "Web shell", Access: integration.AccessAdmin, Write: true,
			Detail: "The key's user may open a root shell in the web interface."})
	}
	return out
}

// classify turns a denied call into an auth Repair: the key's user lacks a
// role Cha Dash can't do without.
func (in *instance) classify(err error) error {
	if re := rpcErr(err); re != nil && re.denied() {
		return &integration.AuthError{Field: "api_key", Message: "TrueNAS denied " + re.Method + " to the API key's user: give it the Read-only Administrator privilege"}
	}
	return err
}

// soft reads a part the integration can do without. A denied call hides
// the part, a method this release doesn't have is skipped quietly, other
// JSON-RPC errors keep what was read before with a warning; transport
// errors fail the sync.
func (in *instance) soft(ctx context.Context, part, what, method string, params []any, dst any) (bool, error) {
	err := in.cl.call(ctx, method, params, dst)
	if err == nil {
		delete(in.hidden, part)
		in.have[part] = true
		return true, nil
	}
	re := rpcErr(err)
	switch {
	case re == nil:
		return false, err
	case re.denied():
		in.hidden[part] = what
	case re.missing():
		in.have[part] = false
	default:
		in.notes = append(in.notes, integration.Warning{Code: "partial_read", Severity: integration.WarnWarning,
			Message: fmt.Sprintf("Couldn't read %s; showing what was read before: %s", what, err.Error())})
	}
	return false, nil
}

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	in.notes = nil
	if in.have == nil {
		in.have = map[string]bool{}
	}
	var info systemInfo
	if err := in.cl.call(ctx, "system.info", nil, &info); err != nil {
		return in.classify(err)
	}
	v, newer, err := checkVersion(info.Version)
	if err != nil {
		return err
	}
	in.info, in.version, in.newer = info, v, newer

	var pools []pool
	if err := in.cl.call(ctx, "pool.query", nil, &pools); err != nil {
		return in.classify(err)
	}
	in.pools = pools
	var datasets []dataset
	if ok, err := in.soft(ctx, "datasets", "the datasets", "pool.dataset.query",
		[]any{[]any{}, map[string]any{"extra": map[string]any{"flat": true, "retrieve_children": false, "user_properties": false}}}, &datasets); err != nil {
		return err
	} else if ok {
		in.datasets = datasets
	}
	var disks []disk
	if ok, err := in.soft(ctx, "disks", "the disks", "disk.query",
		[]any{[]any{}, map[string]any{"extra": map[string]any{"pools": true}}}, &disks); err != nil {
		return err
	} else if ok {
		in.disks = disks
	}
	var apps []app
	if ok, err := in.soft(ctx, "apps", "the apps", "app.query", nil, &apps); err != nil {
		return err
	} else if ok {
		in.apps = apps
	}
	var snaps []snapshotTask
	if ok, err := in.soft(ctx, "snapshots", "the periodic snapshot tasks", "pool.snapshottask.query", nil, &snaps); err != nil {
		return err
	} else if ok {
		in.snaps = snaps
	}
	var repls []replication
	if ok, err := in.soft(ctx, "replication", "the replication tasks", "replication.query", nil, &repls); err != nil {
		return err
	} else if ok {
		in.repls = repls
	}
	var alerts []alert
	if ok, err := in.soft(ctx, "alerts", "the alerts", "alert.list", nil, &alerts); err != nil {
		return err
	} else if ok {
		in.alerts = alerts
	}
	// Temperatures wake nothing (TrueNAS reads them from the drive's
	// cached SMART data) but cost a smartctl per disk: only while watched.
	if want.Watched && len(in.disks) > 0 {
		var raw map[string]json.RawMessage
		if ok, err := in.soft(ctx, "temps", "the disk temperatures", "disk.temperatures", []any{[]string{}, false}, &raw); err != nil {
			return err
		} else if ok {
			in.temps, in.tempsAt = temperatures(raw), now
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	in.loadLastOK(ctx)
	return in.publish(ctx, now)
}

// temperatures reads disk.temperatures: {name: °C}, or {name: {temperature}}
// with thresholds.
func temperatures(raw map[string]json.RawMessage) map[string]float64 {
	out := map[string]float64{}
	for k, b := range raw {
		var n num
		if json.Unmarshal(b, &n) == nil && n.ok {
			out[k] = n.v
			continue
		}
		var o struct {
			Temperature num `json:"temperature"`
		}
		if json.Unmarshal(b, &o) == nil && o.Temperature.ok {
			out[k] = o.Temperature.v
		}
	}
	return out
}

// loadLastOK reads when each replication task last finished, kept across
// restarts: zettarepl's state keeps only the newest run, so a failure
// hides the last good one.
func (in *instance) loadLastOK(ctx context.Context) {
	if in.okLoaded {
		return
	}
	in.okLoaded = true
	in.lastOK = map[string]int64{}
	if in.d.State == nil {
		return
	}
	b, err := in.d.State.Load(ctx, stateKey)
	if err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &in.lastOK)
	}
}

func (in *instance) saveLastOK(ctx context.Context) {
	if in.d.State == nil {
		return
	}
	b, _ := json.Marshal(in.lastOK)
	if err := in.d.State.Save(ctx, stateKey, b); err != nil && in.d.Log != nil {
		in.d.Log.Warn("truenas: couldn't save the replication state", "err", err)
	}
}
