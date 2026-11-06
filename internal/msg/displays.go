package msg

// Displays: wall tablets and TVs showing one board (internal/auth,
// internal/displays).
func init() {
	register(map[string]string{
		"displays.name":       "A display's name must be 1 to 64 characters, with no control characters.",
		"displays.quiet":      "Quiet hours are two different times, such as 22:00 and 07:00.",
		"displays.no_board":   "There's no such board.",
		"displays.not_found":  "There's no such display.",
		"displays.signed_out": "This display isn't signed in. Make a new link for it in Settings → Displays.",
		"displays.revoked":    "This display was removed in Settings → Displays. Make a new link to show a board here again.",
		"displays.only":       "Only a display's own link reaches this.",
	})
}
