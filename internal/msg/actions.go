package msg

// The action engine's API (internal/action/http.go) and approvals.
func init() {
	register(map[string]string{
		"actions.key_too_long": "The Idempotency-Key header is too long.",
		"actions.no_approval":  "There's no such approval.",
		"actions.no_task":      "There's no such task.",
		"actions.no_target":    "Name a target: ?target=<resource id>.",
	})
}
