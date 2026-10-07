package homepage

import (
	"log/slog"
	"net/http"
	"strings"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/importer"
)

// MaxBodyBytes bounds an import request: the three files together.
const MaxBodyBytes = 1 << 20

// Register mounts the Homepage import API, each route wrapped by wrap (the
// hub's owner-session check):
//
//	POST /api/import/homepage?dry_run=1   {services_yaml, docker_yaml, settings_yaml} → 200 {plan, hints, unmapped}
//	POST /api/import/homepage             the same body → 200 {board_id, instance_ids, changes}
//	GET  /api/import/homepage             the last import's record → 200 {record}; 404 before the first import
//
// A dry run's plan carries changes too: what applying it would create,
// update or leave alone. Secret values never appear in a response or a log.
//
// An applied import is audited as import.homepage (rec; nil records
// nothing): the board and instance IDs and how many objects it created and
// updated, never a value from the files.
func Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler, svc *importer.Service, log *slog.Logger, rec *authz.Recorder) {
	h := handler{svc: svc, log: log, audit: rec}
	mux.Handle("POST /api/import/homepage", wrap(http.HandlerFunc(h.post)))
	mux.Handle("GET /api/import/homepage", wrap(http.HandlerFunc(h.get)))
}

type handler struct {
	svc   *importer.Service
	log   *slog.Logger
	audit *authz.Recorder
}

func (h handler) post(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.DecodeJSONLimit(w, r, &in, MaxBodyBytes) {
		return
	}
	if strings.TrimSpace(in.Services) == "" {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "services_yaml is required")
		return
	}
	res, secrets, err := Build(in)
	if err != nil {
		// Build's errors name a file and a line, never the text there.
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid_config", err.Error())
		return
	}

	switch v := r.URL.Query().Get("dry_run"); v {
	case "1", "true":
		if err := h.svc.Diff(r.Context(), res, secrets); err != nil {
			h.internal(w, "homepage import dry run failed", err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, res)
		return
	case "", "0", "false":
	default:
		httpx.Error(w, http.StatusBadRequest, "invalid", "dry_run must be 1 or 0")
		return
	}

	var userID string
	if id, ok := auth.FromContext(r.Context()); ok {
		userID = id.User.ID
	}
	applied, err := h.svc.Apply(r.Context(), res, secrets, userID)
	if err != nil {
		h.internal(w, "homepage import failed", err)
		return
	}
	created, updated := 0, 0
	for _, c := range applied.Changes {
		switch c.Action {
		case importer.ActionCreate:
			created++
		case importer.ActionUpdate:
			updated++
		}
	}
	h.log.Info("homepage import applied", "board", applied.BoardID, "instances", len(applied.InstanceIDs),
		"secrets", res.Plan.SecretsMoved, "created", created, "updated", updated)
	h.audit.Change(r, "import.homepage", "board:"+applied.BoardID, map[string]any{
		"board_id": applied.BoardID, "instance_ids": applied.InstanceIDs,
		"created": created, "updated": updated, "secrets_moved": res.Plan.SecretsMoved,
	}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, applied)
}

func (h handler) get(w http.ResponseWriter, r *http.Request) {
	rec, err := h.svc.LoadRecord(r.Context(), Source)
	if err != nil {
		h.internal(w, "reading the homepage import record failed", err)
		return
	}
	if rec == nil {
		httpx.Error(w, http.StatusNotFound, "not_found", "nothing has been imported from Homepage yet")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]*importer.Record{"record": rec})
}

func (h handler) internal(w http.ResponseWriter, what string, err error) {
	h.log.Error(what, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
}
