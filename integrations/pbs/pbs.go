// Package pbs is the Proxmox Backup Server integration: the server as an
// app resource and each datastore as a backup target, with its usage, the
// estimate of when it fills up, garbage collection, prune and verify jobs,
// and the tasks that failed in the last day. It reads with a token holding
// the Audit role on /; an optional control token lets it prune a datastore
// by its retention policy (actions.go). See README.md.
package pbs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "pbs"

// The polling plan. Usage and failed tasks every sync (60 s while watched,
// the idle interval otherwise); the job status and the node every 5
// minutes; the version and the token's privileges hourly.
const (
	liveInterval = time.Minute
	idleInterval = 5 * time.Minute
	syncTimeout  = 30 * time.Second
	jobsEvery    = 5 * time.Minute
	rareEvery    = time.Hour
	taskWindow   = 24 * time.Hour
	taskLimit    = 1000
	minMajor     = 3 // PBS 3 (Debian 12) and 4 (Debian 13)
	testedMajor  = 4
)

const tokenIDPattern = `^[^\s@!]+@[^\s@!]+![A-Za-z][A-Za-z0-9._-]*$`

func init() { integration.Register(Integration{}) }

// Integration is the PBS integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	tokenID := &integration.Schema{Type: "string", Title: "Token ID", Pattern: tokenIDPattern,
		Description: "The API token, as user@realm!name (e.g. chadash@pbs!dash). The Audit role on / is enough."}
	controlID := &integration.Schema{Type: "string", Title: "Control token ID", Pattern: tokenIDPattern, Group: integration.GroupControl,
		Description: "Optional. A second token that lets Cha Dash prune datastores by their retention policy, as user@realm!name (e.g. chadash-control@pbs!control)."}
	return integration.Manifest{
		ID: ID, Name: "Proxmox Backup Server", Icon: "proxmox-backup-server", Version: "0.2.0", Tier: integration.Silver,
		Category:     integration.CatHosts,
		Capabilities: []integration.Capability{"backups.targets", "backups.jobs"},
		ConfigSchema: integration.Object(
			integration.Prop("url", integration.URL("URL").Describe("Where PBS answers, e.g. https://pbs.lab.example:8007.")),
			integration.TLSProp(),
			integration.Prop("token_id", tokenID),
			integration.Prop("token_secret", integration.Secret("Token secret")),
			integration.Prop("control_token_id", controlID),
			integration.Prop("control_token_secret", integration.Secret("Control token secret").InGroup(integration.GroupControl)),
		).Require("url", "token_id", "token_secret"),
		Control: &integration.Control{
			Fields: []integration.ControlField{
				{Field: "control_token_id", Replaces: "token_id"},
				{Field: "control_token_secret", Replaces: "token_secret"},
			},
			Missing: "Add a control token to {instance} to let Cha Dash prune its datastores. The recipe's control variant makes one.",
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
	}
	if err := cfg.Decode(&c); err != nil {
		return nil, &integration.ConfigError{Message: err.Error()}
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return nil, &integration.ConfigError{Message: "the url needs a host", Field: "url"}
	}
	u.RawQuery, u.Fragment = "", ""
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	return &instance{d: d, key: key, name: cfg.Name, base: strings.TrimRight(u.String(), "/"),
		auth: "PBSAPIToken=" + c.TokenID + ":" + c.TokenSecret, at: map[string]time.Time{},
		hidden: map[string]string{}, raised: map[string]bool{}}, nil
}

type instance struct {
	d    integration.Deps
	key  string // the instance ID
	name string // the instance's name, the app resource's name
	base string
	auth string

	at      map[string]time.Time
	version string
	newer   bool
	perms   privileges

	usage      []datastoreUsage
	tasks      []task
	haveTasks  bool
	stores     map[string]datastoreItem // /admin/datastore: maintenance
	gc         map[string]gcJob
	prune      []pruneJob
	verify     []verifyJob
	node       *nodeStatus
	nodeAt     time.Time
	gcMissing  bool // /admin/gc isn't there (older PBS): GC comes from datastore-usage alone
	jobMissing bool // the same for /admin/prune and /admin/verify
	havePrune  bool // the prune jobs were read, so "none" means none

	hidden map[string]string // part → why the token can't read it
	notes  []integration.Warning
	raised map[string]bool
}

// ── API shapes (pbs-api-types) ──────────────────────────────────────────

// num is a JSON number that also accepts booleans, numeric strings and null.
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	switch s {
	case "", "null", "false":
		*n = 0
		return nil
	case "true":
		*n = 1
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*n = 0
		return nil
	}
	*n = num(f)
	return nil
}

