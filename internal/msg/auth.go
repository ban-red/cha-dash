package msg

// Signing in, setup, API tokens, passkeys and step-up (internal/auth).
func init() {
	register(map[string]string{
		"auth.signin_required":     "sign in required",
		"auth.bad_credentials":     "invalid username or password",
		"auth.rate_limited":        "too many attempts; try again later",
		"auth.ip_banned":           "this address is banned after too many wrong passwords; an admin can clear it",
		"auth.unknown_host":        "Cha Dash doesn't know the name {host}. Open it at its IP address, or set CHADASH_PUBLIC_URL to the address you use, then create the owner.",
		"auth.setup_complete":      "setup has already been completed",
		"auth.setup_signin_failed": "account created, but signing in failed; sign in again",
		"auth.devlogin_local_only": "dev login only answers on this machine, at localhost or 127.0.0.1",
		"auth.owner_disabled":      "the owner account is disabled",

		"auth.token_rate_limited": "Too many bad API tokens from this address; try again later.",
		"auth.token_expired":      "This API token has expired. Make a new one in Settings → API tokens.",
		"auth.token_revoked":      "This API token has been revoked.",
		"auth.token_invalid":      "This API token isn't valid.",

		"auth.passkey_rejected":      "the passkey could not be verified; try again",
		"auth.passkey_failed":        "that passkey didn't work; try again",
		"auth.passkey_exists":        "this passkey is already registered",
		"auth.passkey_not_found":     "no such passkey",
		"auth.passkey_unknown":       "this passkey isn't registered with Cha Dash",
		"auth.passkey_last_method":   "this passkey is the only way to sign in to this account; add another passkey first",
		"auth.passkey_ceremony_gone": "that passkey request expired or was already used; start again",

		"auth.stepup_failed":         "That didn't confirm it's you. Try again.",
		"auth.stepup_use_passkey":    "Use your passkey to confirm it's you.",
		"auth.stepup_passkey_policy": "This hub asks for a passkey to confirm destructive actions. Add one to your account first.",
		"auth.stepup_unavailable":    "Your account has no password or passkey that works here, so it can't confirm destructive actions.",
		"auth.stepup_bad_body":       `send {"password"} or a passkey assertion`,
	})
}
