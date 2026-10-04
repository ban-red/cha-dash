package audit

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"dash.cha.sh/internal/httpx"
)

// Register mounts the audit log's read endpoints, each wrapped by wrap,
// which must admit only those allowed to read it (Admin and Owner):
//
//	GET /api/audit?actor=&action=&target=&result=&since=&before=&limit=&cursor=
//	    → {"events": [...], "next"?}, newest first; pass next back as cursor
//	GET /api/audit/export   every event as NDJSON, oldest first
//	GET /api/audit/verify   {"ok", "checked", "first_break"?}
func (l *Log) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler, log *slog.Logger) {
	mux.Handle("GET /api/audit", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := Filter{Actor: q.Get("actor"), Action: q.Get("action"), Target: q.Get("target"), Result: q.Get("result"), Cursor: q.Get("cursor")}
		bad := func(msg string) { httpx.Error(w, http.StatusUnprocessableEntity, "invalid", msg) }
		switch f.Result {
		case "", OK, Failed, Refused:
		default:
			bad("result must be ok, failed or refused.")
			return
		}
		for _, t := range []struct {
			name string
			dst  *time.Time
		}{{"since", &f.Since}, {"before", &f.Before}} {
			if v := q.Get(t.name); v != "" {
				ts, err := time.Parse(time.RFC3339Nano, v)
				if err != nil {
					bad(t.name + " must be an RFC 3339 time.")
					return
				}
				*t.dst = ts
			}
		}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > MaxLimit {
				bad("limit must be between 1 and " + strconv.Itoa(MaxLimit) + ".")
				return
			}
			f.Limit = n
		}
		page, err := l.Query(r.Context(), f)
		if errors.Is(err, ErrBadCursor) {
			bad("That cursor isn't one this endpoint gave out.")
			return
		} else if err != nil {
			log.Error("audit: query failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, page)
	})))
	mux.Handle("GET /api/audit/export", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="chadash-audit.ndjson"`)
		if err := l.Export(r.Context(), w); err != nil && r.Context().Err() == nil {
			// Headers are gone by now; the truncated file is the signal, and
			// verify says where.
			log.Error("audit: export failed", "err", err)
		}
	})))
	mux.Handle("GET /api/audit/verify", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rep, err := l.Verify(r.Context())
		if err != nil {
			log.Error("audit: verify failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, rep)
	})))
}
