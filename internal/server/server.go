// Package server assembles the hub's HTTP surface:
//
//	GET  /healthz              liveness + database ping, unauthenticated (for `chadash healthcheck`)
//	GET  /api/setup            first-run status
//	POST /api/setup            create the owner (first run only)
//	POST /api/auth/login       password sign-in
//	POST /api/auth/logout      sign out
//	GET  /api/auth/session     current user, session and step-up state (session)
//	POST /api/auth/dev-login   one-click owner sign-in; -tags devlogin builds only, loopback only
//	POST /api/auth/passkeys/login/{begin,finish}      usernameless passkey sign-in
//	POST /api/auth/passkeys/register/{begin,finish}   add a passkey to your account (session)
//	GET  /api/auth/passkeys, PATCH|DELETE …/{id}      manage your passkeys          (session)
//	POST /api/auth/step-up/begin, POST /api/auth/step-up   confirm it's you for T3+ (session)
//	GET  /api/version          build version                       (session)
//	GET  /api/boards           boards; creates "Home" on first use (session)
//	GET  /api/boards/{id}/layout   latest layout and revision      (session)
//	PUT  /api/boards/{id}/layout   save at the next revision       (admin)
//	GET|POST /api/users, PUT /api/users/{id}   users and roles     (admin; see authz.Users)
//	POST /api/users/{id}/transfer-ownership    hand over ownership (owner, step-up)
//	GET /api/bans, DELETE /api/bans/{ip}       banned addresses    (admin; see authz.Bans)
//	/api/integrations/…        catalog, instances, tests, diagnostics (admin; see runtime.Manager.Register)
//	/api/correlate/…           identity suggestions and decisions  (viewer reads, admin decides)
//	POST /api/import/homepage  Homepage import; ?dry_run=1 plans only (admin)
//	GET  /api/import/homepage  the last Homepage import's record   (admin)
//	GET  /api/export           the hub as a Home file, secrets as placeholders (admin)
//	POST /api/export           the same with an encrypted secrets bundle (admin)
//	POST /api/import/chadash   import a Home file; ?dry_run=1 plans only (admin)
//	GET|PUT /api/onboarding, POST /api/onboarding/discover   first-run wizard (admin; see onboarding)
//	GET  /api/settings/security    security posture, step-up rule and their options (viewer)
//	PUT  /api/settings/security    {posture?, step_up?}; step_up needs a step-up (owner)
//	GET  /api/demo             demo clock and seed; demo mode only (session)
//	POST /api/actions/plan, POST /api/plans/{id}/approve, GET /api/tasks/{id},
//	POST /api/tasks/{id}/cancel, GET /api/actions?target=
//	                           the action engine (session; the policy decides, see action.Engine.Register)
//	GET  /api/audit, /api/audit/export, /api/audit/verify   the audit log (admin; audit.read)
//	GET  /api/approvals, GET /api/approvals/{id}, POST …/{id}/approve, POST …/{id}/deny
//	                           plans waiting for a person (admin decides; the token that asked may read its own)
//	GET|POST /api/tokens, DELETE /api/tokens/{id}, GET /api/tokens/scopes
//	                           API tokens: your own; admins and the owner see and revoke any (session only)
//	GET  /api/graph, GET /api/inbox   the Home Graph and the Inbox as plain reads (session, or a token's graph.read / inbox.read)
//	GET  /node                 chadash-node's channel (the node's pinned key, not a session; see nodes.Hub)
//	GET  /api/nodes, POST /api/nodes/{id}/revoke, POST /api/nodes/pairing/regenerate   nodes, revoking one, a fresh pairing secret (admin)
//	/api/stacks/…              owned stacks: list (viewer), files, drafts, validation, secrets (admin; see stacks.Service.Register)
//	/api/notify/…              notifications: channels, rules, delivery log (admin); the VAPID key and your own push subscriptions (session; see notify.Service.Register)
//	GET  /api/history   metrics history: points of up to 20 series over a range (session, or graph.read; see history.go)
//	GET  /api/icons, GET /api/icons/{id}   icons the hub cached for tiles (session; see icons)
//	GET  /ws                   realtime WebSocket                  (session)
//	GET  /…                    the web app (history-mode: unknown paths get index.html,
//	                           with the first-paint snapshot for any signed-in role)
//
// Roles in parentheses are the least a route admits ("session": any signed-in
// role). The table that decides is internal/authz/routes.go, enforced by
// authz.Guard in front of the mux; a route without a row there is refused.
// The same table says which routes take an API token (Authorization:
// Bearer cdt_…) and with which scope; every other route is session only.
//
// Every response gets security headers; state-changing requests from other
// origins are refused (net/http CrossOriginProtection), and the WebSocket
// upgrade checks Origin itself. JSON API responses of 1 KiB or more are
// gzipped for clients that accept it, except on the routes that handle
// secrets (compressAPI); the web app compresses its own files.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/correlate"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/export"
	"dash.cha.sh/internal/firstpaint"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/icons"
	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/importer/homarr"
	"dash.cha.sh/internal/importer/homepage"
	"dash.cha.sh/internal/importer/importhttp"
	"dash.cha.sh/internal/importer/leads"
	"dash.cha.sh/internal/importer/yamlboard"
	"dash.cha.sh/internal/nodes"
	"dash.cha.sh/internal/notify"
	"dash.cha.sh/internal/onboarding"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/security"
	"dash.cha.sh/internal/stacks"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
	"dash.cha.sh/internal/webui"
)

