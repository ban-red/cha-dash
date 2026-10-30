package authz

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/httpx"
)

// DecideIdentityChange decides whether a may change their own linked
// identities (link one, unlink one): a person signed in to a session, any
// role, with a live step-up grant. An identity is a way into the account,
// so adding or removing one is confirmed like a destructive action.
func DecideIdentityChange(a Actor, now time.Time) error {
	switch {
	case a.Kind != KindUser || a.Session == "":
		return forbid("Only a person signed in to Cha Dash can change their linked sign-ins.")
	case !a.StepUpUntil.After(now):
		return &Refusal{Err: ErrStepUpRequired, Msg: "Confirm it's you first: linked sign-ins are a way into your account."}
	}
	return nil
}

// LinkCheck is auth.Service.SetLinkCheck's decision: linking an identity
// at the provider needs DecideIdentityChange.
func LinkCheck(now func() time.Time) func(r *http.Request) error {
	return func(r *http.Request) error {
		a, _ := ActorFrom(r)
		return DecideIdentityChange(a, now())
	}
}

// Identities serves linked identities: your own, and someone else's for
// an admin (read only).
type Identities struct {
	auth *auth.Service
	log  *slog.Logger
	now  func() time.Time
	// Audit records unlinking and forward-auth links (nil: none, for tests).
	Audit *Recorder
}

// NewIdentities returns the identities API over a.
func NewIdentities(a *auth.Service, log *slog.Logger) *Identities {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Identities{auth: a, log: log, now: time.Now}
}

// Register mounts:
//
//	GET    /api/auth/identities                     → 200 {identities, provider?, forward_auth_username?}
//	DELETE /api/auth/identities/{issuer}/{subject}  → 204; needs a step-up (the issuer path-escaped)
//	POST   /api/auth/identities/forward-auth        → 201 link the username this request's trusted proxy names; needs a step-up
//	GET    /api/users/{id}/identities               → 200 {identities} (admin+)
//
// wrap attaches the session (auth.Require); Guard decides the role.
func (h *Identities) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/auth/identities", wrap(http.HandlerFunc(h.list)))
	mux.Handle("DELETE /api/auth/identities/{issuer}/{subject}", wrap(http.HandlerFunc(h.unlink)))
	mux.Handle("POST /api/auth/identities/forward-auth", wrap(http.HandlerFunc(h.linkForward)))
	mux.Handle("GET /api/users/{id}/identities", wrap(http.HandlerFunc(h.listFor)))
}

type identitiesView struct {
	Identities []auth.LinkedIdentity `json:"identities"`
	// Provider names the OIDC provider when one is ready to link.
	Provider string `json:"provider,omitempty"`
	// ForwardAuthUsername is the username this request's trusted proxy
	// names, when forward-auth is on and it isn't linked to anyone yet.
	ForwardAuthUsername string `json:"forward_auth_username,omitempty"`
	// ForwardAuthLinkedElsewhere: that username is someone else's.
	ForwardAuthLinkedElsewhere bool `json:"forward_auth_linked_elsewhere,omitempty"`
}

func (h *Identities) list(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	list, err := h.auth.ListIdentities(r.Context(), id.User.ID)
	if err != nil {
		h.internal(w, "listing identities failed", err)
		return
	}
	v := identitiesView{Identities: list}
	if set := h.auth.SignIn(); set.OIDCReady() {
		v.Provider = set.ProviderName()
	}
	if name := auth.ForwardAuthUsername(r.Context()); name != "" {
		mine := false
		for _, li := range list {
			mine = mine || (li.Kind == auth.MethodForwardAuth && li.Subject == name)
		}
		if !mine {
			owner, err := h.auth.ForwardAuthOwner(r.Context(), name)
			if err != nil {
				h.internal(w, "looking up the proxy's username failed", err)
				return
			}
			v.ForwardAuthUsername = name
			v.ForwardAuthLinkedElsewhere = owner != ""
		}
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}

func (h *Identities) listFor(w http.ResponseWriter, r *http.Request) {
	if _, err := h.auth.GetUser(r.Context(), r.PathValue("id")); errors.Is(err, auth.ErrUserNotFound) {
		httpx.Error(w, http.StatusNotFound, "not_found", "There's no such user.")
		return
	} else if err != nil {
		h.internal(w, "reading the user failed", err)
		return
	}
	list, err := h.auth.ListIdentities(r.Context(), r.PathValue("id"))
	if err != nil {
		h.internal(w, "listing identities failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, identitiesView{Identities: list})
}

func (h *Identities) unlink(w http.ResponseWriter, r *http.Request) {
	actor, _ := ActorFrom(r)
	issuer, subject := r.PathValue("issuer"), r.PathValue("subject")
	action := "auth.oidc.unlink"
	if issuer == auth.ForwardAuthIssuer {
		action = "auth.forward.unlink"
	}
	target := "user:" + actor.ID
	params := map[string]string{"issuer": clipString(issuer, 200), "subject": clipString(subject, 200)}
	if err := DecideIdentityChange(actor, h.now()); err != nil {
		h.Audit.Change(r, action, target, params, T3, err)
		WriteRefusal(w, err)
		return
	}
	err := h.auth.UnlinkIdentity(r.Context(), actor.ID, issuer, subject)
	switch {
	case errors.Is(err, auth.ErrIdentityNotFound):
		httpx.Error(w, http.StatusNotFound, "not_found", "That sign-in isn't linked to your account.")
	case auth.IsLastSignInMethod(err):
		const msg = "That's the only way into your account. Add a passkey or link another sign-in first."
		h.Audit.Refuse(r, action, target, params, T3, "last_sign_in_method", msg)
		httpx.Error(w, http.StatusConflict, "last_sign_in_method", msg)
	case err != nil:
		h.internal(w, "unlinking failed", err)
	default:
		h.Audit.Change(r, action, target, params, T3, nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h *Identities) linkForward(w http.ResponseWriter, r *http.Request) {
	actor, _ := ActorFrom(r)
	name := auth.ForwardAuthUsername(r.Context())
	target := "user:" + actor.ID
	params := map[string]string{"username": clipString(name, 200)}
	if err := DecideIdentityChange(actor, h.now()); err != nil {
		h.Audit.Change(r, "auth.forward.link", target, params, T3, err)
		WriteRefusal(w, err)
		return
	}
	if name == "" {
		const msg = "No trusted proxy named a user on this request. Open Cha Dash through your proxy, with forward-auth on in Settings → Security."
		h.Audit.Refuse(r, "auth.forward.link", target, params, T3, "no_forward_auth", msg)
		httpx.Error(w, http.StatusConflict, "no_forward_auth", msg)
		return
	}
	created, err := h.auth.LinkForwardAuth(r.Context(), actor.ID, name)
	var verr *auth.ValidationError
	switch {
	case errors.As(err, &verr):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", verr.Msg)
	case errors.Is(err, auth.ErrIdentityTaken):
		const msg = "That proxy username is linked to another account."
		h.Audit.Refuse(r, "auth.forward.link", target, params, T3, "already_linked", msg)
		httpx.Error(w, http.StatusConflict, "already_linked", msg)
	case err != nil:
		h.internal(w, "linking failed", err)
	default:
		if created {
			h.Audit.Change(r, "auth.forward.link", target, params, T3, nil)
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]string{"username": name})
	}
}

func (h *Identities) internal(w http.ResponseWriter, msg string, err error) {
	h.log.Error(msg, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
}
