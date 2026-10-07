package correlate

import (
	"errors"
	"log/slog"
	"net/http"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/httpx"
)

// Register mounts the correlation API, each route wrapped by wrap (the
// hub's owner-session check):
//
//	GET    /api/correlate/links                       what the correlator publishes, with rule, confidence and evidence
//	GET    /api/correlate/suggestions                 open suggestions, oldest first
//	POST   /api/correlate/suggestions/{id}/confirm    merge (or link) → 200 the decision; 404 if it isn't open
//	POST   /api/correlate/suggestions/{id}/reject     keep separate → 200 the decision
//	GET    /api/correlate/decisions                   the owner's decisions, oldest first
//	POST   /api/correlate/decisions                   {"from","type","to","verdict"} → 201 the decision: merge or split any pair
//	DELETE /api/correlate/decisions/{id}              forget a decision → 204
func (c *Correlator) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler, log *slog.Logger) {
	h := handlers{c: c, log: log}
	mux.Handle("GET /api/correlate/links", wrap(http.HandlerFunc(h.links)))
	mux.Handle("GET /api/correlate/suggestions", wrap(http.HandlerFunc(h.suggestions)))
	mux.Handle("POST /api/correlate/suggestions/{id}/confirm", wrap(h.answer(Confirm)))
	mux.Handle("POST /api/correlate/suggestions/{id}/reject", wrap(h.answer(Reject)))
	mux.Handle("GET /api/correlate/decisions", wrap(http.HandlerFunc(h.decisions)))
	mux.Handle("POST /api/correlate/decisions", wrap(http.HandlerFunc(h.decide)))
	mux.Handle("DELETE /api/correlate/decisions/{id}", wrap(http.HandlerFunc(h.forget)))
}

type handlers struct {
	c   *Correlator
	log *slog.Logger
}

func userID(r *http.Request) string {
	if id, ok := auth.FromContext(r.Context()); ok {
		return id.User.ID
	}
	return ""
}

func (h handlers) fail(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not_found", "no such suggestion, decision or resource")
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid",
			`a decision needs two different resources, a type (same_as, runs_on, routes_to, stores_on, depends_on, monitors) and a verdict ("confirm" or "reject")`)
	default:
		if h.log != nil {
			h.log.Error(what, "err", err)
		}
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func (h handlers) links(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, nonNil(h.c.Links()))
}

func (h handlers) suggestions(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, nonNil(h.c.Suggestions()))
}

func (h handlers) answer(v Verdict) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, err := h.c.DecideSuggestion(r.Context(), r.PathValue("id"), v, userID(r))
		if err != nil {
			h.fail(w, "answering a suggestion failed", err)
			return
		}
		h.c.Audit.Change(r, "correlate.suggestion."+string(v), "suggestion:"+r.PathValue("id"), decisionParams(d), authz.T1, nil)
		httpx.WriteJSON(w, http.StatusOK, d)
	})
}

func (h handlers) decisions(w http.ResponseWriter, r *http.Request) {
	ds, err := h.c.Decisions(r.Context())
	if err != nil {
		h.fail(w, "listing decisions failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, nonNil(ds))
}

func (h handlers) decide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From    graph.RID     `json:"from"`
		Type    graph.RelType `json:"type"`
		To      graph.RID     `json:"to"`
		Verdict Verdict       `json:"verdict"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	d, err := h.c.Decide(r.Context(), req.From, req.Type, req.To, req.Verdict, userID(r))
	if err != nil {
		h.fail(w, "saving a decision failed", err)
		return
	}
	h.c.Audit.Change(r, "correlate.decision.create", "decision:"+d.ID, decisionParams(d), authz.T1, nil)
	httpx.WriteJSON(w, http.StatusCreated, d)
}

func (h handlers) forget(w http.ResponseWriter, r *http.Request) {
	if err := h.c.Forget(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, "forgetting a decision failed", err)
		return
	}
	h.c.Audit.Change(r, "correlate.decision.delete", "decision:"+r.PathValue("id"), nil, authz.T1, nil)
	w.WriteHeader(http.StatusNoContent)
}

// decisionParams is a decision as the audit log keeps it: the pair, the
// relation and the verdict.
func decisionParams(d Decision) map[string]any {
	return map[string]any{"from": d.From, "type": d.Type, "to": d.To, "verdict": d.Verdict}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
