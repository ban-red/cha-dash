package authz

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"dash.cha.sh/internal/audit"
)

// Recorder appends changes made outside the action engine (users and
// settings) to the audit log, so the log holds every change, not just
// actions. A nil Recorder records nothing.
type Recorder struct {
	Log    *audit.Log
	Logger *slog.Logger
}

// Change records one change by r's actor. params is what was asked for
// (never secrets: passwords are left out by the callers); refusal is the
// authz error that stopped it, or nil when it went through.
func (c *Recorder) Change(r *http.Request, action, target string, params any, tier Tier, refusal error) {
	if c == nil || c.Log == nil {
		return
	}
	a, _ := ActorFrom(r)
	e := audit.Entry{
		Actor:  audit.Actor{Kind: a.Kind, ID: a.ID, Name: a.Name, Via: a.Via, Session: a.Session, IP: a.IP},
		Action: action, Target: target, Tier: string(tier), Result: audit.OK,
	}
	if params != nil {
		if raw, err := json.Marshal(params); err == nil {
			e.Params = raw
		}
	}
	if refusal != nil {
		code := "forbidden"
		if errors.Is(refusal, ErrStepUpRequired) {
			code = "step_up_required"
		}
		e.Result, e.Error = audit.Refused, &audit.Error{Code: code, Message: refusal.Error()}
	}
	if _, err := c.Log.Append(r.Context(), e); err != nil && c.Logger != nil {
		// The change has happened; failing it now would only hide that.
		c.Logger.Error("audit: recording a change failed", "err", err, "action", action)
	}
}
