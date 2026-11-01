package msg

// Imports from other dashboards, their choices, history and revert
// (internal/importer, internal/importer/importhttp).
func init() {
	register(map[string]string{
		"imports.detect_count":    "send 1 to 32 files",
		"imports.head_not_base64": "a file's head isn't base64",
		"imports.wrong_shape":     "{field} has the wrong shape",
		"imports.file_required":   "{field} is required",
		"imports.file_not_base64": "{field} isn't base64",
		"imports.plan_changed":    "Something changed since the preview. Here's the plan as it is now.",
		"imports.nothing_from":    "nothing has been imported from {source} yet",
		"imports.not_found":       "no such import",
		"imports.reverted":        "This import was reverted already.",

		"imports.bad_target":       `target "{target}" isn't new, none or board:<id>`,
		"imports.bad_skip":         `skip "{skip}" isn't instance:<id> or tile:<id>`,
		"imports.no_missing_field": `{name} has no missing field "{field}"`,
		"imports.cant_reuse":       "{name} can't use {existing} instead",
		"imports.no_board":         "no board {board}",
	})
}
