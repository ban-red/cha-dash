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

	set  sync.Mutex // serialises Set: the row and the subscribers change in order
	mu   sync.Mutex
	cur  Posture
	subs []func(Posture)
}

// New loads the posture. An unreadable or unknown row reads as Default.
func New(ctx context.Context, st *store.Store, log *slog.Logger) (*Service, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{st: st, log: log, now: time.Now, cur: Default}
	var raw string
	err := st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, SettingsKey).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("security: reading the posture: %w", err)
	default:
		var v string
		p, ok := Posture(""), false
		if json.Unmarshal([]byte(raw), &v) == nil {
			p, ok = Parse(v)
		}
		if !ok {
			log.Warn("security: unknown posture; using the default", "value", raw, "default", Default)
			break
		}
		s.cur = p
	}
	return s, nil
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
	b, _ := json.Marshal(string(p))
	if _, err := s.st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		SettingsKey, string(b), s.now().UnixMilli()); err != nil {
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
	Posture Posture `json:"posture"`
	Levels  []Level `json:"levels"`
}

func (s *Service) view() View { return View{Posture: s.Posture(), Levels: Levels} }

// Register mounts the routes:
//
//	GET /api/settings/security   {posture, levels: [{id, label, description, default?}]}  (read wraps it)
//	PUT /api/settings/security   {posture} → the same; 422 for an unknown level           (write wraps it)
func (s *Service) Register(mux *http.ServeMux, read, write func(http.Handler) http.Handler) {
	mux.Handle("GET /api/settings/security", read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, s.view())
	})))
	mux.Handle("PUT /api/settings/security", write(http.HandlerFunc(s.handlePut)))
}

func (s *Service) handlePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Posture string `json:"posture"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	err := s.Set(r.Context(), Posture(req.Posture))
	switch {
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", fmt.Sprintf("%q isn't a security level: use relaxed, standard or strict", req.Posture))
	case err != nil:
		s.log.Error("security: saving the posture failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
	default:
		httpx.WriteJSON(w, http.StatusOK, s.view())
	}
}
