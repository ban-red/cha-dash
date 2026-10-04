package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
)

// Sign-in flows (integration.Authorizer): the hub starts one, the browser
// opens its link and polls, and the secrets it yields go back to the form
// being filled in. Nothing is stored: flows live in memory, and a finished
// flow forgets its secrets as it hands them over.
const (
	authDefaultTTL = 15 * time.Minute
	authMaxTTL     = 30 * time.Minute
	authMaxPending = 32
	authPollEvery  = 2 * time.Second  // what the browser is told
	authCallLimit  = 15 * time.Second // per call to the source
	clientIDKey    = "integrations/client_id"
)

// authMinPoll is the least time between two calls to the source for one
// flow; polls in between answer from the last result. A var for tests.
var authMinPoll = time.Second

// Sign-in statuses.
const (
	AuthPending = "pending"
	AuthDone    = "done"
	AuthExpired = "expired"
	AuthFailed  = "failed"
)

// Errors the sign-in routes map to statuses.
var (
	ErrNoSignIn   = errors.New("runtime: this integration has no sign-in flow")
	ErrNoSuchAuth = errors.New("runtime: no such sign-in")
)

// AuthStart is a started sign-in, as POST /api/integrations/authorize/{integration}
// returns it: open URL, show Code if there is one, poll every IntervalS.
type AuthStart struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	IntervalS int       `json:"interval_s"`
}

// AuthStatus is a poll's answer. Secrets appear once, with status done.
type AuthStatus struct {
	Status    string             `json:"status"`
	ExpiresAt *time.Time         `json:"expires_at,omitempty"`
	Secrets   map[string]string  `json:"secrets,omitempty"`
	Error     *httpx.ErrorDetail `json:"error,omitempty"`
}

// authState is the Manager's sign-in bookkeeping; the zero value is ready.
type authState struct {
	mu       sync.Mutex
	flows    map[string]*authFlow
	clientID string
}

type authFlow struct {
	integ   string
	a       integration.Authorization
	expires time.Time

	mu       sync.Mutex // serialises polls
	lastPoll time.Time
	lastErr  *httpx.ErrorDetail
}

func (s *authState) get(id string) *authFlow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flows[id]
}

func (s *authState) drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.flows, id)
}

// add keeps f, first dropping expired flows and, when full, the one closest
// to expiring.
func (s *authState) add(f *authFlow, now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flows == nil {
		s.flows = map[string]*authFlow{}
	}
	for id, g := range s.flows {
		if !now.Before(g.expires) {
			delete(s.flows, id)
		}
	}
	for len(s.flows) >= authMaxPending {
		var oldest string
		for id, g := range s.flows {
			if oldest == "" || g.expires.Before(s.flows[oldest].expires) {
				oldest = id
			}
		}
		delete(s.flows, oldest)
	}
	id := "au_" + strings.ToLower(rand.Text())
	s.flows[id] = f
	return id
}

// clientIDFor is the identifier the hub shows integ's source: derived from a
// random hub ID kept in settings, so it's stable across restarts, and
// different per integration, so two sources can't match it up.
func (m *Manager) clientIDFor(ctx context.Context, integ string) (string, error) {
	m.auth.mu.Lock()
	defer m.auth.mu.Unlock()
	if m.auth.clientID == "" {
		id, err := loadClientID(ctx, m.o.Store)
		if err != nil {
			return "", err
		}
		m.auth.clientID = id
	}
	sum := sha256.Sum256([]byte(m.auth.clientID + "/" + integ))
	return "chadash-" + hex.EncodeToString(sum[:16]), nil
}

