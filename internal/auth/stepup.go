package auth

// Step-up: a signed-in user proves it's still them before something
// destructive (T3 actions, ownership transfer, changing the step-up rule
// itself). A grant lasts StepUpTTL and lives on the session row, server-side,
// so it ends with the session and never travels in a cookie.
//
// Methods. A passkey whenever the user has one that works at this address;
// otherwise, under the default rule, their password. Under "passkey only" a
// user without a usable passkey can't step up at all, and the answer says so.
// Whether a grant is enough for a given action is internal/authz's call.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"dash.cha.sh/internal/httpx"
)

// StepUpTTL is how long a step-up grant lasts.
const StepUpTTL = 5 * time.Minute

const stepUpPath = "/api/auth/step-up"

// StepUpMode is the owner's rule for how users step up
// (Settings → Security → Confirming destructive actions).
type StepUpMode string

const (
	// StepUpPasskeyOrPassword: a passkey, or the password for an account
	// without a passkey that works here. The default.
	StepUpPasskeyOrPassword StepUpMode = "passkey_or_password"
	// StepUpPasskeyOnly refuses step-up to accounts without a passkey.
	StepUpPasskeyOnly StepUpMode = "passkey_only"

	DefaultStepUpMode = StepUpPasskeyOrPassword
)

// ParseStepUpMode returns the mode named s.
func ParseStepUpMode(s string) (StepUpMode, bool) {
	switch m := StepUpMode(s); m {
	case StepUpPasskeyOrPassword, StepUpPasskeyOnly:
		return m, true
	}
	return "", false
}

// Step-up methods as GET /api/auth/session and step-up/begin name them.
const (
	StepUpMethodPasskey  = "passkey"
	StepUpMethodPassword = "password"
)

// stepUpModeFunc is the rule's source; internal/security owns the setting.
type stepUpModeFunc struct {
	mu sync.Mutex
	f  func() StepUpMode
}

// SetStepUpMode tells the service where the step-up rule lives. Until it's
// called, or when f returns an unknown mode, the default applies.
func (s *Service) SetStepUpMode(f func() StepUpMode) {
	s.stepUpMode.mu.Lock()
	defer s.stepUpMode.mu.Unlock()
	s.stepUpMode.f = f
}

func (s *Service) currentStepUpMode() StepUpMode {
	s.stepUpMode.mu.Lock()
	f := s.stepUpMode.f
	s.stepUpMode.mu.Unlock()
	if f == nil {
		return DefaultStepUpMode
	}
	if m, ok := ParseStepUpMode(string(f())); ok {
		return m
	}
	return DefaultStepUpMode
}

// stepUpChoice is how a user can step up at a request's address.
type stepUpChoice struct {
	mode     StepUpMode
	rp       relyingParty
	rpErr    error                 // why passkeys can't work here, if they can't
	creds    []webauthn.Credential // the user's passkeys for rp
	anyPK    bool                  // the user has a passkey under any relying party
	password bool                  // the password fallback applies
}

func (c stepUpChoice) methods() []string {
	switch {
	case len(c.creds) > 0:
		return []string{StepUpMethodPasskey}
	case c.password:
		return []string{StepUpMethodPassword}
	}
	return []string{}
}

