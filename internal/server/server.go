// Package server assembles the hub's HTTP surface:
//
//	GET  /healthz              liveness + database ping, unauthenticated (for `chadash healthcheck`)
//	GET  /api/setup            first-run status
//	POST /api/setup            create the owner with the setup token
//	POST /api/auth/login       password sign-in
//	POST /api/auth/logout      sign out
//	GET  /api/auth/session     current user and session            (session)
//	GET  /api/version          build version                       (session)
//	GET  /ws                   realtime WebSocket                  (session)
//	GET  /                     the web app (placeholder until the SPA is embedded)
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
	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/store"
)

// Deps are the subsystems the HTTP layer exposes.
type Deps struct {
	Log      *slog.Logger
	Store    *store.Store
	Auth     *auth.Service
	Realtime http.Handler // the WebSocket hub; mounted behind the session check
	// Web serves the SPA. Nil serves Placeholder. Step 3.8 passes the embedded
	// web/dist handler here (with history-mode fallback to index.html).
	Web            http.Handler
	Proxies        httpx.Proxies
	AllowedOrigins []string // extra trusted origins, scheme://host[:port]
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

// Placeholder stands in for the SPA until it is embedded (step 3.8).
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
