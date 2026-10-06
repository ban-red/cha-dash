// Package proxmox is the Proxmox VE integration: one instance per PVE
// "domain" (a cluster, or a standalone node). It reads the fleet from
// /cluster/resources every poll and the rest on the slower plan from spike
// 4.1, and publishes the cluster, hosts, guests, storage and backup jobs
// with the Demo Home's kinds and metric keys. It reads with a privilege-
// separated PVEAuditor token; an optional second token, the control token,
// lets it start, stop, snapshot and back up guests (actions.go). See
// README.md.
package proxmox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "proxmox"

// The polling plan (spike 4.1). The live tier is every sync: the
// coordinator polls at liveInterval while someone watches and at the idle
// interval otherwise. Slower tiers run when they're due.
const (
	liveInterval = 15 * time.Second // never below 10 s: pvestatd refreshes every 10 s
	idleInterval = 5 * time.Minute
	syncTimeout  = 45 * time.Second

	recentEvery     = time.Minute      // cluster status, tasks
	slowEvery       = 5 * time.Minute  // node status
	configEvery     = 10 * time.Minute // storage config, guest configs, addresses
	configIdleEvery = 30 * time.Minute // the same while nobody watches (addresses are skipped)
	backupEvery     = 15 * time.Minute // backup jobs, coverage
	rareEvery       = time.Hour        // version, permissions, pending updates
	logLimit        = 1000             // lines of a vzdump log to read
)

// tokenIDPattern is user@realm!tokenname.
const tokenIDPattern = `^[^\s@!]+@[^\s@!]+![A-Za-z][A-Za-z0-9._-]*$`

func init() { integration.Register(Integration{}) }

// Integration is the Proxmox VE integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	tokenID := &integration.Schema{Type: "string", Title: "Token ID", Pattern: tokenIDPattern,
		Description: "The API token, as user@realm!name (e.g. chadash@pve!dash). A privilege-separated token with PVEAuditor on / is enough."}
	controlID := &integration.Schema{Type: "string", Title: "Control token ID", Pattern: tokenIDPattern, Group: integration.GroupControl,
		Description: "Optional. A second token that lets Cha Dash start, stop, snapshot and back up guests, as user@realm!name (e.g. chadash-control@pve!control)."}
	return integration.Manifest{
		ID: ID, Name: "Proxmox VE", Icon: "proxmox", Version: "0.2.0", Tier: integration.Silver,
		Capabilities: []integration.Capability{"host.metrics", "compute.guests", "backups.jobs"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Where the PVE API answers, e.g. https://pve.lab.example:8006. For a cluster, any node.")),
			integration.TLSProp(),
			integration.Prop("token_id", tokenID),
			integration.Prop("token_secret", integration.Secret("Token secret")),
			integration.Prop("control_token_id", controlID),
			integration.Prop("control_token_secret", integration.Secret("Control token secret").InGroup(integration.GroupControl)),
			integration.Prop("hub_vmid", integration.Integer("Cha Dash's own guest", 100, 999999999).
				Describe("Optional. The VMID of the guest Cha Dash runs in, when it runs on this cluster. Rebooting or stopping it then needs the critical (T4) confirmation. Cha Dash also looks for it by its own addresses.")),
		).Require("url", "token_id", "token_secret"),
		Control: &integration.Control{
			Fields: []integration.ControlField{
				{Field: "control_token_id", Replaces: "token_id"},
				{Field: "control_token_secret", Replaces: "token_secret"},
			},
			Missing: "Add a control token to {instance} to let Cha Dash start, stop, snapshot and back up its guests. The recipe's control variant makes one.",
		},
		Poll: integration.Poll{Interval: liveInterval, Idle: idleInterval, Timeout: syncTimeout},
	}
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	u, err := url.Parse(cfg.String("url"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return []integration.FieldError{{Path: "/url", Message: "must start with https:// (or http:// behind a proxy)"}}
	}
	return nil
}

