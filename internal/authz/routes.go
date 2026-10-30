package authz

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
)

// Access is what a route needs from the caller.
type Access int

const (
	// Public routes need no session: health, setup, signing in, the web app.
	Public Access = iota
	// SignedIn admits every role, household included.
	SignedIn
	// Viewer admits viewer, operator, admin and owner: everything but household.
	Viewer
	// Operator admits operator, admin and owner.
	Operator
	// Admin admits admin and owner.
	Admin
	// Owner admits the owner only.
	Owner
	// NodeKey routes are for chadash-node, not people: the node proves its
	// pinned Ed25519 key inside the channel and internal/nodes checks it
	// against the registry. Guard passes them through without a session,
	// like public routes, and no person's role ever satisfies one. What a
	// node may do on its host is its own policy file's business.
	NodeKey
	// Signature routes are for machines that sign each request, such as a
	// forge's webhook: the handler verifies the request itself. Guard
	// passes them through without a session, like NodeKey, and no person's
	// role ever satisfies one, so a session gains nothing. Every Signature
	// row is in TestSignatureRoutes' reviewed list.
	Signature
)

func (a Access) String() string {
	switch a {
	case Public:
		return "public"
	case SignedIn:
		return "signed in"
	case Viewer:
		return "viewer+"
	case Operator:
		return "operator+"
	case Admin:
		return "admin+"
	case Owner:
		return "owner"
	case NodeKey:
		return "node key"
	case Signature:
		return "signature"
	}
	return "unknown"
}

// Allows reports whether a signed-in user with role passes a.
func (a Access) Allows(role string) bool {
	r := rank(role)
	switch a {
	case Public:
		return true
	case SignedIn:
		return r >= 0
	case Viewer:
		return r >= rank(auth.RoleViewer)
	case Operator:
		return r >= rank(auth.RoleOperator)
	case Admin:
		return r >= rank(auth.RoleAdmin)
	case Owner:
		return r >= rank(auth.RoleOwner)
	}
	return false // NodeKey, Signature: never a person
}

func (a Access) refusal() string {
	switch a {
	case Viewer:
		return "Household accounts can't open this."
	case Operator:
		return "Only operators, admins and the owner can do this."
	case Admin:
		return "Only an admin or the owner can do this."
	case Owner:
		return "Only the owner can do this."
	}
	return "You can't do this."
}

// Route is one row of the table.
type Route struct {
	// Pattern is exactly as registered with the ServeMux: "GET /api/boards/{id}/layout".
	Pattern string
	Access  Access
	// Optional rows may be missing from a router: mounted only in some
	// builds (dev-login), or added by a wave 7 track that hasn't merged yet.
	// The router test skips them when they're absent.
	Optional bool
	// Token is what an API token needs to call the route, on top of Access
	// (which a token's user must pass too): a read scope such as
	// ScopeGraphRead, or TokenAny when every token may (the action engine
	// then decides per action, or the handler narrows to the token's own).
	// Empty means session only: a token is refused with 403 session_only.
	// Signing in, step-up, passkeys, users, tokens and every setting are
	// session only, so a token can never mint a token or raise itself.
	Token string
}

// TokenAny marks a route every API token may call (subject to Access).
const TokenAny = "(any token)"

