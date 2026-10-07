package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"dash.cha.sh/internal/httpx"
)

// registerPasskeys mounts the passkey endpoints. Options and credentials are
// the WebAuthn JSON forms, with binary fields base64url (no padding): begin's
// publicKey goes to PublicKeyCredential.parseCreationOptionsFromJSON or
// parseRequestOptionsFromJSON, and finish takes what the credential's toJSON()
// returns.
//
// The management routes act on the signed-in user's own passkeys, whatever
// their role: every account needs one to step up under "passkey only".
//
//	POST   /api/auth/passkeys/register/begin   (session) → 200 {"publicKey": creation options}
//	POST   /api/auth/passkeys/register/finish  (session) registration JSON → 201 {"id","name"}
//	POST   /api/auth/passkeys/login/begin      → 200 {"publicKey": request options}
//	POST   /api/auth/passkeys/login/finish     authentication JSON → 200 {"user"} + session cookie
//	GET    /api/auth/passkeys                  (session) → 200 [passkey]
//	PATCH  /api/auth/passkeys/{id}             (session) {"name"} → 200 passkey
//	DELETE /api/auth/passkeys/{id}             (session) → 204
func (s *Service) registerPasskeys(mux *http.ServeMux) {
	own := func(h http.HandlerFunc) http.Handler { return s.Require(h) }
	mux.Handle("POST /api/auth/passkeys/register/begin", own(s.handleRegisterBegin))
	mux.Handle("POST /api/auth/passkeys/register/finish", own(s.handleRegisterFinish))
	mux.HandleFunc("POST /api/auth/passkeys/login/begin", s.handlePasskeyLoginBegin)
	mux.HandleFunc("POST /api/auth/passkeys/login/finish", s.handlePasskeyLoginFinish)
	mux.Handle("GET /api/auth/passkeys", own(s.handleListPasskeys))
	mux.Handle("PATCH /api/auth/passkeys/{id}", own(s.handleRenamePasskey))
	mux.Handle("DELETE /api/auth/passkeys/{id}", own(s.handleDeletePasskey))
}

func (s *Service) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := FromContext(ctx)
	rp, ok := s.passkeyRP(w, r)
	if !ok {
		return
	}
	handle, err := s.userHandle(ctx, id.User.ID, true)
	if err != nil {
		s.internalError(w, "could not create a passkey user handle", err)
		return
	}
	creds, err := s.userCredentials(ctx, id.User.ID, rp.ID)
	if err != nil {
		s.internalError(w, "could not load passkeys", err)
		return
	}
	wa, err := rp.webauthn()
	if err != nil {
		s.internalError(w, "passkey relying party", err)
		return
	}
	// Excluding the user's passkeys stops an authenticator from registering twice.
	creation, session, err := wa.BeginRegistration(&passkeyUser{user: id.User, handle: handle},
		webauthn.WithExclusions(webauthn.Credentials(creds).CredentialDescriptors()))
	if err != nil {
		s.internalError(w, "could not begin passkey registration", err)
		return
	}
	s.beginCeremony(w, r, ceremony{kind: ceremonyRegister, rp: rp, session: *session, userID: id.User.ID})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"publicKey": creation.Response})
}

func (s *Service) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := FromContext(ctx)
	c, ok := s.takeCeremony(w, r, ceremonyRegister)
	if !ok || c.userID != id.User.ID {
		ceremonyGone(w)
		return
	}
	body, ok := readCredential(w, r)
	if !ok {
		return
	}
	reject := func(err error) {
		s.log.Info("passkey registration failed", "user", id.User.Username, "reason", describe(err))
		httpx.Error(w, http.StatusBadRequest, "passkey_rejected", "the passkey could not be verified; try again")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		reject(err)
		return
	}
	handle, err := s.userHandle(ctx, id.User.ID, false)
	if err != nil {
		s.internalError(w, "could not load the passkey user handle", err)
		return
	}
	wa, err := c.rp.webauthn()
	if err != nil {
		s.internalError(w, "passkey relying party", err)
		return
	}
	cred, err := wa.CreateCredential(&passkeyUser{user: id.User, handle: handle}, c.session, parsed)
	if err != nil {
		reject(err)
		return
	}
	p, err := s.savePasskey(ctx, id.User.ID, c.rp.ID, cred)
	if errors.Is(err, errPasskeyExists) {
		httpx.Error(w, http.StatusConflict, "passkey_exists", "this passkey is already registered")
		return
	}
	if err != nil {
		s.internalError(w, "could not save the passkey", err)
		return
	}
	s.log.Info("passkey registered", "user", id.User.Username, "passkey", p.ID, "name", p.Name, "rp_id", p.RPID)
	s.emit(Event{Kind: EventPasskeyRegistered, UserID: id.User.ID, SessionID: id.Session.ID, Passkey: p.ID, Name: p.Name, Request: r})
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"id": p.ID, "name": p.Name})
}

