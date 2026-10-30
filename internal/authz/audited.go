package authz

// What every route that can change something records in the audit log.
// TestEveryUnsafeRouteIsAudited (here) fails on a non-GET row that's in
// neither map below, and server.TestAuditedRoutesRecord fails on an entry
// of Audited without a test proving it.
//
// Refusals: authz decisions (Decide, the engine's policy) are audited with
// result refused where the route records anything, and so are the users
// API's protected-account and validation refusals, tokens with a scope the
// role can't hand out, and revoking a token that isn't yours. Guard's
// role refusals (403 forbidden for the wrong role on a route row) are not:
// they change nothing, a misbehaving client would flood the chain with
// them, and the access log already has them.

// Audited maps each audited unsafe route to the audit action it records.
// "(the action's ID)" is the engine: every plan and approval, refusals
// included, under the action's own ID.
var Audited = map[string]string{
	"POST /api/auth/passkeys/register/finish":      "auth.passkey.register",
	"PATCH /api/auth/passkeys/{id}":                "auth.passkey.rename",
	"DELETE /api/auth/passkeys/{id}":               "auth.passkey.delete",
	"POST /api/auth/step-up":                       "auth.step-up",
	"PUT /api/boards/{id}/layout":                  "boards.layout.save",
	"POST /api/boards/{id}/discovery/dismiss":      "boards.discovery.dismiss",
	"POST /api/boards/{id}/discovery/restore":      "boards.discovery.restore",
	"POST /api/actions/plan":                       "(the action's ID)",
	"POST /api/plans/{id}/approve":                 "(the action's ID)",
	"POST /api/tasks/{id}/cancel":                  "task.cancel",
	"POST /api/correlate/suggestions/{id}/confirm": "correlate.suggestion.confirm",
	"POST /api/correlate/suggestions/{id}/reject":  "correlate.suggestion.reject",
	"POST /api/correlate/decisions":                "correlate.decision.create",
	"DELETE /api/correlate/decisions/{id}":         "correlate.decision.delete",
	"POST /api/integrations/instances":             "settings.integrations.instance.create",
	"PUT /api/integrations/instances/{id}":         "settings.integrations.instance.update",
	"DELETE /api/integrations/instances/{id}":      "settings.integrations.instance.delete",
	"POST /api/import/homepage":                    "import.homepage",
	"POST /api/import/homarr":                      "import.homarr",
	"POST /api/import/yaml":                        "import.yaml",
	"POST /api/import/discovery":                   "import.discovery",
	"POST /api/imports/{id}/revert":                "import.revert",
	"POST /api/export":                             "export.home",
	"POST /api/import/chadash":                     "import.chadash",
	"PUT /api/onboarding":                          "settings.onboarding.update",
	"POST /api/onboarding/discover":                "onboarding.discover",
	"POST /api/discover":                           "onboarding.discover",
	"POST /api/discover/dismiss":                   "discover.dismiss",
	"POST /api/discover/restore":                   "discover.restore",
	"PUT /api/settings/security":                   "settings.security.update",
	"PUT /api/settings/history":                    "settings.history.update",
	"POST /api/settings/history/clear":             "settings.history.clear",
	"POST /api/users":                              "users.create",
	"PUT /api/users/{id}":                          "users.update",
	"POST /api/users/{id}/transfer-ownership":      "users.transfer-ownership",
	"DELETE /api/bans/{ip}":                        "security.ban.clear",
	"POST /api/nodes/{id}/revoke":                  "nodes.revoke",
	"POST /api/nodes/pairing/regenerate":           "nodes.pairing.regenerate",
	"POST /api/nodes/tokens":                       "nodes.token.create",
	"DELETE /api/nodes/tokens/{id}":                "nodes.token.revoke",
	"POST /api/nodes/{id}/approve":                 "nodes.approve",
	"PUT /api/stacks/secrets/{name}":               "settings.stacks.secret.update",
	"DELETE /api/stacks/secrets/{name}":            "settings.stacks.secret.delete",
	"PUT /api/stacks/{node}/{stack}":               "settings.stacks.draft.update",
	"DELETE /api/stacks/{node}/{stack}/draft":      "settings.stacks.draft.delete",
	"PUT /api/stacks/{node}/{stack}/sync":          "settings.stacks.sync.create",
	"DELETE /api/stacks/{node}/{stack}/sync":       "settings.stacks.sync.delete",
	"POST /api/stacks/{node}/{stack}/sync/resume":  "settings.stacks.sync.resume",
	"POST /api/stacks/hooks":                       "settings.stacks.hook.create",
	"POST /api/stacks/hooks/{id}/rotate":           "settings.stacks.hook.rotate",
	"DELETE /api/stacks/hooks/{id}":                "settings.stacks.hook.delete",
	"POST /api/notify/channels":                    "settings.notify.channel.create",
	"PUT /api/notify/channels/{id}":                "settings.notify.channel.update",
	"DELETE /api/notify/channels/{id}":             "settings.notify.channel.delete",
	"POST /api/notify/channels/{id}/test":          "settings.notify.channel.test",
	"PUT /api/notify/rules/{id}":                   "settings.notify.rule.update",
	"POST /api/tokens":                             "tokens.create",
	"DELETE /api/tokens/{id}":                      "tokens.revoke",
	"POST /api/approvals/{id}/approve":             "approval.approve",
	"POST /api/approvals/{id}/deny":                "approval.deny",

	// Sign-in settings and linked sign-ins (wave 13). Sign-ins through the
	// provider or a proxy are auth events, audited by the server
	// (auth.oidc.signin, auth.oidc.refused, auth.oidc.link,
	// auth.forward.signin, auth.forward.refused, auth.forward.untrusted,
	// auth.forward.duplicate, users.role.from_groups).
	"PUT /api/settings/sign-in":                      "settings.sign_in.update",
	"DELETE /api/auth/identities/{issuer}/{subject}": "auth.oidc.unlink", // auth.forward.unlink for a proxy username
	"POST /api/auth/identities/forward-auth":         "auth.forward.link",
}

