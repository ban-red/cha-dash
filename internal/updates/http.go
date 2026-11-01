package updates

import (
	"errors"
	"net/http"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// Register mounts the Update Center's API. Who may call each route is
// decided by internal/authz/routes.go (reading is viewer+, holds and
// rollouts operator+, settings and windows admin+); session only attaches
// the identity. {resource} is a row's resource ID, path-escaped
// (docker:i_x%2Fcontainer%2Fsonarr).
//
//	GET    /api/updates                         {"updates": [Row…]}
//	POST   /api/updates/check                   Check now → {"asked": n}
//	POST   /api/updates/{resource}/hold         {"kind": "hold"|"snooze"} → Row
//	DELETE /api/updates/{resource}/hold         release it → Row
//	GET    /api/updates/{resource}/notes        release notes → Notes
//	GET    /api/updates/settings                {"release_notes": bool}
//	PUT    /api/updates/settings                {"release_notes": bool}
//	POST   /api/updates/rollouts/preview        {"targets"} → RolloutPlan
//	GET    /api/updates/rollouts                {"rollouts": [Rollout…]}
//	POST   /api/updates/rollouts/{id}/cancel    a scheduled or waiting one → Rollout
//	GET    /api/windows                         {"windows": [MaintenanceWindow…]}
//	POST   /api/windows                         WindowInput → MaintenanceWindow
//	PUT    /api/windows/{id}                    WindowInput → MaintenanceWindow
//	DELETE /api/windows/{id}                    → 204
//
// Updating one container is an action (POST /api/actions/plan), the one
// Row.Update names; updating several, or scheduling them into a window,
// is updates.rollout (rollout.go).
func (s *Service) Register(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	mux.Handle("GET /api/updates", session(http.HandlerFunc(s.handleList)))
	mux.Handle("POST /api/updates/check", session(http.HandlerFunc(s.handleCheck)))
	mux.Handle("POST /api/updates/{resource}/hold", session(http.HandlerFunc(s.handleHold)))
	mux.Handle("DELETE /api/updates/{resource}/hold", session(http.HandlerFunc(s.handleRelease)))
	mux.Handle("GET /api/updates/{resource}/notes", session(http.HandlerFunc(s.handleNotes)))
	mux.Handle("GET /api/updates/settings", session(http.HandlerFunc(s.handleGetSettings)))
	mux.Handle("PUT /api/updates/settings", session(http.HandlerFunc(s.handlePutSettings)))
	mux.Handle("POST /api/updates/rollouts/preview", session(http.HandlerFunc(s.handlePreview)))
	mux.Handle("GET /api/updates/rollouts", session(http.HandlerFunc(s.handleRollouts)))
	mux.Handle("POST /api/updates/rollouts/{id}/cancel", session(http.HandlerFunc(s.handleCancel)))
	mux.Handle("GET /api/windows", session(http.HandlerFunc(s.handleWindows)))
	mux.Handle("POST /api/windows", session(http.HandlerFunc(s.handleCreateWindow)))
	mux.Handle("PUT /api/windows/{id}", session(http.HandlerFunc(s.handleUpdateWindow)))
	mux.Handle("DELETE /api/windows/{id}", session(http.HandlerFunc(s.handleDeleteWindow)))
}

// Settings is GET and PUT /api/updates/settings.
type Settings struct {
	// ReleaseNotes lets the hub read release notes from GitHub. Off by
	// default (decision 3).
	ReleaseNotes bool `json:"release_notes"`
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

// userOf is the user behind a request: a token's user for a token.
func userOf(r *http.Request) string {
	if a, ok := authz.ActorFrom(r); ok {
		if a.Kind == authz.KindToken {
			return a.User
		}
		return a.ID
	}
	return ""
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
		httpx.Say(w, http.StatusBadRequest, "invalid", msg.New("updates.hold_kind"))
		return
	}
	row, err := s.Hold(r.Context(), resource, body.Kind, userOf(r))
	if errors.Is(err, ErrNotFound) {
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("updates.no_container"))
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
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("updates.no_container"))
		return
	}
	if err != nil {
		s.internal(w, "updates: release", err)
		return
	}
	if !ok {
		httpx.Say(w, http.StatusNotFound, "not_held", msg.New("updates.not_held", "name", row.Name))
		return
	}
	s.Audit.Change(r, "update.release", resource, map[string]any{"container": row.Name, "engine": row.Engine}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, row)
}

func (s *Service) handleNotes(w http.ResponseWriter, r *http.Request) {
	n, err := s.ReleaseNotes(r.Context(), r.PathValue("resource"))
	switch {
	case errors.Is(err, ErrNotesOff):
		httpx.Say(w, http.StatusConflict, "release_notes_off", msg.New("updates.notes_off"))
	case errors.Is(err, ErrNotFound):
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("updates.no_container"))
	case errors.Is(err, ErrNoSource):
		httpx.Say(w, http.StatusNotFound, "no_source", msg.New("updates.no_source"))
	case errors.Is(err, errRateLimited):
		httpx.Say(w, http.StatusBadGateway, "rate_limited", msg.New("updates.github_limit"))
	case err != nil:
		s.log.Warn("updates: release notes", "err", err)
		httpx.Say(w, http.StatusBadGateway, "upstream", msg.New("updates.github_failed"))
	default:
		httpx.WriteJSON(w, http.StatusOK, n)
	}
}

