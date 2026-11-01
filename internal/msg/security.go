package msg

// The security level, the step-up rule and the sign-in settings
// (internal/security).
func init() {
	register(map[string]string{
		"security.nothing_sent": "send posture, step_up or both",
		"security.bad_posture":  `"{posture}" isn't a security level: use relaxed, standard or strict`,
		"security.bad_step_up":  `"{rule}" isn't a step-up rule: use passkey_or_password or passkey_only`,

		"security.signin_unreadable":  "the sign-in settings aren't valid: {reason}",
		"security.client_secret_long": "the client secret is too long",
		"security.no_vault":           "This hub has no vault to keep the client secret in.",
		"security.issuer_first":       "Save the provider's issuer URL first.",
	})
}