// AuditExempt maps each unsafe route that records nothing to why.
var AuditExempt = map[string]string{
	"POST /api/setup":                        "First run: creates the owner before anyone can read the log; the hub logs it.",
	"POST /api/auth/login":                   "Signing in changes no setting; the log would fill with sign-ins. Failures are logged and rate-limited.",
	"POST /api/auth/logout":                  "Ends the caller's own session (and closes its sockets); nothing else changes.",
	"POST /api/auth/passkeys/login/begin":    "Starts a sign-in ceremony in memory.",
	"POST /api/auth/passkeys/login/finish":   "Signing in, as with a password.",
	"POST /api/auth/dev-login":               "Development builds only (-tags devlogin), loopback only.",
	"POST /api/auth/passkeys/register/begin": "Starts a ceremony in memory; finishing it is audited.",
	"POST /api/auth/step-up/begin":           "Starts a ceremony in memory; the step-up itself is audited.",
	"POST /api/integrations/test":            "Tests a connection and saves nothing; saving the instance is audited.",
	"POST /api/import/detect":                "Reads files' names and first bytes to name their source; saves nothing. Applying an import is audited.",
	"POST /api/integrations/instances/{id}/test": "Tests a saved instance. The one change it can make is pinning the first certificate a TOFU instance sees, " +
		"which the instance's next audited update shows in its config.",
	"POST /api/integrations/instances/{id}/refresh":  "Asks a running instance to read its source now; it changes no config and no state of its own.",
	"POST /api/integrations/authorize/{integration}": "Starts an integration sign-in in memory; the instance save that uses its token is audited.",
	"POST /api/stacks/{node}/{stack}/validate":       "Validates a draft on the node and saves nothing.",
	"POST /api/stacks/{node}/{stack}/sync/check":     "Tests a remote from the node and saves nothing. The saved credential goes only to the remote it was saved for; another URL brings its own. Following it is audited.",
	"POST /api/stacks/{node}/{stack}/sync/now":       "Asks the sync to check now, as a poll would; a new commit it fetches is audited as stack.sync.fetch.",
	"POST /api/hooks/{id}": "A webhook only pokes the syncs on its remote, as a poll would. A new commit is audited as stack.sync.fetch, " +
		"and the first refused request of a hook in each 10 minutes as stack.hook.refused.",
	"POST /api/notify/push/subscriptions":        "The caller's own device subscribing to its own notifications.",
	"DELETE /api/notify/push/subscriptions/{id}": "The caller's own device.",
	"POST /api/notify/push/unsubscribe":          "The caller's own device.",
	"POST /api/settings/sign-in/test":            "Reads the provider's discovery document and starts a test sign-in that signs nobody in; saving the settings is audited.",
	// Fleet stacks (wave 14).
	"POST /api/stacks/{node}/{stack}/move/preview": "A dry run of stack.move on the node: it saves nothing. The move itself is an audited action.",
}