// versionInfo is GET /version. PBS splits the version differently from PVE:
// version is major.minor ("4.0") and release is the patch level ("14").
type versionInfo struct {
	Version string `json:"version"`
	Release string `json:"release"`
}

func (v versionInfo) full() string {
	if v.Release == "" {
		return v.Version
	}
	return v.Version + "." + v.Release
}

// gcStatus is GarbageCollectionStatus.
type gcStatus struct {
	UPID          string `json:"upid"`
	DiskBytes     num    `json:"disk-bytes"`
	RemovedBytes  num    `json:"removed-bytes"`
	PendingBytes  num    `json:"pending-bytes"`
	IndexDataSize num    `json:"index-data-bytes"`
	StillBad      num    `json:"still-bad"`
}

// datastoreUsage is one DataStoreStatusListItem of GET
// /status/datastore-usage. Totals are absent when the token may only see
// that the datastore exists.
type datastoreUsage struct {
	Store       string    `json:"store"`
	Total       *num      `json:"total"`
	Used        *num      `json:"used"`
	Avail       *num      `json:"avail"`
	MountStatus string    `json:"mount-status"` // mounted, notmounted, nonremovable
	Backend     string    `json:"backend-type"` // filesystem, s3
	FullDate    *num      `json:"estimated-full-date"`
	Error       string    `json:"error"`
	GC          *gcStatus `json:"gc-status"`
}

// datastoreItem is a DataStoreListItem of GET /admin/datastore.
type datastoreItem struct {
	Store       string `json:"store"`
	Maintenance string `json:"maintenance"`
}

// schedule is JobScheduleStatus, flattened into every job's status.
type schedule struct {
	NextRun     *num   `json:"next-run"`
	LastState   string `json:"last-run-state"`
	LastEndtime *num   `json:"last-run-endtime"`
	LastUPID    string `json:"last-run-upid"`
}

// gcJob is a GarbageCollectionJobStatus of GET /admin/gc.
type gcJob struct {
	Store       string `json:"store"`
	Schedule    string `json:"schedule"`
	NextRun     *num   `json:"next-run"`
	LastState   string `json:"last-run-state"`
	LastEndtime *num   `json:"last-run-endtime"`
}

// keepOptions are the retention settings.
type keepOptions struct {
	Last    num `json:"keep-last"`
	Hourly  num `json:"keep-hourly"`
	Daily   num `json:"keep-daily"`
	Weekly  num `json:"keep-weekly"`
	Monthly num `json:"keep-monthly"`
	Yearly  num `json:"keep-yearly"`
}

// pruneJob is a PruneJobStatus of GET /admin/prune.
type pruneJob struct {
	ID       string `json:"id"`
	Store    string `json:"store"`
	Disable  num    `json:"disable"`
	Schedule string `json:"schedule"`
	NS       string `json:"ns"`
	MaxDepth *num   `json:"max-depth"`
	keepOptions
	schedule
}

// verifyJob is a VerificationJobStatus of GET /admin/verify.
type verifyJob struct {
	ID       string `json:"id"`
	Store    string `json:"store"`
	Schedule string `json:"schedule"`
	schedule
}

// nodeStatus is GET /nodes/localhost/status.
type nodeStatus struct {
	Memory struct {
		Total num `json:"total"`
		Used  num `json:"used"`
	} `json:"memory"`
	Root struct {
		Total num `json:"total"`
		Used  num `json:"used"`
	} `json:"root"`
	Uptime  num `json:"uptime"`
	CPU     num `json:"cpu"`
	CPUInfo struct {
		Model string `json:"model"`
	} `json:"cpuinfo"`
}

// task is a TaskListItem of GET /nodes/localhost/tasks.
type task struct {
	UPID       string `json:"upid"`
	WorkerType string `json:"worker_type"`
	WorkerID   string `json:"worker_id"`
	User       string `json:"user"`
	StartTime  int64  `json:"starttime"`
	EndTime    int64  `json:"endtime"`
	Status     string `json:"status"` // OK, WARNINGS: n, the error, unknown; empty while running
}

// failed reports a finished task that didn't end OK or with warnings.
func (t task) failed() bool {
	return t.EndTime != 0 && t.Status != "" && t.Status != "OK" && !strings.HasPrefix(t.Status, "WARNINGS")
}

// store is the datastore a task worked on, from its worker ID ("store-2:
// verify-weekly", "mars:ct/101", "pve.lab.example").
func (t task) store() string {
	switch t.WorkerType {
	case "syncjob":
		// remote:remote-store:local-store:ns:job
		if f := strings.Split(t.WorkerID, ":"); len(f) >= 3 {
			return f[2]
		}
		return ""
	}
	s, _, _ := strings.Cut(t.WorkerID, ":")
	return s
}

