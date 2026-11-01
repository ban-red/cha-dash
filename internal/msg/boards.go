package msg

// Boards, their layouts, tiles and discovery dismissals (internal/boards).
func init() {
	register(map[string]string{
		"boards.not_found":          "no such board",
		"boards.bad_revision":       "revision must be the revision the layout was loaded at (0 for a board never saved)",
		"boards.bad_format_version": "format_version must be a positive integer",
		"boards.layout_not_object":  "layout must be a JSON object",
		"boards.layout_conflict":    "the layout changed since it was loaded; reload it and try again",

		"boards.tiles_not_object": "invalid tiles: tiles must be a JSON object of layout item ids to tiles",
		"boards.tiles_unreadable": "invalid tiles: {reason}",
		"boards.tiles_trailing":   "invalid tiles: trailing data after the object",
		"boards.tiles_too_many":   "invalid tiles: {count} tiles is more than {max}",
		"boards.tiles_bad_id":     "invalid tiles: item ids must be 1–{max} bytes of UTF-8",
		"boards.tiles_bad_href":   "invalid tiles: {id}: href must be an http(s) URL",
		"boards.tiles_bad_source": "invalid tiles: {id}: every source needs an instance and a capability",

		"boards.dismiss_batch": "invalid resource id: dismiss 1–{max} resources at a time",
		"boards.restore_batch": "invalid resource id: restore 1–{max} resources at a time",
		"boards.bad_ref":       "invalid resource id: resource ids are 1–{max} bytes of UTF-8",
	})
}
