package boards

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
)

// MaxLayoutBytes bounds a layout save. A board with every widget on it is a
// few tens of KB; the default 64 KiB API limit would be too tight to grow.
const MaxLayoutBytes = 1 << 20

// Register mounts the boards API, each route wrapped by wrap (the hub's
// owner-session check):
//
//	GET /api/boards               {"boards":[{"id","name"}]}; creates "Home" on first call
//	GET /api/boards/{id}/layout   {"revision","format_version","layout"}; layout is null until saved
//	PUT /api/boards/{id}/layout   {"revision","format_version","layout"} → 200 {"revision"};
//	                              409 {"error":{…},"revision"} if revision isn't the latest
func (s *Service) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler, log *slog.Logger) {
	h := handlers{s: s, log: log}
	mux.Handle("GET /api/boards", wrap(http.HandlerFunc(h.list)))
	mux.Handle("GET /api/boards/{id}/layout", wrap(http.HandlerFunc(h.getLayout)))
	mux.Handle("PUT /api/boards/{id}/layout", wrap(http.HandlerFunc(h.putLayout)))
}

type handlers struct {
	s   *Service
	log *slog.Logger
}

func (h handlers) internal(w http.ResponseWriter, what string, err error) {
	h.log.Error(what, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	boards, err := h.s.List(r.Context())
	if err != nil {
		h.internal(w, "listing boards failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string][]Board{"boards": boards})
}

// layoutBody is the layout endpoint's request and response body.
type layoutBody struct {
	Revision      *int64          `json:"revision"`
	FormatVersion *int64          `json:"format_version"`
	Layout        json.RawMessage `json:"layout"`
}

func (h handlers) getLayout(w http.ResponseWriter, r *http.Request) {
	l, err := h.s.Layout(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", "no such board")
		return
	}
	if err != nil {
		h.internal(w, "reading a layout failed", err)
		return
	}
	doc := l.Document
	if doc == nil {
		doc = json.RawMessage("null") // the SPA creates the first layout
	}
	httpx.WriteJSON(w, http.StatusOK, layoutBody{Revision: &l.Revision, FormatVersion: &l.FormatVersion, Layout: doc})
}

// conflictBody is a 409 with the revision the client should reload.
type conflictBody struct {
	httpx.ErrorBody
	Revision int64 `json:"revision"`
}

func (h handlers) putLayout(w http.ResponseWriter, r *http.Request) {
	var req layoutBody
	if !httpx.DecodeJSONLimit(w, r, &req, MaxLayoutBytes) {
		return
	}
	switch trimmed := bytes.TrimSpace(req.Layout); {
	case req.Revision == nil || *req.Revision < 0:
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "revision must be the revision the layout was loaded at (0 for a board never saved)")
		return
	case req.FormatVersion == nil || *req.FormatVersion < 1:
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "format_version must be a positive integer")
		return
	case len(trimmed) == 0 || trimmed[0] != '{':
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "layout must be a JSON object")
		return
	}

	var userID string
	if id, ok := auth.FromContext(r.Context()); ok {
		userID = id.User.ID
	}
	rev, err := h.s.SaveLayout(r.Context(), r.PathValue("id"), *req.Revision, *req.FormatVersion, req.Layout, userID)
	var conflict *ConflictError
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not_found", "no such board")
	case errors.As(err, &conflict):
		httpx.WriteJSON(w, http.StatusConflict, conflictBody{
			ErrorBody: httpx.ErrorBody{Error: httpx.ErrorDetail{Code: "conflict", Message: "the layout changed since it was loaded; reload it and try again"}},
			Revision:  conflict.Current,
		})
	case err != nil:
		h.internal(w, "saving a layout failed", err)
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]int64{"revision": rev})
	}
}