func (s *Service) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	// Its own budget: a sign-in page may begin a ceremony on every load, for
	// passkey autofill, without spending sign-in attempts.
	if s.rateLimited(w, r, "passkey-begin") {
		return
	}
	rp, ok := s.passkeyRP(w, r)
	if !ok {
		return
	}
	wa, err := rp.webauthn()
	if err != nil {
		s.internalError(w, "passkey relying party", err)
		return
	}
	assertion, session, err := wa.BeginDiscoverableLogin()
	if err != nil {
		s.internalError(w, "could not begin passkey sign-in", err)
		return
	}
	s.beginCeremony(w, r, ceremony{kind: ceremonyLogin, rp: rp, session: *session})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"publicKey": assertion.Response})
}

// passkeySignal is what a browser needs for PublicKeyCredential.signalUnknownCredential.
type passkeySignal struct {
	RPID         string `json:"rpId"`
	CredentialID string `json:"credentialId"`
}

func (s *Service) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	// The same budget as password sign-in.
	if s.rateLimited(w, r, "login") {
		return
	}
	ctx := r.Context()
	c, ok := s.takeCeremony(w, r, ceremonyLogin)
	if !ok {
		ceremonyGone(w)
		return
	}
	body, ok := readCredential(w, r)
	if !ok {
		return
	}
	client := s.proxies.ClientIP(r).String()
	reject := func(passkey string, err error) {
		s.log.Info("passkey sign-in failed", "client", client, "passkey", passkey, "reason", describe(err))
		httpx.Error(w, http.StatusUnauthorized, "passkey_rejected", "that passkey didn't work; try again")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		reject("", err)
		return
	}
	var passkeyID string
	var lookupErr error
	discover := func(rawID, _ []byte) (webauthn.User, error) {
		// The library checks the returned user handle against this user's.
		var pu *passkeyUser
		passkeyID, pu, lookupErr = s.lookupPasskey(ctx, c.rp.ID, rawID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		return pu, nil
	}
	wa, err := c.rp.webauthn()
	if err != nil {
		s.internalError(w, "passkey relying party", err)
		return
	}
	user, cred, err := wa.ValidatePasskeyLogin(discover, c.session, parsed)
	switch {
	case errors.Is(lookupErr, errUnknownPasskey):
		// Deleted here, or never registered: tell the browser, so the password
		// manager can stop offering it.
		s.log.Info("passkey sign-in with an unknown passkey", "client", client)
		httpx.WriteJSON(w, http.StatusUnauthorized, struct {
			httpx.ErrorBody
			Signal passkeySignal `json:"signal"`
		}{
			httpx.ErrorBody{Error: httpx.ErrorDetail{Code: "unknown_passkey", Message: "this passkey isn't registered with Cha Dash"}},
			passkeySignal{RPID: c.rp.ID, CredentialID: parsed.ID},
		})
		return
	case errors.Is(lookupErr, errDisabledUser):
		reject(passkeyID, lookupErr)
		return
	case lookupErr != nil:
		s.internalError(w, "passkey lookup failed", lookupErr)
		return
	case err != nil:
		reject(passkeyID, err)
		return
	}
	pu := user.(*passkeyUser)
	stored, got := pu.creds[0].Authenticator.SignCount, parsed.Response.AuthenticatorData.Counter
	if got <= stored && (got != 0 || stored != 0) {
		// Synced passkeys always report 0; a counter that goes backwards means two
		// copies of the key may be in use. Signing in still works, as most RPs do,
		// but the passkey is flagged and the owner can delete it.
		s.log.Warn("passkey signature counter went backwards; the authenticator may have been cloned",
			"passkey", passkeyID, "user", pu.user.Username, "stored", stored, "got", got, "client", client)
	}
	if err := s.recordPasskeyUse(ctx, passkeyID, cred); err != nil {
		s.internalError(w, "could not record passkey use", err)
		return
	}
	if _, err := s.StartSession(w, r, pu.user, MethodPasskey); err != nil {
		s.internalError(w, "could not start session", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]User{"user": pu.user})
}

