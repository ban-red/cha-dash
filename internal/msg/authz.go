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

		// Decisions: Decide, DecideApproval, ReadScope, TokenRoute.
		"authz.tier_unknown":          `"{tier}" isn't a tier.`,
		"authz.action_unknown":        `"{action}" isn't an action.`,
		"authz.no_role":               "Your account has no role that allows this.",
		"authz.stacks_admin":          "Only an admin or the owner can change stacks: they're settings, whatever the tier.",
		"authz.household_actions":     "Household accounts can only run actions meant for the household.",
		"authz.role_tier":             "Your role ({role}) can't run {tier} actions.",
		"authz.reads_data":            "Only operators, admins and the owner can run {action}: what it reads back can hold secrets.",
		"authz.token_action":          "This token isn't allowed to run {action}.",
		"authz.token_tier":            "API tokens can't run {tier} actions; they need a person to confirm.",
		"authz.sync_tier":             "A git sync can't run {tier} actions; they need a person to confirm.",
		"authz.t4_off":                "T4 actions are off for {action}. The owner can enable them in Settings.",
		"authz.stepup_always":         "Confirm it's you again: this action always asks.",
		"authz.stepup_first":          "Confirm it's you first.",
		"authz.approve_person_only":   "Only a person signed in to Cha Dash can approve or deny this, never a token.",
		"authz.approve_admin_only":    "Only an admin or the owner can approve or deny plans that wait for a person.",
		"authz.approve_stepup":        "Confirm it's you first: approving this needs a fresh check.",
		"authz.t4_unreadable":         "T4 actions are unavailable: the hub couldn't read their settings.",
		"authz.check_failed":          "The hub couldn't check what you're allowed to do.",
		"authz.tokens_not_here":       "API tokens aren't accepted here.",
		"authz.token_check_failed":    "The hub couldn't check what this token is allowed to do.",
		"authz.token_gone":            "This API token has been revoked or has expired.",
		"authz.syncs_not_here":        "Git syncs aren't accepted here.",
		"authz.sync_user_gone":        "The user this sync acts for no longer exists.",
		"authz.sync_check_failed":     "The hub couldn't check what this sync is allowed to do.",
		"authz.sync_user_disabled":    "The user this sync acts for has been disabled.",
		"authz.sync_read":             "A git sync can't read {scope}.",
		"authz.admins_see":            "Only admins and the owner can see this.",
		"authz.token_scope_missing":   "This token doesn't have the {scope} scope.",
		"authz.session_only":          "API tokens can't use this; sign in to Cha Dash instead.",
		"authz.import_security_owner": "Only the owner can change the security settings, so this file's security settings weren't imported.",

		"authz.token_viewer_refused": "Household accounts can't open this. This token acts as its user, so it can't either.",
		"authz.token_operator_only":  "Only operators, admins and the owner can do this. This token acts as its user, so it can't either.",
		"authz.token_admin_only":     "Only an admin or the owner can do this. This token acts as its user, so it can't either.",
		"authz.token_owner_only":     "Only the owner can do this. This token acts as its user, so it can't either.",
		"authz.token_refused":        "You can't do this. This token acts as its user, so it can't either.",

		"authz.identities_person_only": "Only a person signed in to Cha Dash can change their linked sign-ins.",
		"authz.identities_stepup":      "Confirm it's you first: linked sign-ins are a way into your account.",

		// Token scopes.
		"authz.scope_unknown":    `"{scope}" isn't a scope. Use an action ID such as docker.container.restart, a pattern such as proxmox.guest.*, or a read scope.`,
		"authz.scope_role":       "Your role ({role}) can't use {scope}, so a token of yours can't either.",
		"authz.token_no_approve": "Tokens can't approve plans: a person approves them, in a session.",
		"authz.scope_none":       "Pick at least one scope: what the token may read or run.",
		"authz.too_many_scopes":  "A token can have at most {max} scopes. Use a pattern such as proxmox.guest.* instead.",
	})
}
