package authz

import (
	"log/slog"
	"net/http"

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
	return false // NodeKey: never a person
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
}

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
	{Pattern: "GET /api/setup", Access: Public},
	{Pattern: "POST /api/setup", Access: Public},
	{Pattern: "POST /api/auth/login", Access: Public},
	{Pattern: "POST /api/auth/logout", Access: Public},
	{Pattern: "POST /api/auth/passkeys/login/begin", Access: Public},
	{Pattern: "POST /api/auth/passkeys/login/finish", Access: Public},
	{Pattern: "POST /api/auth/dev-login", Access: Public, Optional: true}, // -tags devlogin, loopback only
	{Pattern: "/api/", Access: Public},                                    // the API's 404
	{Pattern: "/", Access: Public},                                        // the web app; first paint checks the session itself

	// chadash-node's channel: authenticated by the node's pinned key inside
	// mutual TLS, never by a session (internal/nodes).
	{Pattern: "GET /node", Access: NodeKey},

	// The signed-in user's own account, whatever the role.
	{Pattern: "GET /api/auth/session", Access: SignedIn},
	{Pattern: "GET /api/version", Access: SignedIn},
	{Pattern: "POST /api/auth/passkeys/register/begin", Access: SignedIn},
	{Pattern: "POST /api/auth/passkeys/register/finish", Access: SignedIn},
	{Pattern: "GET /api/auth/passkeys", Access: SignedIn},
	{Pattern: "PATCH /api/auth/passkeys/{id}", Access: SignedIn},
	{Pattern: "DELETE /api/auth/passkeys/{id}", Access: SignedIn},
	{Pattern: "POST /api/auth/step-up/begin", Access: SignedIn},
	{Pattern: "POST /api/auth/step-up", Access: SignedIn},

	// Boards and live data.
	{Pattern: "GET /api/boards", Access: SignedIn},
	{Pattern: "GET /api/boards/{id}/layout", Access: SignedIn},
	{Pattern: "GET /ws", Access: SignedIn},
	{Pattern: "GET /api/demo", Access: SignedIn},
	{Pattern: "GET /api/boards/{id}/discovery", Access: Viewer},
	{Pattern: "PUT /api/boards/{id}/layout", Access: Admin},
	{Pattern: "POST /api/boards/{id}/discovery/dismiss", Access: Admin},
	{Pattern: "POST /api/boards/{id}/discovery/restore", Access: Admin},

	// Actions: the engine authorizes each one (Policy), so the routes only
	// need a session. Added by the action engine track.
	{Pattern: "POST /api/actions/plan", Access: SignedIn},
	{Pattern: "POST /api/plans/{id}/approve", Access: SignedIn},
	{Pattern: "GET /api/tasks/{id}", Access: SignedIn},
	// Cancelling: your own task, or anyone's with tasks.all (the engine asks
	// the policy). Listing what could be planned on a target: filtered by
	// the policy, per action.
	{Pattern: "POST /api/tasks/{id}/cancel", Access: SignedIn},
	{Pattern: "GET /api/actions", Access: SignedIn},

	// Identity across sources: reads for viewers, decisions for admins.
	{Pattern: "GET /api/correlate/links", Access: Viewer},
	{Pattern: "GET /api/correlate/suggestions", Access: Viewer},
	{Pattern: "GET /api/correlate/decisions", Access: Viewer},
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
	{Pattern: "GET /api/integrations/instances/{id}/diagnostics", Access: Admin},
	{Pattern: "POST /api/integrations/authorize/{integration}", Access: Admin},
	{Pattern: "GET /api/integrations/authorize/{integration}/{id}", Access: Admin},

	// Import, export and the first-run wizard (which adds integrations).
	{Pattern: "POST /api/import/homepage", Access: Admin},
	{Pattern: "GET /api/import/homepage", Access: Admin},
	{Pattern: "GET /api/export", Access: Admin},
	{Pattern: "POST /api/export", Access: Admin},
	{Pattern: "POST /api/import/chadash", Access: Admin},
	{Pattern: "GET /api/onboarding", Access: Admin},
	{Pattern: "PUT /api/onboarding", Access: Admin},
	{Pattern: "POST /api/onboarding/discover", Access: Admin},

	// Settings → Security: the posture is shown to viewers; changing it, and
	// the step-up rule, is the owner's.
	{Pattern: "GET /api/settings/security", Access: Viewer},
	{Pattern: "PUT /api/settings/security", Access: Owner},

	// Settings → Users. An admin manages everyone but the owner (users.go);
	// handing over ownership is the owner's, and T3.
	{Pattern: "GET /api/users", Access: Admin},
	{Pattern: "POST /api/users", Access: Admin},
	{Pattern: "PUT /api/users/{id}", Access: Admin},
	{Pattern: "POST /api/users/{id}/transfer-ownership", Access: Owner},

	// Nodes: the list is admin+, and so is revoking one (audited as
	// nodes.revoke).
	{Pattern: "GET /api/nodes", Access: Admin},
	{Pattern: "POST /api/nodes/{id}/revoke", Access: Admin},
	// Settings → Stacks (track 9B). The list (names and status) is a read,
	// for viewers; a stack's files, history and drafts are admin+, because
	// compose files (adopted ones especially) often hold credentials in
	// plain text, and stacks are Settings in the Roles table. Deploying,
	// rolling back and adopting are actions, admin+ by authz.Decide.
	{Pattern: "GET /api/stacks", Access: Viewer},
	{Pattern: "GET /api/stacks/secrets", Access: Admin},
	{Pattern: "PUT /api/stacks/secrets/{name}", Access: Admin},
	{Pattern: "DELETE /api/stacks/secrets/{name}", Access: Admin},
	{Pattern: "GET /api/stacks/{node}/{stack}", Access: Admin},
	{Pattern: "PUT /api/stacks/{node}/{stack}", Access: Admin},
	{Pattern: "DELETE /api/stacks/{node}/{stack}/draft", Access: Admin},
	{Pattern: "POST /api/stacks/{node}/{stack}/validate", Access: Admin},

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

	// Settings → Audit. Added by the audit track.
	{Pattern: "GET /api/audit", Access: Admin},
	{Pattern: "GET /api/audit/export", Access: Admin},
	{Pattern: "GET /api/audit/verify", Access: Admin},
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
// it: public routes as they are, the rest behind a session (auth.Require,
// which attaches the identity handlers read) and the row's role. A route
// with no row is refused with 500 and an error in the log, so a forgotten
// row fails closed and loudly. Requests mux would answer with 404 or 405
// itself pass straight through.
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
		if route.Access == Public || route.Access == NodeKey {
			mux.ServeHTTP(w, r) // a node authenticates in the channel, by key
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

// ActorFrom is the actor behind a request that passed Guard (or
// auth.Require): the signed-in user, their session and its step-up grant.
// The action engine and the audit log read it.
func ActorFrom(r *http.Request) (Actor, bool) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		return Actor{}, false
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
