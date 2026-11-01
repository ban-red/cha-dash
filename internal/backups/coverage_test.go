package backups

import (
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
)

var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// home is a small lab: a PVE cluster with two guests, a daily job to a PBS
// datastore through a storage, and a weekly job; a Docker host on guest
// 100 (pvedocker) reached through its node; an Unraid host with a share
// and its flash drive.
type lab struct{ doc graph.Doc }

func newLab() *lab {
	return &lab{doc: graph.Doc{Resources: map[graph.RID]*graph.Resource{}, Relations: map[string]*graph.Relation{}}}
}

func (l *lab) add(id, kind, name string, attrs map[string]any) *graph.Resource {
	if attrs == nil {
		attrs = map[string]any{}
	}
	r := &graph.Resource{ID: graph.RID(id), Kind: graph.Kind(kind), Name: name, Attrs: attrs, Health: graph.HealthOK}
	l.doc.Resources[r.ID] = r
	return r
}

func (l *lab) rel(from, typ, to, source string) {
	r := &graph.Relation{From: graph.RID(from), Type: graph.RelType(typ), To: graph.RID(to), Source: source}
	l.doc.Relations[r.Key()] = r
}

const (
	guest    = "proxmox:i_pve/qemu/100"
	guest2   = "proxmox:i_pve/lxc/101"
	daily    = "proxmox:i_pve/backup/daily"
	weekly   = "proxmox:i_pve/backup/weekly"
	storage  = "proxmox:i_pve/storage/pve01/pbs-main"
	dstore   = "pbs:i_pbs/datastore/main"
	dhost    = "docker:i_dock/host"
	engine   = "docker:i_dock/engine"
	ctr      = "docker:i_dock/container/paperless"
	bare     = "docker:i_dock/container/whoami"
	uhost    = "unraid:i_ur/host"
	share    = "unraid:i_ur/share/appdata"
	flash    = "unraid:i_ur/disk/flash"
	specTgt  = "backrest:i_br/target/appdata"
	pveSrc   = "integration:i_pve"
	dockSrc  = "integration:i_dock"
	corrSrc  = "correlator"
	urSrc    = "integration:i_ur"
	brSource = "integration:i_br"
)

func baseLab() *lab {
	l := newLab()
	l.add(guest, "vm", "pvedocker", map[string]any{"backup.covered": true, "backup.last_ok": now.Add(-10 * time.Hour), "backup.last_at": now.Add(-10 * time.Hour), "backup.last_status": "ok"})
	l.add(guest2, "lxc", "dns", map[string]any{"backup.covered": true, "backup.last_ok": now.Add(-50 * time.Hour).Format(time.RFC3339)})
	l.add(daily, "backup_job", "daily", map[string]any{"schedule": "daily 03:00", "enabled": true, "last_ok": now.Add(-10 * time.Hour)})
	l.add(weekly, "backup_job", "weekly", map[string]any{"schedule": "weekly sun 05:00", "enabled": true})
	l.add(storage, "storage", "pbs-main", nil)
	l.add(dstore, "backup_target", "main", nil)
	l.rel(guest, "backed_up_by", daily, pveSrc)
	l.rel(guest2, "backed_up_by", weekly, pveSrc)
	l.rel(daily, "stores_on", storage, pveSrc)
	l.rel(storage, "stores_on", dstore, corrSrc)

	l.add(dhost, "host", "pvedocker", nil)
	l.add(engine, "engine", "pvedocker", nil)
	l.add(ctr, "container", "paperless", map[string]any{"mounts": []any{map[string]any{"type": "volume", "source": "/var/lib/docker/volumes/paperless/_data", "target": "/data"}}})
	l.add(bare, "container", "whoami", nil)
	l.rel(ctr, "runs_on", engine, dockSrc)
	l.rel(bare, "runs_on", engine, dockSrc)
	l.rel(engine, "runs_on", dhost, corrSrc)
	l.rel(dhost, "same_as", guest, corrSrc)

	l.add(uhost, "host", "jupiter", nil)
	l.add(share, "share", "appdata", nil)
	l.add(flash, "disk", "flash", map[string]any{"role": "boot"})
	l.rel(share, "part_of", uhost, urSrc)
	l.rel(flash, "part_of", uhost, urSrc)
	return l
}

