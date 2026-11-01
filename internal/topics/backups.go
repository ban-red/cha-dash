package topics

import (
	"context"
	"encoding/json"

	"dash.cha.sh/internal/realtime"
)

// BackupItems is the backups topic's collection, keyed by the protected
// resource's ID: {"items": {id: item}}. internal/backups defines an item.
const BackupItems = "items"

// BackupsView is how one viewer sees the coverage map. Whole: every item
// (viewer and up, and household when a board shows backup coverage).
// Otherwise only items Relevant accepts: the protected resource, what
// covers it or the guest it inherits from is on one of the viewer's
// boards. The zero value shows nothing.
type BackupsView struct {
	Whole    bool
	Relevant func(resource string) bool
}

// Sees reports whether the viewer sees the item raw.
func (v BackupsView) Sees(raw json.RawMessage) bool {
	if v.Whole {
		return true
	}
	if v.Relevant == nil {
		return false
	}
	var it struct {
		Resource string `json:"resource"`
		Through  string `json:"through"`
		By       []struct {
			ID     string `json:"id"`
			Target string `json:"target"`
		} `json:"by"`
	}
	if json.Unmarshal(raw, &it) != nil {
		return false
	}
	ids := []string{it.Resource, it.Through}
	for _, c := range it.By {
		ids = append(ids, c.ID, c.Target)
	}
	for _, id := range ids {
		if id != "" && v.Relevant(id) {
			return true
		}
	}
	return false
}

// Project returns the coverage map's items the viewer sees.
func (v BackupsView) Project(items map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(items))
	for id, raw := range items {
		if v.Sees(raw) {
			out[id] = raw
		}
	}
	return out
}

// BackupsSource serves the backups topic per viewer (view reads the
// viewer from the connection's context). What household sees is decided
// once per item, when the feed first sees it, as for tasks; the boards
// are read when the subscription opens.
func (s *Set) BackupsSource(view func(ctx context.Context) BackupsView) realtime.Source {
	return realtime.ViewerDocSource{Doc: s.Backups, Topic: Backups, Coll: BackupItems,
		Visible: func(ctx context.Context) func(string, json.RawMessage) bool {
			v := view(ctx)
			return func(_ string, raw json.RawMessage) bool { return v.Sees(raw) }
		}}
}
