package security

// Agents (ADR-0017; docs/agents.md): one switch, off by default, in
// Settings → Security, and the limits on an agent token's proposals. While
// it's off no agent token can be minted (authz.Tokens asks AgentsOn), the
// MCP endpoint answers 404, the policy refuses every agent (so a proposal
// left waiting can't be approved), and turning it off withdraws the
// proposals waiting (OnAgentsOff). It revokes no token: turning it on again
// lets the same tokens back in.
//
// It's the settings row AgentsKey, a JSON object, {"enabled": false} when
// missing. It's read on every use rather than kept in memory: minting a
// token is rare, MCP calls are few, and a Home file import or a restore
// then needs no reload.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// AgentsKey is the settings row the Agents switch lives in.
const AgentsKey = "agents"

// Limits on an agent token's proposals the owner may set.
const (
	MaxAgentPending = 20
	MaxAgentPerHour = 200
)

// AgentSettings is the Agents switch and the limits on each agent token's
// proposals, as the row and the API hold them. A limit of 0 in the row is
// the default (action.DefaultAgentLimits); the API always shows the limit
// in force.
type AgentSettings struct {
	Enabled bool `json:"enabled"`
	// MaxPending is how many proposals one agent token may have waiting
	// for a person at once (3 by default).
	MaxPending int `json:"max_pending"`
	// MaxPerHour is how many one agent token may make in any hour (20 by
	// default).
	MaxPerHour int `json:"max_per_hour"`
}

// withDefaults fills unset limits with the defaults.
func (a AgentSettings) withDefaults() AgentSettings {
	if a.MaxPending <= 0 {
		a.MaxPending = action.DefaultAgentLimits.Pending
	}
	if a.MaxPerHour <= 0 {
		a.MaxPerHour = action.DefaultAgentLimits.PerHour
	}
	return a
}

// Agents reads the switch and the limits. A missing or unreadable row is
// off, with the default limits.
func (s *Service) Agents(ctx context.Context) (AgentSettings, error) {
	var raw string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, AgentsKey).Scan(&raw)
	if isNoRows(err) {
		return AgentSettings{}.withDefaults(), nil
	}
	if err != nil {
		return AgentSettings{}, fmt.Errorf("security: reading the Agents setting: %w", err)
	}
	var a AgentSettings
	if json.Unmarshal([]byte(raw), &a) != nil {
		s.log.Warn("security: the Agents setting isn't readable; Agents is off")
		return AgentSettings{}.withDefaults(), nil
	}
	return a.withDefaults(), nil
}

// AgentsOn reports whether Agents is on, for authz.Tokens, the policy and
// the MCP endpoint.
func (s *Service) AgentsOn(ctx context.Context) (bool, error) {
	a, err := s.Agents(ctx)
	return a.Enabled, err
}

// AgentLimits are the limits on an agent token's proposals, for the
// action engine.
func (s *Service) AgentLimits(ctx context.Context) (action.AgentLimits, error) {
	a, err := s.Agents(ctx)
	return action.AgentLimits{Pending: a.MaxPending, PerHour: a.MaxPerHour}, err
}

// SetAgents stores the switch and the limits.
func (s *Service) SetAgents(ctx context.Context, a AgentSettings) error {
	b, _ := json.Marshal(a)
	if _, err := s.st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		AgentsKey, string(b), s.now().UnixMilli()); err != nil {
		return fmt.Errorf("security: saving the Agents setting: %w", err)
	}
	s.log.Warn("Agents setting changed", "enabled", a.Enabled, "max_pending", a.MaxPending, "max_per_hour", a.MaxPerHour)
	return nil
}

// RegisterAgents mounts the routes, both the owner's and session only
// (authz/routes.go):
//
//	GET /api/settings/agents   {"enabled", "max_pending", "max_per_hour"}
//	PUT /api/settings/agents   {"enabled", "max_pending"?, "max_per_hour"?} → the same; audited as settings.agents.update
func (s *Service) RegisterAgents(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/settings/agents", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := s.Agents(r.Context())
		if err != nil {
			s.log.Error("security: reading the Agents setting failed", "err", err)
			httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, a)
	})))
	mux.Handle("PUT /api/settings/agents", wrap(http.HandlerFunc(s.putAgents)))
}

func (s *Service) putAgents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled    *bool `json:"enabled"`
		MaxPending *int  `json:"max_pending"`
		MaxPerHour *int  `json:"max_per_hour"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("agents.settings_invalid"))
		return
	}
	before, err := s.Agents(r.Context())
	if err != nil {
		s.log.Error("security: reading the Agents setting failed", "err", err)
		httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
		return
	}
	next := AgentSettings{Enabled: *req.Enabled, MaxPending: before.MaxPending, MaxPerHour: before.MaxPerHour}
	if req.MaxPending != nil {
		next.MaxPending = *req.MaxPending
	}
	if req.MaxPerHour != nil {
		next.MaxPerHour = *req.MaxPerHour
	}
	if next.MaxPending < 1 || next.MaxPending > MaxAgentPending || next.MaxPerHour < 1 || next.MaxPerHour > MaxAgentPerHour {
		httpx.Say(w, http.StatusUnprocessableEntity, "invalid", msg.New("agents.limits_invalid"))
		return
	}
	if err := s.SetAgents(r.Context(), next); err != nil {
		s.log.Error("security: saving the Agents setting failed", "err", err)
		httpx.Say(w, http.StatusInternalServerError, "internal", msg.New("hub.internal_error"))
		return
	}
	s.Audit.Change(r, "settings.agents.update", "settings:agents", map[string]any{
		"before": before.Enabled, "enabled": next.Enabled,
		"max_pending": next.MaxPending, "max_per_hour": next.MaxPerHour,
	}, authz.T1, nil)
	if before.Enabled && !next.Enabled && s.OnAgentsOff != nil {
		s.OnAgentsOff(context.WithoutCancel(r.Context()))
	}
	httpx.WriteJSON(w, http.StatusOK, next)
}
