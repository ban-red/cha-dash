package icons

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"dash.cha.sh/internal/httpx"
)

// Map is GET /api/icons: for each thing a tile can name, the path of its
// cached icon. Only icons that have been fetched are listed; anything
// missing keeps its monogram.
type Map struct {
	ByIcon     map[string]string `json:"by_icon"`
	ByOrigin   map[string]string `json:"by_origin"`
	ByInstance map[string]string `json:"by_instance"`
}

// Register mounts the routes. wrap attaches the session; who may call them
// is internal/authz's table (any signed-in role: boards show icons).
func (s *Service) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler, log *slog.Logger) {
	if log == nil {
		log = s.log
	}
	mux.Handle("GET /api/icons", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := s.Map(r.Context())
		if err != nil {
			log.Error("icons: listing failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		s.Kick()
		httpx.WriteJSON(w, http.StatusOK, m)
	})))
	mux.Handle("GET /api/icons/{id}", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.serve(w, r, log)
	})))
}

// Map lists the cached icons by what refers to them.
func (s *Service) Map(ctx context.Context) (Map, error) {
	m := Map{ByIcon: map[string]string{}, ByOrigin: map[string]string{}, ByInstance: map[string]string{}}
	src, err := s.ReadSources(ctx)
	if err != nil {
		return m, err
	}
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, source, etag FROM icons WHERE body IS NOT NULL`)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	path := map[string]string{}
	for rows.Next() {
		var id, source, etag string
		if err := rows.Scan(&id, &source, &etag); err != nil {
			return m, err
		}
		path[source] = "/api/icons/" + id + "?v=" + etag
	}
	if err := rows.Err(); err != nil {
		return m, err
	}
	for k, source := range src.ByIcon {
		if p, ok := path[source]; ok {
			m.ByIcon[k] = p
		}
	}
	for k, source := range src.ByOrigin {
		if p, ok := path[source]; ok {
			m.ByOrigin[k] = p
		}
	}
	for k, source := range src.ByInstance {
		if p, ok := path[source]; ok {
			m.ByInstance[k] = p
		}
	}
	return m, nil
}

var idRE = regexp.MustCompile(`^ic_[a-z0-9]{8,64}$`)

// iconCSP: an icon opened on its own (not in an <img>) still can't run
// anything or load anything.
const iconCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

var extOf = map[string]string{TypePNG: "png", TypeSVG: "svg", TypeICO: "ico", TypeWebP: "webp"}

func (s *Service) serve(w http.ResponseWriter, r *http.Request, log *slog.Logger) {
	id := r.PathValue("id")
	if !idRE.MatchString(id) {
		httpx.Error(w, http.StatusNotFound, "not_found", "no such icon")
		return
	}
	var ctype, etag string
	var body []byte
	err := s.st.Reader().QueryRowContext(r.Context(), `SELECT content_type, body, etag FROM icons WHERE id = ? AND body IS NOT NULL`, id).Scan(&ctype, &body, &etag)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "not_found", "no such icon")
		return
	}
	if err != nil {
		log.Error("icons: reading an icon failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	ext, ok := extOf[ctype]
	if !ok {
		// Never serve a type the checks didn't produce.
		httpx.Error(w, http.StatusNotFound, "not_found", "no such icon")
		return
	}
	h := w.Header()
	tag := `"` + etag + `"`
	h.Set("ETag", tag)
	// Private: icons sit behind the session. A week, or a year when the
	// URL names this version (GET /api/icons hands out ?v=<etag>).
	if r.URL.Query().Get("v") == etag {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, max-age=604800")
	}
	h.Set("Content-Security-Policy", iconCSP)
	h.Set("Content-Disposition", `inline; filename="icon.`+ext+`"`)
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", ctype)
	_, _ = w.Write(body)
}

func etagMatch(header, tag string) bool {
	for t := range strings.SplitSeq(header, ",") {
		t = strings.TrimSpace(t)
		if t == tag || t == "W/"+tag || t == "*" {
			return true
		}
	}
	return false
}
