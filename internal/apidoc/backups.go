package apidoc

import "dash.cha.sh/internal/backups"

// Mirrors of what the backup coverage routes write.
type backupsBody struct {
	Items map[string]backups.Item `json:"items"`
}

func init() {
	add("backups", map[string]Op{
		"GET /api/backups": {Summary: "The backup coverage map as your role sees it: every guest, container with volumes, share and flash drive, what backs it up and how fresh.",
			Resp: backupsBody{}},
		"PUT /api/backups/objectives/{resource}": {Summary: "Set how old a resource's newest good backup may be before it's stale. {resource} is path-escaped.",
			Body: backups.ObjectiveRequest{}, Resp: backups.Objective{}, Errors: []string{"invalid", "not_found"}},
		"DELETE /api/backups/objectives/{resource}": {Summary: "Clear a resource's objective, so its schedule's default applies again.",
			Resp: backups.Objective{}, Errors: []string{"not_set"}},
	})
}
