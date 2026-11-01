package msg

// Backup coverage (internal/backups) and verify now (integrations/pbs).
func init() {
	register(map[string]string{
		// Where a resource stands on the coverage map.
		"backups.state_fresh":     "Backed up within its objective of {objective}.",
		"backups.state_stale":     "Its newest good backup is older than its objective of {objective}.",
		"backups.state_failing":   "Its backups are failing, and none has succeeded yet.",
		"backups.state_never":     "A backup job covers it, but no run has been seen yet.",
		"backups.state_uncovered": "Nothing backs it up.",
		"backups.state_disabled":  "The backup jobs that cover it are turned off.",
		"backups.state_unread":    "Cha Dash can't read the backup jobs that would cover it.",
		"backups.through":         "Through {guest}'s backup.",
		// Why it's stale, when something on the way isn't ok.
		"backups.cause_target":      "Its backups go to {target}, which isn't ok: {reason}.",
		"backups.cause_target_down": "Its backups go to {target}, which isn't ok.",
		"backups.cause_failed":      "{job}'s last run failed: {error}",
		// The objectives' API.
		"backups.objective_range":   "An objective is between one hour and 366 days (max_age_ms).",
		"backups.not_protectable":   "That resource isn't on the backup coverage map.",
		"backups.objective_not_set": "That resource has no objective of its own.",

		// Verify now (pbs.datastore.verify).
		"pbs.verify_label":       "Verify {target} now",
		"pbs.verify_label_group": "Verify {group} on {target} now",
		"pbs.verify_step":        "Check every chunk of {what} on {target} against its checksum",
		"pbs.verify_step_result": "Read the verify task's result back; it shows on the datastore",
		"pbs.verify_maintenance": "{target} is in maintenance ({mode}), so it can't be verified now.",
		"pbs.verify_lacks":       "The control token can't verify {target}: it lacks Datastore.Verify on {path}. Give it the DatastoreAdmin role there, as the control recipe does.",
		"pbs.verify_running":     "Verifying {what} on {target}",
		"pbs.verify_failed":      "Proxmox Backup Server couldn't verify {target}: {reason}.",
		"pbs.verify_forbidden":   "Proxmox Backup Server refused the control token ({reason}). It needs Datastore.Verify on /datastore/{target}; the recipe's control variant grants it.",
		"pbs.verify_lost":        "Cha Dash lost track of the verify task.",
		"pbs.verify_unreadable":  "Cha Dash couldn't read the verify task back: {reason}.",
		"pbs.verify_reported":    "Proxmox Backup Server reports the verify task as {status}.",
		"pbs.verify_every_group": "every snapshot",
	})
}
