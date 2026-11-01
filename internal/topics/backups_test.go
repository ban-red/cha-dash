package topics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/realtime"
)

// The backups topic per viewer: whole, only what the boards show (the
// resource, what covers it, the guest it inherits from), or nothing.
func TestBackupsSourcePerViewer(t *testing.T) {
	s := New(graph.New(), time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	s.Backups.Update(func(tx *livedoc.Tx) error {
		tx.Put(BackupItems, "proxmox:i_p/qemu/100", json.RawMessage(`{"resource":"proxmox:i_p/qemu/100","state":"fresh","by":[{"id":"proxmox:i_p/backup/daily","target":"pbs:i_b/datastore/main"}]}`))
		tx.Put(BackupItems, "docker:i_d/container/paperless", json.RawMessage(`{"resource":"docker:i_d/container/paperless","state":"fresh","through":"proxmox:i_p/qemu/100","by":[]}`))
		tx.Put(BackupItems, "unraid:i_u/share/appdata", json.RawMessage(`{"resource":"unraid:i_u/share/appdata","state":"uncovered","by":[]}`))
		return nil
	})
	type who struct{}
	src := s.BackupsSource(func(ctx context.Context) BackupsView {
		switch ctx.Value(who{}) {
		case "viewer":
			return BackupsView{Whole: true}
		case "household-datastore":
			return BackupsView{Relevant: func(id string) bool { return id == "pbs:i_b/datastore/main" }}
		case "household-guest":
			return BackupsView{Relevant: func(id string) bool { return id == "proxmox:i_p/qemu/100" }}
		case "household-share":
			return BackupsView{Relevant: func(id string) bool { return id == "unraid:i_u/share/appdata" }}
		}
		return BackupsView{}
	})
	seen := func(name string) map[string]json.RawMessage {
		f, err := realtime.OpenWith(context.WithValue(context.Background(), who{}, name), src, Backups, make(chan struct{}, 1))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		_, doc := f.Snapshot()
		return doc.(map[string]any)[BackupItems].(map[string]json.RawMessage)
	}
	for name, want := range map[string][]string{
		"viewer":              {"proxmox:i_p/qemu/100", "docker:i_d/container/paperless", "unraid:i_u/share/appdata"},
		"household-datastore": {"proxmox:i_p/qemu/100"},
		"household-guest":     {"proxmox:i_p/qemu/100", "docker:i_d/container/paperless"},
		"household-share":     {"unraid:i_u/share/appdata"},
		"stranger":            nil,
	} {
		got := seen(name)
		if len(got) != len(want) {
			t.Errorf("%s sees %d items, want %d", name, len(got), len(want))
		}
		for _, id := range want {
			if _, ok := got[id]; !ok {
				t.Errorf("%s doesn't see %s", name, id)
			}
		}
	}

	// Project is the same view for GET /api/backups.
	_, doc := s.Backups.Snapshot()
	items := doc[BackupItems].(map[string]json.RawMessage)
	if got := (BackupsView{Relevant: func(id string) bool { return id == "unraid:i_u/share/appdata" }}).Project(items); len(got) != 1 {
		t.Errorf("Project = %v", got)
	}
	if got := (BackupsView{}).Project(items); len(got) != 0 {
		t.Errorf("the zero view sees %d", len(got))
	}
}
