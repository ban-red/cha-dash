package apidoc

import "dash.cha.sh/internal/updates"

// Mirrors of what internal/updates' handlers read and write.
type (
	updateList struct {
		Updates []updates.Row `json:"updates"`
	}
	askedBody struct {
		Asked int `json:"asked"`
	}
	holdRequest struct {
		Kind string `json:"kind"`
	}
	rolloutList struct {
		Rollouts []updates.Rollout `json:"rollouts"`
	}
	windowList struct {
		Windows []updates.WindowView `json:"windows"`
	}
)

func init() {
	add("updates", map[string]Op{
		"GET /api/updates":                    {Summary: "The Update Center: containers with update checks, PVE hosts' pending packages and Unraid's version.", Resp: updateList{}},
		"POST /api/updates/check":             {Summary: "Check for updates now.", Resp: askedBody{}},
		"POST /api/updates/{resource}/hold":   {Summary: "Hold or snooze a container's update.", Body: holdRequest{}, Resp: updates.Row{}},
		"DELETE /api/updates/{resource}/hold": {Summary: "Release a hold or snooze.", Resp: updates.Row{}, Errors: []string{"not_held"}},
		"DELETE /api/updates/{resource}/rejected": {Summary: "Try again: offer the digest a container's update rolled back from, so a rollout takes it again.",
			Resp: updates.Row{}, Errors: []string{"not_rejected"}},
		"GET /api/updates/{resource}/notes": {Summary: "A container's release notes from GitHub, as untrusted text: the releases since the version it runs.", Resp: updates.Notes{},
			Errors: []string{"release_notes_off", "no_source", "rate_limited", "upstream"}},
		"GET /api/updates/settings":          {Summary: "The Update Center's settings: release notes on or off.", Resp: updates.Settings{}},
		"PUT /api/updates/settings":          {Summary: "Turn release notes on or off.", Body: updates.Settings{}, Resp: updates.Settings{}},
		"POST /api/updates/rollouts/preview": {Summary: "Plan a rollout of the given containers (all available ones when empty), saving nothing.", Body: updates.PreviewRequest{}, Resp: updates.RolloutPlan{}},
		"GET /api/updates/rollouts":          {Summary: "Rollouts still to run, and those of the last 30 days.", Resp: rolloutList{}},
		"POST /api/updates/rollouts/{id}/cancel": {Summary: "Cancel a scheduled or waiting rollout, or stop a running one before its next step (202: the step in progress finishes or rolls back on its own).",
			Resp: updates.Rollout{}, Errors: []string{"not_pending", "stopping"}},
		"GET /api/windows":         {Summary: "The maintenance windows.", Resp: windowList{}},
		"POST /api/windows":        {Summary: "Add a maintenance window: a time range, once or weekly, for a set of hosts.", Body: updates.WindowInput{}, Status: 201, Resp: updates.WindowView{}},
		"PUT /api/windows/{id}":    {Summary: "Change a maintenance window. Rollouts scheduled into it move with it.", Body: updates.WindowInput{}, Resp: updates.WindowView{}},
		"DELETE /api/windows/{id}": {Summary: "Delete a maintenance window. Rollouts scheduled into it are cancelled."},
	})
}