func (s *Service) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	id, _ := FromContext(r.Context())
	list, err := s.ListPasskeys(r.Context(), id.User.ID)
	if err != nil {
		s.internalError(w, "could not list passkeys", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

type renamePasskeyRequest struct {
	Name string `json:"name"`
}

func (s *Service) handleRenamePasskey(w http.ResponseWriter, r *http.Request) {
	id, _ := FromContext(r.Context())
	var req renamePasskeyRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	p, err := s.RenamePasskey(r.Context(), id.User.ID, r.PathValue("id"), req.Name)
	var verr *ValidationError
	switch {
	case errors.As(err, &verr):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", verr.Msg)
	case errors.Is(err, ErrPasskeyNotFound):
		httpx.Error(w, http.StatusNotFound, "not_found", "no such passkey")
	case err != nil:
		s.internalError(w, "could not rename passkey", err)
	default:
		s.emit(Event{Kind: EventPasskeyRenamed, UserID: id.User.ID, SessionID: id.Session.ID, Passkey: p.ID, Name: p.Name, Request: r})
		httpx.WriteJSON(w, http.StatusOK, p)
	}
}

func (s *Service) handleDeletePasskey(w http.ResponseWriter, r *http.Request) {
	id, _ := FromContext(r.Context())
	pk := r.PathValue("id")
	err := s.DeletePasskey(r.Context(), id.User.ID, pk)
	switch {
	case errors.Is(err, ErrPasskeyNotFound):
		httpx.Error(w, http.StatusNotFound, "not_found", "no such passkey")
	case errors.Is(err, errLastSignInMethod):
		httpx.Error(w, http.StatusConflict, "last_sign_in_method", "this passkey is the only way to sign in to this account; add another passkey first")
	case err != nil:
		s.internalError(w, "could not delete passkey", err)
	default:
		s.log.Info("passkey deleted", "user", id.User.Username, "passkey", pk)
		s.emit(Event{Kind: EventPasskeyDeleted, UserID: id.User.ID, SessionID: id.Session.ID, Passkey: pk, Request: r})
		w.WriteHeader(http.StatusNoContent)
	}
}

// passkeyRP resolves the relying party, answering 409 when passkeys can't work here.
func (s *Service) passkeyRP(w http.ResponseWriter, r *http.Request) (relyingParty, bool) {
	rp, err := s.relyingParty(r)
	var unavailable *unavailableError
	if errors.As(err, &unavailable) {
		httpx.Error(w, http.StatusConflict, "passkeys_unavailable", unavailable.msg)
		return rp, false
	}
	return rp, err == nil
}

func ceremonyGone(w http.ResponseWriter) {
	httpx.Error(w, http.StatusBadRequest, "ceremony_expired", "that passkey request expired or was already used; start again")
}

// readCredential reads a finish body: one JSON object, parsed by the library.
func readCredential(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var raw json.RawMessage
	if !httpx.DecodeJSON(w, r, &raw) {
		return nil, false
	}
	return raw, true
}

func (s *Service) internalError(w http.ResponseWriter, msg string, err error) {
	s.log.Error(msg, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
}

// describe renders a WebAuthn failure for the logs, with the library's detail.
func describe(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.DevInfo != "" {
		return pe.Details + ": " + pe.DevInfo
	}
	return err.Error()
}