func TestCoverageFromJobsAndSchedules(t *testing.T) {
	items := Compute(baseLab().doc, nil, now)

	g := items[guest]
	if g.State != Fresh || g.Objective != FromSchedule || g.MaxAgeMS != DefaultDaily.Milliseconds() || !g.LastOK.Equal(now.Add(-10*time.Hour)) {
		t.Fatalf("daily guest: %+v", g)
	}
	if len(g.By) != 1 || g.By[0].ID != daily || g.By[0].Target != dstore || g.By[0].TargetName != "main" {
		t.Fatalf("daily guest's cover: %+v", g.By)
	}
	// 50 hours old on a weekly job: fresh against 8 days.
	if w := items[guest2]; w.State != Fresh || w.MaxAgeMS != DefaultWeekly.Milliseconds() {
		t.Fatalf("weekly guest: %+v", w)
	}
	if s := items[share]; s.State != Uncovered || s.Message != "backups.state_uncovered" {
		t.Fatalf("share: %+v", s)
	}
	if f, ok := items[flash]; !ok || f.State != Uncovered {
		t.Fatalf("flash: %+v", f)
	}
	if _, ok := items[bare]; ok {
		t.Fatal("a container without volumes isn't protectable")
	}
	if _, ok := items[dhost]; ok {
		t.Fatal("a host isn't on the map")
	}
}

func TestStaleAndOverride(t *testing.T) {
	l := baseLab()
	l.doc.Resources[guest].Attrs["backup.last_ok"] = now.Add(-30 * time.Hour)
	l.doc.Resources[daily].Attrs["last_ok"] = now.Add(-30 * time.Hour)
	items := Compute(l.doc, nil, now)
	if g := items[guest]; g.State != Stale || g.Args["objective"] != "26 hours" {
		t.Fatalf("30 hours on a daily job: %+v", g)
	}
	items = Compute(l.doc, map[string]time.Duration{guest: 48 * time.Hour}, now)
	if g := items[guest]; g.State != Fresh || g.Objective != FromOverride || g.MaxAgeMS != (48*time.Hour).Milliseconds() {
		t.Fatalf("with a 48 h objective: %+v", g)
	}
	items = Compute(l.doc, map[string]time.Duration{guest2: 24 * time.Hour}, now)
	if w := items[guest2]; w.State != Stale || w.Objective != FromOverride {
		t.Fatalf("weekly guest held to a day: %+v", w)
	}
}

// A container with volumes on a Docker host that is a PVE guest inherits
// the guest's backup (decision 8), and says so.
func TestContainerInheritsItsGuest(t *testing.T) {
	items := Compute(baseLab().doc, nil, now)
	c := items[ctr]
	if c.State != Fresh || c.Through != guest || c.ThroughName != "pvedocker" || len(c.By) != 1 || c.By[0].ID != daily {
		t.Fatalf("container: %+v", c)
	}
	if want := "Through pvedocker's backup."; len(c.Text) < len(want) || c.Text[len(c.Text)-len(want):] != want {
		t.Fatalf("text: %q", c.Text)
	}
	// Its own objective applies to the inherited backups.
	items = Compute(baseLab().doc, map[string]time.Duration{ctr: 2 * time.Hour}, now)
	if c := items[ctr]; c.State != Stale || c.Through != guest {
		t.Fatalf("container held to 2 h: %+v", c)
	}
	// Without the same_as, nothing covers it.
	l := baseLab()
	delete(l.doc.Relations, (&graph.Relation{From: dhost, Type: "same_as", To: guest}).Key())
	if c := Compute(l.doc, nil, now)[ctr]; c.State != Uncovered || c.Through != "" {
		t.Fatalf("container on an unknown host: %+v", c)
	}
}

// Something that backs the container up directly wins over its guest.
func TestDirectCoverBeatsInheritance(t *testing.T) {
	l := baseLab()
	l.add(specTgt, "backup_target", "appdata", map[string]any{"backup.last_ok": now.Add(-2 * time.Hour).Format(time.RFC3339), "backup.schedule": "hourly", "caps": []any{"backups.targets"}})
	l.rel(ctr, "backed_up_by", specTgt, corrSrc)
	c := Compute(l.doc, nil, now)[ctr]
	if c.State != Fresh || c.Through != "" || len(c.By) != 1 || c.By[0].ID != specTgt || c.By[0].Target != "" {
		t.Fatalf("container backed up by a spec target: %+v", c)
	}
}

