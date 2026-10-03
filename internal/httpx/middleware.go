package httpx

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// SecurityHeaders sets the hardening headers every response carries. The CSP
// matches the architecture's rule: no inline scripts, no eval, no third-party
// origins. API responses are never cached.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// Recover turns a handler panic into a 500 and a log line instead of a
// dropped connection.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					log.Error("handler panic", "method", r.Method, "path", r.URL.Path, "panic", v)
					Error(w, http.StatusInternalServerError, "internal", "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog logs one line per request. Health checks log at debug so Docker's
// HEALTHCHECK doesn't flood the log.
func AccessLog(log *slog.Logger, proxies Proxies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			level := slog.LevelInfo
			if r.URL.Path == "/healthz" {
				level = slog.LevelDebug
			}
			log.Log(r.Context(), level, "http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status(),
				"duration", time.Since(start).Round(time.Microsecond),
				"client", proxies.ClientIP(r).String(),
			)
		})
	}
}

// statusRecorder captures the response status. Unwrap keeps Hijack (for
// WebSockets) and Flush reachable through http.ResponseController.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) status() int {
	if s.code == 0 {
		return http.StatusOK
	}
	return s.code
}
