package runtime

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
)

// pinny is a fake integration with a PIN sign-in, like Plex's.
type pinny struct {
	mu        sync.Mutex
	clientIDs []string
	polls     int
	approve   bool
	pollErr   error
	beginErr  error
	link      string
	ttl       time.Duration
}

const signInToken = "tok-from-sign-in-0001"

func (p *pinny) Manifest() integration.Manifest {
	tok := integration.Secret("Token")
	tok.Authorize = true
	return integration.Manifest{ID: "pinny", Name: "Pinny", Icon: "pinny", Version: "0.1.0", Tier: integration.Bronze,
		ConfigSchema: integration.Object(integration.URLProp(), integration.TLSProp(), integration.Prop("token", tok)).Require("url", "token")}
}

func (p *pinny) Connect(context.Context, integration.Config, integration.Deps) (integration.Instance, error) {
	return nil, errors.New("not used")
}

func (p *pinny) BeginAuth(_ context.Context, d integration.AuthDeps) (integration.Authorization, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clientIDs = append(p.clientIDs, d.ClientID)
	if p.beginErr != nil {
		return integration.Authorization{}, p.beginErr
	}
	if d.HTTP == nil || d.Product != "Cha Dash" {
		return integration.Authorization{}, errors.New("deps missing")
	}
	link, ttl := p.link, p.ttl
	if link == "" {
		link = "https://auth.lab.example/link#code=PIN1"
	}
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	return integration.Authorization{Code: "PIN1", URL: link, Expires: d.Now().Add(ttl), State: "state-1"}, nil
}

func (p *pinny) PollAuth(_ context.Context, _ integration.AuthDeps, a integration.Authorization) (map[string]string, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polls++
	switch {
	case a.State != "state-1":
		return nil, false, errors.New("lost the state")
	case p.pollErr != nil:
		return nil, false, p.pollErr
	case !p.approve:
		return nil, false, nil
	}
	// url isn't a secret field: the hub must not hand it over.
	return map[string]string{"token": signInToken, "url": "https://not-a-secret.lab.example"}, true, nil
}

func (p *pinny) set(f func(p *pinny)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func (p *pinny) pollCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.polls
}

func noThrottle(t *testing.T) {
	old := authMinPoll
	authMinPoll = 0
	t.Cleanup(func() { authMinPoll = old })
}

func TestAuthorizeFlow(t *testing.T) {
	noThrottle(t)
	p := &pinny{}
	e := newEnv(t, p)

	code, body := e.call(t, "POST", "/api/integrations/authorize/pinny", nil)
	start := decodeAs[AuthStart](t, body)
	if code != 200 || !strings.HasPrefix(start.ID, "au_") || start.Code != "PIN1" || start.URL != "https://auth.lab.example/link#code=PIN1" ||
		start.IntervalS != 2 || start.ExpiresAt.Before(time.Now().Add(9*time.Minute)) {
		t.Fatalf("begin: %d %s", code, body)
	}
	if strings.Contains(body, "state-1") {
		t.Fatalf("the integration's state left the hub: %s", body)
	}
	poll := "/api/integrations/authorize/pinny/" + start.ID

	code, body = e.call(t, "GET", poll, nil)
	if st := decodeAs[AuthStatus](t, body); code != 200 || st.Status != AuthPending || st.ExpiresAt == nil || st.Secrets != nil {
		t.Fatalf("pending: %d %s", code, body)
	}
	// The user approves: the secrets come back once, write-only fields only.
	p.set(func(p *pinny) { p.approve = true })
	code, body = e.call(t, "GET", poll, nil)
	st := decodeAs[AuthStatus](t, body)
	if code != 200 || st.Status != AuthDone || len(st.Secrets) != 1 || st.Secrets["token"] != signInToken {
		t.Fatalf("done: %d %s", code, body)
	}
	if code, body := e.call(t, "GET", poll, nil); code != 404 || !strings.Contains(body, `"not_found"`) {
		t.Fatalf("handed over twice: %d %s", code, body)
	}
	// Nothing was stored, and the token was never logged.
	if names, _ := e.v.List(t.Context()); len(names) != 0 {
		t.Fatalf("vault = %v", names)
	}
	if strings.Contains(e.logs.String(), signInToken) || !strings.Contains(e.logs.String(), "sign-in finished") {
		t.Fatalf("logs: %s", e.logs.String())
	}

	// The client id is stable (kept in settings) and namespaced.
	e.call(t, "POST", "/api/integrations/authorize/pinny", nil)
	e.m.auth.mu.Lock()
	e.m.auth.clientID = "" // as after a restart
	e.m.auth.mu.Unlock()
	e.call(t, "POST", "/api/integrations/authorize/pinny", nil)
	p.mu.Lock()
	ids := p.clientIDs
	p.mu.Unlock()
	if len(ids) != 3 || ids[0] != ids[1] || ids[1] != ids[2] || !strings.HasPrefix(ids[0], "chadash-") || len(ids[0]) != len("chadash-")+32 {
		t.Fatalf("client ids = %v", ids)
	}
	var raw string
	if err := e.st.Reader().QueryRow(`SELECT value FROM settings WHERE key = ?`, clientIDKey).Scan(&raw); err != nil || strings.Contains(ids[0], strings.Trim(raw, `"`)) {
		t.Fatalf("settings row %q (%v): the hub id itself shouldn't be shown to sources", raw, err)
	}
}

