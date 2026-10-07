package nodes

import (
	"errors"
	"net/http"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/httpx"
)

// Register mounts the node endpoint and the nodes API on mux:
//
//	GET  /node                     the node channel (authenticated by key, not session)
//	GET  /api/nodes                {"nodes": [Status]}                    (admin)
//	POST /api/nodes/{id}/revoke    the revoked node; 404, 409 already revoked (admin)
//	POST /api/nodes/pairing/regenerate  204: a fresh pairing secret in the file; 409 pairing off (admin)
//
// session attaches the signed-in identity (authz.Guard decides roles);
// actor names who's asking, for the audit log.
func (h *Hub) Register(mux *http.ServeMux, session func(http.Handler) http.Handler, actor func(*http.Request) audit.Actor) {
	mux.Handle("GET /node", h)
	mux.Handle("GET /api/nodes", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := h.List(r.Context())
		if err != nil {
			h.log.Error("listing nodes failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "The nodes couldn't be listed.")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"nodes": list, "hub_pin": h.Pin(), "pairing": h.pairing != nil})
	})))
	mux.Handle("POST /api/nodes/{id}/revoke", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, err := h.Revoke(r.Context(), r.PathValue("id"), actor(r))
		switch {
		case errors.Is(err, ErrNotFound):
			httpx.Error(w, http.StatusNotFound, "not_found", "There's no such node.")
		case errors.Is(err, errAlreadyRevoked):
			httpx.Error(w, http.StatusConflict, "already_revoked", "That node is already revoked.")
		case err != nil:
			h.log.Error("revoking a node failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "The node couldn't be revoked.")
		default:
			httpx.WriteJSON(w, http.StatusOK, rec)
		}
	})))
	mux.Handle("POST /api/nodes/pairing/regenerate", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := h.RegeneratePairing(r.Context(), actor(r))
		switch {
		case errors.Is(err, ErrPairingOff):
			httpx.Error(w, http.StatusConflict, "pairing_off", "Local pairing is off on this hub (CHADASH_NODE_PAIRING_FILE).")
		case err != nil:
			h.log.Error("regenerating the pairing secret failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "The pairing secret couldn't be regenerated.")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})))
}
