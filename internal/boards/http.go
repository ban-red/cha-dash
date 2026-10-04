package boards

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
)

// MaxLayoutBytes bounds a layout save (layout and tiles together). A board
// with every widget on it is a few tens of KB; the default 64 KiB API limit
// would be too tight to grow.
const MaxLayoutBytes = 1 << 20

// Register mounts the boards API, each route wrapped by wrap (the hub's
// owner-session check):
//
//	GET  /api/boards                         {"boards":[{"id","name"}]}; creates "Home" on first call
//	GET  /api/boards/{id}/layout             {"revision","format_version","layout","tiles"}; layout is null
//	                                         until saved, tiles {} until set
//	PUT  /api/boards/{id}/layout             {"revision","format_version","layout","tiles"?} → 200 {"revision"};
//	                                         409 {"error":{…},"revision"} if revision isn't the latest.
//	                                         Without tiles (or with null), the latest revision's carry over
//	GET  /api/boards/{id}/discovery          {"dismissed":["<resource id>",…]}
//	POST /api/boards/{id}/discovery/dismiss  {"resources":["<resource id>",…]} → 200 {"dismissed":[…]}
func (s *Service) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler, log *slog.Logger) {
	h := handlers{s: s, log: log}
	mux.Handle("GET /api/boards", wrap(http.HandlerFunc(h.list)))
	mux.Handle("GET /api/boards/{id}/layout", wrap(http.HandlerFunc(h.getLayout)))
	mux.Handle("PUT /api/boards/{id}/layout", wrap(http.HandlerFunc(h.putLayout)))
	mux.Handle("GET /api/boards/{id}/discovery", wrap(http.HandlerFunc(h.getDiscovery)))
	mux.Handle("POST /api/boards/{id}/discovery/dismiss", wrap(http.HandlerFunc(h.dismiss)))
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
	Tiles         json.RawMessage `json:"tiles,omitempty"`
}

func (h handlers) getLayout(w http.ResponseWriter, r *http.Request) {
	l, err := h.s.Layout(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		notFound(w)
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
	httpx.WriteJSON(w, http.StatusOK, layoutBody{Revision: &l.Revision, FormatVersion: &l.FormatVersion, Layout: doc, Tiles: l.Tiles})
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
	tiles := req.Tiles
	if t := bytes.TrimSpace(tiles); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		tiles = nil // keep the latest revision's
	}

	var userID string
	if id, ok := auth.FromContext(r.Context()); ok {
		userID = id.User.ID
	}
	rev, err := h.s.SaveLayout(r.Context(), r.PathValue("id"), *req.Revision, *req.FormatVersion, req.Layout, tiles, userID)
	var conflict *ConflictError
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w)
	case errors.Is(err, ErrInvalidTiles):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", strings.TrimPrefix(err.Error(), "boards: "))
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

type dismissedBody struct {
	Dismissed []string `json:"dismissed"`
}

func (h handlers) getDiscovery(w http.ResponseWriter, r *http.Request) {
	refs, err := h.s.Dismissed(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		h.internal(w, "reading dismissals failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, dismissedBody{Dismissed: refs})
}

func (h handlers) dismiss(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Resources []string `json:"resources"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	refs, err := h.s.Dismiss(r.Context(), r.PathValue("id"), req.Resources)
	switch {
	case errors.Is(err, ErrNotFound):
		notFound(w)
	case errors.Is(err, ErrInvalidRef):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", err.Error())
	case err != nil:
		h.internal(w, "saving a dismissal failed", err)
	default:
		httpx.WriteJSON(w, http.StatusOK, dismissedBody{Dismissed: refs})
	}
}

func notFound(w http.ResponseWriter) {
	httpx.Error(w, http.StatusNotFound, "not_found", "no such board")
}