// Connect implements integration.Integration. No I/O.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	var c struct {
		URL         string `json:"url"`
		TokenID     string `json:"token_id"`
		TokenSecret string `json:"token_secret"`
		HubVMID     int    `json:"hub_vmid"`
	}
	if err := cfg.Decode(&c); err != nil {
		return nil, &integration.ConfigError{Message: err.Error()}
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return nil, &integration.ConfigError{Message: "the url needs a host", Field: "url"}
	}
	u.RawQuery, u.Fragment = "", ""
	base := strings.TrimRight(u.String(), "/")
	user, token, _ := strings.Cut(c.TokenID, "!")
	key := cfg.InstanceID
	if key == "" {
		key = "test" // an unsaved config under test
	}
	return &instance{
		d: d, key: key, base: base, auth: "PVEAPIToken=" + c.TokenID + "=" + c.TokenSecret,
		user: user, token: token, hubVMID: c.HubVMID,
		guestCfg: map[string]*guestConfig{}, addrs: map[string]addr{}, nodeStat: map[string]nodeSample{},
		logs: map[string]*runLog{}, updates: map[string]int{}, hidden: map[string]string{},
		raised: map[string]bool{}, at: map[string]time.Time{},
	}, nil
}

// instance is one PVE domain. Everything below d is owned by the Sync
// goroutine; Test runs on a separate instance.
type instance struct {
	d           integration.Deps
	key         string // the instance ID, the middle of every resource ID
	base        string // the URL without a trailing slash; also the UI root for links
	auth        string // the Authorization header
	user, token string // token_id split at "!"
	hubVMID     int    // the hub_vmid hint: the guest Cha Dash runs in, or 0

	ad      *adapter
	version string
	perms   privileges
	at      map[string]time.Time // when each tier last ran

	status   []statusEntry
	haveStat bool // /cluster/status was read at least once
	tasks    []task
	nodeStat map[string]nodeSample
	storCfg  map[string]storageConfig
	guestCfg map[string]*guestConfig // "qemu/100" → config
	addrs    map[string]addr         // "lxc/101" → primary address
	jobs     []backupJob
	haveJobs bool
	notBkp   []notBackedUp
	logs     map[string]*runLog // UPID → the parsed log of a finished, failed run
	updates  map[string]int     // node → pending updates, when the token may read them

	hidden map[string]string // part → why it's hidden (a 403 the token can't get past)
	notes  []integration.Warning
	raised map[string]bool // incidents raised by this instance
}

// nodeSample is one node's status and when it was read.
type nodeSample struct {
	st nodeStatus
	at time.Time
}

// addr is a guest's primary address, from its interfaces.
type addr struct{ ip, mac string }

// Close implements integration.Instance.
func (in *instance) Close() error {
	in.d.HTTP.CloseIdleConnections()
	return nil
}

// Test implements integration.Instance: the version, the token's grants
// read back from /access/permissions, and what this token won't show.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	var v versionInfo
	if err := in.get(ctx, "/version", nil, &v); err != nil {
		return integration.Failed(err)
	}
	ad, err := adapterFor(v)
	if err != nil {
		return integration.Failed(err)
	}
	var perms privileges
	if err := in.get(ctx, "/access/permissions", nil, &perms); err != nil {
		return integration.Failed(err)
	}
	res := integration.TestResult{OK: true, Version: displayVersion(v), Grants: perms.grants()}
	res.Warnings = append(res.Warnings, perms.readWarnings()...)
	if ad.newer {
		res.Warnings = append(res.Warnings, newerWarning(v))
	}
	// privsep is readable with Sys.Audit on /access (the token's own detail
	// endpoint needs User.Modify, so it's not used).
	var u userInfo
	if in.user != "" && in.get(ctx, p("access", "users", in.user), nil, &u) == nil {
		if t, ok := u.Tokens[in.token]; ok && !t.Privsep.bool() {
			res.Warnings = append(res.Warnings, integration.Warning{Code: "privsep_off", Severity: integration.WarnWarning,
				Message: fmt.Sprintf("This token isn't privilege-separated: it has all of %s's rights. Recreate it with --privsep 1 and grant it PVEAuditor.", in.user)})
		}
	}
	if in.user == "root@pam" {
		res.Grants = append(res.Grants, integration.Grant{Name: "root@pam", Access: integration.AccessAdmin, Write: true,
			Detail: "a token of root@pam is admin-equivalent unless privilege-separated"})
	}
	return res
}