// Deps are the subsystems the HTTP layer exposes.
type Deps struct {
	Log   *slog.Logger
	Store *store.Store
	Auth  *auth.Service
	// Realtime is the WebSocket hub, mounted behind the session check. When
	// it's a *realtime.Hub, sign-outs and account changes close the sockets
	// they end (build it with Recheck: SocketRecheck for the timer half).
	Realtime http.Handler
	// Web serves the SPA (webui.New in the hub). Nil serves Placeholder.
	Web     http.Handler
	Proxies httpx.Proxies
	// ProxyCheck notices an untrusted reverse proxy in front of the hub.
	// Nil skips the check.
	ProxyCheck     *httpx.ProxyCheck
	AllowedOrigins []string // extra trusted origins, scheme://host[:port]
	// Demo is the Demo Home driver in demo mode, nil otherwise. Without it
	// the /api/demo endpoints answer 404.
	Demo *demo.Driver
	// Vault holds integration credentials. Without it the import routes
	// aren't mounted (they move secrets into it).
	Vault *vault.Vault
	// Integrations runs integration instances and serves their API. Nil
	// leaves /api/integrations unmounted.
	Integrations *runtime.Manager
	// Correlator merges what several sources report as one thing and
	// serves its suggestions. Nil leaves /api/correlate unmounted.
	Correlator *correlate.Correlator
	// Onboarding is the first-run wizard's state and discovery. Nil leaves
	// /api/onboarding unmounted.
	Onboarding *onboarding.Service
	// FirstPaint builds the snapshot index.html carries for a signed-in
	// user (see firstPaint); its Inbox field gives each role its view of the
	// inbox (InboxView). Nil serves the generic page to everyone.
	FirstPaint *firstpaint.Builder
	// Security is the security posture. Nil leaves /api/settings/security
	// unmounted.
	Security *security.Service
	// Actions is the action engine; it also serves the audit log. Nil
	// leaves /api/actions, /api/plans, /api/tasks and /api/audit unmounted.
	Actions *action.Engine
	// Audit records changes to users and settings; actions record their
	// own through the engine. Nil records nothing.
	Audit *audit.Log
	// Nodes serves chadash-node's channel at /node and the nodes API. Nil
	// leaves both unmounted.
	Nodes *nodes.Hub
	// Stacks is the owned-stacks API over the nodes. Nil leaves
	// /api/stacks unmounted. Its actions are registered with the engine by
	// the caller (stacks.Service.Actions).
	Stacks *stacks.Service
	// Notify is notifications (channels, rules, devices, the delivery
	// log). Nil leaves /api/notify unmounted.
	Notify *notify.Service
	// Topics are the live documents GET /api/graph and GET /api/inbox read
	// (the realtime topics). Nil leaves both unmounted.
	Topics *topics.Set
	// Icons is the hub's icon cache for tiles. Nil leaves /api/icons
	// unmounted.
	Icons *icons.Service
	// History is the metrics history (internal/history). Nil answers
	// /api/history with level "off". It needs Topics for household's view.
	History *history.Recorder
}

