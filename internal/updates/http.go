package updates

import (
	"errors"
	"net/http"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
)

// Register mounts the Update Center's API. Who may call each route is
// decided by internal/authz/routes.go (reading is viewer+, the rest
// operator+); session only attaches the identity. {resource} is a
// container's resource ID, path-escaped (docker:i_x%2Fcontainer%2Fsonarr).
//
//	GET    /api/updates                    {"updates": [Row…]}
//	POST   /api/updates/check              Check now → {"asked": n}
//	POST   /api/updates/{resource}/hold    {"kind": "hold"|"snooze"} → Row
//	DELETE /api/updates/{resource}/hold    release it → Row
//
// Updating one is an action (POST /api/actions/plan), the one Row.Update
// names.
func (s *Service) Register(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	mux.Handle("GET /api/updates", session(http.HandlerFunc(s.handleList)))
	mux.Handle("POST /api/updates/check", session(http.HandlerFunc(s.handleCheck)))
	mux.Handle("POST /api/updates/{resource}/hold", session(http.HandlerFunc(s.handleHold)))
	mux.Handle("DELETE /api/updates/{resource}/hold", session(http.HandlerFunc(s.handleRelease)))
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.List(r.Context())
	if err != nil {
		s.internal(w, "updates: list", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"updates": rows})
}

func (s *Service) handleCheck(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"asked": s.Check()})
}

func (s *Service) handleHold(w http.ResponseWriter, r *http.Request) {
	resource := r.PathValue("resource")
	var body struct {
		Kind string `json:"kind"`
	}
	if !httpx.DecodeJSONLimit(w, r, &body, 1<<10) {
		return
	}
	if body.Kind != KindHold && body.Kind != KindSnooze {
		httpx.Error(w, http.StatusBadRequest, "invalid", `kind must be "hold" or "snooze".`)
		return
	}
	user := ""
	if a, ok := authz.ActorFrom(r); ok {
		user = a.ID
		if a.Kind == authz.KindToken {
			user = a.User
		}
	}
	row, err := s.Hold(r.Context(), resource, body.Kind, user)
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", "There's no such container with an update check.")
		return
	}
	if err != nil {
		s.internal(w, "updates: hold", err)
		return
	}
	params := map[string]any{"kind": body.Kind, "container": row.Name, "engine": row.Engine}
	if row.Hold != nil && row.Hold.Digest != "" {
		params["digest"] = row.Hold.Digest
	}
	s.Audit.Change(r, "update.hold", resource, params, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, row)
}

func (s *Service) handleRelease(w http.ResponseWriter, r *http.Request) {
	resource := r.PathValue("resource")
	row, ok, err := s.Release(r.Context(), resource)
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", "There's no such container with an update check.")
		return
	}
	if err != nil {
		s.internal(w, "updates: release", err)
		return
	}
	if !ok {
		httpx.Error(w, http.StatusNotFound, "not_held", row.Name+" isn't held or snoozed.")
		return
	}
	s.Audit.Change(r, "update.release", resource, map[string]any{"container": row.Name, "engine": row.Engine}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, row)
}

func (s *Service) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
}