func TestAuthorizeErrors(t *testing.T) {
	noThrottle(t)
	p := &pinny{}
	e := newEnv(t, p)
	begin := func() string {
		t.Helper()
		code, body := e.call(t, "POST", "/api/integrations/authorize/pinny", nil)
		if code != 200 {
			t.Fatalf("begin: %d %s", code, body)
		}
		return decodeAs[AuthStart](t, body).ID
	}

	for _, c := range []struct{ path, want string }{
		{"/api/integrations/authorize/nope", `"unknown_integration"`},
		{"/api/integrations/authorize/echo", `"no_sign_in"`},
	} {
		if code, body := e.call(t, "POST", c.path, nil); code != 422 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s", c.path, code, body)
		}
	}
	id := begin()
	for _, path := range []string{"/api/integrations/authorize/echo/" + id, "/api/integrations/authorize/pinny/au_nope"} {
		if code, body := e.call(t, "GET", path, nil); code != 404 && code != 422 {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}

	// A transient failure keeps it pending, with the error; a repair-class
	// one ends it.
	p.set(func(p *pinny) { p.pollErr = &net.OpError{Op: "dial", Err: errors.New("connection refused")} })
	_, body := e.call(t, "GET", "/api/integrations/authorize/pinny/"+id, nil)
	if st := decodeAs[AuthStatus](t, body); st.Status != AuthPending || st.Error == nil || st.Error.Code != integration.CodeUnreachable {
		t.Fatalf("transient: %s", body)
	}
	p.set(func(p *pinny) { p.pollErr = &integration.AuthError{Message: "the source refused Cha Dash"} })
	_, body = e.call(t, "GET", "/api/integrations/authorize/pinny/"+id, nil)
	if st := decodeAs[AuthStatus](t, body); st.Status != AuthFailed || st.Error.Code != integration.CodeAuth || st.Error.Message != "the source refused Cha Dash" {
		t.Fatalf("failed: %s", body)
	}
	if code, _ := e.call(t, "GET", "/api/integrations/authorize/pinny/"+id, nil); code != 404 {
		t.Fatalf("a failed sign-in is forgotten: %d", code)
	}

	// The source says it expired.
	id = begin()
	p.set(func(p *pinny) { p.pollErr = integration.ErrAuthExpired })
	if _, body := e.call(t, "GET", "/api/integrations/authorize/pinny/"+id, nil); decodeAs[AuthStatus](t, body).Status != AuthExpired {
		t.Fatalf("expired by the source: %s", body)
	}
	// The hub's own clock says it expired, without asking the source.
	p.set(func(p *pinny) { p.pollErr, p.ttl, p.polls = nil, 50*time.Millisecond, 0 })
	id = begin()
	time.Sleep(60 * time.Millisecond)
	if _, body := e.call(t, "GET", "/api/integrations/authorize/pinny/"+id, nil); decodeAs[AuthStatus](t, body).Status != AuthExpired || p.pollCount() != 0 {
		t.Fatalf("expired by the clock: %s (%d polls)", body, p.pollCount())
	}

	// Polls closer together than authMinPoll answer from the last result.
	p.set(func(p *pinny) { p.ttl = 0 })
	id = begin()
	authMinPoll = time.Hour
	for range 3 {
		e.call(t, "GET", "/api/integrations/authorize/pinny/"+id, nil)
	}
	if n := p.pollCount(); n != 1 {
		t.Fatalf("%d polls reached the source", n)
	}

	// The source is down, or hands back a link the browser shouldn't open.
	p.set(func(p *pinny) { p.beginErr = &net.OpError{Op: "dial", Err: errors.New("connection refused")} })
	if code, body := e.call(t, "POST", "/api/integrations/authorize/pinny", nil); code != 502 || !strings.Contains(body, `"unreachable"`) {
		t.Fatalf("source down: %d %s", code, body)
	}
	p.set(func(p *pinny) { p.beginErr, p.link = nil, "javascript:alert(1)" })
	if code, _ := e.call(t, "POST", "/api/integrations/authorize/pinny", nil); code != 500 {
		t.Fatalf("a non-https link: %d", code)
	}
}

func TestAuthorizeBounded(t *testing.T) {
	p := &pinny{}
	e := newEnv(t, p)
	var first string
	for i := range authMaxPending + 5 {
		s, err := e.m.BeginAuth(t.Context(), "pinny")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = s.ID
		}
	}
	e.m.auth.mu.Lock()
	n := len(e.m.auth.flows)
	e.m.auth.mu.Unlock()
	if n != authMaxPending {
		t.Fatalf("%d sign-ins kept", n)
	}
	if _, err := e.m.PollAuth(t.Context(), "pinny", first); !errors.Is(err, ErrNoSuchAuth) {
		t.Fatalf("the oldest should make room: %v", err)
	}
}
