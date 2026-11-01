package apidoc

import (
	"dash.cha.sh/internal/notify"
	"dash.cha.sh/internal/pvehook"
	"dash.cha.sh/internal/timeline"
)

// Mirrors of what the operations handlers (wave 28) write: the timeline
// (internal/server/timeline.go), PVE's webhook (internal/pvehook) and the
// morning digest (internal/notify/digest.go).
type (
	pveHookStatus struct {
		// Status: queued (a sync starts now), coalesced (one started in the
		// last 15 s) or not_running (the instance is off).
		Status string `json:"status"`
	}
)

func init() {
	add("operations", map[string]Op{
		"GET /api/timeline": {Summary: "Incidents, actions, health changes and maintenance windows on one time axis, oldest first, as the caller's role sees them.",
			Query: []Param{
				{Name: "resource", Desc: "One resource's timeline; without it, the whole home."},
				{Name: "from", Desc: "Unix milliseconds; a week before to by default."},
				{Name: "to", Desc: "Unix milliseconds; now by default. At most 92 days after from."},
			},
			Resp: timeline.Timeline{}, Errors: []string{"bad_resource", "bad_range", "timeline_failed"}},
		"POST /api/hooks/pve/{instance}": {Summary: "PVE's notification webhook, proven by the instance's secret in the X-Chadash-Webhook-Secret header. The body is never read; it makes the instance sync now.",
			BodyMedia: "*/*", Status: 202, Resp: pveHookStatus{}, Errors: []string{"unauthorized", "too_large", "rate_limited"}},
		"GET /api/integrations/instances/{id}/pve-hook": {Summary: "Whether a Proxmox instance has a webhook secret, and where PVE sends notifications.",
			Resp: pvehook.View{}, Errors: []string{"not_found", "not_proxmox"}},
		"POST /api/integrations/instances/{id}/pve-hook": {Summary: "Make a Proxmox instance's webhook secret, replacing any. The secret is in this answer only.",
			Resp: pvehook.View{}, Errors: []string{"not_found", "not_proxmox", "unavailable"}},
		"DELETE /api/integrations/instances/{id}/pve-hook": {Summary: "Remove a Proxmox instance's webhook secret; the webhook refuses everything until a new one.",
			Errors: []string{"not_found", "not_proxmox", "not_set"}},
		"GET /api/notify/digest": {Summary: "The morning digest: its time in the hub's time zone, all quiet, and when it last went and goes next.", Resp: notify.DigestView{}},
		"PUT /api/notify/digest": {Summary: "Change the digest's time or all quiet. Its rule, digest, turns it on and routes it.",
			Body: notify.DigestUpdate{}, Resp: notify.DigestView{}, Errors: []string{"bad_time", "nothing_to_change"}},
	})
}