// New builds the root handler.
func New(d Deps) (http.Handler, error) {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.Store.Ping(ctx); err != nil {
			d.Log.Warn("health check failed", "err", err)
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	d.Auth.Register(mux)
	mux.Handle("GET /api/version", d.Auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"version": buildinfo.Version, "commit": buildinfo.Commit})
	})))
	// session attaches the signed-in identity. Who may call each route is
	// decided by authz.Guard, in front of the whole mux, from the table in
	// internal/authz/routes.go: a new route needs a row there.
	session := d.Auth.Require
	rec := &authz.Recorder{Log: d.Audit, Logger: d.Log}
	bs := boards.New(d.Store)
	bs.Audit = rec
	bs.Register(mux, session, d.Log)
	auditAuthEvents(d.Auth, rec)
	if hub, ok := d.Realtime.(*realtime.Hub); ok {
		// Sign-outs and account changes close the sockets they end.
		closeSocketsOnAuthEvents(d.Auth, hub)
	}
	users := authz.NewUsers(d.Auth, d.Log)
	users.Audit = rec
	users.Register(mux, session)
	bans := authz.NewBans(d.Auth, d.Log)
	bans.Audit = rec
	bans.Register(mux, session)
	tokens := authz.NewTokens(d.Auth, d.Log)
	tokens.Audit = rec
	if d.Actions != nil {
		reg, eng := d.Actions.Registry(), d.Actions
		tokens.Actions = func() []authz.TokenAction {
			ids := reg.IDs()
			out := make([]authz.TokenAction, 0, len(ids))
			for _, id := range ids {
				if def, ok := reg.Get(id); ok {
					out = append(out, authz.TokenAction{ID: id, Title: def.Title, Tier: authz.Tier(def.Tier.String()), Household: def.Household, ReadsData: def.ReadsData})
				}
			}
			return out
		}
		// Revoking a token withdraws the plans it left waiting for a person.
		tokens.OnRevoke = func(ctx context.Context, id string) { eng.Withdraw(ctx, id, "The token that asked was revoked.") }
	}
	tokens.Register(mux, session)
	if d.Topics != nil {
		registerReads(mux, session, d.Topics, InboxView(boards.New(d.Store), d.Topics.Graph, d.Log))
		registerHistory(mux, session, d.History, d.Topics.Graph)
	}
	if d.Integrations != nil {
		d.Integrations.Audit = rec
		d.Integrations.Register(mux, session)
	}
	if d.Correlator != nil {
		d.Correlator.Audit = rec
		d.Correlator.Register(mux, session, d.Log)
	}
	if d.Vault != nil {
		wrap := session
		if d.Integrations != nil {
			// An applied import writes instances directly; start them now
			// rather than at the manager's next periodic reload.
			wrap = func(h http.Handler) http.Handler { return session(reloadAfter(h, d.Integrations, d.Log)) }
		}
		importhttp.Register(mux, wrap, importer.New(d.Store, d.Vault), d.Log, rec, importSources(d)...)
		exp := export.New(d.Store, d.Vault, export.Options{HubVersion: buildinfo.Version})
		exp.Audit = rec
		// A Home file can carry the security settings and correlation
		// decisions: the running hub takes them up at once.
		exp.AfterApply = func(ctx context.Context) {
			ctx = context.WithoutCancel(ctx)
			if d.Security != nil {
				if err := d.Security.Reload(ctx); err != nil {
					d.Log.Error("reloading the security settings after an import failed", "err", err)
				}
			}
			if d.Correlator != nil {
				if err := d.Correlator.Sync(ctx); err != nil {
					d.Log.Warn("correlating after an import failed", "err", err)
				}
			}
		}
		export.Register(mux, wrap, exp, d.Log)
	}
	if d.Demo != nil {
		d.Demo.Register(mux, session)
	}
	if d.Onboarding != nil {
		d.Onboarding.Audit = rec
		d.Onboarding.Register(mux, session)
	}
	if d.Security != nil {
		d.Security.Audit = rec
		d.Security.Register(mux, session, session)
		d.Auth.SetStepUpMode(d.Security.StepUp)
	}
	if d.Actions != nil {
		d.Actions.Register(mux, d.Auth.Require, d.Log)
	}
	if d.Nodes != nil {
		d.Nodes.Register(mux, session, func(r *http.Request) audit.Actor {
			a, _ := authz.ActorFrom(r)
			return authz.AuditActor(a)
		})
	}
	if d.Stacks != nil {
		d.Stacks.Audit = rec
		d.Stacks.Register(mux, session)
	}
	if d.Notify != nil {
		d.Notify.Audit = rec
		d.Notify.Register(mux, session)
	}
	if d.Icons != nil {
		d.Icons.Register(mux, session, d.Log)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	mux.Handle("GET /ws", d.Auth.Require(d.Realtime))

	web := d.Web
	if web == nil {
		web = Placeholder()
	}
	// "/" rather than "GET /": the latter would conflict with the any-method
	// "/api/" fallback in ServeMux's precedence rules.
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.FirstPaint != nil {
			r = r.WithContext(webui.WithInline(r.Context(), func() []byte { return firstPaint(d, r) }))
		}
		web.ServeHTTP(w, r)
	}))

	cop := http.NewCrossOriginProtection()
	for _, o := range d.AllowedOrigins {
		if err := cop.AddTrustedOrigin(o); err != nil {
			return nil, fmt.Errorf("server: allowed origin %q: %w", o, err)
		}
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusForbidden, "cross_origin", "cross-origin request refused")
	}))

	var h http.Handler = authz.Guard(mux, d.Auth, d.Log)
	h = compressAPI(h)
	h = cop.Handler(h)
	h = httpx.Recover(d.Log)(h)
	if d.ProxyCheck != nil {
		h = d.ProxyCheck.Middleware(h)
	}
	h = httpx.AccessLog(d.Log, d.Proxies)(h)
	h = httpx.SecurityHeaders(h)
	return h, nil
}

