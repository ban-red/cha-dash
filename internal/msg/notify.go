package msg

// Notifications' API (internal/notify/http.go). The digest's sentences are
// in operations.go.
func init() {
	register(map[string]string{
		"notify.no_channel":     "There's no such channel.",
		"notify.no_rule":        "There's no such rule.",
		"notify.no_device":      "There's no such device on your account.",
		"notify.not_subscribed": "That device isn't subscribed on your account.",
		"notify.no_vault":       "The hub has no vault, so it can't keep a channel's secrets or a Web Push key.",
		"notify.bad_state":      "state is pending, sent, failed or dropped.",
		"notify.bad_limit":      "limit is 1 to 200.",
	})
}