// ── client ──────────────────────────────────────────────────────────────

// apiError is a non-2xx answer, unwrapping to the framework's StatusError
// so a 401 or 403 on a call the integration can't do without becomes an
// auth Repair.
type apiError struct {
	*integration.StatusError
	Reason string
}

func (e *apiError) Error() string {
	msg := e.StatusError.Error()
	if e.Reason != "" && e.Reason != http.StatusText(e.Code) {
		msg += ": " + e.Reason
	}
	return msg
}

func (e *apiError) Unwrap() error { return e.StatusError }

func statusOf(err error) int {
	if ae, ok := errors.AsType[*apiError](err); ok {
		return ae.Code
	}
	return 0
}

func (in *instance) get(ctx context.Context, path string, q url.Values, dst any) error {
	u := in.base + "/api2/json" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return &integration.ConfigError{Message: "the url can't be used: " + err.Error(), Field: "url"}
	}
	req.Header.Set("Authorization", in.auth)
	req.Header.Set("Accept", "application/json")
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := integration.CheckResponse(resp); err != nil {
		var se *integration.StatusError
		errors.As(err, &se)
		return &apiError{StatusError: se, Reason: reason(resp.Status, body)}
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("GET %s: the answer isn't PBS's JSON: %w", path, err)
	}
	if len(env.Data) == 0 || bytes.Equal(env.Data, []byte("null")) {
		return fmt.Errorf("GET %s: the answer has no data", path)
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		return fmt.Errorf("GET %s: unexpected shape: %w", path, err)
	}
	return nil
}

func reason(status string, body []byte) string {
	code, text, _ := strings.Cut(status, " ")
	if n, err := strconv.Atoi(code); err == nil && text != http.StatusText(n) && text != "" {
		return text
	}
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		return strings.TrimSpace(e.Message)
	}
	return ""
}

// ── Test and Sync ───────────────────────────────────────────────────────

// Close implements integration.Instance.
func (in *instance) Close() error {
	in.d.HTTP.CloseIdleConnections()
	return nil
}

// version reads /version and checks it. /version answers anybody, so it
// proves nothing about the token.
func (in *instance) readVersion(ctx context.Context) (versionInfo, bool, error) {
	var v versionInfo
	if err := in.get(ctx, "/version", nil, &v); err != nil {
		return v, false, err
	}
	major, err := strconv.Atoi(strings.SplitN(v.Version, ".", 2)[0])
	if err != nil {
		return v, false, &integration.RepairError{Code: integration.CodeAPIChanged,
			Message: fmt.Sprintf("Proxmox Backup Server reported a version Cha Dash can't read (%q)", v.Version)}
	}
	if major < minMajor {
		return v, false, &integration.RepairError{Code: integration.CodeUnsupportedVersion,
			Message: fmt.Sprintf("Proxmox Backup Server %s isn't supported: Cha Dash reads 3.x and 4.x. Upgrade it to read it", v.full())}
	}
	return v, major > testedMajor, nil
}

// Test implements integration.Instance: the version, the token's grants
// from /access/permissions (which also proves the token works), and what
// it won't show.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	v, newer, err := in.readVersion(ctx)
	if err != nil {
		return integration.Failed(err)
	}
	var perms privileges
	if err := in.get(ctx, "/access/permissions", nil, &perms); err != nil {
		return integration.Failed(err)
	}
	res := integration.TestResult{OK: true, Version: v.full(), Grants: perms.grants(), Warnings: perms.readWarnings()}
	if newer {
		res.Warnings = append(res.Warnings, newerWarning(v.full()))
	}
	return res
}

func newerWarning(v string) integration.Warning {
	return integration.Warning{Code: "version_untested", Severity: integration.WarnInfo,
		Message: fmt.Sprintf("Proxmox Backup Server %s is newer than this integration has been tested with (up to 4.x).", v)}
}

func (in *instance) due(tier string, every time.Duration, now time.Time) bool {
	at, ok := in.at[tier]
	return !ok || now.Sub(at) >= every-every/10
}