func TestUnknownAndDisabled(t *testing.T) {
	l := baseLab()
	// A job that never ran.
	delete(l.doc.Resources[guest2].Attrs, "backup.last_ok")
	if w := Compute(l.doc, nil, now)[guest2]; w.State != Unknown || w.Message != "backups.state_never" {
		t.Fatalf("never ran: %+v", w)
	}
	// Proxmox couldn't read the jobs: no backup.covered, no relations.
	l = baseLab()
	delete(l.doc.Relations, (&graph.Relation{From: guest2, Type: "backed_up_by", To: weekly}).Key())
	delete(l.doc.Resources[guest2].Attrs, "backup.covered")
	if w := Compute(l.doc, nil, now)[guest2]; w.State != Unknown || w.Message != "backups.state_unread" {
		t.Fatalf("jobs unread: %+v", w)
	}
	l.doc.Resources[guest2].Attrs["backup.covered"] = false
	if w := Compute(l.doc, nil, now)[guest2]; w.State != Uncovered {
		t.Fatalf("read and not covered: %+v", w)
	}
	// A disabled job covers nothing.
	l = baseLab()
	l.doc.Resources[daily].Attrs["enabled"] = false
	if g := Compute(l.doc, nil, now)[guest]; g.State != Uncovered || g.Message != "backups.state_disabled" || len(g.By) != 1 || !g.By[0].Disabled {
		t.Fatalf("disabled job: %+v", g)
	}
}

// Stale because the datastore is full: a hint only when the datastore
// itself isn't ok; a failed run's error otherwise.
func TestStaleHints(t *testing.T) {
	l := baseLab()
	l.doc.Resources[guest].Attrs["backup.last_ok"] = now.Add(-40 * time.Hour)
	l.doc.Resources[daily].Attrs["last_ok"] = now.Add(-40 * time.Hour)
	l.doc.Resources[daily].Attrs["last_run.status"] = "failed"
	l.doc.Resources[daily].Attrs["last_run.error"] = "no space left on device"
	l.doc.Resources[guest].Attrs["backup.last_at"] = now.Add(-time.Hour)
	l.doc.Resources[guest].Attrs["backup.last_status"] = "failed"
	l.doc.Resources[guest].Attrs["backup.last_error"] = "no space left on device"
	g := Compute(l.doc, nil, now)[guest]
	if g.State != Stale || g.Cause == nil || g.Cause.Resource != daily || g.Cause.Message != "backups.cause_failed" {
		t.Fatalf("failed job: %+v", g.Cause)
	}
	l.doc.Resources[dstore].Health = graph.HealthAttention
	l.doc.Resources[dstore].Attrs["health.reason"] = "92% used · full in about 3 weeks"
	g = Compute(l.doc, nil, now)[guest]
	if g.Cause == nil || g.Cause.Resource != dstore || g.Cause.Text != "Its backups go to main, which isn't ok: 92% used · full in about 3 weeks." {
		t.Fatalf("full datastore: %+v", g.Cause)
	}
	// Fresh backups get no hint, whatever the datastore says.
	l = baseLab()
	l.doc.Resources[dstore].Health = graph.HealthAttention
	if g := Compute(l.doc, nil, now)[guest]; g.Cause != nil {
		t.Fatalf("fresh with a full datastore: %+v", g.Cause)
	}
}

// Two Proxmox instances on one cluster report one guest: one item.
func TestOneItemPerIdentity(t *testing.T) {
	l := baseLab()
	dup := "proxmox:i_pve2/qemu/100"
	l.add(dup, "vm", "pvedocker", map[string]any{"backup.covered": true})
	l.rel(dup, "same_as", guest, corrSrc)
	items := Compute(l.doc, nil, now)
	if _, ok := items[dup]; ok {
		t.Fatal("a member got its own item")
	}
	if items[guest].State != Fresh {
		t.Fatalf("primary: %+v", items[guest])
	}
}

func TestScheduleObjective(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"daily", DefaultDaily, true},
		{"daily 03:00", DefaultDaily, true},
		{"02:30", DefaultDaily, true},
		{"*/2:00", DefaultDaily, true},
		{"hourly", DefaultDaily, true},
		{"weekly", DefaultWeekly, true},
		{"weekly sun 05:00", DefaultWeekly, true},
		{"sun 01:00", DefaultWeekly, true},
		{"mon..fri 02:00", 4 * day, true},
		{"sat,sun 04:00", 7 * day, true},
		{"mon..sun", DefaultDaily, true},
		{"mon,tue,wed,thu,fri,sat", 3 * day, true},
		{"*-*-01 03:00", 32 * day, true},
		{"sun *-*-1..7 3:00", 32 * day, true},
		{"monthly", 32 * day, true},
		{"0 3 * * *", DefaultDaily, true},
		{"0 3 * * 0", DefaultWeekly, true},
		{"0 3 * * 1-5", 4 * day, true},
		{"0 3 1 * *", 32 * day, true},
		{"", 0, false},
		{"whenever", 0, false},
	} {
		got, ok := ScheduleObjective(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: %v %v, want %v %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
