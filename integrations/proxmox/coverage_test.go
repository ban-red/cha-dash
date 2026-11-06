package proxmox

import (
	"slices"
	"testing"
	"time"

	"dash.cha.sh/internal/backups"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// coverJob puts ct-200 into minig5's one backup job, the way adding it to
// the job in PVE's Datacenter → Backup does: the job's vmid list grows and
// the guest leaves not-backed-up. Editing a job makes no task, so nothing
// in /cluster/tasks says it happened.
func coverJob(t *testing.T, srv *replay) {
	t.Helper()
	edit(t, srv, "/cluster/backup", "cluster/backup.json", func(d []map[string]any) {
		d[0]["vmid"] = "100,200"
	})
	srv.set("/cluster/backup-info/not-backed-up", 200, `{"data":[]}`)
}

// coverage is ct-200's row on the coverage map, computed from what the
// integration published.
func coverage(t *testing.T, h *integrationtest.Harness) backups.Item {
	t.Helper()
	it, ok := backups.Compute(h.Graph(), nil, h.Now())[pid("lxc/200")]
	if !ok {
		t.Fatal("ct-200 has no coverage row")
	}
	return it
}

func uncovered(t *testing.T, h *integrationtest.Harness, when string) {
	t.Helper()
	if it := coverage(t, h); it.State != backups.Uncovered || len(it.By) != 0 {
		t.Fatalf("%s: row = %s by %v, want uncovered", when, it.State, it.By)
	}
	if attr(res(t, h, "lxc/200"), "backup.covered") != false {
		t.Fatalf("%s: backup.covered = %v", when, attr(res(t, h, "lxc/200"), "backup.covered"))
	}
	if inc, ok := h.Incident("hint.not-backed-up"); !ok || inc.Status != integration.StatusOpen || inc.Scenario != "backup-coverage-gap" {
		t.Fatalf("%s: coverage hint = %+v", when, inc)
	}
}

func covered(t *testing.T, h *integrationtest.Harness) {
	t.Helper()
	job := pid("backup/backup-d22893c3-d0bb")
	it := coverage(t, h)
	if it.State == backups.Uncovered || !slices.ContainsFunc(it.By, func(c backups.Cover) bool { return c.ID == job }) {
		t.Fatalf("row = %s by %v, want covered by %s", it.State, it.By, job)
	}
	if attr(res(t, h, "lxc/200"), "backup.covered") != true || !hasRel(h, pid("lxc/200"), "backed_up_by", job) {
		t.Fatalf("ct-200 = %+v", res(t, h, "lxc/200"))
	}
	if inc, _ := h.Incident("hint.not-backed-up"); inc.Status != integration.StatusResolved || inc.ResolvedBy != "auto" {
		t.Fatalf("coverage hint = %+v", inc)
	}
}

// TestCoverageAfterAJobChange (14b, item 7): ct-200 is in no job, then
// someone adds it to the weekly job in PVE. Its coverage row turns covered
// and the coverage-gap hint resolves.
//
// An ordinary poll doesn't see the change at once: a job edit makes no
// task, so the jobs are re-read at the backup tier's pace (15 minutes,
// read after 13.5). Check now (a Refresh) reads them on that sync.
func TestCoverageAfterAJobChange(t *testing.T) {
	t.Run("poll", func(t *testing.T) {
		srv, h, in := connect(t, "minig5")
		syncOK(t, in, integrationtest.Watched)
		uncovered(t, h, "before")
		coverJob(t, srv)
		// Live polls every 15 s for 13 minutes: the row stays uncovered.
		for range 13 * 4 {
			h.Advance(liveInterval)
			syncOK(t, in, integrationtest.Watched)
		}
		uncovered(t, h, "13 minutes after the edit")
		// The backup tier comes due: this sync re-reads the jobs.
		for range 2 {
			h.Advance(liveInterval)
			syncOK(t, in, integrationtest.Watched)
		}
		covered(t, h)
		if u := srv.unknownPaths(); len(u) > 0 {
			t.Fatalf("asked for uncaptured paths: %v", u)
		}
	})
	t.Run("refresh", func(t *testing.T) {
		srv, h, in := connect(t, "minig5")
		syncOK(t, in, integrationtest.Watched)
		uncovered(t, h, "before")
		coverJob(t, srv)
		h.Advance(liveInterval)
		syncOK(t, in, integrationtest.Watched)
		uncovered(t, h, "the next ordinary poll")
		h.Advance(time.Second)
		refresh := integrationtest.Watched
		refresh.Refresh = true
		syncOK(t, in, refresh)
		covered(t, h)
	})
}
