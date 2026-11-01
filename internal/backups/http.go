package backups

import (
	"errors"
	"net/http"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// ObjectiveRequest is PUT /api/backups/objectives/{resource}'s body.
type ObjectiveRequest struct {
	MaxAgeMS int64 `json:"max_age_ms"`
}

// Register mounts the objectives' routes. Who may call them is decided by
// internal/authz/routes.go (admin+); session only attaches the identity.
// {resource} is a protectable resource's ID, path-escaped
// (proxmox:i_x%2Fqemu%2F100). The map itself is the backups topic, and
// GET /api/backups (internal/server) as each viewer sees it.
//
//	PUT    /api/backups/objectives/{resource}  {"max_age_ms": n} → Objective
//	DELETE /api/backups/objectives/{resource}  → Objective, as it was
func (s *Service) Register(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	mux.Handle("PUT /api/backups/objectives/{resource}", session(http.HandlerFunc(s.handleSet)))
	mux.Handle("DELETE /api/backups/objectives/{resource}", session(http.HandlerFunc(s.handleClear)))
}

func (s *Service) handleSet(w http.ResponseWriter, r *http.Request) {
	resource := r.PathValue("resource")
	var body ObjectiveRequest
	if !httpx.DecodeJSONLimit(w, r, &body, 1<<10) {
		return
	}
	user := ""
	if a, ok := authz.ActorFrom(r); ok {
		user = a.ID
		if a.Kind == authz.KindToken {
			user = a.User
		}
	}
	ob, err := s.SetObjective(r.Context(), resource, time.Duration(body.MaxAgeMS)*time.Millisecond, user)
	switch {
	case errors.Is(err, ErrOutOfRange):
		httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("backups.objective_range"))
		return
	case errors.Is(err, ErrNotProtectable):
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("backups.not_protectable"))
		return
	case err != nil:
		s.internal(w, "backups: set objective", err)
		return
	}
	it, _ := s.Item(resource)
	s.Audit.Change(r, "backups.objective.set", resource, map[string]any{"name": it.Name, "max_age_ms": ob.MaxAgeMS}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, ob)
}

func (s *Service) handleClear(w http.ResponseWriter, r *http.Request) {
	resource := r.PathValue("resource")
	ob, err := s.ClearObjective(r.Context(), resource)
	switch {
	case errors.Is(err, ErrNotSet):
		httpx.Say(w, http.StatusNotFound, "not_set", msg.New("backups.objective_not_set"))
		return
	case err != nil:
		s.internal(w, "backups: clear objective", err)
		return
	}
	it, _ := s.Item(resource)
	s.Audit.Change(r, "backups.objective.clear", resource, map[string]any{"name": it.Name, "max_age_ms": ob.MaxAgeMS}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, ob)
}

func (s *Service) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
}
