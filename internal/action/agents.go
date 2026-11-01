package action

// Agents' proposals (ADR-0017). An agent's plan always waits for a person
// (the policy's NeedsApproval), like a token's T2, with these differences:
//
//   - it expires after AgentApprovalTTL (15 minutes), not ApprovalTTL;
//   - it carries the agent's rationale (Plan.Rationale), shown quoted and
//     never part of the hash;
//   - limits per token: at most AgentLimits.Pending waiting at once and
//     AgentLimits.PerHour made in the last hour. Beyond them the proposal
//     is refused (429 rate_limited, audited), and the Inbox gets one item
//     per token saying the agent is pushing (a burst), resolved an hour
//     after its last refusal or when the token is revoked;
//   - an approval re-checks the blast radius and tier, not only the
//     target's state: a plan that would now affect something else isn't
//     the one that was proposed (approval_void, plan_changed);
//   - Approval.NthToday counts the agent's proposals in the 24 hours up to
//     this one, for the sheet's "3rd proposal from this agent today".

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode"

	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/msg"
)

// AgentApprovalTTL is how long an agent's proposal waits for a person.
const AgentApprovalTTL = 15 * time.Minute

// MaxRationale bounds an agent's rationale, in characters.
const MaxRationale = 500

// AgentLimits are an agent token's limits on proposals.
type AgentLimits struct {
	// Pending is how many may wait for a person at once.
	Pending int `json:"max_pending"`
	// PerHour is how many may be made in any hour.
	PerHour int `json:"max_per_hour"`
}

// DefaultAgentLimits are ADR-0017's: 3 waiting, 20 an hour.
var DefaultAgentLimits = AgentLimits{Pending: 3, PerHour: 20}

// CleanRationale trims an agent's rationale and drops control characters
// (newlines become spaces), bounded to MaxRationale characters.
func CleanRationale(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1 // bidi overrides and other invisible format characters too
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > MaxRationale {
		s = string(r[:MaxRationale])
	}
	return s
}

// agentLimits are the limits in force: Options.AgentLimits, or the defaults.
func (e *Engine) agentLimits(ctx context.Context) AgentLimits {
	l := DefaultAgentLimits
	if e.limits != nil {
		if got, err := e.limits(ctx); err == nil {
			if got.Pending > 0 {
				l.Pending = got.Pending
			}
			if got.PerHour > 0 {
				l.PerHour = got.PerHour
			}
		} else {
			e.log.Error("action: reading the agents' limits failed; using the defaults", "err", err)
		}
	}
	return l
}

// overLimit refuses an agent's proposal beyond its token's limits, and
// notes the burst in the Inbox. Callers hold agentMu, so two proposals at
// once can't both slip under a limit.
func (e *Engine) overLimit(ctx context.Context, a Actor) *Refusal {
	l := e.agentLimits(ctx)
	var pending, lastHour int
	err := e.st.Reader().QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN created_at > ? THEN 1 ELSE 0 END), 0)
		FROM approvals WHERE originator_id = ?`,
		ApprovalAwaiting, e.now().Add(-time.Hour).UnixMilli(), a.ID).Scan(&pending, &lastHour)
	if err != nil {
		e.log.Error("action: counting an agent's proposals failed; refusing", "err", err)
		return &Refusal{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: msg.T("agents.check_failed", nil)}
	}
	var r *Refusal
	switch {
	case pending >= l.Pending:
		r = &Refusal{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: msg.New("agents.too_many_pending", "count", pending).Text()}
	case lastHour >= l.PerHour:
		r = &Refusal{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: msg.New("agents.too_many_per_hour", "count", lastHour).Text()}
	default:
		return nil
	}
	e.noteBurst(a)
	return r
}

// burst is one token's refused proposals in the last hour, for its Inbox item.
type burst struct {
	opened  time.Time
	last    time.Time
	refused []time.Time
	name    string
}

// BurstItemID is the Inbox item's ID for an agent token pushing past its
// limits.
func BurstItemID(token string) string { return "agent." + token + ".burst" }

// noteBurst counts a refused proposal and puts the token's burst item in
// the Inbox, open, for admins and the owner.
func (e *Engine) noteBurst(a Actor) {
	now := e.now().UTC().Truncate(time.Millisecond)
	e.mu.Lock()
	b := e.bursts[a.ID]
	if b == nil {
		b = &burst{opened: now, name: a.Name}
		e.bursts[a.ID] = b
	}
	b.last = now
	b.refused = append(trimHour(b.refused, now), now)
	count := len(b.refused)
	opened := b.opened
	delete(e.resolvedItems, BurstItemID(a.ID))
	e.mu.Unlock()
	e.putBurst(a.ID, a.Name, opened, now, count, nil)
}

func trimHour(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < time.Hour {
			out = append(out, t)
		}
	}
	return out
}

// burstItem is the Inbox item for a burst: an incident of class problem
// and category security, about the token, never about a resource.
type burstItem struct {
	ID           string     `json:"id"`
	Scenario     string     `json:"scenario"`
	Class        string     `json:"class"`
	Category     string     `json:"category"`
	Severity     string     `json:"severity"`
	Status       string     `json:"status"`
	Title        string     `json:"title"`
	Summary      string     `json:"summary"`
	Target       string     `json:"target"`
	OpenedAt     time.Time  `json:"opened_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy   string     `json:"resolved_by,omitempty"`
	Facts        any        `json:"facts"`
	Causes       []any      `json:"causes"`
	BlastRadius  []Impact   `json:"blast_radius"`
	ResolvesWhen string     `json:"resolves_when"`
}

