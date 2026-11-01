package msg

// Installed specs (internal/specstore).
func init() {
	register(map[string]string{
		"specs.source_too_long": "source can be at most 512 characters.",
		"specs.not_installed":   "No spec with that ID is installed.",
		"specs.on_not_bool":     "on must be true or false.",
	})
}
