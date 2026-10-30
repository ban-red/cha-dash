package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
)

// EventKind names something that happened to an account or a session.
type EventKind string

// Events. auth never imports authz or the realtime hub: the server wiring
// listens (OnEvent) and audits them, and closes the sockets they end.
const (
	// EventSessionEnded: a session was deleted (sign-out). SessionID names it.
	EventSessionEnded EventKind = "session.ended"
	// EventUserChanged: an account's role changed or it was disabled (which
	// deletes its sessions), or ownership moved to or from it. Whatever its
	// sessions were allowed before may no longer hold.
	EventUserChanged EventKind = "user.changed"
	// EventSignedIn: a person signed in and a session started. Method is
	// the one the session records (password, passkey, dev). Every sign-in
	// method ends in StartSession, which emits it once.
	EventSignedIn EventKind = "signed_in"
	// EventStepUp: a step-up was granted (Method is password or passkey).
	EventStepUp EventKind = "step_up"
	// EventStepUpFailed: a step-up attempt didn't confirm the person
	// (Reason says why, never what was sent).
	EventStepUpFailed EventKind = "step_up.failed"
	// EventPasskeyRegistered, EventPasskeyRenamed, EventPasskeyDeleted:
	// the signed-in user changed their passkeys. Passkey and Name identify
	// it; no key material.
	EventPasskeyRegistered EventKind = "passkey.registered"
	EventPasskeyRenamed    EventKind = "passkey.renamed"
	EventPasskeyDeleted    EventKind = "passkey.deleted"
)

// Event is one auth event. Fields that don't apply are empty. It never
// carries a secret: no password, token or key material.
type Event struct {
	Kind      EventKind
	UserID    string
	SessionID string
	// Passkey and Name: the passkey's ID and name (passkey events).
	Passkey, Name string
	// Method: how a sign-in or a step-up confirmed the person (password,
	// passkey; dev for a sign-in).
	Method string
	// Reason: why a step-up failed, in a few words.
	Reason string
	// Request is the HTTP request behind it, with the signed-in identity on
	// its context when there was one; nil when there was none.
	Request *http.Request
}

// listeners are the functions OnEvent registered.
type listeners struct {
	mu sync.RWMutex
	fs []func(Event)
}

// OnEvent adds f to what's told about every auth event, after the change
// has committed. f runs on the request's goroutine, so it must return
// quickly; it must not call back into the Service's write paths.
func (s *Service) OnEvent(f func(Event)) {
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	s.events.fs = append(s.events.fs, f)
}

func (s *Service) emit(e Event) {
	s.events.mu.RLock()
	fs := s.events.fs
	s.events.mu.RUnlock()
	for _, f := range fs {
		f(e)
	}
}

// StillValid reports whether id's session still stands as it was when id
// was read: the session exists and hasn't expired, its user isn't
// disabled, and the user's role is unchanged. A long-lived connection
// (the realtime socket) checks this now and then, since it read its
// identity once, when it opened.
func (s *Service) StillValid(ctx context.Context, id Identity) (bool, error) {
	if id.Session.ID == "" {
		return false, nil
	}
	var role string
	err := s.st.Reader().QueryRowContext(ctx, `
		SELECT u.role FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = ? AND s.user_id = ? AND s.expires_at > ? AND u.disabled_at IS NULL`,
		id.Session.ID, id.User.ID, s.now().UnixMilli()).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return role == id.User.Role, nil
}
