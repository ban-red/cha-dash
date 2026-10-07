package authz

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"dash.cha.sh/internal/audit"
)

// Recorder appends changes made outside the action engine (users, settings,
// integrations, boards, imports) to the audit log, so the log holds every
// change, not just actions. A nil Recorder records nothing.
//
// Params are names and IDs only: never a secret, a password or a whole URL
// (callers keep a URL's host at most).
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
	var e *audit.Error
	if refusal != nil {
		code := "forbidden"
		if errors.Is(refusal, ErrStepUpRequired) {
			code = "step_up_required"
		}
		e = &audit.Error{Code: code, Message: refusal.Error()}
	}
	c.record(r, action, target, params, tier, e)
}

// Refuse records an attempt refused for a reason other than an authz
// decision (validation, the owner's protected account, someone else's
// token), with the code and sentence the caller answered.
func (c *Recorder) Refuse(r *http.Request, action, target string, params any, tier Tier, code, message string) {
	if c == nil || c.Log == nil {
		return
	}
	c.record(r, action, target, params, tier, &audit.Error{Code: code, Message: message})
}

func (c *Recorder) record(r *http.Request, action, target string, params any, tier Tier, refusal *audit.Error) {
	a, _ := ActorFrom(r)
	e := audit.Entry{Actor: AuditActor(a), Action: action, Target: target, Tier: string(tier), Result: audit.OK}
	if params != nil {
		if raw, err := json.Marshal(params); err == nil {
			e.Params = raw
		}
	}
	if refusal != nil {
		e.Result, e.Error = audit.Refused, refusal
	}
	// WithoutCancel: a client that hangs up mid-request mustn't keep a
	// change that already happened out of the log.
	if _, err := c.Log.Append(context.WithoutCancel(r.Context()), e); err != nil && c.Logger != nil {
		// The change has happened; failing it now would only hide that.
		c.Logger.Error("audit: recording a change failed", "err", err, "action", action)
	}
}

// AuditActor is a's audit record. A token's (or an agent's) names the user
// it acts for in on_behalf_of.
func AuditActor(a Actor) audit.Actor {
	out := audit.Actor{Kind: a.Kind, ID: a.ID, Name: a.Name, Via: a.Via, Session: a.Session, IP: a.IP}
	if a.Kind == KindToken || a.Kind == KindAgent {
		out.OnBehalfOf = a.User
	}
	return out
}
