package proxmox

import (
	"cmp"
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Backup state, from what a read-only token can see: the jobs
// (/cluster/backup), the vzdump tasks (/cluster/tasks) and, for a run that
// failed, its log. An auditor can't list backup volumes, and per-guest
// results only exist in the log.

// runLog is the parsed log of a finished vzdump task.
type runLog struct {
	ok     map[int]bool   // guests that finished
	failed map[int]string // guest → error
}

var (
	logFinishedRE = regexp.MustCompile(`^INFO: Finished Backup of (?:VM|CT) (\d+)`)
	logFailedRE   = regexp.MustCompile(`^ERROR: Backup of (?:VM|CT) (\d+) failed - (.*)$`)
	storageDownRE = regexp.MustCompile(`could not activate storage '([^']+)'`)
	connectRE     = regexp.MustCompile(`(?:Can't connect to|connect to) ([^\s()]+)[^(]*\(([^)]+)\)`)
	pciBusyRE     = regexp.MustCompile(`PCI device '([0-9A-Fa-f:.]+)' already in use by VMID '(\d+)'`)
)

func parseLog(lines []logLine) *runLog {
	l := &runLog{ok: map[int]bool{}, failed: map[int]string{}}
	for _, ln := range lines {
		if m := logFinishedRE.FindStringSubmatch(ln.T); m != nil {
			id, _ := strconv.Atoi(m[1])
			l.ok[id] = true
		} else if m := logFailedRE.FindStringSubmatch(ln.T); m != nil {
			id, _ := strconv.Atoi(m[1])
			l.failed[id] = strings.TrimSpace(m[2])
		}
	}
	return l
}

// unreachable is a run that couldn't reach its target storage: one cause
// behind many failures, often on several PVE instances at once (spike 4.1:
// pluto's and mars's jobs failed on the same 12 of 21 nights, PBS on
// jupiter unreachable).
type unreachable struct {
	storage  string
	endpoint string // host:port, when the error names it
	reason   string // "No route to host"
}

func parseUnreachable(status string) *unreachable {
	m := storageDownRE.FindStringSubmatch(status)
	if m == nil {
		return nil
	}
	u := &unreachable{storage: m[1]}
	if c := connectRE.FindStringSubmatch(status); c != nil {
		u.endpoint, u.reason = c[1], c[2]
	}
	return u
}

// run is one execution of a backup job.
type run struct {
	task    task
	running bool
	failed  bool
	err     string         // the task's error, or the first guest's
	ok      []int          // guests that finished, when known
	bad     map[int]string // guests that failed, when known
	known   bool           // per-guest results are known
	unreach *unreachable
}

// guestRef is a guest as the job selection sees it.
type guestRef struct {
	vmid int
	node string
}

// selection lists the guests a job backs up, or nil when it can't tell (a
// pool).
func (j backupJob) selection(guests []guestRef) []int {
	var out []int
	switch {
	case j.All.bool():
		excl := map[int]bool{}
		for _, s := range splitIDs(j.Exclude) {
			excl[s] = true
		}
		for _, g := range guests {
			if (j.Node == "" || g.node == j.Node) && !excl[g.vmid] {
				out = append(out, g.vmid)
			}
		}
	case j.Pool != "":
		return nil
	default:
		out = splitIDs(j.VMID)
	}
	slices.Sort(out)
	return out
}

func splitIDs(s string) []int {
	var out []int
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// claims reports whether a vzdump task is a scheduled run of the job. PVE
// doesn't record the job in the task, so it's matched the way the GUI's
// history reads: same node (when the job names one); the task's ID is the
// guest for a one-guest job and empty otherwise; and it started within five
// minutes after a scheduled time, judged against next-run (one scheduled
// instant) at the schedule's period. A manual backup (pluto's to usb) or a
// "Run now" isn't a scheduled run.
func (j backupJob) claims(t task, sel []int) bool {
	if t.Type != "vzdump" || (j.Node != "" && t.Node != j.Node) {
		return false
	}
	if !j.All.bool() && j.Pool == "" && len(sel) == 1 {
		if t.ID != strconv.Itoa(sel[0]) {
			return false
		}
	} else if t.ID != "" {
		return false
	}
	if j.NextRun == 0 {
		return true // no schedule instant to compare with: node and selection decide
	}
	const slack = 300
	period := int64(86400)
	if subDaily(j.Schedule) {
		period = 3600
	}
	d := ((t.StartTime-j.NextRun)%period + period) % period
	switch {
	case d <= slack:
		return true
	case period == 86400 && (d >= 3600 && d <= 3600+slack || d >= 86400-3600 && d <= 86400-3600+slack):
		return true // a daylight-saving change between that run and the next
	}
	return false
}

// subDaily reports a schedule that repeats within a day ("*/6:00",
// "0,12:00", "hourly").
func subDaily(s string) bool {
	s = strings.TrimSpace(s)
	if s == "hourly" {
		return true
	}
	f := strings.Fields(s)
	if len(f) == 0 {
		return false
	}
	hour, _, ok := strings.Cut(f[len(f)-1], ":")
	return ok && strings.ContainsAny(hour, "*/,.")
}

// period is how often a job runs, for the "last good backup" age: a week
// for a schedule naming one weekday, else a day.
func (j backupJob) period() time.Duration {
	f := strings.Fields(j.Schedule)
	if len(f) >= 2 && !strings.ContainsAny(f[0], ",.") && isWeekday(f[0]) {
		return 7 * 24 * time.Hour
	}
	if j.Schedule == "weekly" {
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

func isWeekday(s string) bool {
	switch strings.ToLower(s) {
	case "mon", "tue", "wed", "thu", "fri", "sat", "sun":
		return true
	}
	return false
}

// scheduleText is the schedule as people read it: "03:00" → "daily
// 03:00", "sun 05:00" → "weekly sun 05:00"; anything else as written.
var hhmmRE = regexp.MustCompile(`^\d{1,2}:\d{2}$`)

func scheduleText(s string) string {
	f := strings.Fields(s)
	switch {
	case hhmmRE.MatchString(s):
		return "daily " + s
	case len(f) == 2 && isWeekday(f[0]) && hhmmRE.MatchString(f[1]):
		return "weekly " + s
	}
	return s
}

// runs returns the job's runs in the task window, newest first.
func (in *instance) runs(j backupJob, sel []int) []run {
	var out []run
	for _, t := range in.tasks {
		if j.claims(t, sel) {
			out = append(out, in.outcome(t, sel))
		}
	}
	slices.SortFunc(out, func(a, b run) int { return cmp.Compare(b.task.StartTime, a.task.StartTime) })
	return out
}

// outcome reads a task's result: OK means every selected guest finished; a
// task-level error (the storage couldn't be activated) means none did; "job
// errors" means some failed, and only the log says which.
func (in *instance) outcome(t task, sel []int) run {
	r := run{task: t}
	switch {
	case t.running():
		r.running = true
		return r
	case t.ok():
		r.ok, r.known = slices.Clone(sel), sel != nil
		return r
	}
	r.failed, r.err = true, t.Status
	r.unreach = parseUnreachable(t.Status)
	if l := in.logs[t.UPID]; l != nil && (len(l.ok) > 0 || len(l.failed) > 0) {
		r.known, r.bad = true, l.failed
		for id := range l.ok {
			r.ok = append(r.ok, id)
		}
		slices.Sort(r.ok)
		if len(l.failed) > 0 {
			ids := make([]int, 0, len(l.failed))
			for id := range l.failed {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			r.err = l.failed[ids[0]]
		}
		return r
	}
	if t.Status != "job errors" && sel != nil {
		// The whole run failed before any guest: every guest failed with it.
		r.known, r.bad = true, map[int]string{}
		for _, id := range sel {
			r.bad[id] = t.Status
		}
	}
	return r
}

// fetchLogs reads the log of each job's newest run when it failed, once per
// task. An OK run needs no log: every selected guest finished.
func (in *instance) fetchLogs(ctx context.Context, res []resource) error {
	want := map[string]task{}
	guests := guestRefs(res)
	for _, j := range in.jobs {
		sel := j.selection(guests)
		for _, t := range in.tasks {
			if !j.claims(t, sel) || t.running() {
				continue
			}
			if cur, ok := want[j.ID]; !ok || t.StartTime > cur.StartTime {
				want[j.ID] = t
			}
		}
	}
	keep := map[string]bool{}
	for _, t := range want {
		keep[t.UPID] = true
		if t.ok() || in.logs[t.UPID] != nil || !in.nodeOnline(t.Node, res) {
			continue
		}
		var lines []logLine
		err := in.get(ctx, p("nodes", t.Node, "tasks", t.UPID, "log"), map[string][]string{"limit": {strconv.Itoa(logLimit)}}, &lines)
		ok, _, fatal := in.soft(ctx, "task-log", "a backup task's log", err)
		if fatal != nil {
			return fatal
		}
		if ok {
			in.logs[t.UPID] = parseLog(lines)
		}
	}
	for upid := range in.logs {
		if !keep[upid] {
			delete(in.logs, upid)
		}
	}
	return nil
}

func guestRefs(res []resource) []guestRef {
	var out []guestRef
	for _, r := range res {
		if (r.Type == "qemu" || r.Type == "lxc") && !r.Template.bool() {
			out = append(out, guestRef{vmid: int(r.VMID), node: r.Node})
		}
	}
	return out
}
