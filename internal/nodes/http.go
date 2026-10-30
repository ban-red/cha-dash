package nodes

import (
	"errors"
	"net/http"
	"strings"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/httpx"
)

// Register mounts the node endpoint and the nodes API on mux:
//
//	GET    /node                     the node channel (authenticated by key, not session)
//	GET    /api/nodes                {"nodes": [Status], "hub_pin", "pairing"}       (admin)
//	POST   /api/nodes/{id}/revoke    the revoked node; 404, 409 already revoked     (admin)
//	                                 (on a pending node it's Reject)
//	POST   /api/nodes/{id}/approve   the approved node; 404, 409 not pending        (admin, step-up)
//	POST   /api/nodes/pairing/regenerate  204: a fresh pairing secret in the file; 409 pairing off (admin)
//	GET    /api/nodes/tokens         {"tokens": [JoinToken]}: never a secret         (admin)
//	POST   /api/nodes/tokens         {name?, ttl_minutes?, add_docker?} → 201 Created  (admin)
//	                                 {"token": JoinToken, "secret", "hub_url", "env", "command"},
//	                                 the only time the secret is shown; 422 invalid
//	DELETE /api/nodes/tokens/{id}    the revoked token; 404, 409 used, expired or revoked (admin)
//
// session attaches the signed-in identity (authz.Guard decides roles);
// fresh refuses a request without a step-up (approving a node is T3);
// actor names who's asking, for the audit log.
func (h *Hub) Register(mux *http.ServeMux, session, fresh func(http.Handler) http.Handler, actor func(*http.Request) audit.Actor) {
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
	mux.Handle("POST /api/nodes/{id}/approve", session(fresh(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, err := h.Approve(r.Context(), r.PathValue("id"), actor(r))
		switch {
		case errors.Is(err, ErrNotFound):
			httpx.Error(w, http.StatusNotFound, "not_found", "There's no such node.")
		case errors.Is(err, errNotPending):
			httpx.Error(w, http.StatusConflict, "not_pending", "That node isn't waiting for approval.")
		case err != nil:
			h.log.Error("approving a node failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "The node couldn't be approved.")
		default:
			httpx.WriteJSON(w, http.StatusOK, rec)
		}
	}))))
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

	// Join tokens. /api/nodes/tokens is a literal segment beside
	// /api/nodes/{id}: node IDs are n_…, so they can't collide.
	mux.Handle("GET /api/nodes/tokens", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := h.tokens.List(r.Context())
		if err != nil {
			h.log.Error("listing join tokens failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "The join tokens couldn't be listed.")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"tokens": list})
	})))
	mux.Handle("POST /api/nodes/tokens", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name       string `json:"name"`
			TTLMinutes int    `json:"ttl_minutes"`
			AddDocker  *bool  `json:"add_docker"`
		}
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		ttl, err := TTL(req.TTLMinutes)
		if err == nil {
			err = validTokenName(req.Name)
		}
		if err != nil {
			httpx.Error(w, http.StatusUnprocessableEntity, "invalid", upper(err.Error())+".")
			return
		}
		addDocker := req.AddDocker == nil || *req.AddDocker // on by default (decisions 7)
		t, token, err := h.CreateJoinToken(r.Context(), actor(r), req.Name, ttl, addDocker)
		if err != nil {
			h.log.Error("making a join token failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "The join token couldn't be made.")
			return
		}
		hub := HubURL(h.o.PublicURL, r.Host)
		name := t.Name
		if name == "" {
			name = "<this host>"
		}
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{
			"token": t, "secret": token, "hub_url": hub,
			"env":     EnvBlock(hub, name, token),
			"command": NodeCommand,
		})
	})))
	mux.Handle("DELETE /api/nodes/tokens/{id}", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t, err := h.RevokeJoinToken(r.Context(), actor(r), r.PathValue("id"))
		switch {
		case errors.Is(err, ErrTokenNotFound):
			httpx.Error(w, http.StatusNotFound, "not_found", "There's no such join token.")
		case errors.Is(err, ErrTokenSpent):
			httpx.Error(w, http.StatusConflict, "token_spent", "That join token is already used, expired or revoked.")
		case err != nil:
			h.log.Error("revoking a join token failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "The join token couldn't be revoked.")
		default:
			httpx.WriteJSON(w, http.StatusOK, t)
		}
	})))
}

// NodeCommand starts a node from deploy/compose.node.yaml, next to its .env.
const NodeCommand = "docker compose -f compose.node.yaml up -d"

// EnvBlock is the .env a node on another host needs, for compose.node.yaml.
func EnvBlock(hubURL, name, token string) string {
	return "CHADASH_HUB=" + hubURL + "\nCHADASH_NODE_NAME=" + name + "\nCHADASH_NODE_TOKEN=" + token + "\n"
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