func newerWarning(v versionInfo) integration.Warning {
	return integration.Warning{Code: "version_untested", Severity: integration.WarnInfo,
		Message: fmt.Sprintf("Proxmox VE %s is newer than this integration has been tested with (up to 9.x); it's read like 9.x.", displayVersion(v))}
}

// due reports whether a tier should run. A tenth of the interval of slack
// absorbs the coordinator's jitter.
func (in *instance) due(tier string, every time.Duration, now time.Time) bool {
	at, ok := in.at[tier]
	return !ok || now.Sub(at) >= every-every/10
}

// soft sorts the error of a call the integration can do without. A 401 or a
// cancelled sync fails the sync; a 403 marks the part hidden (the token
// lacks a privilege) and is retried at the tier's pace; anything else is a
// warning and is retried on the next sync. The cached data stays.
func (in *instance) soft(ctx context.Context, part, what string, err error) (ok, retry bool, fatal error) {
	if err == nil {
		delete(in.hidden, part)
		return true, false, nil
	}
	if ctx.Err() != nil {
		return false, false, ctx.Err()
	}
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == http.StatusUnauthorized {
		return false, false, err
	}
	if forbidden(err) {
		in.hidden[part] = ae.Reason
		return false, false, nil
	}
	_, _, msg := integration.Classify(err)
	in.notes = append(in.notes, integration.Warning{Code: "partial_read", Severity: integration.WarnWarning,
		Message: fmt.Sprintf("Couldn't read %s; showing what was read before: %s", what, msg)})
	return false, true, nil
}

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	in.notes = nil
	if in.ad == nil || in.due("rare", rareEvery, now) {
		if err := in.refreshRare(ctx, now); err != nil {
			return err
		}
	}
	var res []resource
	if err := in.get(ctx, "/cluster/resources", nil, &res); err != nil {
		return err
	}
	if in.due("recent", recentEvery, now) {
		if err := in.refreshRecent(ctx, now); err != nil {
			return err
		}
	}
	if in.due("slow", slowEvery, now) {
		if err := in.refreshNodes(ctx, res, now); err != nil {
			return err
		}
	}
	every := configEvery
	if !want.Watched {
		every = configIdleEvery
	}
	if in.due("config", every, now) || in.configChanged() {
		if err := in.refreshConfig(ctx, res, nil, want, now); err != nil {
			return err
		}
	} else if fresh := in.unseenGuests(res); len(fresh) > 0 {
		if err := in.refreshConfig(ctx, res, fresh, want, now); err != nil {
			return err
		}
	}
	if in.due("backup", backupEvery, now) || in.backupFinished() {
		if err := in.refreshBackups(ctx, now); err != nil {
			return err
		}
	}
	if err := in.fetchLogs(ctx, res); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return in.publish(res, now)
}

// refreshRare reads the version (picking the adapter) and the token's
// privileges: a token that gained write privileges warns at sync time.
func (in *instance) refreshRare(ctx context.Context, now time.Time) error {
	var v versionInfo
	if err := in.get(ctx, "/version", nil, &v); err != nil {
		return err
	}
	ad, err := adapterFor(v)
	if err != nil {
		return err
	}
	var perms privileges
	err = in.get(ctx, "/access/permissions", nil, &perms)
	ok, retry, fatal := in.soft(ctx, "permissions", "the token's permissions", err)
	if fatal != nil {
		return fatal
	}
	if ok {
		in.perms = perms
	}
	ad.withGrants(in.perms)
	in.ad, in.version = ad, displayVersion(v)
	if !retry {
		in.at["rare"] = now
	}
	return nil
}

// refreshRecent reads quorum and node membership, and the recent tasks.
func (in *instance) refreshRecent(ctx context.Context, now time.Time) error {
	retry := false
	var st []statusEntry
	err := in.get(ctx, "/cluster/status", nil, &st)
	ok, r, fatal := in.soft(ctx, "status", "the cluster status", err)
	if fatal != nil {
		return fatal
	}
	if ok {
		in.status, in.haveStat = st, true
	}
	retry = retry || r
	var ts []task
	err = in.get(ctx, "/cluster/tasks", nil, &ts)
	ok, r, fatal = in.soft(ctx, "tasks", "the recent tasks", err)
	if fatal != nil {
		return fatal
	}
	if ok {
		in.tasks = ts
	}
	if !(retry || r) {
		in.at["recent"] = now
	}
	return nil
}

