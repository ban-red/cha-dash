// Package security holds the hub's security posture: how much security
// findings (incidents with category "security") interrupt the user.
//
// Integrations declare each finding's severity: attention for a real
// exposure, info for advice. The posture decides the severity the inbox
// publishes:
//
//	relaxed   every security finding at info (the default: a LAN-only home)
//	standard  the declared severity
//	strict    every security finding at attention
//
// At info a finding is an Inbox hint only, never in Needs attention, the
// Briefing, the Inbox badge or the Incidents tile. The transform runs where
// incidents enter the inbox topic (runtime.Emitter for real instances, the
// demo mirror for the Demo Home); both keep the declared severity in their
// own state, so a change re-applies to every open finding at once.
//
// The posture is one settings row, SettingsKey, holding a JSON string.
//
// The same settings page holds the step-up rule (StepUpKey): how people
// confirm destructive actions, a passkey or the password fallback
// (auth.StepUpMode). auth reads it through StepUp; changing it is a T3
// change, so it needs a step-up under the rule in force.
package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
)

// Posture is a security level's ID.
type Posture string

// The levels.
const (
	Relaxed  Posture = "relaxed"
	Standard Posture = "standard"
	Strict   Posture = "strict"

	Default = Relaxed
)

// SettingsKey is the settings row the posture lives in.
const SettingsKey = "security.posture"

