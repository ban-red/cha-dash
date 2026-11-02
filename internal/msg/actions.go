package msg

// The action engine's API (internal/action/http.go) and approvals.
func init() {
	register(map[string]string{
		"actions.key_too_long": "The Idempotency-Key header is too long.",
		"actions.no_approval":  "There's no such approval.",
		"actions.no_task":      "There's no such task.",
		"actions.no_target":    "Name a target: ?target=<resource id>.",

		// An approved item's resolution, with the approver's step-up.
		"actions.approved_by":            "Approved by {name}.",
		"actions.approved_by_passkey":    "Approved by {name}, confirmed with a passkey.",
		"actions.approved_by_password":   "Approved by {name}, confirmed with their password.",
		"actions.approved_by_oidc":       "Approved by {name}, confirmed by signing in again at the provider.",
		"actions.approved_by_automation": "Approved by {name}, confirmed with the test-automation key.",
	})
}
