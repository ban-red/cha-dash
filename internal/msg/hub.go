package msg

// The hub's own sentences, said by any package, and the server's
// (internal/server).
func init() {
	register(map[string]string{
		"hub.internal_error":      "internal error",
		"hub.automation_title":    "Test automation is on",
		"hub.automation_summary":  "CHADASH_TEST_AUTOMATION is set: anyone who can read automation.key in the data directory signs in as the owner and confirms destructive actions without a passkey. It's for automated tests only, never for a hub that holds anything real.",
		"hub.automation_resolves": "Resolves when the hub starts without CHADASH_TEST_AUTOMATION.",
		"hub.automation_action":   "Open Cha Dash's sheet",
		"hub.no_endpoint":         "no such endpoint",
		"hub.cross_origin":        "cross-origin request refused",
		"hub.causes_bad_resource": "resource must name a resource.",
	})
}