// stepUpOptions works out c for the user behind r.
func (s *Service) stepUpOptions(r *http.Request, userID string) (stepUpChoice, error) {
	ctx := r.Context()
	c := stepUpChoice{mode: s.currentStepUpMode()}
	c.rp, c.rpErr = s.relyingParty(r)
	if c.rpErr == nil {
		creds, err := s.userCredentials(ctx, userID, c.rp.ID)
		if err != nil {
			return c, err
		}
		c.creds = creds
	}
	var hasPassword bool
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT
		password_hash IS NOT NULL,
		EXISTS (SELECT 1 FROM passkeys WHERE user_id = users.id)
		FROM users WHERE id = ?`, userID).Scan(&hasPassword, &c.anyPK); err != nil {
		return c, err
	}
	// "Password if no passkey": a passkey that can't be used at this address
	// (plain http, another host name) doesn't lock its owner out.
	c.password = c.mode == StepUpPasskeyOrPassword && hasPassword && len(c.creds) == 0
	return c, nil
}

// registerStepUp mounts:
//
//	POST /api/auth/step-up/begin  (session) → 200 {"method":"passkey","publicKey": request options} or {"method":"password"}
//	POST /api/auth/step-up        (session) the assertion's toJSON(), or {"password"} → 200 {"until"}
func (s *Service) registerStepUp(mux *http.ServeMux) {
	mux.Handle("POST "+stepUpPath+"/begin", s.Require(http.HandlerFunc(s.handleStepUpBegin)))
	mux.Handle("POST "+stepUpPath, s.Require(http.HandlerFunc(s.handleStepUp)))
}

// refuseStepUp answers when c offers no method.
func (s *Service) refuseStepUp(w http.ResponseWriter, c stepUpChoice) {
	var unavailable *unavailableError
	switch {
	case c.anyPK && errors.As(c.rpErr, &unavailable):
		httpx.Error(w, http.StatusConflict, "passkeys_unavailable", unavailable.msg)
	case c.mode == StepUpPasskeyOnly:
		httpx.Error(w, http.StatusForbidden, "passkey_required", "This hub asks for a passkey to confirm destructive actions. Add one to your account first.")
	default:
		httpx.Error(w, http.StatusForbidden, "step_up_unavailable", "Your account has no password or passkey that works here, so it can't confirm destructive actions.")
	}
}

func (s *Service) handleStepUpBegin(w http.ResponseWriter, r *http.Request) {
	if s.rateLimited(w, r, "step-up-begin") {
		return
	}
	ctx := r.Context()
	id, _ := FromContext(ctx)
	c, err := s.stepUpOptions(r, id.User.ID)
	if err != nil {
		s.internalError(w, "could not work out the step-up methods", err)
		return
	}
	switch {
	case len(c.creds) > 0:
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
		// Re-verifying the person, not just the device: user verification is required.
		assertion, session, err := wa.BeginLogin(&passkeyUser{user: id.User, handle: handle, creds: c.creds},
			webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			s.internalError(w, "could not begin passkey step-up", err)
			return
		}
		s.beginCeremony(w, r, ceremony{kind: ceremonyStepUp, rp: c.rp, session: *session, userID: id.User.ID})
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"method": StepUpMethodPasskey, "publicKey": assertion.Response})
	case c.password:
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"method": StepUpMethodPassword})
	default:
		s.refuseStepUp(w, c)
	}
}

func (s *Service) handleStepUp(w http.ResponseWriter, r *http.Request) {
	// The same allowance as sign-in, on its own budget so a fumbled step-up
	// can't lock anyone out of signing in.
	if s.rateLimited(w, r, "step-up") {
		return
	}
	ctx := r.Context()
	id, _ := FromContext(ctx)
	body, ok := readCredential(w, r)
	if !ok {
		return
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad_json", "request body must be a JSON object")
		return
	}
	c, err := s.stepUpOptions(r, id.User.ID)
	if err != nil {
		s.internalError(w, "could not work out the step-up methods", err)
		return
	}
	client := s.proxies.ClientIP(r).String()
	fail := func(reason string, err error) {
		s.log.Info("step-up failed", "user", id.User.Username, "client", client, "reason", reason, "err", err)
		httpx.Error(w, http.StatusForbidden, "step_up_failed", "That didn't confirm it's you. Try again.")
	}
	if raw, isPassword := probe["password"]; isPassword {
		if !c.password {
			if len(c.creds) > 0 {
				httpx.Error(w, http.StatusForbidden, "passkey_required", "Use your passkey to confirm it's you.")
				return
			}
			s.refuseStepUp(w, c)
			return
		}
		var password string
		if len(probe) != 1 || json.Unmarshal(raw, &password) != nil {
			httpx.Error(w, http.StatusBadRequest, "bad_json", `send {"password"} or a passkey assertion`)
			return
		}
		ok, err := s.checkPassword(ctx, id.User.ID, password)
		if err != nil {
			s.internalError(w, "could not check the password", err)
			return
		}
		if !ok {
			fail("wrong password", nil)
			return
		}
		s.grantStepUp(w, r, id, MethodPassword)
		return
	}

	cer, ok := s.takeCeremony(w, r, ceremonyStepUp)
	if !ok || cer.userID != id.User.ID {
		ceremonyGone(w)
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		fail("unparsable assertion", err)
		return
	}
	passkeyID, pu, err := s.lookupPasskey(ctx, cer.rp.ID, parsed.RawID)
	switch {
	case errors.Is(err, errUnknownPasskey), errors.Is(err, errDisabledUser):
		fail("unknown passkey", err)
		return
	case err != nil:
		s.internalError(w, "passkey lookup failed", err)
		return
	case pu.user.ID != id.User.ID:
		fail("someone else's passkey", nil)
		return
	}
	wa, err := cer.rp.webauthn()
	if err != nil {
		s.internalError(w, "passkey relying party", err)
		return
	}
	cred, err := wa.ValidateLogin(pu, cer.session, parsed)
	if err != nil {
		fail(describe(err), nil)
		return
	}
	if err := s.recordPasskeyUse(ctx, passkeyID, cred); err != nil {
		s.internalError(w, "could not record passkey use", err)
		return
	}
	s.grantStepUp(w, r, id, MethodPasskey)
}

// checkPassword verifies the user's password.
func (s *Service) checkPassword(ctx context.Context, userID, password string) (bool, error) {
	if len(password) > MaxPasswordLen {
		return false, nil
	}
	var hash sql.NullString
	if err := s.st.Reader().QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ? AND disabled_at IS NULL`, userID).Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if !hash.Valid {
		return false, nil
	}
	return verifyPassword(password, hash.String)
}

func (s *Service) grantStepUp(w http.ResponseWriter, r *http.Request, id Identity, method string) {
	until := s.now().Add(StepUpTTL)
	res, err := s.st.Writer().ExecContext(r.Context(), `UPDATE sessions SET step_up_until = ? WHERE id = ?`, until.UnixMilli(), id.Session.ID)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			err = ErrNoSession // signed out in the meantime
		}
	}
	if err != nil {
		s.internalError(w, "could not record the step-up", err)
		return
	}
	s.log.Info("step-up granted", "user", id.User.Username, "session", id.Session.ID, "method", method)
	httpx.WriteJSON(w, http.StatusOK, map[string]time.Time{"until": time.UnixMilli(until.UnixMilli()).UTC()})
}

// ClearStepUp ends a session's step-up grant. The action engine calls it
// after a T4 approval, so T4 always asks again.
func (s *Service) ClearStepUp(ctx context.Context, sessionID string) error {
	_, err := s.st.Writer().ExecContext(ctx, `UPDATE sessions SET step_up_until = NULL WHERE id = ?`, sessionID)
	return err
}
