package server

import (
	"context"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/realtime"
)

// A realtime socket reads its identity once, when it opens, and every
// viewer filter (the tasks a viewer sees, the household's inbox and graph)
// keeps using it. These keep open sockets in step with auth: realtime
// never imports auth, and auth never imports realtime or authz.

// SocketRecheck is realtime.Options.Recheck for the hub: whether the
// identity a socket opened with still stands (its session exists and
// hasn't expired, its user isn't disabled, its role is unchanged).
func SocketRecheck(a *auth.Service) func(ctx context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		id, ok := auth.FromContext(ctx)
		if !ok {
			return false, nil
		}
		return a.StillValid(ctx, id)
	}
}

// closeSocketsOnAuthEvents closes the sockets an auth event ends at once,
// rather than at their next recheck: a signed-out session's, and every
// socket of a user who was disabled, changed role, or gained or handed
// over ownership. Closing waits for each close handshake, so it runs off
// the request's goroutine.
func closeSocketsOnAuthEvents(a *auth.Service, h *realtime.Hub) {
	a.OnEvent(func(e auth.Event) {
		var match func(ctx context.Context) bool
		switch e.Kind {
		case auth.EventSessionEnded:
			match = func(ctx context.Context) bool {
				id, ok := auth.FromContext(ctx)
				return ok && id.Session.ID == e.SessionID
			}
		case auth.EventUserChanged:
			match = func(ctx context.Context) bool {
				id, ok := auth.FromContext(ctx)
				return ok && id.User.ID == e.UserID
			}
		default:
			return
		}
		go h.CloseWhere(match, "session ended or changed")
	})
}

// auditAuthEvents records the signed-in user's own security changes:
// passkeys added, renamed or removed, and step-ups granted or failed.
// Names and IDs only; never key material or what was typed.
func auditAuthEvents(a *auth.Service, rec *authz.Recorder) {
	a.OnEvent(func(e auth.Event) { auditAuthEvent(rec, e) })
}

// auditAuthEvent records one auth event, if it's one the log keeps.
func auditAuthEvent(rec *authz.Recorder, e auth.Event) {
	if e.Request == nil {
		return
	}
	r := e.Request
	switch e.Kind {
	case auth.EventPasskeyRegistered:
		rec.Change(r, "auth.passkey.register", "passkey:"+e.Passkey, map[string]string{"name": e.Name}, authz.T1, nil)
	case auth.EventPasskeyRenamed:
		rec.Change(r, "auth.passkey.rename", "passkey:"+e.Passkey, map[string]string{"name": e.Name}, authz.T1, nil)
	case auth.EventPasskeyDeleted:
		rec.Change(r, "auth.passkey.delete", "passkey:"+e.Passkey, nil, authz.T1, nil)
	case auth.EventStepUp:
		rec.Change(r, "auth.step-up", "session:"+e.SessionID, map[string]string{"method": e.Method}, authz.T0, nil)
	case auth.EventStepUpFailed:
		rec.Refuse(r, "auth.step-up", "session:"+e.SessionID, map[string]string{"reason": e.Reason}, authz.T0,
			"step_up_failed", "That didn't confirm it's you.")
	}
}
