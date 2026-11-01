package specstore

import (
	"errors"
	"net/http"
	"regexp"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// Register mounts the installed specs' API, the owner's only
// (internal/authz/routes.go). Installing and turning actions on ask for a
// step-up, which the handlers check.
//
//	GET    /api/specs/installed                {"specs": [Installed…]}
//	POST   /api/specs/installed/preview        {yaml, replace?} → Preview: what it reads and adds
//	POST   /api/specs/installed                {yaml, source?, replace?} → Installed (step-up)
//	DELETE /api/specs/installed/{id}           → the spec, removed; 409 in_use with instances
//	PUT    /api/specs/installed/{id}/actions   {on, sha256} → Installed (step-up to turn them on; 409 changed when sha256 isn't the installed version)
func (s *Service) Register(mux *http.ServeMux, session func(http.Handler) http.Handler) {
	mux.Handle("GET /api/specs/installed", session(http.HandlerFunc(s.handleList)))
	mux.Handle("POST /api/specs/installed/preview", session(http.HandlerFunc(s.handlePreview)))
	mux.Handle("POST /api/specs/installed", session(http.HandlerFunc(s.handleInstall)))
	mux.Handle("DELETE /api/specs/installed/{id}", session(http.HandlerFunc(s.handleRemove)))
	mux.Handle("PUT /api/specs/installed/{id}/actions", session(http.HandlerFunc(s.handleActions)))
}

// maxBody bounds a request carrying a spec (escaped in JSON).
const maxBody = 3 * MaxYAML

var idRE = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.List(r.Context())
	if err != nil {
		s.internal(w, "specstore: list", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"specs": list})
}

type installBody struct {
	YAML    string `json:"yaml"`
	Source  string `json:"source"`
	Replace bool   `json:"replace"`
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	var b installBody
	if !httpx.DecodeJSONLimit(w, r, &b, maxBody) {
		return
	}
	_, pv, err := s.Check(r.Context(), b.YAML, b.Replace)
	if err != nil {
		s.fail(w, err, pv)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, pv)
}

func (s *Service) handleInstall(w http.ResponseWriter, r *http.Request) {
	var b installBody
	if !httpx.DecodeJSONLimit(w, r, &b, maxBody) {
		return
	}
	if len(b.Source) > 512 {
		httpx.Say(w, http.StatusBadRequest, "invalid", msg.New("specs.source_too_long"))
		return
	}
	action := "settings.specs.install"
	if b.Replace {
		action = "settings.specs.replace"
	}
	// What the audit record names: the spec's ID and digest once it parses.
	params := map[string]any{"source": b.Source}
	if !s.stepUp(w, r, action, "spec", params) {
		return
	}
	v, replaced, err := s.Install(r.Context(), b.YAML, b.Source, s.userOf(r), b.Replace)
	if err != nil {
		if p, ok := errors.AsType[*Problem](err); ok {
			s.Audit.Refuse(r, action, "spec", params, authz.T3, p.Code, p.Message)
		}
		s.fail(w, err, Preview{})
		return
	}
	params["spec"], params["sha256"], params["actions"] = v.ID, v.SHA256, len(v.Summary.Actions)
	action = "settings.specs.install"
	if replaced {
		action = "settings.specs.replace"
	}
	s.Audit.Change(r, action, "spec:"+v.ID, params, authz.T3, nil)
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (s *Service) handleRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idRE.MatchString(id) {
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("specs.not_installed"))
		return
	}
	v, err := s.Remove(r.Context(), id)
	if err != nil {
		s.fail(w, err, Preview{})
		return
	}
	s.Audit.Change(r, "settings.specs.remove", "spec:"+id, map[string]any{"spec": id, "sha256": v.SHA256}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (s *Service) handleActions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idRE.MatchString(id) {
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("specs.not_installed"))
		return
	}
	var b struct {
		On     *bool  `json:"on"`
		SHA256 string `json:"sha256"` // the version reviewed; required to turn them on
	}
	if !httpx.DecodeJSONLimit(w, r, &b, 1<<10) {
		return
	}
	if b.On == nil {
		httpx.Say(w, http.StatusBadRequest, "invalid", msg.New("specs.on_not_bool"))
		return
	}
	params := map[string]any{"spec": id, "on": *b.On}
	// Turning actions on lets the spec write upstream: a step-up, like
	// installing. Off only takes power away.
	if *b.On && !s.stepUp(w, r, "settings.specs.actions", "spec:"+id, params) {
		return
	}
	v, err := s.SetActions(r.Context(), id, *b.On, b.SHA256)
	if err != nil {
		s.fail(w, err, Preview{})
		return
	}
	tier := authz.T1
	if *b.On {
		tier = authz.T3
	}
	s.Audit.Change(r, "settings.specs.actions", "spec:"+id, params, tier, nil)
	httpx.WriteJSON(w, http.StatusOK, v)
}

// stepUp checks the actor may make a T3 change now, recording the refusal.
func (s *Service) stepUp(w http.ResponseWriter, r *http.Request, auditAction, target string, params any) bool {
	a, _ := s.actor(r)
	if err := authz.Decide(a, authz.Request{Scope: "spec.install", Target: target, Tier: authz.T3}, authz.Env{Now: s.now()}); err != nil {
		s.Audit.Change(r, auditAction, target, params, authz.T3, err)
		authz.WriteRefusal(w, err)
		return false
	}
	return true
}

func (s *Service) actor(r *http.Request) (authz.Actor, bool) {
	if s.actorOf != nil {
		return s.actorOf(r)
	}
	return authz.ActorFrom(r)
}

func (s *Service) userOf(r *http.Request) string {
	a, ok := s.actor(r)
	if !ok {
		return ""
	}
	if a.Kind == authz.KindToken {
		return a.User
	}
	return a.ID
}

// fail answers a Problem with its status and details (and the preview, so
// a refusal to replace still shows what it would replace), ErrNotFound with
// 404, and anything else as an internal error.
func (s *Service) fail(w http.ResponseWriter, err error, pv Preview) {
	var p *Problem
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Say(w, http.StatusNotFound, "not_found", msg.New("specs.not_installed"))
	case errors.As(err, &p):
		status := http.StatusBadRequest
		if p.Code == "exists" || p.Code == "bundled" || p.Code == "in_use" || p.Code == "changed" {
			status = http.StatusConflict
		}
		body := map[string]any{"error": p}
		if pv.Summary.ID != "" {
			body["preview"] = pv
		}
		httpx.WriteJSON(w, status, body)
	default:
		s.internal(w, "specstore", err)
	}
}

func (s *Service) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
}
