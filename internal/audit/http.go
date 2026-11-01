package audit

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// HeadHeader carries the chain's head (Head.String) on the NDJSON export.
const HeadHeader = "X-Chadash-Audit-Head"

// Register mounts the audit log's read endpoints, each wrapped by wrap,
// which must admit only those allowed to read it (Admin and Owner):
//
//	GET /api/audit?actor=&action=&target=&result=&since=&before=&limit=&cursor=
//	    → {"events": [...], "next"?}, newest first; pass next back as cursor
//	GET /api/audit/export   every event as NDJSON, oldest first; the head in X-Chadash-Audit-Head
//	GET /api/audit/verify   {"ok", "checked", "first_break"?, "head"?}
func (l *Log) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler, log *slog.Logger) {
	mux.Handle("GET /api/audit", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := Filter{Actor: q.Get("actor"), Action: q.Get("action"), Target: q.Get("target"), Result: q.Get("result"), Cursor: q.Get("cursor")}
		bad := func(m msg.M) { httpx.Say(w, http.StatusUnprocessableEntity, "invalid", m) }
		switch f.Result {
		case "", OK, Failed, Refused:
		default:
			bad(msg.New("audit.bad_result"))
			return
		}
		for _, t := range []struct {
			name string
			dst  *time.Time
		}{{"since", &f.Since}, {"before", &f.Before}} {
			if v := q.Get(t.name); v != "" {
				ts, err := time.Parse(time.RFC3339Nano, v)
				if err != nil {
					bad(msg.New("audit.bad_time", "name", t.name))
					return
				}
				*t.dst = ts
			}
		}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > MaxLimit {
				bad(msg.New("audit.bad_limit", "max", MaxLimit))
				return
			}
			f.Limit = n
		}
		page, err := l.Query(r.Context(), f)
		if errors.Is(err, ErrBadCursor) {
			bad(msg.New("audit.bad_cursor"))
			return
		} else if err != nil {
			log.Error("audit: query failed", "err", err)
			httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, page)
	})))
	mux.Handle("GET /api/audit/export", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="chadash-audit.ndjson"`)
		// The head as of the export, in a header: an owner can record it
		// somewhere the hub can't write and compare later exports with it.
		head := func(h Head) {
			if h.Seq > 0 {
				w.Header().Set(HeadHeader, h.String())
			}
		}
		if err := l.ExportWithHead(r.Context(), w, head); err != nil && r.Context().Err() == nil {
			// Headers are gone by now; the truncated file is the signal, and
			// verify says where.
			log.Error("audit: export failed", "err", err)
		}
	})))
	mux.Handle("GET /api/audit/verify", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rep, err := l.Verify(r.Context())
		if err != nil {
			log.Error("audit: verify failed", "err", err)
			httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, rep)
	})))
}