func (s *Service) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	on, err := s.NotesEnabled(r.Context())
	if err != nil {
		s.internal(w, "updates: settings", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, Settings{ReleaseNotes: on})
}

func (s *Service) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body Settings
	if !httpx.DecodeJSONLimit(w, r, &body, 1<<10) {
		return
	}
	if err := s.SetNotesEnabled(r.Context(), body.ReleaseNotes); err != nil {
		s.internal(w, "updates: settings", err)
		return
	}
	s.Audit.Change(r, "settings.updates.update", "", map[string]any{"release_notes": body.ReleaseNotes}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, body)
}

// PreviewRequest is POST /api/updates/rollouts/preview.
type PreviewRequest struct {
	Targets []string `json:"targets"`
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	var body PreviewRequest
	if !httpx.DecodeJSONLimit(w, r, &body, 64<<10) {
		return
	}
	if len(body.Targets) > 500 {
		httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("updates.too_many_targets", "max", 500))
		return
	}
	p, err := s.Plan(r.Context(), body.Targets)
	if err != nil {
		s.internal(w, "updates: preview", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (s *Service) handleRollouts(w http.ResponseWriter, r *http.Request) {
	ros, err := s.Rollouts(r.Context())
	if err != nil {
		s.internal(w, "updates: rollouts", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"rollouts": ros})
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ro, err := s.CancelRollout(r.Context(), id)
	switch {
	case errors.Is(err, ErrNoRollout):
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("updates.no_rollout"))
		return
	case errors.Is(err, errNotPending):
		httpx.Say(w, http.StatusConflict, "not_pending", msg.New("updates.rollout_not_pending"))
		return
	case err != nil:
		s.internal(w, "updates: cancel rollout", err)
		return
	}
	s.Audit.Change(r, "update.rollout.cancel", id, map[string]any{"window": ro.WindowID}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, ro)
}

func (s *Service) handleWindows(w http.ResponseWriter, r *http.Request) {
	ws, err := s.Windows(r.Context())
	if err != nil {
		s.internal(w, "updates: windows", err)
		return
	}
	now := s.now()
	out := make([]WindowView, len(ws))
	for i, win := range ws {
		out[i] = viewOf(win, now)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"windows": out})
}

// WindowView is a window as the routes answer it: with whether it's open
// now and its next opening.
type WindowView struct {
	MaintenanceWindow
	Open     bool      `json:"open"`
	NextOpen time.Time `json:"next_open,omitzero"` // the opening now or next; zero when it's over
	NextEnd  time.Time `json:"next_end,omitzero"`
}

func viewOf(win MaintenanceWindow, now time.Time) WindowView {
	v := WindowView{MaintenanceWindow: win, Open: win.Open(now)}
	if start, end, ok := win.Occurrence(now); ok {
		v.NextOpen, v.NextEnd = start, end
	}
	return v
}

func (s *Service) handleCreateWindow(w http.ResponseWriter, r *http.Request) {
	var in WindowInput
	if !httpx.DecodeJSONLimit(w, r, &in, 64<<10) {
		return
	}
	win, err := s.CreateWindow(r.Context(), in, userOf(r))
	if s.windowError(w, err) {
		return
	}
	s.Audit.Change(r, "settings.updates.window.create", win.ID, windowParams(win), authz.T1, nil)
	httpx.WriteJSON(w, http.StatusCreated, viewOf(win, s.now()))
}

func (s *Service) handleUpdateWindow(w http.ResponseWriter, r *http.Request) {
	var in WindowInput
	if !httpx.DecodeJSONLimit(w, r, &in, 64<<10) {
		return
	}
	win, err := s.UpdateWindow(r.Context(), r.PathValue("id"), in)
	if s.windowError(w, err) {
		return
	}
	s.Audit.Change(r, "settings.updates.window.update", win.ID, windowParams(win), authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, viewOf(win, s.now()))
}

func (s *Service) handleDeleteWindow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.windowError(w, s.DeleteWindow(r.Context(), id)) {
		return
	}
	s.Audit.Change(r, "settings.updates.window.delete", id, nil, authz.T1, nil)
	w.WriteHeader(http.StatusNoContent)
}

func windowParams(w MaintenanceWindow) map[string]any {
	return map[string]any{"name": w.Name, "hosts": w.Hosts, "starts_at": w.StartsAt, "ends_at": w.EndsAt, "weekly": w.Weekly}
}

// windowError writes err's answer, if any.
func (s *Service) windowError(w http.ResponseWriter, err error) bool {
	var inv *InvalidError
	switch {
	case err == nil:
		return false
	case errors.As(err, &inv):
		httpx.Say(w, http.StatusUnprocessableEntity, "invalid", inv.M)
	case errors.Is(err, ErrNoWindow):
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("updates.no_window"))
	default:
		s.internal(w, "updates: window", err)
	}
	return true
}

func (s *Service) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
}
