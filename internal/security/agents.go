package security

// Agents (ADR-0017; docs/plan/14-v0.4.md, "Agents"): one switch, off by
// default, in Settings → Security. While it's off no agent token can be
// minted (authz.Tokens asks AgentsOn), and from wave 29 the MCP endpoint
// won't answer either. Turning it off doesn't revoke agent tokens already
// made; what they may do while it's off is wave 29's to decide.
//
// It's the settings row AgentsKey, a JSON object, {"enabled": false} when
// missing. It's read on every use rather than kept in memory: minting a
// token is rare, and a Home file import or a restore then needs no reload.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
)

// AgentsKey is the settings row the Agents switch lives in.
const AgentsKey = "agents"

// AgentSettings is the Agents switch as the row and the API hold it.
type AgentSettings struct {
	Enabled bool `json:"enabled"`
}

// Agents reads the switch. A missing or unreadable row is off.
func (s *Service) Agents(ctx context.Context) (AgentSettings, error) {
	var raw string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, AgentsKey).Scan(&raw)
	if isNoRows(err) {
		return AgentSettings{}, nil
	}
	if err != nil {
		return AgentSettings{}, fmt.Errorf("security: reading the Agents setting: %w", err)
	}
	var a AgentSettings
	if json.Unmarshal([]byte(raw), &a) != nil {
		s.log.Warn("security: the Agents setting isn't readable; Agents is off")
		return AgentSettings{}, nil
	}
	return a, nil
}

// AgentsOn reports whether Agents is on, for authz.Tokens.
func (s *Service) AgentsOn(ctx context.Context) (bool, error) {
	a, err := s.Agents(ctx)
	return a.Enabled, err
}

// SetAgents stores the switch.
func (s *Service) SetAgents(ctx context.Context, a AgentSettings) error {
	b, _ := json.Marshal(a)
	if _, err := s.st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		AgentsKey, string(b), s.now().UnixMilli()); err != nil {
		return fmt.Errorf("security: saving the Agents setting: %w", err)
	}
	s.log.Warn("Agents setting changed", "enabled", a.Enabled)
	return nil
}

// RegisterAgents mounts the routes, both the owner's and session only
// (authz/routes.go):
//
//	GET /api/settings/agents   {"enabled"}
//	PUT /api/settings/agents   {"enabled"} → the same; audited as settings.agents.update
func (s *Service) RegisterAgents(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/settings/agents", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := s.Agents(r.Context())
		if err != nil {
			s.log.Error("security: reading the Agents setting failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, a)
	})))
	mux.Handle("PUT /api/settings/agents", wrap(http.HandlerFunc(s.putAgents)))
}

func (s *Service) putAgents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "Send enabled, true or false.")
		return
	}
	before, err := s.Agents(r.Context())
	if err == nil {
		err = s.SetAgents(r.Context(), AgentSettings{Enabled: *req.Enabled})
	}
	if err != nil {
		s.log.Error("security: saving the Agents setting failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.Audit.Change(r, "settings.agents.update", "settings:agents", map[string]bool{"before": before.Enabled, "enabled": *req.Enabled}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, AgentSettings{Enabled: *req.Enabled})
}