// Level describes a posture for the settings page.
type Level struct {
	ID          Posture `json:"id"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Default     bool    `json:"default,omitempty"`
}

// Levels are the postures, least interrupting first.
var Levels = []Level{
	{ID: Relaxed, Label: "Relaxed · LAN-only home network", Description: "Security findings stay in the Inbox as hints; they never notify you.", Default: true},
	{ID: Standard, Label: "Standard · reachable beyond the LAN (VPN, reverse proxy)", Description: "Exposures that hand out control or credentials need you; advice stays a hint."},
	{ID: Strict, Label: "Strict · exposed to the internet or shared networks", Description: "Every security finding needs you, advice included."},
}

// Parse returns the posture named s.
func Parse(s string) (Posture, bool) {
	for _, l := range Levels {
		if string(l.ID) == s {
			return l.ID, true
		}
	}
	return "", false
}

// Severity is the severity the inbox publishes for an incident of category
// with the declared severity. Only security findings change; an unknown
// posture changes nothing.
func (p Posture) Severity(category, declared string) string {
	if category != integration.CategorySecurity {
		return declared
	}
	switch p {
	case Relaxed:
		return integration.SeverityInfo
	case Strict:
		return integration.SeverityAttention
	}
	return declared
}

// ErrInvalid is an unknown posture.
var ErrInvalid = errors.New("security: unknown posture")

// Service reads and changes the posture and tells subscribers about changes.
type Service struct {
	st  *store.Store
	log *slog.Logger
	now func() time.Time

	set    sync.Mutex // serialises Set: the row and the subscribers change in order
	mu     sync.Mutex
	cur    Posture
	stepUp auth.StepUpMode
	subs   []func(Posture)
}

// New loads the posture and the step-up rule. An unreadable or unknown row
// reads as its default.
func New(ctx context.Context, st *store.Store, log *slog.Logger) (*Service, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{st: st, log: log, now: time.Now, cur: Default, stepUp: auth.DefaultStepUpMode}
	if v, ok, err := s.load(ctx, SettingsKey); err != nil {
		return nil, fmt.Errorf("security: reading the posture: %w", err)
	} else if ok {
		if p, known := Parse(v); known {
			s.cur = p
		} else {
			log.Warn("security: unknown posture; using the default", "value", v, "default", Default)
		}
	}
	if v, ok, err := s.load(ctx, StepUpKey); err != nil {
		return nil, fmt.Errorf("security: reading the step-up rule: %w", err)
	} else if ok {
		if m, known := auth.ParseStepUpMode(v); known {
			s.stepUp = m
		} else {
			log.Warn("security: unknown step-up rule; using the default", "value", v, "default", auth.DefaultStepUpMode)
		}
	}
	return s, nil
}

// load reads a settings row holding a JSON string. A row that isn't one
// reads as the string "?", which no setting accepts.
func (s *Service) load(ctx context.Context, key string) (string, bool, error) {
	var raw string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	var v string
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw, true, nil
	}
	return v, true, nil
}

func (s *Service) save(ctx context.Context, key, value string) error {
	b, _ := json.Marshal(value)
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(b), s.now().UnixMilli())
	return err
}

// StepUpKey is the settings row the step-up rule lives in, a JSON string.
const StepUpKey = "security.step_up"

// StepUpModes describes the step-up rules for the settings page.
var StepUpModes = []StepUpLevel{
	{ID: auth.StepUpPasskeyOrPassword, Label: "Passkey, or password if no passkey", Description: "Destructive actions ask for a passkey. Accounts without one re-enter their password.", Default: true},
	{ID: auth.StepUpPasskeyOnly, Label: "Passkey only", Description: "Destructive actions need a passkey. Accounts without one can't run them until they add one."},
}

// StepUpLevel describes a step-up rule.
type StepUpLevel struct {
	ID          auth.StepUpMode `json:"id"`
	Label       string          `json:"label"`
	Description string          `json:"description"`
	Default     bool            `json:"default,omitempty"`
}

// StepUp is the current step-up rule; auth.Service.SetStepUpMode reads it.
func (s *Service) StepUp() auth.StepUpMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stepUp
}

// SetStepUp stores the step-up rule. Whether the caller may change it (a
// T3 change, so it needs a step-up) is decided by the handler, through authz.
func (s *Service) SetStepUp(ctx context.Context, m auth.StepUpMode) error {
	if _, ok := auth.ParseStepUpMode(string(m)); !ok {
		return fmt.Errorf("%w %q", ErrInvalid, m)
	}
	s.set.Lock()
	defer s.set.Unlock()
	if err := s.save(ctx, StepUpKey, string(m)); err != nil {
		return fmt.Errorf("security: saving the step-up rule: %w", err)
	}
	s.mu.Lock()
	changed := s.stepUp != m
	s.stepUp = m
	s.mu.Unlock()
	if changed {
		s.log.Warn("step-up rule changed", "step_up", m)
	}
	return nil
}

// Posture is the current posture.
func (s *Service) Posture() Posture {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// OnChange registers f, called with the new posture after each change, in
// the order changes happen.
func (s *Service) OnChange(f func(Posture)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = append(s.subs, f)
}

// Set stores p and applies it: subscribers re-publish the open findings
// before Set returns.
func (s *Service) Set(ctx context.Context, p Posture) error {
	if _, ok := Parse(string(p)); !ok {
		return fmt.Errorf("%w %q", ErrInvalid, p)
	}
	s.set.Lock()
	defer s.set.Unlock()
	if err := s.save(ctx, SettingsKey, string(p)); err != nil {
		return fmt.Errorf("security: saving the posture: %w", err)
	}
	s.mu.Lock()
	changed := s.cur != p
	s.cur = p
	subs := append([]func(Posture){}, s.subs...)
	s.mu.Unlock()
	if changed {
		s.log.Info("security posture changed", "posture", p)
		for _, f := range subs {
			f(p)
		}
	}
	return nil
}

// View is the API's answer.
type View struct {
	Posture     Posture         `json:"posture"`
	Levels      []Level         `json:"levels"`
	StepUp      auth.StepUpMode `json:"step_up"`
	StepUpModes []StepUpLevel   `json:"step_up_modes"`
}

func (s *Service) view() View {
	return View{Posture: s.Posture(), Levels: Levels, StepUp: s.StepUp(), StepUpModes: StepUpModes}
}

// Register mounts the routes:
//
//	GET /api/settings/security   {posture, levels, step_up, step_up_modes}       (read wraps it)
//	PUT /api/settings/security   {posture?, step_up?} → the same; 422 for an     (write wraps it)
//	                             unknown value; changing step_up needs a step-up
func (s *Service) Register(mux *http.ServeMux, read, write func(http.Handler) http.Handler) {
	mux.Handle("GET /api/settings/security", read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, s.view())
	})))
	mux.Handle("PUT /api/settings/security", write(http.HandlerFunc(s.handlePut)))
}

func (s *Service) handlePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Posture *string `json:"posture"`
		StepUp  *string `json:"step_up"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Posture == nil && req.StepUp == nil {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", "send posture, step_up or both")
		return
	}
	// Check everything before changing anything.
	if req.Posture != nil {
		if _, ok := Parse(*req.Posture); !ok {
			httpx.Error(w, http.StatusUnprocessableEntity, "invalid", fmt.Sprintf("%q isn't a security level: use relaxed, standard or strict", *req.Posture))
			return
		}
	}
	if req.StepUp != nil {
		m, ok := auth.ParseStepUpMode(*req.StepUp)
		if !ok {
			httpx.Error(w, http.StatusUnprocessableEntity, "invalid", fmt.Sprintf("%q isn't a step-up rule: use passkey_or_password or passkey_only", *req.StepUp))
			return
		}
		if m != s.StepUp() {
			// Changing how people confirm destructive actions is itself one (T3).
			actor, _ := authz.ActorFrom(r)
			err := authz.Decide(actor, authz.Request{Scope: "settings.security.update", Tier: authz.T3}, authz.Env{Now: s.now()})
			if authz.WriteRefusal(w, err) {
				return
			}
		}
	}
	if req.Posture != nil {
		if err := s.Set(r.Context(), Posture(*req.Posture)); err != nil {
			s.log.Error("security: saving the posture failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
	}
	if req.StepUp != nil {
		if err := s.SetStepUp(r.Context(), auth.StepUpMode(*req.StepUp)); err != nil {
			s.log.Error("security: saving the step-up rule failed", "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, s.view())
}
