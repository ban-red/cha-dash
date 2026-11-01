package correlate

import "testing"

// A backup tool's target (a spec with backups.targets) says what it backs
// up; the correlator turns each entry into backed_up_by.
func TestCoversJoinsTargets(t *testing.T) {
	const tgt = "backrest:i_b/plan/nightly"
	h := jupiter(t).
		add("unraid:i_u/share/appdata", "share", "appdata").rel("unraid:i_u/share/appdata", "part_of", "unraid:i_u/array").
		add("unraid:i_u/share/media", "share", "media").rel("unraid:i_u/share/media", "part_of", "unraid:i_u/array").
		add("unraid:i_u/share/isos", "share", "isos").rel("unraid:i_u/share/isos", "part_of", "unraid:i_u/host").
		add("unraid:i_u/disk/flash", "disk", "flash", "role", "boot").rel("unraid:i_u/disk/flash", "part_of", "unraid:i_u/host").
		add("docker:i_d/container/paperless", "container", "paperless", "container_id", "ee"+fullID[2:12], "mounts", []any{
			map[string]any{"type": "bind", "source": "/mnt/user/appdata/paperless/data", "target": "/data"},
			map[string]any{"type": "bind", "source": "/mnt/user/appdata/paperless/media/", "target": "/media"},
		}).
		rel("docker:i_d/container/paperless", "runs_on", "docker:i_d/engine").
		add("docker:i_d/container/plex", "container", "plex", "container_id", "ff"+fullID[2:12], "mounts", []any{
			map[string]any{"type": "bind", "source": "/mnt/user/appdata/plex", "target": "/config"},
			map[string]any{"type": "bind", "source": "/mnt/user/media", "target": "/media"},
		}).
		rel("docker:i_d/container/plex", "runs_on", "docker:i_d/engine").
		add("proxmox:i_p/qemu/100", "vm", "pvedocker", "ip", "10.20.30.40").
		add("proxmox:i_p/qemu/101", "vm", "twin", "ip", "10.20.30.41").
		add("proxmox:i_p/qemu/102", "vm", "twin", "ip", "10.20.30.42").
		add(tgt, "backup_target", "nightly", "caps", []any{"backups.targets"}, "backup.covers", []any{
			"10.20.30.40",
			map[string]any{"host": "jupiter.lab.example", "path": "/mnt/user/appdata"},
			map[string]any{"host": "jupiter", "path": "/boot"},
			map[string]any{"resource": "unraid:i_u/share/isos"},
			map[string]any{"host": "twin"},
		}).
		// Without the capability, covers mean nothing.
		add("other:i_o/target/x", "backup_target", "x", "backup.covers", []any{"10.20.30.40"})
	r := h.run()
	mustLink(t, r, "proxmox:i_p/qemu/100", BackedUpBy, tgt, Strong, "covers")
	mustLink(t, r, "unraid:i_u/share/appdata", BackedUpBy, tgt, Strong, "covers")
	mustLink(t, r, "unraid:i_u/disk/flash", BackedUpBy, tgt, Strong, "covers")
	mustLink(t, r, "unraid:i_u/share/isos", BackedUpBy, tgt, Exact, "covers")
	mustLink(t, r, "docker:i_d/container/paperless", BackedUpBy, tgt, Strong, "covers")
	// A mount outside the path: plex's media isn't backed up.
	noLink(t, r, "docker:i_d/container/plex", BackedUpBy, tgt)
	noLink(t, r, "unraid:i_u/share/media", BackedUpBy, tgt)
	// A name two guests share joins nothing.
	noLink(t, r, "proxmox:i_p/qemu/101", BackedUpBy, tgt)
	noLink(t, r, "proxmox:i_p/qemu/102", BackedUpBy, tgt)
	noLink(t, r, "proxmox:i_p/qemu/100", BackedUpBy, "other:i_o/target/x")
	for _, s := range r.Suggestions {
		if s.Type == BackedUpBy {
			t.Errorf("coverage is never a suggestion: %+v", s)
		}
	}
	l, _ := hasLink(r, "unraid:i_u/share/appdata", BackedUpBy, tgt)
	if want := "nightly backs up jupiter.lab.example:/mnt/user/appdata, on jupiter."; l.Evidence != want {
		t.Fatalf("evidence %q, want %q", l.Evidence, want)
	}
}