// compressAPI gzips the API's JSON responses (httpx.GzipJSON) on the routes
// compressible allows. The web app compresses its own files (webui), and
// /ws and /healthz are never touched.
func compressAPI(next http.Handler) http.Handler {
	gz := httpx.GzipJSON(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if compressible(r.URL.Path) {
			gz.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// noCompress are the API routes whose responses can carry a secret, or take
// one in: sign-in, passkey ceremonies and the session; first-run setup with
// its token; an integration sign-in, whose poll hands the new token to the
// form once; and creating an API token, whose response carries its secret. Compressing a secret next to anything an attacker might
// influence is what BREACH needs, so these stay identity whatever their size
// (internal/httpx/gzip.go has the full reasoning).
var noCompress = []string{"/api/auth", "/api/setup", "/api/integrations/authorize", "/api/tokens"}

// compressible reports whether GzipJSON may handle a request for path.
func compressible(path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return false
	}
	for _, p := range noCompress {
		if path == p || strings.HasPrefix(path, p+"/") {
			return false
		}
	}
	return true
}

// importSources are the importers the hub offers. Homarr and plain YAML ask
// the integration catalog which fields are secret and which are required.
func importSources(d Deps) []importhttp.Source {
	cat := importCatalog{d.Integrations}
	return []importhttp.Source{homepage.ImportSource(), homarr.ImportSource(cat), yamlboard.ImportSource(cat), leads.ImportSource(cat)}
}

// importCatalog answers importer.Catalog from the running manager's catalog.
type importCatalog struct{ m *runtime.Manager }

func (c importCatalog) Integration(id string) (secret, required []string, ok bool) {
	if c.m == nil {
		return nil, nil, false
	}
	for _, e := range c.m.Catalog() {
		if e.ID == id {
			return e.SecretFields(), e.ConfigSchema.Required, true
		}
	}
	return nil, nil, false
}

// reloadAfter reloads the integration instances after a successful POST.
func reloadAfter(next http.Handler, m *runtime.Manager, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if r.Method == http.MethodPost && rec.code < 300 {
			if err := m.Reload(context.WithoutCancel(r.Context())); err != nil {
				log.Error("reloading integration instances failed", "err", err)
			}
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Placeholder answers "/" when Deps.Web is nil, as in tests that don't need
// the web app.
func Placeholder() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Cha Dash</title></head>
<body><h1>Cha Dash</h1>
<p>The hub is running. This build doesn't include the web app yet.</p>
<p>First run? The web app creates the owner account.</p></body></html>
`)
	})
}

// ShutdownTimeout bounds how long in-flight requests get to finish.
const ShutdownTimeout = 10 * time.Second

// Serve serves h on ln until ctx is cancelled, then shuts down gracefully:
// stop accepting, let in-flight requests finish, then run onShutdown (e.g.
// closing WebSockets, which Shutdown doesn't track because they're hijacked).
func Serve(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger, onShutdown ...func()) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		// No ReadTimeout/WriteTimeout: they would also cut long-lived
		// WebSockets. Bodies are bounded by MaxBytesReader instead.
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	err := srv.Shutdown(sctx)
	for _, f := range onShutdown {
		f()
	}
	if serr := <-errc; !errors.Is(serr, http.ErrServerClosed) {
		err = errors.Join(err, serr)
	}
	return err
}
