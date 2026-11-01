package authz

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// Users serves Settings → Users. Guard has already admitted admin+ (owner
// for the transfer); the rules here are the ones that depend on whose
// account it is. auth keeps the invariants whoever asks: one owner, never
// demoted or disabled except by transfer, users disabled rather than deleted.
type Users struct {
	auth *auth.Service
	log  *slog.Logger
	now  func() time.Time
	// Audit records every change (nil: none, for tests).
	Audit *Recorder
}

// NewUsers returns the users API over a.
func NewUsers(a *auth.Service, log *slog.Logger) *Users {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Users{auth: a, log: log, now: time.Now}
}

// Register mounts:
//
//	GET  /api/users                            → 200 [{id, username, role, disabled, has_passkey, last_seen_at}]
//	POST /api/users                            {username, password, role} → 201 user
//	PUT  /api/users/{id}                       {role?, disabled?} → 200 user
//	POST /api/users/{id}/transfer-ownership    → 200 the new owner; T3, so it needs a step-up
//
// wrap attaches the session (auth.Require); Guard decides the role.
func (u *Users) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/users", wrap(http.HandlerFunc(u.list)))
	mux.Handle("POST /api/users", wrap(http.HandlerFunc(u.create)))
	mux.Handle("PUT /api/users/{id}", wrap(http.HandlerFunc(u.update)))
	mux.Handle("POST /api/users/{id}/transfer-ownership", wrap(http.HandlerFunc(u.transfer)))
}

func (u *Users) list(w http.ResponseWriter, r *http.Request) {
	list, err := u.auth.ListUsers(r.Context())
	if err != nil {
		u.internal(w, "listing users failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (u *Users) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	params := map[string]string{"username": clipString(req.Username, 100), "role": clipString(req.Role, 20)}
	user, err := u.auth.CreateUser(r.Context(), req.Username, req.Password, req.Role)
	if u.refused(w, r, err, "users.create", "", params) {
		return
	}
	u.Audit.Change(r, "users.create", "user:"+user.ID, params, T1, nil)
	httpx.WriteJSON(w, http.StatusCreated, user)
}

func (u *Users) update(w http.ResponseWriter, r *http.Request) {
	actor, _ := ActorFrom(r)
	var req struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	target := "user:" + clipString(r.PathValue("id"), 100)
	cur, err := u.auth.GetUser(r.Context(), r.PathValue("id"))
	if u.refused(w, r, err, "users.update", target, req) {
		return
	}
	// An admin manages every account but the owner's.
	if cur.Role == auth.RoleOwner && actor.Role != auth.RoleOwner {
		m := msg.New("authz.owner_account")
		u.Audit.Refuse(r, "users.update", target, req, T1, "forbidden", m.Text())
		httpx.Say(w, http.StatusForbidden, "forbidden", m)
		return
	}
	user, err := u.auth.UpdateUser(r.Context(), cur.ID, req.Role, req.Disabled)
	if u.refused(w, r, err, "users.update", target, req) {
		return
	}
	u.Audit.Change(r, "users.update", "user:"+cur.ID, req, T1, nil)
	httpx.WriteJSON(w, http.StatusOK, user)
}

func (u *Users) transfer(w http.ResponseWriter, r *http.Request) {
	actor, _ := ActorFrom(r)
	target := "user:" + r.PathValue("id")
	err := Decide(actor, Request{Scope: "users.transfer-ownership", Target: target, Tier: T3}, Env{Now: u.now()})
	if err != nil {
		u.Audit.Change(r, "users.transfer-ownership", target, nil, T3, err)
	}
	if WriteRefusal(w, err) {
		return
	}
	user, err := u.auth.TransferOwnership(r.Context(), actor.ID, r.PathValue("id"))
	if u.refused(w, r, err, "users.transfer-ownership", target, nil) {
		return
	}
	u.Audit.Change(r, "users.transfer-ownership", target, nil, T3, nil)
	httpx.WriteJSON(w, http.StatusOK, user)
}

// refused answers err, if there is one, and audits the refusal as action
// on target (params as asked, never a password). Internal errors aren't
// refusals and aren't audited.
func (u *Users) refused(w http.ResponseWriter, r *http.Request, err error, action, target string, params any) bool {
	var verr *auth.ValidationError
	status, code, text := 0, "", ""
	var m msg.M
	switch {
	case err == nil:
		return false
	case errors.As(err, &verr):
		// Validation sentences aren't in the catalog yet.
		status, code, text = http.StatusUnprocessableEntity, "invalid", verr.Msg
	case errors.Is(err, auth.ErrUserNotFound):
		status, code, m = http.StatusNotFound, "not_found", msg.New("authz.user_not_found")
	case errors.Is(err, auth.ErrUserExists):
		status, code, m = http.StatusConflict, "conflict", msg.New("authz.username_taken")
	case errors.Is(err, auth.ErrOwnerProtected):
		status, code, m = http.StatusConflict, "owner_protected", msg.New("authz.owner_protected")
	case errors.Is(err, auth.ErrNotOwner):
		status, code, m = http.StatusConflict, "conflict", msg.New("authz.not_owner")
	default:
		u.internal(w, "users: request failed", err)
		return true
	}
	tier := T1
	if action == "users.transfer-ownership" {
		tier = T3
	}
	if m.ID != "" {
		text = m.Text()
	}
	u.Audit.Refuse(r, action, target, params, tier, code, text)
	if m.ID != "" {
		httpx.Say(w, status, code, m)
	} else {
		httpx.Error(w, status, code, text)
	}
	return true
}

func (u *Users) internal(w http.ResponseWriter, what string, err error) {
	u.log.Error(what, "err", err)
	httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
}

// WriteRefusal answers a Decide or Policy refusal: 403 step_up_required when
// a step-up would change the answer, 403 forbidden otherwise. It reports
// whether it wrote anything (err != nil).
func WriteRefusal(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	code := "forbidden"
	if errors.Is(err, ErrStepUpRequired) {
		code = "step_up_required"
	}
	httpx.Error(w, http.StatusForbidden, code, err.Error())
	return true
}
