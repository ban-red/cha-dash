package msg

// Dozzle deep links: Settings → Integrations → Dozzle (internal/server).
func init() {
	register(map[string]string{
		"dozzle.no_url":  "Send url: Dozzle's address, or \"\" to turn the links off.",
		"dozzle.bad_url": "Dozzle's address must be an http or https URL with no user name, query or fragment, such as https://logs.home.example.",
	})
}