// refreshNodes reads each online node's status, and hourly its pending
// updates when the token may read them (Sys.Modify; a read-only token
// can't, and nothing is requested then).
func (in *instance) refreshNodes(ctx context.Context, res []resource, now time.Time) error {
	retry := false
	updates := in.perms.has("/nodes", "Sys.Modify") && in.due("updates", rareEvery, now)
	if !in.perms.has("/nodes", "Sys.Modify") {
		in.updates = map[string]int{}
	}
	for _, n := range in.nodeNames(res) {
		if !in.nodeOnline(n, res) {
			continue
		}
		var st nodeStatus
		err := in.get(ctx, p("nodes", n, "status"), nil, &st)
		ok, r, fatal := in.soft(ctx, "node-status", "the status of "+n, err)
		if fatal != nil {
			return fatal
		}
		if ok {
			in.nodeStat[n] = nodeSample{st: st, at: now}
		}
		retry = retry || r
		if updates {
			var pkgs []struct {
				Package string `json:"Package"`
			}
			err := in.get(ctx, p("nodes", n, "apt", "update"), nil, &pkgs)
			if ok, _, fatal := in.soft(ctx, "updates", "the pending updates of "+n, err); fatal != nil {
				return fatal
			} else if ok {
				in.updates[n] = len(pkgs)
			}
		}
	}
	if updates {
		in.at["updates"] = now
	}
	if !retry {
		in.at["slow"] = now
	}
	return nil
}

// refreshConfig reads the storage config and guest configs (all of them, or
// only the keys in only), and while someone watches, the running guests'
// addresses.
func (in *instance) refreshConfig(ctx context.Context, res []resource, only map[string]bool, want integration.Interest, now time.Time) error {
	retry := false
	if only == nil {
		var sc []storageConfig
		err := in.get(ctx, "/storage", nil, &sc)
		ok, r, fatal := in.soft(ctx, "storage-config", "the storage config", err)
		if fatal != nil {
			return fatal
		}
		if ok {
			in.storCfg = map[string]storageConfig{}
			for _, s := range sc {
				in.storCfg[s.Storage] = s
			}
		}
		retry = retry || r
	}
	seen := map[string]bool{}
	for _, g := range res {
		if g.Type != "qemu" && g.Type != "lxc" || g.Template.bool() {
			continue
		}
		key := g.guestKey()
		seen[key] = true
		if only != nil && !only[key] || !in.nodeOnline(g.Node, res) {
			continue // a call for an offline node's guest only times out; its last config stays
		}
		var raw map[string]any
		err := in.get(ctx, p("nodes", g.Node, g.Type, strconv.Itoa(int(g.VMID)), "config"), nil, &raw)
		ok, r, fatal := in.soft(ctx, "guest-config", "the config of "+key, err)
		if fatal != nil {
			return fatal
		}
		if ok {
			in.guestCfg[key] = parseGuestConfig(raw)
		}
		retry = retry || r
		if want.Watched && (want.WantsKind("vm") || want.WantsKind("lxc")) && g.Status == "running" {
			if err := in.refreshAddr(ctx, g); err != nil {
				return err
			}
		}
	}
	if only == nil {
		for key := range in.guestCfg {
			if !seen[key] {
				delete(in.guestCfg, key)
				delete(in.addrs, key)
			}
		}
		if !retry {
			in.at["config"] = now
		}
	}
	return nil
}