// Routes is every HTTP route the hub serves and who may call it. Guard
// refuses a route that isn't here, and the authz route test fails on one,
// so a new route needs a row before it can answer anyone.
//
// Settings → Security and handing over ownership are the owner's; users,
// integrations, boards edits, import and export, onboarding and the audit
// log are admin+; reads are open to viewers; boards and the realtime socket
// to every signed-in role, household included. Actions (plan, approve,
// tasks) are open to every signed-in role because the engine decides per
// action, through Policy.
var Routes = []Route{
	// Unauthenticated.
	{Pattern: "GET /healthz", Access: Public},
	// Prometheus scrapes, on a listener of their own (server.MetricsHandler,
	// CHADASH_METRICS_LISTEN): no session, an optional bearer token. The
	// hub's own listener doesn't serve it; there it gets the web app, like
	// any unknown path.
	{Pattern: "GET /metrics", Access: Public},
	{Pattern: "GET /api/setup", Access: Public},
	{Pattern: "POST /api/setup", Access: Public},
	{Pattern: "POST /api/auth/login", Access: Public},
	{Pattern: "POST /api/auth/logout", Access: Public},
	{Pattern: "POST /api/auth/passkeys/login/begin", Access: Public},
	{Pattern: "POST /api/auth/passkeys/login/finish", Access: Public},
	{Pattern: "POST /api/auth/dev-login", Access: Public, Optional: true}, // -tags devlogin, loopback only
	{Pattern: "/api/", Access: Public},                                    // the API's 404
	{Pattern: "/", Access: Public},                                        // the web app; first paint checks the session itself
	// OIDC sign-in (wave 13): browser redirects to and from the provider.
	// Linking and step-up through them check the session themselves.
	{Pattern: "GET /api/auth/oidc/start", Access: Public},
	{Pattern: "GET /api/auth/oidc/callback", Access: Public},

	// chadash-node's channel: authenticated by the node's pinned key inside
	// mutual TLS, never by a session (internal/nodes).
	{Pattern: "GET /node", Access: NodeKey},

	// The signed-in user's own account, whatever the role.
	{Pattern: "GET /api/auth/session", Access: SignedIn},
	{Pattern: "GET /api/version", Access: SignedIn, Token: TokenAny},
	{Pattern: "POST /api/auth/passkeys/register/begin", Access: SignedIn},
	{Pattern: "POST /api/auth/passkeys/register/finish", Access: SignedIn},
	{Pattern: "GET /api/auth/passkeys", Access: SignedIn},
	{Pattern: "PATCH /api/auth/passkeys/{id}", Access: SignedIn},
	{Pattern: "DELETE /api/auth/passkeys/{id}", Access: SignedIn},
	{Pattern: "POST /api/auth/step-up/begin", Access: SignedIn},
	{Pattern: "POST /api/auth/step-up", Access: SignedIn},
	// Your own linked sign-ins (wave 13): every role lists and unlinks its
	// own, with a step-up (identities.go).
	{Pattern: "GET /api/auth/identities", Access: SignedIn},
	{Pattern: "DELETE /api/auth/identities/{issuer}/{subject}", Access: SignedIn},
	{Pattern: "POST /api/auth/identities/forward-auth", Access: SignedIn},

	// Boards and live data.
	{Pattern: "GET /api/boards", Access: SignedIn, Token: ScopeGraphRead},
	{Pattern: "GET /api/boards/{id}/layout", Access: SignedIn, Token: ScopeGraphRead},
	{Pattern: "GET /ws", Access: SignedIn},
	{Pattern: "GET /api/demo", Access: SignedIn, Token: ScopeGraphRead},
	{Pattern: "GET /api/boards/{id}/discovery", Access: Viewer, Token: ScopeGraphRead},
	{Pattern: "PUT /api/boards/{id}/layout", Access: Admin},
	{Pattern: "POST /api/boards/{id}/discovery/dismiss", Access: Admin},
	{Pattern: "POST /api/boards/{id}/discovery/restore", Access: Admin},

	// Icons the hub cached for tiles (internal/icons): boards show them, so
	// every signed-in role. What gets fetched comes from the owner's tiles
	// and instances, never from these requests.
	{Pattern: "GET /api/icons", Access: SignedIn},
	{Pattern: "GET /api/icons/{id}", Access: SignedIn},

	// Actions: the engine authorizes each one (Policy), so the routes only
	// need a session. Added by the action engine track.
	{Pattern: "POST /api/actions/plan", Access: SignedIn, Token: TokenAny},
	{Pattern: "POST /api/plans/{id}/approve", Access: SignedIn, Token: TokenAny},
	{Pattern: "GET /api/tasks/{id}", Access: SignedIn, Token: TokenAny},
	// Cancelling: your own task, or anyone's with tasks.all (the engine asks
	// the policy). Listing what could be planned on a target: filtered by
	// the policy, per action.
	{Pattern: "POST /api/tasks/{id}/cancel", Access: SignedIn, Token: TokenAny},
	{Pattern: "GET /api/actions", Access: SignedIn, Token: TokenAny},

	// Identity across sources: reads for viewers, decisions for admins.
	{Pattern: "GET /api/correlate/links", Access: Viewer, Token: ScopeGraphRead},
	{Pattern: "GET /api/correlate/suggestions", Access: Viewer, Token: ScopeGraphRead},
	{Pattern: "GET /api/correlate/decisions", Access: Viewer, Token: ScopeGraphRead},
	{Pattern: "POST /api/correlate/suggestions/{id}/confirm", Access: Admin},
	{Pattern: "POST /api/correlate/suggestions/{id}/reject", Access: Admin},
	{Pattern: "POST /api/correlate/decisions", Access: Admin},
	{Pattern: "DELETE /api/correlate/decisions/{id}", Access: Admin},

	// Settings → Integrations.
	{Pattern: "GET /api/integrations/catalog", Access: Admin},
	{Pattern: "GET /api/integrations/instances", Access: Admin},
	{Pattern: "POST /api/integrations/instances", Access: Admin},
	{Pattern: "GET /api/integrations/instances/{id}", Access: Admin},
	{Pattern: "PUT /api/integrations/instances/{id}", Access: Admin},
	{Pattern: "DELETE /api/integrations/instances/{id}", Access: Admin},
	{Pattern: "POST /api/integrations/test", Access: Admin},
	{Pattern: "POST /api/integrations/instances/{id}/test", Access: Admin},
	{Pattern: "POST /api/integrations/instances/{id}/refresh", Access: Viewer},
	{Pattern: "GET /api/integrations/instances/{id}/diagnostics", Access: Admin},
	// A camera's latest frame: whoever sees the board sees its cameras.
	{Pattern: "GET /api/integrations/instances/{id}/picture", Access: SignedIn, Token: ScopeGraphRead},
	{Pattern: "POST /api/integrations/authorize/{integration}", Access: Admin},
	{Pattern: "GET /api/integrations/authorize/{integration}/{id}", Access: Admin},

	// Import, export and the first-run wizard (which adds integrations).
	{Pattern: "POST /api/import/detect", Access: Admin},
	{Pattern: "POST /api/import/homepage", Access: Admin},
	{Pattern: "GET /api/import/homepage", Access: Admin},
	{Pattern: "POST /api/import/homarr", Access: Admin},
	{Pattern: "GET /api/import/homarr", Access: Admin},
	{Pattern: "POST /api/import/yaml", Access: Admin},
	{Pattern: "GET /api/import/yaml", Access: Admin},
	{Pattern: "POST /api/import/discovery", Access: Admin},
	{Pattern: "GET /api/import/discovery", Access: Admin},
	{Pattern: "GET /api/imports", Access: Admin},
	{Pattern: "GET /api/imports/{id}", Access: Admin},
	{Pattern: "POST /api/imports/{id}/revert", Access: Admin},
	{Pattern: "GET /api/export", Access: Admin},
	{Pattern: "POST /api/export", Access: Admin},
	{Pattern: "POST /api/import/chadash", Access: Admin},
	{Pattern: "GET /api/onboarding", Access: Admin},
	{Pattern: "PUT /api/onboarding", Access: Admin},
	{Pattern: "POST /api/onboarding/discover", Access: Admin},
	{Pattern: "POST /api/discover", Access: Admin},
	{Pattern: "GET /api/discover/dismissed", Access: Admin},
	{Pattern: "POST /api/discover/dismiss", Access: Admin},
	{Pattern: "POST /api/discover/restore", Access: Admin},

	// Settings → Security: the posture is shown to viewers; changing it, and
	// the step-up rule, is the owner's.
	{Pattern: "GET /api/settings/security", Access: Viewer},
	// Settings → History: the recording level, what history.db holds, clearing it.
	{Pattern: "GET /api/settings/history", Access: Admin},
	{Pattern: "PUT /api/settings/history", Access: Admin},
	{Pattern: "POST /api/settings/history/clear", Access: Admin},
	{Pattern: "PUT /api/settings/security", Access: Owner},
	// Settings → Security → Sign-in (wave 13): OIDC, forward-auth and the
	// group mappings are the owner's; changing them needs a step-up.
	{Pattern: "GET /api/settings/sign-in", Access: Owner},
	{Pattern: "PUT /api/settings/sign-in", Access: Owner},
	{Pattern: "POST /api/settings/sign-in/test", Access: Owner},

	// Settings → Users. An admin manages everyone but the owner (users.go);
	// handing over ownership is the owner's, and T3.
	{Pattern: "GET /api/users", Access: Admin},
	{Pattern: "POST /api/users", Access: Admin},
	{Pattern: "PUT /api/users/{id}", Access: Admin},
	{Pattern: "POST /api/users/{id}/transfer-ownership", Access: Owner},
	// Someone's linked sign-ins, read only, for support (wave 13).
	{Pattern: "GET /api/users/{id}/identities", Access: Admin},
	// Addresses banned after too many wrong passwords (Settings → Users).
	{Pattern: "GET /api/bans", Access: Admin},
	{Pattern: "DELETE /api/bans/{ip}", Access: Admin},

	// Nodes: the list is admin+, and so is revoking one (audited as
	// nodes.revoke) and regenerating the pairing secret (audited as
	// nodes.pairing.regenerate; the secret stays in the pairing file).
	{Pattern: "GET /api/nodes", Access: Admin},
	{Pattern: "POST /api/nodes/{id}/revoke", Access: Admin},
	{Pattern: "POST /api/nodes/pairing/regenerate", Access: Admin},
	// Remote nodes (wave 14a): join tokens are admin+ (audited as
	// nodes.token.create and nodes.token.revoke; the secret is shown once
	// and never stored). Approving a pending node is admin+ with a step-up
	// (T3, nodes.approve): the hub sends an approved node stack secrets.
	// Rejecting one is the revoke above.
	{Pattern: "GET /api/nodes/tokens", Access: Admin},
	{Pattern: "POST /api/nodes/tokens", Access: Admin},
	{Pattern: "DELETE /api/nodes/tokens/{id}", Access: Admin},
	{Pattern: "POST /api/nodes/{id}/approve", Access: Admin},
	// Settings → Stacks (track 9B). The list (names and status) is a read,
	// for viewers; a stack's files, history and drafts are admin+, because
	// compose files (adopted ones especially) often hold credentials in
	// plain text, and stacks are Settings in the Roles table. Deploying,
	// rolling back and adopting are actions, admin+ by authz.Decide.
	{Pattern: "GET /api/stacks", Access: Viewer, Token: ScopeGraphRead},
	{Pattern: "GET /api/stacks/secrets", Access: Admin},
	{Pattern: "PUT /api/stacks/secrets/{name}", Access: Admin},
	{Pattern: "DELETE /api/stacks/secrets/{name}", Access: Admin},
	{Pattern: "GET /api/stacks/{node}/{stack}", Access: Admin},
	{Pattern: "PUT /api/stacks/{node}/{stack}", Access: Admin},
	{Pattern: "DELETE /api/stacks/{node}/{stack}/draft", Access: Admin},
	{Pattern: "POST /api/stacks/{node}/{stack}/validate", Access: Admin},
	// Git sync and deploy webhooks (wave 12). Following git, the hooks and
	// their secrets are stack settings, admin+; turning on deploy on push,
	// changing a sync's remote and taking one over also need a step-up,
	// which the handlers ask for (stacks.Service.stepUp). The webhook
	// itself answers to its hook's signature, never a session.
	{Pattern: "GET /api/stacks/{node}/{stack}/sync", Access: Admin},
	{Pattern: "PUT /api/stacks/{node}/{stack}/sync", Access: Admin},
	{Pattern: "DELETE /api/stacks/{node}/{stack}/sync", Access: Admin},
	{Pattern: "POST /api/stacks/{node}/{stack}/sync/check", Access: Admin},
	{Pattern: "POST /api/stacks/{node}/{stack}/sync/now", Access: Admin},
	{Pattern: "POST /api/stacks/{node}/{stack}/sync/resume", Access: Admin},
	{Pattern: "GET /api/stacks/hooks", Access: Admin},
	{Pattern: "POST /api/stacks/hooks", Access: Admin},
	{Pattern: "POST /api/stacks/hooks/{id}/rotate", Access: Admin},
	{Pattern: "DELETE /api/stacks/hooks/{id}", Access: Admin},
	{Pattern: "POST /api/hooks/{id}", Access: Signature},

	// Settings → Notifications (track 10A): channels, rules and the
	// delivery log are admin+ (notifications are Settings in the Roles
	// table). The VAPID key and your own devices' push subscriptions are
	// every signed-in role's: notify scopes them to the caller.
	{Pattern: "GET /api/notify/channels", Access: Admin},
	{Pattern: "POST /api/notify/channels", Access: Admin},
	{Pattern: "PUT /api/notify/channels/{id}", Access: Admin},
	{Pattern: "DELETE /api/notify/channels/{id}", Access: Admin},
	{Pattern: "POST /api/notify/channels/{id}/test", Access: Admin},
	{Pattern: "GET /api/notify/rules", Access: Admin},
	{Pattern: "PUT /api/notify/rules/{id}", Access: Admin},
	{Pattern: "GET /api/notify/deliveries", Access: Admin},
	{Pattern: "GET /api/notify/push/key", Access: SignedIn},
	{Pattern: "GET /api/notify/push/subscriptions", Access: SignedIn},
	{Pattern: "POST /api/notify/push/subscriptions", Access: SignedIn},
	{Pattern: "DELETE /api/notify/push/subscriptions/{id}", Access: SignedIn},
	{Pattern: "POST /api/notify/push/unsubscribe", Access: SignedIn},
	// Reads for scripts: the Home Graph and the Inbox as the caller's role
	// sees them over realtime (household gets its projection). Sessions can
	// call them too; the web app uses the socket.
	{Pattern: "GET /api/graph", Access: SignedIn, Token: ScopeGraphRead},
	{Pattern: "GET /api/inbox", Access: SignedIn, Token: ScopeInboxRead},
	// Metrics history, as the metrics topic: household reads the series of
	// what its graph shows (server/history.go).
	{Pattern: "GET /api/history", Access: SignedIn, Token: ScopeGraphRead},
	{Pattern: "GET /api/history/states", Access: SignedIn, Token: ScopeGraphRead},
	{Pattern: "GET /api/history/pulse", Access: SignedIn, Token: ScopeGraphRead},

	// Settings → API tokens: every role manages its own; admins and the
	// owner see and revoke everyone's (tokens.go). Session only: a token
	// can't mint, list or revoke tokens.
	{Pattern: "GET /api/tokens", Access: SignedIn},
	{Pattern: "POST /api/tokens", Access: SignedIn},
	{Pattern: "DELETE /api/tokens/{id}", Access: SignedIn},
	{Pattern: "GET /api/tokens/scopes", Access: SignedIn},

	// Plans waiting for a person (a token's T2 plans). The list and the
	// decisions are an approver's, in a session (the engine asks the policy:
	// admin+, a step-up from T2); one approval is readable by the token that
	// asked, so it can poll, and by approvers.
	{Pattern: "GET /api/approvals", Access: Admin},
	{Pattern: "GET /api/approvals/{id}", Access: SignedIn, Token: TokenAny},
	{Pattern: "POST /api/approvals/{id}/approve", Access: Admin},
	{Pattern: "POST /api/approvals/{id}/deny", Access: Admin},

	// Settings → Audit. Added by the audit track.
	{Pattern: "GET /api/audit", Access: Admin, Token: ScopeAuditRead},
	{Pattern: "GET /api/audit/export", Access: Admin, Token: ScopeAuditRead},
	{Pattern: "GET /api/audit/verify", Access: Admin, Token: ScopeAuditRead},
}

