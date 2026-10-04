// Package server assembles the hub's HTTP surface:
//
//	GET  /healthz              liveness + database ping, unauthenticated (for `chadash healthcheck`)
//	GET  /api/setup            first-run status
//	POST /api/setup            create the owner with the setup token
//	POST /api/auth/login       password sign-in
//	POST /api/auth/logout      sign out
//	GET  /api/auth/session     current user and session            (session)
//	POST /api/auth/dev-login   one-click owner sign-in; -tags devlogin builds only, loopback only
//	POST /api/auth/passkeys/login/{begin,finish}      usernameless passkey sign-in
//	POST /api/auth/passkeys/register/{begin,finish}   add a passkey  (owner)
//	GET  /api/auth/passkeys, PATCH|DELETE …/{id}      manage them    (owner)
//	GET  /api/version          build version                       (session)
//	GET  /api/boards           boards; creates "Home" on first use (owner)
//	GET  /api/boards/{id}/layout   latest layout and revision      (owner)
//	PUT  /api/boards/{id}/layout   save at the next revision       (owner)
//	/api/integrations/…        catalog, instances, tests, diagnostics (owner; see runtime.Manager.Register)
//	POST /api/import/homepage  Homepage import; ?dry_run=1 plans only (owner)
//	GET  /api/import/homepage  the last Homepage import's record   (owner)
//	GET  /api/demo             demo clock and seed; demo mode only (owner)
//	POST /api/demo/incidents/{id}/fix   press the suggested action (owner)
//	GET  /ws                   realtime WebSocket                  (session)
//	GET  /…                    the web app (history-mode: unknown paths get index.html,
//	                           with the first-paint snapshot for a signed-in owner)
//
// Every response gets security headers; state-changing requests from other
// origins are refused (net/http CrossOriginProtection), and the WebSocket
// upgrade checks Origin itself.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/demo"
	"dash.cha.sh/internal/firstpaint"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/importer"
	"dash.cha.sh/internal/importer/homepage"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/vault"
	"dash.cha.sh/internal/webui"
)

// Deps are the subsystems the HTTP layer exposes.
type Deps struct {
	Log      *slog.Logger
	Store    *store.Store
	Auth     *auth.Service
	Realtime http.Handler // the WebSocket hub; mounted behind the session check
	// Web serves the SPA (webui.New in the hub). Nil serves Placeholder.
	Web            http.Handler
	Proxies        httpx.Proxies
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
	// FirstPaint builds the snapshot index.html carries for a signed-in
	// owner (see firstPaint). Nil serves the generic page to everyone.
	FirstPaint *firstpaint.Builder
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
	owner := func(h http.Handler) http.Handler { return d.Auth.Require(requireOwner(h)) }
	boards.New(d.Store).Register(mux, owner, d.Log)
	if d.Integrations != nil {
		d.Integrations.Register(mux, owner)
	}
	if d.Vault != nil {
		wrap := owner
		if d.Integrations != nil {
			// An applied import writes instances directly; start them now
			// rather than at the manager's next periodic reload.
			wrap = func(h http.Handler) http.Handler { return owner(reloadAfter(h, d.Integrations, d.Log)) }
		}
		homepage.Register(mux, wrap, importer.New(d.Store, d.Vault), d.Log)
	}
	if d.Demo != nil {
		d.Demo.Register(mux, owner)
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

	var h http.Handler = mux
	h = cop.Handler(h)
	h = httpx.Recover(d.Log)(h)
	h = httpx.AccessLog(d.Log, d.Proxies)(h)
	h = httpx.SecurityHeaders(h)
	return h, nil
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

// requireOwner admits the owner's session only. Every account is the owner
// today; other roles arrive later and must not inherit these routes.
func requireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := auth.FromContext(r.Context()); !ok || id.User.Role != auth.RoleOwner {
			httpx.Error(w, http.StatusForbidden, "forbidden", "only the owner can do this")
			return
		}
		next.ServeHTTP(w, r)
	})
}

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
<p>First run? The setup token is in the hub's logs.</p></body></html>
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