// refreshAddr reads a running guest's addresses: LXC interfaces on any
// version, the QEMU guest agent where the adapter says it's readable. A
// guest without an agent answers 500; that's not worth a warning.
func (in *instance) refreshAddr(ctx context.Context, g resource) error {
	key, vmid := g.guestKey(), strconv.Itoa(int(g.VMID))
	cfg := in.guestCfg[key]
	mac := ""
	if cfg != nil {
		mac = cfg.mac
	}
	switch {
	case g.Type == "lxc":
		var ifs []lxcIface
		err := in.get(ctx, p("nodes", g.Node, "lxc", vmid, "interfaces"), nil, &ifs)
		if fatal := quiet(ctx, err); fatal != nil {
			return fatal
		}
		if err == nil {
			if a, ok := primaryLXC(ifs, mac); ok {
				in.addrs[key] = a
			}
		}
	case in.ad != nil && in.ad.agentIPs && cfg != nil && cfg.agent:
		var ai agentIfaces
		err := in.get(ctx, p("nodes", g.Node, "qemu", vmid, "agent", "network-get-interfaces"), nil, &ai)
		if fatal := quiet(ctx, err); fatal != nil {
			return fatal
		}
		if err == nil {
			if a, ok := primaryAgent(ai, mac); ok {
				in.addrs[key] = a
			}
		}
	}
	return nil
}

// quiet drops an optional call's error unless it must fail the sync.
func quiet(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == http.StatusUnauthorized {
		return err
	}
	return nil
}

// refreshBackups reads the backup jobs and the guests no job covers.
func (in *instance) refreshBackups(ctx context.Context, now time.Time) error {
	var jobs []backupJob
	err := in.get(ctx, "/cluster/backup", nil, &jobs)
	ok, retry, fatal := in.soft(ctx, "backups", "the backup jobs", err)
	if fatal != nil {
		return fatal
	}
	if ok {
		in.jobs, in.haveJobs = nil, true
		for _, j := range jobs {
			if j.Type == "" || j.Type == "vzdump" {
				in.jobs = append(in.jobs, j)
			}
		}
	}
	var nb []notBackedUp
	err = in.get(ctx, "/cluster/backup-info/not-backed-up", nil, &nb)
	ok, r, fatal := in.soft(ctx, "backups", "the backup coverage", err)
	if fatal != nil {
		return fatal
	}
	if ok {
		in.notBkp = nb
	}
	if !(retry || r) {
		in.at["backup"] = now
	}
	return nil
}

// configTaskTypes are tasks after which guest configs are re-read.
var configTaskTypes = map[string]bool{
	"qmconfig": true, "qmcreate": true, "qmdestroy": true, "qmclone": true, "qmrestore": true, "qmigrate": true, "qmmove": true,
	"vzcreate": true, "vzdestroy": true, "vzrestore": true, "vzmigrate": true, "vzclone": true, "hamigrate": true, "hastart": true,
}

// configChanged reports a config-changing task since the configs were read.
func (in *instance) configChanged() bool {
	at, ok := in.at["config"]
	if !ok {
		return true
	}
	for _, t := range in.tasks {
		if configTaskTypes[t.Type] && t.StartTime >= at.Unix() {
			return true
		}
	}
	return false
}

// backupFinished reports a vzdump task that ended since the jobs were read.
func (in *instance) backupFinished() bool {
	at, ok := in.at["backup"]
	if !ok {
		return true
	}
	for _, t := range in.tasks {
		if t.Type == "vzdump" && t.EndTime >= at.Unix() {
			return true
		}
	}
	return false
}

// unseenGuests are guests whose config hasn't been read yet.
func (in *instance) unseenGuests(res []resource) map[string]bool {
	var out map[string]bool
	for _, g := range res {
		if (g.Type == "qemu" || g.Type == "lxc") && !g.Template.bool() {
			if _, ok := in.guestCfg[g.guestKey()]; !ok {
				if out == nil {
					out = map[string]bool{}
				}
				out[g.guestKey()] = true
			}
		}
	}
	return out
}

// nodeNames lists the domain's nodes, from the cluster status and the
// resources.
func (in *instance) nodeNames(res []resource) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, s := range in.status {
		if s.Type == "node" {
			add(s.Name)
		}
	}
	for _, r := range res {
		if r.Type == "node" {
			add(r.Node)
		}
	}
	return out
}

// nodeOnline says whether a node answers: the cluster status when it has the
// node, else the resources.
func (in *instance) nodeOnline(n string, res []resource) bool {
	for _, s := range in.status {
		if s.Type == "node" && s.Name == n {
			return s.Online.bool()
		}
	}
	for _, r := range res {
		if r.Type == "node" && r.Node == n {
			return r.Status == "online"
		}
	}
	return false
}
