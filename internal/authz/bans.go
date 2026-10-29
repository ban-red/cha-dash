package authz

import (
	"errors"
	"log/slog"
	"net/http"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
)

// Bans serves the banned-address list in Settings → Users (admin+; Guard
// has admitted the role). auth bans an address after auth.BanAfter wrong
// passwords; only these routes or `chadash bans clear` lift a ban.
type Bans struct {
	auth *auth.Service
	log  *slog.Logger
	// Audit records every cleared ban (nil: none, for tests).
	Audit *Recorder
}

// NewBans returns the bans API over a.
func NewBans(a *auth.Service, log *slog.Logger) *Bans {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Bans{auth: a, log: log}
}

// Register mounts:
//
//	GET    /api/bans        → 200 [{ip, banned_at, failures}], newest first
//	DELETE /api/bans/{ip}   → 204; 404 when it isn't banned, 422 when it isn't an address
//
// wrap attaches the session (auth.Require); Guard decides the role.
func (b *Bans) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/bans", wrap(http.HandlerFunc(b.list)))
	mux.Handle("DELETE /api/bans/{ip}", wrap(http.HandlerFunc(b.clear)))
}

func (b *Bans) list(w http.ResponseWriter, r *http.Request) {
	list, err := b.auth.ListBans(r.Context())
	if err != nil {
		b.log.Error("listing bans failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (b *Bans) clear(w http.ResponseWriter, r *http.Request) {
	ip := clipString(r.PathValue("ip"), 64)
	target := "ip:" + ip
	err := b.auth.Unban(r.Context(), ip)
	var verr *auth.ValidationError
	switch {
	case err == nil:
		b.Audit.Change(r, "security.ban.clear", target, nil, T1, nil)
		w.WriteHeader(http.StatusNoContent)
	case errors.As(err, &verr):
		b.Audit.Refuse(r, "security.ban.clear", target, nil, T1, "invalid", "That isn't an IP address.")
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "That isn't an IP address.")
	case errors.Is(err, auth.ErrBanNotFound):
		b.Audit.Refuse(r, "security.ban.clear", target, nil, T1, "not_found", "That address isn't banned.")
		httpx.Error(w, http.StatusNotFound, "not_found", "That address isn't banned.")
	default:
		b.log.Error("clearing a ban failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
