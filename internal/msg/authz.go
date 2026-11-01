package msg

// Who may do what: routes, users, tokens, bans and linked sign-ins
// (internal/authz).
func init() {
	register(map[string]string{
		"authz.no_rule":        "This route has no authorization rule yet, so it's refused.",
		"authz.token_api_only": "API tokens work only on /api/ routes.",
		"authz.viewer_refused": "Household accounts can't open this.",
		"authz.operator_only":  "Only operators, admins and the owner can do this.",
		"authz.admin_only":     "Only an admin or the owner can do this.",
		"authz.owner_only":     "Only the owner can do this.",
		"authz.refused":        "You can't do this.",

		"authz.user_not_found":  "There's no such user.",
		"authz.username_taken":  "That username is taken.",
		"authz.owner_account":   "Only the owner can change the owner's account.",
		"authz.owner_protected": "The owner can't be demoted or disabled. Hand over ownership first.",
		"authz.not_owner":       "You're no longer the owner.",

		"authz.token_days":            "A token must expire within 1 to 365 days.",
		"authz.token_kind":            "A token's kind is api or agent.",
		"authz.agents_off":            "Agents is off, so agent tokens can't be made. The owner can turn it on in Settings → Security.",
		"authz.token_not_found":       "There's no such token.",
		"authz.token_already_revoked": "This token was already revoked.",

		"authz.not_an_ip":  "That isn't an IP address.",
		"authz.not_banned": "That address isn't banned.",

		"authz.identity_not_linked": "That sign-in isn't linked to your account.",
		"authz.last_sign_in_method": "That's the only way into your account. Add a passkey or link another sign-in first.",
		"authz.no_forward_auth":     "No trusted proxy named a user on this request. Open Cha Dash through your proxy, with forward-auth on in Settings → Security.",
		"authz.proxy_name_taken":    "That proxy username is linked to another account.",
	})
}
