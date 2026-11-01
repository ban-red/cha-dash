package msg

// Operations (v0.4, wave 28): the incident timeline (internal/timeline,
// internal/server/timeline.go), incident retention in Settings → History,
// the PVE notifications webhook (internal/pvehook) and the morning digest
// (internal/notify/digest.go).
func init() {
	register(map[string]string{
		// The timeline's entries.
		"timeline.opened":         "{title} opened",
		"timeline.reopened":       "{title} opened again",
		"timeline.severity":       "{title} went from {from} to {to}",
		"timeline.phase":          "{title}: {phase}",
		"timeline.caused":         "{title}: {cause}",
		"timeline.acknowledged":   "{title} was acknowledged by {who}",
		"timeline.ack_note":       "{title} was acknowledged by {who}: {note}",
		"timeline.unacknowledged": "{who} took back the acknowledgement of {title}",
		"timeline.expected":       "{title} opened during a maintenance window, so it didn't notify",
		"timeline.resolved":       "{title} resolved",
		"timeline.resolved_by":    "{title} resolved after an action",
		"timeline.action_ok":      "{actor} ran {action} on {target}",
		"timeline.action_failed":  "{actor} ran {action} on {target}, and it failed",
		"timeline.state":          "{name} became {status}",
		"timeline.state_from":     "{name} went from {from} to {status}",
		"timeline.window_start":   "Maintenance window {name} began",
		"timeline.window_end":     "Maintenance window {name} ended",
		// The timeline's errors.
		"timeline.bad_resource": "resource must name a resource, at most 512 characters",
		"timeline.bad_range":    "from and to are Unix milliseconds, from before to, at most {days} days apart",
		"timeline.failed":       "The timeline couldn't be read.",

		// Acknowledging an incident (internal/incidents/http.go).
		"incidents.no_incident":          "There's no such incident in the Inbox.",
		"incidents.not_problem":          "Only a problem can be acknowledged.",
		"incidents.ack_resolved":         "It has resolved, so there's nothing to acknowledge.",
		"incidents.already_acknowledged": "Someone has acknowledged it already.",
		"incidents.not_acknowledged":     "It isn't acknowledged.",
		"incidents.note_long":            "Keep the note under {max} characters.",

		// Settings → History: incident retention.
		"incidents.bad_keep":   "incident_keep_ms is 7, 30, 90, 180 or 365 days in milliseconds",
		"incidents.failed":     "The incident retention couldn't be saved.",
		"history.nothing":      "send level, max_bytes, exclude, exclude_keys, keep or incident_keep_ms",
		"history.locked_parts": "History is off for this hub (CHADASH_HISTORY=off, or demo mode); only incident_keep_ms can change.",

		// The PVE notifications webhook.
		"pvehook.unauthorized": "unauthorized",
		"pvehook.too_large":    "A notification's body can be at most 64 KiB.",
		"pvehook.bad_body":     "The body couldn't be read.",
		"pvehook.rate_limited": "Too many requests; try again shortly.",
		"pvehook.not_found":    "There's no such integration instance.",
		"pvehook.not_proxmox":  "Only a Proxmox VE instance takes PVE's notifications.",
		"pvehook.no_vault":     "The hub has no vault, so it can't keep the webhook's secret.",
		"pvehook.not_set":      "This instance has no webhook secret.",
		"pvehook.failed":       "The webhook's secret couldn't be saved.",
		"pvehook.refused":      "PVE's notification didn't prove itself: {reason}.",

		// The morning digest.
		"digest.title":             "Morning digest",
		"digest.title_quiet":       "All quiet",
		"digest.overnight_none":    "Nothing opened since {since}.",
		"digest.overnight":         "Opened since {since}: {count}, {resolved} of them resolved.",
		"digest.overnight_item":    "· {title}",
		"digest.open_none":         "Nothing is open.",
		"digest.open":              "Open now: {count}.",
		"digest.open_none_unacked": "Nothing open needs attention.",
		"digest.acked":             "Acknowledged, someone is on it: {count}.",
		"digest.acked_item":        "· {title} ({who})",
		"digest.updates":           "Updates waiting: {count}.",
		"digest.backups_stale":     "Backups stale: {names}.",
		"digest.more":              "and {count} more.",
		"digest.quiet_body":        "Nothing opened overnight, nothing is open, no updates are waiting and no backups are stale.",
		"digest.bad_time":          "time is HH:MM, 24-hour, in the hub's time zone",
		"digest.nothing_to_change": "send time or all_quiet",
		"digest.failed":            "The digest's settings couldn't be saved.",
	})
}