func (e *Engine) putBurst(token, name string, opened, updated time.Time, count int, resolved *time.Time) {
	if e.inbox == nil {
		return
	}
	item := burstItem{
		// A security finding: the household never sees it.
		ID: BurstItemID(token), Scenario: "agent.burst", Class: "problem", Category: "security", Severity: "attention", Status: "open",
		Title:   msg.New("agents.burst_title", "agent", name).Text(),
		Summary: msg.New("agents.burst_summary", "count", count).Text(),
		Target:  "token:" + token, OpenedAt: opened, UpdatedAt: updated,
		Facts:  map[string]any{"token": token, "refused_last_hour": count},
		Causes: []any{}, BlastRadius: []Impact{},
		ResolvesWhen: msg.New("agents.burst_resolves").Text(),
	}
	if resolved != nil {
		item.Status, item.Severity, item.ResolvedAt, item.ResolvedBy, item.UpdatedAt = "resolved", "info", resolved, "auto", *resolved
	}
	_, _ = e.inbox.Update(func(tx *livedoc.Tx) error {
		tx.Put(incidentsColl, item.ID, item)
		return nil
	})
}

// resolveBursts resolves the burst items whose last refusal is an hour old,
// or every one of token's when token isn't "" (it was revoked).
func (e *Engine) resolveBursts(token string) {
	now := e.now().UTC().Truncate(time.Millisecond)
	type done struct {
		id, name     string
		opened, last time.Time
		count        int
	}
	var ended []done
	e.mu.Lock()
	for id, b := range e.bursts {
		if (token != "" && id == token) || (token == "" && now.Sub(b.last) >= time.Hour) {
			ended = append(ended, done{id, b.name, b.opened, b.last, len(b.refused)})
			delete(e.bursts, id)
			e.resolvedItems[BurstItemID(id)] = now
		}
	}
	e.mu.Unlock()
	for _, d := range ended {
		e.putBurst(d.id, d.name, d.opened, d.last, d.count, &now)
	}
}

// nthToday counts originator's approvals in the 24 hours up to and
// including one made at created: this is its Nth.
func (e *Engine) nthToday(ctx context.Context, originator string, created time.Time) int {
	var n int
	if err := e.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE originator_id = ? AND created_at > ? AND created_at <= ?`,
		originator, created.Add(-24*time.Hour).UnixMilli(), created.UnixMilli()).Scan(&n); err != nil {
		return 0
	}
	return n
}

// WithdrawKind ends every approval awaiting a person whose originator is of
// kind (agent), as expired with reason: turning Agents off takes back what
// agents asked for.
func (e *Engine) WithdrawKind(ctx context.Context, kind, reason string) {
	e.endAwaiting(ctx, `SELECT id FROM approvals WHERE state = ? AND json_extract(originator, '$.kind') = ?`, []any{ApprovalAwaiting, kind}, reason)
}

// changedSince reports whether p, approved now for an agent, would no
// longer be the plan that was proposed: another tier or another blast
// radius, so another hash.
func (e *Engine) changedSince(ctx context.Context, d *Def, p *storedPlan, t Target, r Request) bool {
	tier := d.Tier
	if d.TierFor != nil {
		tier = max(tier, d.TierFor(t))
	}
	if d.TierOf != nil {
		tier = max(tier, d.TierOf(ctx, r))
	}
	return planHash(d.ID, t.ID, p.params, tier, e.impactOf(ctx, d, r), t.State) != p.hash
}