func loadClientID(ctx context.Context, st *store.Store) (string, error) {
	fresh, _ := json.Marshal(strings.ToLower(rand.Text()))
	var raw string
	err := st.WriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`,
			clientIDKey, string(fresh), store.NowMillis()); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, clientIDKey).Scan(&raw)
	})
	if err != nil {
		return "", fmt.Errorf("runtime: the hub's client id: %w", err)
	}
	var id string
	if err := json.Unmarshal([]byte(raw), &id); err != nil || id == "" {
		return "", fmt.Errorf("runtime: the hub's client id isn't a string: %q", raw)
	}
	return id, nil
}

// authorizer returns the integration's sign-in flow.
func (m *Manager) authorizer(id string) (integration.Authorizer, integration.Manifest, error) {
	integ, man, ok := m.reg.Get(id)
	if !ok {
		return nil, man, ErrUnknownIntegration
	}
	az, ok := integ.(integration.Authorizer)
	if !ok {
		return nil, man, ErrNoSignIn
	}
	return az, man, nil
}

// authDeps builds what a flow gets: a client that always verifies TLS
// against the system roots, whatever any form says.
func (m *Manager) authDeps(ctx context.Context, integ string) (integration.AuthDeps, func(), error) {
	cid, err := m.clientIDFor(ctx, integ)
	if err != nil {
		return integration.AuthDeps{}, nil, err
	}
	c, err := NewHTTPClient(ClientOptions{
		HTTPOptions: integration.HTTPOptions{TLS: integration.TLSPolicy{Mode: integration.TLSVerify}, Timeout: authCallLimit},
		UserAgent:   m.o.UserAgent, RootCAs: m.o.RootCAs,
	})
	if err != nil {
		return integration.AuthDeps{}, nil, err
	}
	d := integration.AuthDeps{HTTP: c.Client, ClientID: cid, Product: "Cha Dash", Now: m.o.Now,
		Log: m.log.With("integration", integ, "sign_in", true)}
	return d, func() { c.Close(context.Background()) }, nil
}

// upstreamError is a sign-in the source refused or couldn't serve.
type upstreamError struct {
	code, msg string
}

func (e *upstreamError) Error() string { return e.msg }

// BeginAuth starts a sign-in with integ's source.
func (m *Manager) BeginAuth(ctx context.Context, integ string) (AuthStart, error) {
	az, _, err := m.authorizer(integ)
	if err != nil {
		return AuthStart{}, err
	}
	d, release, err := m.authDeps(ctx, integ)
	if err != nil {
		return AuthStart{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, authCallLimit)
	defer cancel()
	a, err := az.BeginAuth(ctx, d)
	if err != nil {
		code, _, msg := integration.Classify(err)
		m.log.Warn("starting a sign-in failed", "integration", integ, "code", code, "err", msg)
		return AuthStart{}, &upstreamError{code: code, msg: msg}
	}
	if u, perr := url.Parse(a.URL); perr != nil || u.Scheme != "https" || u.Host == "" {
		return AuthStart{}, fmt.Errorf("runtime: %s's sign-in link isn't an https URL", integ)
	}
	now := m.o.Now()
	exp := a.Expires
	if !exp.After(now) {
		exp = now.Add(authDefaultTTL)
	}
	if limit := now.Add(authMaxTTL); exp.After(limit) {
		exp = limit
	}
	id := m.auth.add(&authFlow{integ: integ, a: a, expires: exp}, now)
	m.log.Info("sign-in started", "integration", integ)
	return AuthStart{ID: id, Code: a.Code, URL: a.URL, ExpiresAt: exp.UTC().Truncate(time.Second), IntervalS: int(authPollEvery.Seconds())}, nil
}

// PollAuth checks a sign-in. Done hands over the secrets and forgets the
// flow, so they're handed over once.
func (m *Manager) PollAuth(ctx context.Context, integ, id string) (AuthStatus, error) {
	f := m.auth.get(id)
	if f == nil || f.integ != integ {
		return AuthStatus{}, ErrNoSuchAuth
	}
	az, man, err := m.authorizer(integ)
	if err != nil {
		return AuthStatus{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.auth.get(id) != f {
		return AuthStatus{}, ErrNoSuchAuth // a concurrent poll finished it
	}
	now := m.o.Now()
	exp := f.expires.UTC().Truncate(time.Second)
	pending := func() AuthStatus { return AuthStatus{Status: AuthPending, ExpiresAt: &exp, Error: f.lastErr} }
	if !now.Before(f.expires) {
		m.auth.drop(id)
		return AuthStatus{Status: AuthExpired}, nil
	}
	if !f.lastPoll.IsZero() && now.Sub(f.lastPoll) < authMinPoll {
		return pending(), nil
	}
	f.lastPoll = now

	d, release, err := m.authDeps(ctx, integ)
	if err != nil {
		return AuthStatus{}, err
	}
	defer release()
	cctx, cancel := context.WithTimeout(ctx, authCallLimit)
	defer cancel()
	secrets, done, err := az.PollAuth(cctx, d, f.a)
	switch {
	case errors.Is(err, integration.ErrAuthExpired):
		m.auth.drop(id)
		return AuthStatus{Status: AuthExpired}, nil
	case err != nil:
		code, class, msg := integration.Classify(err)
		e := &httpx.ErrorDetail{Code: code, Message: msg}
		if class == integration.Transient {
			f.lastErr = e
			return pending(), nil
		}
		m.auth.drop(id)
		m.log.Warn("sign-in failed", "integration", integ, "code", code, "err", msg)
		return AuthStatus{Status: AuthFailed, Error: e}, nil
	case !done:
		f.lastErr = nil
		return pending(), nil
	}
	m.auth.drop(id)
	out := map[string]string{}
	for k, v := range secrets {
		if man.IsSecret(k) && v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return AuthStatus{Status: AuthFailed, Error: &httpx.ErrorDetail{Code: integration.CodeUpstream,
			Message: "the sign-in finished without a credential; try again, or paste one"}}, nil
	}
	m.log.Info("sign-in finished", "integration", integ)
	return AuthStatus{Status: AuthDone, Secrets: out}, nil
}

func (m *Manager) handleAuthBegin(w http.ResponseWriter, r *http.Request) {
	res, err := m.BeginAuth(r.Context(), r.PathValue("integration"))
	if err != nil {
		m.writeAuthError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (m *Manager) handleAuthPoll(w http.ResponseWriter, r *http.Request) {
	res, err := m.PollAuth(r.Context(), r.PathValue("integration"), r.PathValue("id"))
	if err != nil {
		m.writeAuthError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (m *Manager) writeAuthError(w http.ResponseWriter, err error) {
	var ue *upstreamError
	switch {
	case errors.As(err, &ue):
		httpx.Error(w, http.StatusBadGateway, ue.code, ue.msg)
	case errors.Is(err, ErrNoSignIn):
		httpx.Error(w, http.StatusUnprocessableEntity, "no_sign_in", "this integration has no sign-in flow; paste the credential instead")
	case errors.Is(err, ErrNoSuchAuth):
		httpx.Error(w, http.StatusNotFound, "not_found", "no such sign-in; it may have finished or expired")
	default:
		m.writeError(w, err)
	}
}
