package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Rules: the fixed, symptom-first set. A row in notify_rules records only
// what an admin changed; everything else is the default here.
const (
	RuleDown      = "incident.down"
	RuleAttention = "incident.attention"
	RuleRepair    = "repair"
	RuleStuck     = "stack.stuck"
	RuleAction    = "action.t4"
	RuleApproval  = "approval"
)

// Grace bounds for the attention rule, in minutes.
const (
	defaultGraceMinutes = 5
	minGraceMinutes     = 1
	maxGraceMinutes     = 240
)

// RuleSettings is what an admin can change about a rule.
type RuleSettings struct {
	Enabled  bool `json:"enabled"`
	Resolved bool `json:"resolved"` // also say when it's resolved
	// GraceMinutes is the attention rule's wait before notifying; 0 elsewhere.
	GraceMinutes int `json:"grace_minutes,omitempty"`
	// Channels are the channel IDs it goes to; nil means every enabled one.
	Channels []string `json:"channels"`
}

// Rule is one rule as GET /api/notify/rules shows it.
type Rule struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	RuleSettings
	// Locked rules can't be turned off: every critical (T4) action reaches
	// the admins and the owner (02 · Experience → the safety ladder).
	Locked bool `json:"locked,omitempty"`
	// HasResolved says whether a resolution notice makes sense for it.
	HasResolved bool         `json:"has_resolved"`
	Defaults    RuleSettings `json:"defaults"`
	UpdatedAt   *time.Time   `json:"updated_at"`
	UpdatedBy   string       `json:"updated_by,omitempty"`
}

type ruleDef struct {
	id, label, description string
	defaults               RuleSettings
	locked, hasResolved    bool
}

// ruleDefs are the rules, in the order Settings shows them.
var ruleDefs = []ruleDef{
	{id: RuleDown, label: "Something is down",
		description: "A problem opens at down, or gets worse until it is. Sent at once, and again when it's resolved.",
		defaults:    RuleSettings{Enabled: true, Resolved: true}, hasResolved: true},
	{id: RuleAttention, label: "Needs attention for a while",
		description: "A problem that needs attention and is still open after the grace period, so a flap that clears by itself never pages anyone. Not while a fix is running.",
		defaults:    RuleSettings{Enabled: true, GraceMinutes: defaultGraceMinutes}, hasResolved: true},
	{id: RuleRepair, label: "An integration needs reconnecting",
		description: "Cha Dash can't read a source any more, usually a rejected token or a changed certificate. Once per problem.",
		defaults:    RuleSettings{Enabled: true}, hasResolved: true},
	{id: RuleStuck, label: "A stack is stuck",
		description: "A deploy failed and so did its rollback, so the stack may be half-updated.",
		defaults:    RuleSettings{Enabled: true, Resolved: true}, hasResolved: true},
	{id: RuleApproval, label: "A plan is waiting for approval",
		description: "An API token planned something that needs a person (T2 and up). Web Push goes to admins and the owner, who can approve it; it expires after an hour.",
		defaults:    RuleSettings{Enabled: true}},
	{id: RuleAction, label: "Critical actions",
		description: "Every T4 action that runs, whoever ran it and however it ended. Web Push goes to admins and the owner only. Always on.",
		defaults:    RuleSettings{Enabled: true}, locked: true},
}

func ruleDefOf(id string) (ruleDef, bool) {
	for _, d := range ruleDefs {
		if d.id == id {
			return d, true
		}
	}
	return ruleDef{}, false
}

type storedRule struct {
	settings  RuleSettings
	updatedAt time.Time
	updatedBy string
}

