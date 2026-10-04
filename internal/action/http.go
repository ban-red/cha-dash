package action

import (
	"errors"
	"log/slog"
	"net/http"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/httpx"
)

// maxIdempotencyKey bounds the Idempotency-Key header.
const maxIdempotencyKey = 128

// Register mounts the engine's endpoints, and the audit log's, each behind
// session (the hub's session check). Authorization is the Policy's, per
// action or per read scope:
//
//	POST /api/actions/plan        {"action","target","params"?} → 200 Plan
//	POST /api/plans/{id}/approve  {"hash","typed"?}, optional Idempotency-Key → 202 {"task"}
//	GET  /api/tasks/{id}          → 200 Task (your own, or anyone's with ScopeTasksAll)
//	GET  /api/audit, /api/audit/export, /api/audit/verify   (ScopeAuditRead; see audit.Log.Register)
//
// Refusals use the error envelope with the codes in docs/plan/08-v0.2.md;
// "unavailable" adds "recipe" and "invalid" adds "fields" beside it.
func (e *Engine) Register(mux *http.ServeMux, session func(http.Handler) http.Handler, log *slog.Logger) {
	sessions := e.sessions
	actorOf := func(w http.ResponseWriter, r *http.Request) (Actor, bool) {
		a, ok := sessions.FromRequest(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
		}
		return a, ok
	}
	fail := func(w http.ResponseWriter, err error) {
		var ref *Refusal
		if errors.As(err, &ref) {
			writeRefusal(w, ref)
			return
		}
		log.Error("action: request failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
	}

	mux.Handle("POST /api/actions/plan", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := actorOf(w, r)
		if !ok {
			return
		}
		var req PlanRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		p, err := e.Plan(r.Context(), a, req)
		if err != nil {
			fail(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, p)
	})))
	mux.Handle("POST /api/plans/{id}/approve", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := actorOf(w, r)
		if !ok {
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) > maxIdempotencyKey {
			httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "The Idempotency-Key header is too long.")
			return
		}
		var req ApproveRequest
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		t, err := e.Approve(r.Context(), a, r.PathValue("id"), req, key)
		if err != nil {
			fail(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusAccepted, map[string]Task{"task": t})
	})))
	mux.Handle("GET /api/tasks/{id}", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := actorOf(w, r)
		if !ok {
			return
		}
		t, err := e.Task(r.Context(), a, r.PathValue("id"))
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "not_found", "There's no such task.")
			return
		} else if err != nil {
			fail(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, t)
	})))

	e.audit.Register(mux, func(h http.Handler) http.Handler { return session(e.Guard(ScopeAuditRead)(h)) }, log)
}

// Guard admits requests whose session's actor the policy allows scope
// (at T0, with no target), and answers 403 otherwise.
func (e *Engine) Guard(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a, ok := e.sessions.FromRequest(r)
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
				return
			}
			if err := e.policy.Authorize(r.Context(), a, scope, "", T0); err != nil {
				writeRefusal(w, forbidden(err))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeRefusal(w http.ResponseWriter, r *Refusal) {
	body := struct {
		Error  httpx.ErrorDetail `json:"error"`
		Recipe string            `json:"recipe,omitempty"`
		Fields []FieldError      `json:"fields,omitempty"`
	}{httpx.ErrorDetail{Code: r.Code, Message: r.Message}, r.Recipe, r.Fields}
	status := r.Status
	if status == 0 {
		status = http.StatusConflict
	}
	httpx.WriteJSON(w, status, body)
}

// Audit is the engine's audit log.
func (e *Engine) Audit() *audit.Log { return e.audit }