var routeIndex = func() map[string]Route {
	m := make(map[string]Route, len(Routes))
	for _, r := range Routes {
		if _, dup := m[r.Pattern]; dup {
			panic("authz: route listed twice: " + r.Pattern)
		}
		m[r.Pattern] = r
	}
	return m
}()

// Lookup returns the row for a ServeMux pattern.
func Lookup(pattern string) (Route, bool) {
	r, ok := routeIndex[pattern]
	return r, ok
}

// Guard authorizes every request to mux by the route it matches, then serves
// it: public, node-key and signature routes as they are, the rest behind a session (auth.Require,
// which attaches the identity handlers read) and the row's role. A route
// with no row is refused with 500 and an error in the log, so a forgotten
// row fails closed and loudly. Requests mux would answer with 404 or 405
// itself pass straight through.
//
// A request carrying an API token (Authorization: Bearer cdt_…) is
// authenticated by that token alone, whatever cookie came with it: only on
// /api/ routes, and only on rows whose Token admits it (TokenRoute), as the
// token's user narrowed to its scopes. Bad tokens are answered 401 and
// rate-limited per client (auth.RequireToken). A token is a credential the
// browser never sends by itself, so token requests need no CSRF defence.
//
// Guard sits inside the cross-origin protection, so CSRF checks run first.
func Guard(mux *http.ServeMux, a *auth.Service, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern == "" {
			mux.ServeHTTP(w, r)
			return
		}
		route, ok := Lookup(pattern)
		if !ok {
			log.Error("authz: route has no row in internal/authz/routes.go; refusing", "pattern", pattern)
			httpx.Error(w, http.StatusInternalServerError, "no_authz_rule", "This route has no authorization rule yet, so it's refused.")
			return
		}
		if route.Access == Public || route.Access == NodeKey || route.Access == Signature {
			// A node authenticates in the channel, by key; a signed request
			// in its handler. A cookie or bearer token counts for nothing.
			mux.ServeHTTP(w, r)
			return
		}
		if _, ok := auth.BearerToken(r); ok {
			// An API token: it alone is the credential, whatever cookie
			// came with it, and only on /api/ routes.
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				httpx.Error(w, http.StatusUnauthorized, "unauthenticated", "API tokens work only on /api/ routes.")
				return
			}
			a.RequireToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id, _ := auth.FromContext(r.Context())
				if err := TokenRoute(route, id.User.Role, id.Token.Scopes); err != nil {
					var ref *Refusal
					code, msg := "forbidden", err.Error()
					if errors.As(err, &ref) && errors.Is(ref.Err, ErrSessionOnly) {
						code = "session_only"
					}
					httpx.Error(w, http.StatusForbidden, code, msg)
					return
				}
				mux.ServeHTTP(w, r)
			})).ServeHTTP(w, r)
			return
		}
		a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := auth.FromContext(r.Context())
			if !route.Access.Allows(id.User.Role) {
				httpx.Error(w, http.StatusForbidden, "forbidden", route.Access.refusal())
				return
			}
			mux.ServeHTTP(w, r)
		})).ServeHTTP(w, r)
	})
}

