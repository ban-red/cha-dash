package msg

// Integration instances, their pictures and sign-ins (internal/runtime).
func init() {
	register(map[string]string{
		"integrations.unknown":            "this hub doesn't have that integration",
		"integrations.instance_not_found": "no such instance",
		"integrations.not_running":        "the instance isn't running; it's disabled or can't start",
		"integrations.name_taken":         "another instance of this integration already has that name",

		"integrations.bad_picture_height": "h must be a whole number of pixels",
		"integrations.no_picture":         "there's no picture of that resource",
		"integrations.no_logs":            "there's no log of that resource here",

		"integrations.no_sign_in":            "this integration has no sign-in flow; paste the credential instead",
		"integrations.sign_in_gone":          "no such sign-in; it may have finished or expired",
		"integrations.sign_in_no_credential": "the sign-in finished without a credential; try again, or paste one",
	})
}
