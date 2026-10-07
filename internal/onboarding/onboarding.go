// Package onboarding serves the first-run wizard, "Connect your home": where
// the owner is in it, kept server-side so it shows once per hub, and the
// discovery that finds what to connect.
//
//	GET  /api/onboarding            {status, step, show, sidecar, updated_at}
//	PUT  /api/onboarding            {status: started|skipped|done, step?} → the same
//	POST /api/onboarding/discover   {sidecar?, docker?, hosts?} → {found, checked}
//
// status is new (never opened), started (open, or closed to finish later),
// skipped or done. show says whether the web app opens the wizard on its own:
// when it was started and not finished, or on a new hub that has no
// integrations yet (outside demo mode).
//
// Discovery only probes what the owner confirmed (Docker endpoints and
// machines they list), plus the socket proxy the shipped compose.yaml runs
// beside the hub. Every request is a GET; it never scans a network. See
// discover.go.
package onboarding

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/store"
)

// SettingsKey is the settings row the wizard's state lives in.
const SettingsKey = "onboarding"

// SidecarEndpoint is the socket proxy deploy/compose.yaml runs beside the
// hub, by its compose service name. It's the one address discovery may probe
// without asking: it exists only on the hub's own compose network.
const SidecarEndpoint = "tcp://socket-proxy:2375"

// Statuses.
const (
	StatusNew     = "new"
	StatusStarted = "started"
	StatusSkipped = "skipped"
	StatusDone    = "done"
)

// Integrations is what the wizard needs from the runtime: a connection test
// (for Docker endpoints) and the instances there are.
type Integrations interface {
	Test(ctx context.Context, req runtime.TestRequest) (integration.TestResult, error)
	List() []runtime.InstanceView
}

// Options configure a Service.
type Options struct {
	Store        *store.Store
	Integrations Integrations
	Registry     *integration.Registry // nil: integration.Default
	Log          *slog.Logger
	// Demo mode: a new hub doesn't open the wizard on its own, so the Demo
	// Home stays in view.
	Demo bool
	// Sidecar replaces SidecarEndpoint (tests).
	Sidecar string
	// Dial replaces the host probes' dialer (tests).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	Now  func() time.Time
}

// Service serves the wizard's state and discovery.
type Service struct {
	o      Options
	client *http.Client
	// Audit records the wizard's state changes and every discovery (what
	// it was asked to probe). Nil records nothing. Set it before serving.
	Audit *authz.Recorder
}

// New returns a Service.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Integrations == nil {
		return nil, errors.New("onboarding: Store and Integrations are required")
	}
	if o.Registry == nil {
		o.Registry = integration.Default
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Sidecar == "" {
		o.Sidecar = SidecarEndpoint
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o, client: probeClient(o.Dial)}, nil
}

// State is the wizard's state as the API serves it.
type State struct {
	Status    string     `json:"status"`
	Step      string     `json:"step"`
	Show      bool       `json:"show"`
	Sidecar   string     `json:"sidecar"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// stored is the settings row's JSON.
type stored struct {
	Status string `json:"status"`
	Step   string `json:"step,omitempty"`
}

// State reads the wizard's state.
func (s *Service) State(ctx context.Context) (State, error) {
	var (
		raw string
		at  int64
	)
	st := State{Status: StatusNew, Sidecar: s.o.Sidecar}
	err := s.o.Store.Reader().QueryRowContext(ctx, `SELECT value, updated_at FROM settings WHERE key = ?`, SettingsKey).Scan(&raw, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return State{}, err
	default:
		var v stored
		if err := json.Unmarshal([]byte(raw), &v); err != nil || !validStatus(v.Status) {
			s.o.Log.Warn("onboarding: unreadable state; starting over", "value", raw)
			break
		}
		t := time.UnixMilli(at).UTC()
		st.Status, st.Step, st.UpdatedAt = v.Status, v.Step, &t
	}
	switch st.Status {
	case StatusStarted:
		st.Show = true
	case StatusNew:
		st.Show = !s.o.Demo && len(s.o.Integrations.List()) == 0
	}
	return st, nil
}

// Update records where the owner is in the wizard.
type Update struct {
	Status string `json:"status"`
	Step   string `json:"step"`
}

var stepRE = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)

// ErrInvalid is returned for a status or step the wizard doesn't have.
var ErrInvalid = errors.New("onboarding: invalid state")

func validStatus(s string) bool {
	return s == StatusNew || s == StatusStarted || s == StatusSkipped || s == StatusDone
}

// Set stores the wizard's state.
func (s *Service) Set(ctx context.Context, u Update) (State, error) {
	if u.Status == StatusNew || !validStatus(u.Status) {
		return State{}, fmt.Errorf("%w: status %q isn't started, skipped or done", ErrInvalid, u.Status)
	}
	if u.Step != "" && !stepRE.MatchString(u.Step) {
		return State{}, fmt.Errorf("%w: step %q", ErrInvalid, u.Step)
	}
	b, _ := json.Marshal(stored(u))
	if _, err := s.o.Store.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		SettingsKey, string(b), s.o.Now().UnixMilli()); err != nil {
		return State{}, err
	}
	return s.State(ctx)
}

// Register mounts the routes, each wrapped by wrap (the owner check).
func (s *Service) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /api/onboarding", wrap(http.HandlerFunc(s.handleGet)))
	mux.Handle("PUT /api/onboarding", wrap(http.HandlerFunc(s.handlePut)))
	mux.Handle("POST /api/onboarding/discover", wrap(http.HandlerFunc(s.handleDiscover)))
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	st, err := s.State(r.Context())
	if err != nil {
		s.o.Log.Error("onboarding: reading the state failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) handlePut(w http.ResponseWriter, r *http.Request) {
	var u Update
	if !httpx.DecodeJSON(w, r, &u) {
		return
	}
	st, err := s.Set(r.Context(), u)
	switch {
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", err.Error())
	case err != nil:
		s.o.Log.Error("onboarding: saving the state failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
	default:
		s.Audit.Change(r, "settings.onboarding.update", "settings:onboarding", u, authz.T1, nil)
		httpx.WriteJSON(w, http.StatusOK, st)
	}
}

func (s *Service) handleDiscover(w http.ResponseWriter, r *http.Request) {
	var req DiscoverRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	res, err := s.Discover(r.Context(), req)
	if errors.Is(err, ErrInvalid) {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	if err != nil {
		s.o.Log.Error("onboarding: discovery failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	// The hub probed the network on someone's behalf: what it was asked to.
	s.Audit.Change(r, "onboarding.discover", "settings:onboarding", req, authz.T0, nil)
	httpx.WriteJSON(w, http.StatusOK, res)
}