// TokenRoute decides whether an API token whose user has role and whose
// scopes are scopes may call route: nil, or a *Refusal unwrapping to
// ErrSessionOnly (the route never takes a token) or ErrForbidden (the role
// or the scopes don't reach it).
func TokenRoute(route Route, role string, scopes []string) error {
	switch {
	case route.Access == Public:
		return nil
	case route.Token == "" || route.Access == NodeKey || route.Access == Signature:
		return &Refusal{Err: ErrSessionOnly, Msg: "API tokens can't use this; sign in to Cha Dash instead."}
	case !route.Access.Allows(role):
		return forbid("%s This token acts as its user, so it can't either.", route.Access.refusal())
	case route.Token == TokenAny:
		return nil
	}
	sc, err := ParseScope(route.Token)
	if err != nil || !covered(scopes, sc, "") {
		return forbid("This token doesn't have the %s scope.", route.Token)
	}
	return nil
}

// TokenActor is the actor an API token's identity acts as: kind token, the
// token's ID, named after the token and its user ("backup script (sam)"),
// with the user's current role. It can't step up.
func TokenActor(id auth.Identity) Actor {
	return Actor{
		Kind:     KindToken,
		ID:       id.Token.ID,
		Name:     TokenActorName(id.Token.Name, id.User.Username),
		Role:     id.User.Role,
		Via:      "api",
		IP:       id.IP,
		User:     id.User.ID,
		UserName: id.User.Username,
	}
}

// TokenActorName names a token's actor in the audit log and the Inbox.
func TokenActorName(token, user string) string { return token + " (" + user + ")" }

// ActorFrom is the actor behind a request that passed Guard (or
// auth.Require): the signed-in user, their session and its step-up grant,
// or the API token and the user it acts for (TokenActor). The action engine
// and the audit log read it.
func ActorFrom(r *http.Request) (Actor, bool) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		return Actor{}, false
	}
	if id.Token != nil {
		return TokenActor(id), true
	}
	return Actor{
		Kind:        KindUser,
		ID:          id.User.ID,
		Name:        id.User.Username,
		Role:        id.User.Role,
		Via:         "web",
		Session:     id.Session.ID,
		IP:          id.IP,
		StepUpUntil: id.Session.StepUpUntil,
	}, true
}