// loadRules reads the admins' changes.
func (s *Service) loadRules(ctx context.Context) error {
	rows, err := s.st.Reader().QueryContext(ctx, `SELECT id, settings, updated_at, updated_by FROM notify_rules`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := map[string]storedRule{}
	for rows.Next() {
		var id, raw, by string
		var at int64
		if err := rows.Scan(&id, &raw, &at, &by); err != nil {
			return err
		}
		d, ok := ruleDefOf(id)
		if !ok {
			continue
		}
		rs := d.defaults
		if json.Unmarshal([]byte(raw), &rs) != nil {
			continue
		}
		m[id] = storedRule{settings: normalize(d, rs), updatedAt: time.UnixMilli(at).UTC(), updatedBy: by}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.rules = m
	s.mu.Unlock()
	return nil
}

// normalize applies a rule's fixed parts to settings.
func normalize(d ruleDef, rs RuleSettings) RuleSettings {
	if d.locked {
		rs.Enabled = true
	}
	if !d.hasResolved {
		rs.Resolved = false
	}
	if d.id == RuleAttention {
		if rs.GraceMinutes == 0 {
			rs.GraceMinutes = defaultGraceMinutes
		}
		rs.GraceMinutes = min(max(rs.GraceMinutes, minGraceMinutes), maxGraceMinutes)
	} else {
		rs.GraceMinutes = 0
	}
	return rs
}

// settings is a rule's current settings.
func (s *Service) settings(id string) RuleSettings {
	d, _ := ruleDefOf(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rules[id]; ok {
		return r.settings
	}
	return normalize(d, d.defaults)
}

// grace is the attention rule's grace period.
func (s *Service) grace() time.Duration {
	return time.Duration(s.settings(RuleAttention).GraceMinutes) * time.Minute
}

// Rules lists every rule with its settings.
func (s *Service) Rules() []Rule {
	out := make([]Rule, 0, len(ruleDefs))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range ruleDefs {
		r := Rule{ID: d.id, Label: d.label, Description: d.description, Locked: d.locked, HasResolved: d.hasResolved, Defaults: normalize(d, d.defaults)}
		r.RuleSettings = r.Defaults
		if st, ok := s.rules[d.id]; ok {
			r.RuleSettings = st.settings
			at := st.updatedAt
			r.UpdatedAt, r.UpdatedBy = &at, st.updatedBy
		}
		if r.Channels != nil {
			r.Channels = slices.Clone(r.Channels)
		}
		out = append(out, r)
	}
	return out
}

// RuleUpdate is PUT /api/notify/rules/{id}: fields left out keep their value.
type RuleUpdate struct {
	Enabled      *bool            `json:"enabled"`
	Resolved     *bool            `json:"resolved"`
	GraceMinutes *int             `json:"grace_minutes"`
	Channels     *json.RawMessage `json:"channels"` // null: every channel; [] or a list
}

// Rule errors, as sentences.
var (
	errUnknownRule = sentence("There's no such rule.")
	errLocked      = sentence("Critical actions always notify the admins and the owner; this rule can't be turned off.")
)

// UpdateRule changes one rule.
func (s *Service) UpdateRule(ctx context.Context, id string, u RuleUpdate, by string) (Rule, error) {
	d, ok := ruleDefOf(id)
	if !ok {
		return Rule{}, errUnknownRule
	}
	rs := s.settings(id)
	if u.Enabled != nil {
		if d.locked && !*u.Enabled {
			return Rule{}, errLocked
		}
		rs.Enabled = *u.Enabled
	}
	if u.Resolved != nil {
		if !d.hasResolved && *u.Resolved {
			return Rule{}, sentence("This rule has no resolution to tell anyone about.")
		}
		rs.Resolved = *u.Resolved
	}
	if u.GraceMinutes != nil {
		if d.id != RuleAttention {
			return Rule{}, sentence("Only the attention rule has a grace period.")
		}
		if *u.GraceMinutes < minGraceMinutes || *u.GraceMinutes > maxGraceMinutes {
			return Rule{}, sentence("The grace period is 1 to 240 minutes.")
		}
		rs.GraceMinutes = *u.GraceMinutes
	}
	if u.Channels != nil {
		var ids []string
		if string(*u.Channels) != "null" {
			if err := json.Unmarshal(*u.Channels, &ids); err != nil {
				return Rule{}, sentence("channels is a list of channel IDs, or null for every channel.")
			}
			if ids == nil {
				ids = []string{}
			}
			known, err := s.channelIDs(ctx)
			if err != nil {
				return Rule{}, err
			}
			seen := map[string]bool{}
			clean := []string{}
			for _, c := range ids {
				if !known[c] {
					return Rule{}, sentence("There's no channel " + clip(c, 40) + ".")
				}
				if !seen[c] {
					seen[c] = true
					clean = append(clean, c)
				}
			}
			ids = clean
		}
		rs.Channels = ids
	}
	rs = normalize(d, rs)
	raw, _ := json.Marshal(rs)
	now := s.now().UTC()
	if _, err := s.st.Writer().ExecContext(ctx, `INSERT INTO notify_rules (id, settings, updated_at, updated_by) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET settings = excluded.settings, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		id, string(raw), now.UnixMilli(), by); err != nil {
		return Rule{}, err
	}
	s.mu.Lock()
	if s.rules == nil {
		s.rules = map[string]storedRule{}
	}
	s.rules[id] = storedRule{settings: rs, updatedAt: now.Truncate(time.Millisecond), updatedBy: by}
	s.mu.Unlock()
	for _, r := range s.Rules() {
		if r.ID == id {
			return r, nil
		}
	}
	return Rule{}, errUnknownRule
}

// routes reports whether rule sends to channel.
func (s *Service) routes(rule, channel string) bool {
	rs := s.settings(rule)
	return rs.Channels == nil || slices.Contains(rs.Channels, channel)
}

// dropChannel forgets a deleted channel in every rule that names it.
func (s *Service) dropChannel(ctx context.Context, id string) {
	for _, r := range s.Rules() {
		if r.Channels == nil || !slices.Contains(r.Channels, id) {
			continue
		}
		rest, _ := json.Marshal(slices.DeleteFunc(slices.Clone(r.Channels), func(c string) bool { return c == id }))
		raw := json.RawMessage(rest)
		if _, err := s.UpdateRule(ctx, r.ID, RuleUpdate{Channels: &raw}, r.UpdatedBy); err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("notify: dropping a deleted channel from a rule", "rule", r.ID, "err", err)
		}
	}
}