// soft sorts the error of a call the integration can do without: a 401 or
// a cancelled sync fails the sync; a 403 hides the part (retried at the
// tier's pace); anything else is a warning, retried on the next sync.
func (in *instance) soft(ctx context.Context, part, what string, err error) (ok, retry bool, fatal error) {
	if err == nil {
		delete(in.hidden, part)
		return true, false, nil
	}
	if ctx.Err() != nil {
		return false, false, ctx.Err()
	}
	switch statusOf(err) {
	case http.StatusUnauthorized:
		return false, false, err
	case http.StatusForbidden:
		var ae *apiError
		errors.As(err, &ae)
		in.hidden[part] = ae.Reason
		return false, false, nil
	}
	_, _, msg := integration.Classify(err)
	in.notes = append(in.notes, integration.Warning{Code: "partial_read", Severity: integration.WarnWarning,
		Message: fmt.Sprintf("Couldn't read %s; showing what was read before: %s", what, msg)})
	return false, true, nil
}

// missing reports a 404 or 501: this PBS doesn't have the endpoint.
func missing(err error) bool {
	s := statusOf(err)
	return s == http.StatusNotFound || s == http.StatusNotImplemented
}

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, want integration.Interest) error {
	now := in.d.Now()
	in.notes = nil
	if in.version == "" || in.due("rare", rareEvery, now) {
		v, newer, err := in.readVersion(ctx)
		if err != nil {
			return err
		}
		// /version answers anybody: the permissions are what proves the
		// token, so they aren't optional.
		var perms privileges
		if err := in.get(ctx, "/access/permissions", nil, &perms); err != nil {
			return err
		}
		in.version, in.newer, in.perms = v.full(), newer, perms
		in.at["rare"] = now
	}
	var usage []datastoreUsage
	if err := in.get(ctx, "/status/datastore-usage", nil, &usage); err != nil {
		return err
	}
	in.usage = usage
	if in.perms.has("/system/tasks", "Sys.Audit") {
		q := url.Values{"errors": {"1"}, "limit": {strconv.Itoa(taskLimit)}, "since": {strconv.FormatInt(now.Add(-taskWindow).Unix(), 10)}}
		var ts []task
		err := in.get(ctx, "/nodes/localhost/tasks", q, &ts)
		ok, _, fatal := in.soft(ctx, "tasks", "the failed tasks", err)
		if fatal != nil {
			return fatal
		}
		if ok {
			in.tasks, in.haveTasks = ts, true
		}
	} else {
		// Without Sys.Audit on /system/tasks PBS lists only the token's own
		// tasks; counting those as "no failures" would be a lie.
		in.tasks, in.haveTasks = nil, false
	}
	if in.due("jobs", jobsEvery, now) {
		if err := in.refreshJobs(ctx, want, now); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return in.publish(now)
}

// refreshJobs reads maintenance modes and the GC, prune and verify jobs,
// and while someone watches, the server's load.
func (in *instance) refreshJobs(ctx context.Context, want integration.Interest, now time.Time) error {
	retry := false
	read := func(part, what, path string, dst any) (bool, error) {
		err := in.get(ctx, path, nil, dst)
		ok, r, fatal := in.soft(ctx, part, what, err)
		retry = retry || r
		return ok, fatal
	}
	var items []datastoreItem
	if ok, err := read("datastores", "the datastore list", "/admin/datastore", &items); err != nil {
		return err
	} else if ok {
		in.stores = map[string]datastoreItem{}
		for _, it := range items {
			in.stores[it.Store] = it
		}
	}
	if !in.gcMissing {
		var gc []gcJob
		err := in.get(ctx, "/admin/gc", nil, &gc)
		if missing(err) {
			in.gcMissing = true // PBS before 3.3: GC state comes from datastore-usage
		} else if ok, r, fatal := in.soft(ctx, "jobs", "the garbage collection jobs", err); fatal != nil {
			return fatal
		} else if ok {
			in.gc = map[string]gcJob{}
			for _, j := range gc {
				in.gc[j.Store] = j
			}
		} else {
			retry = retry || r
		}
	}
	if !in.jobMissing {
		var prune []pruneJob
		err := in.get(ctx, "/admin/prune", nil, &prune)
		if missing(err) {
			in.jobMissing = true // no prune jobs on this PBS
		} else {
			ok, r, fatal := in.soft(ctx, "jobs", "the prune jobs", err)
			if fatal != nil {
				return fatal
			}
			if ok {
				in.prune, in.havePrune = prune, true
			}
			retry = retry || r
			var verify []verifyJob
			if ok, err := read("jobs", "the verify jobs", "/admin/verify", &verify); err != nil {
				return err
			} else if ok {
				in.verify = verify
			}
		}
	}
	if want.Watched && in.perms.has("/system/status", "Sys.Audit") {
		var st nodeStatus
		if ok, err := read("node", "the server's status", "/nodes/localhost/status", &st); err != nil {
			return err
		} else if ok {
			in.node, in.nodeAt = &st, now
		}
	}
	if !retry {
		in.at["jobs"] = now
	}
	return nil
}
