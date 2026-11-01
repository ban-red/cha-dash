package msg

// Metrics and state history, and its settings (internal/history,
// internal/server). The settings' "nothing to change" and "locked" for a
// partial change are in operations.go.
func init() {
	register(map[string]string{
		"history.bad_resource": "resource is a resource ID",
		"history.bad_range":    "from and to are Unix milliseconds, from before to",
		"history.bad_points":   "points is 1 to {max}",
		"history.bad_series":   "ask for 1 to {max} series, as series=<resource>#<metric>",
		"history.read_failed":  "couldn't read the history",

		"history.locked":                "History is off for this hub (CHADASH_HISTORY=off, or demo mode).",
		"history.bad_exclude":           "exclude lists at most {max} instances",
		"history.bad_exclude_keys":      "exclude_keys lists at most {max} patterns",
		"history.bad_keep":              "keep maps 1m, 15m, 1h or 1d to milliseconds, or null for the default",
		"history.bad_level":             "level is off, standard or long-term",
		"history.bad_limit":             "max_bytes is 0 (no limit) or at least 64 MB",
		"history.level_failed":          "couldn't change the history level",
		"history.level_unsaved":         "the level applies now but couldn't be saved for the next start",
		"history.limit_unsaved":         "the limit applies now but couldn't be saved for the next start",
		"history.exclusions_unsaved":    "the exclusions apply now but couldn't be saved for the next start",
		"history.retention_unsaved":     "the retention applies now but couldn't be saved for the next start",
		"history.clear_instance_failed": "couldn't clear that integration's history",
		"history.clear_failed":          "couldn't clear the history",

		// CheckKeep and CheckKeyPattern.
		"history.no_tier":            "no tier {tier}",
		"history.keep_range":         "the {tier} tier keeps {low} to {high}",
		"history.keep_order":         "the {tier} tier can't keep less than the {finer} tier",
		"history.pattern_length":     "a pattern is 1 to {max} characters",
		"history.pattern_only_stars": "a pattern of only * would leave out every metric; turn history off instead",
		"history.pattern_control":    "a pattern has no control characters",
	})
}
