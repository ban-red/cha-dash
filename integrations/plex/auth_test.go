package plex

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/runtime"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/vault"
)

// plextv is a fake plex.tv over TLS: it creates the fixture's PIN, and
// hands over the token once approved.
type plextv struct {
	*httptest.Server
	dir string

	mu       sync.Mutex
	approved bool
	requests []*http.Request
}

func newPlexTV(t *testing.T) *plextv {
	p := &plextv{dir: fixture.Dir(t, ID)}
	p.Server = httptest.NewUnstartedServer(http.HandlerFunc(p.serve))
	p.Config.ErrorLog = log.New(io.Discard, "", 0) // the untrusted-client test logs a handshake error
	p.StartTLS()
	t.Cleanup(p.Close)
	old := plexTV
	plexTV = p.URL
	t.Cleanup(func() { plexTV = old })
	return p
}

func (p *plextv) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, r.Clone(context.Background()))
	w.Header().Set("Content-Type", "application/json")
	file := ""
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/v2/pins" && r.URL.Query().Get("strong") == "true":
		w.WriteHeader(http.StatusCreated)
		file = "pin_created.json"
	case r.Method == "GET" && r.URL.Path == "/api/v2/pins/564964751" && r.URL.Query().Get("code") == "syntheticstrongpincode001":
		file = "pin_waiting.json"
		if p.approved {
			file = "pin_approved.json"
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"errors":[{"code":1020,"message":"Code not found or expired"}]}`)
		return
	}
	b, _ := os.ReadFile(filepath.Join(p.dir, file))
	w.Write(b)
}

func (p *plextv) approve() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.approved = true
}

func (p *plextv) last() *http.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[len(p.requests)-1]
}

// deps is what the runtime hands a sign-in: a verifying client, trusting
// the fake's certificate only because roots says so.
func deps(t *testing.T, roots *x509.CertPool) integration.AuthDeps {
	c, err := runtime.NewHTTPClient(runtime.ClientOptions{HTTPOptions: integration.HTTPOptions{TLS: integration.TLSPolicy{Mode: integration.TLSVerify}}, RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return integration.AuthDeps{HTTP: c.Client, ClientID: "chadash-0123456789abcdef0123456789abcdef", Product: "Cha Dash",
		Now: func() time.Time { return time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC) }, Log: slog.New(slog.DiscardHandler)}
}

func pool(p *plextv) *x509.CertPool {
	roots := x509.NewCertPool()
	roots.AddCert(p.Certificate())
	return roots
}

func TestPINSignIn(t *testing.T) {
	p := newPlexTV(t)
	d := deps(t, pool(p))
	a, err := Integration{}.BeginAuth(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "https://app.plex.tv/auth#?clientID=chadash-0123456789abcdef0123456789abcdef&code=syntheticstrongpincode001&context%5Bdevice%5D%5Bproduct%5D=Cha%20Dash"
	if a.Code != "syntheticstrongpincode001" || a.URL != wantURL || a.State != "564964751:syntheticstrongpincode001" ||
		!a.Expires.Equal(time.Date(2026, 10, 3, 14, 35, 0, 0, time.UTC)) {
		t.Fatalf("authorization = %+v", a)
	}
	req := p.last()
	if req.Header.Get("X-Plex-Client-Identifier") != d.ClientID || req.Header.Get("X-Plex-Product") != "Cha Dash" || req.Header.Get("Accept") != "application/json" {
		t.Fatalf("pin request headers = %v", req.Header)
	}

	secrets, done, err := Integration{}.PollAuth(t.Context(), d, a)
	if err != nil || done || secrets != nil {
		t.Fatalf("waiting: %v %v %v", secrets, done, err)
	}
	if req := p.last(); req.Header.Get("X-Plex-Client-Identifier") != d.ClientID {
		t.Fatalf("poll headers = %v", req.Header)
	}
	p.approve()
	secrets, done, err = Integration{}.PollAuth(t.Context(), d, a)
	if err != nil || !done || len(secrets) != 1 || secrets["token"] != token {
		t.Fatalf("approved: %v %v %v", secrets, done, err)
	}

	// A PIN plex.tv no longer knows has expired.
	_, _, err = Integration{}.PollAuth(t.Context(), d, integration.Authorization{State: "1:gone"})
	if !errors.Is(err, integration.ErrAuthExpired) {
		t.Fatalf("unknown pin: %v", err)
	}
}

func TestPINVerifiesTLS(t *testing.T) {
	p := newPlexTV(t)
	// The runtime's client with the system roots doesn't trust the fake.
	_, err := Integration{}.BeginAuth(t.Context(), deps(t, nil))
	if code, _, _ := integration.Classify(err); code != integration.CodeTLSUntrusted {
		t.Fatalf("untrusted plex.tv: %v", err)
	}
	// And plain http is refused before anything is sent.
	plexTV = strings.Replace(p.URL, "https://", "http://", 1)
	if _, err := (Integration{}).BeginAuth(t.Context(), deps(t, pool(p))); !errors.Is(err, errNotHTTPS) {
		t.Fatalf("http plex.tv: %v", err)
	}
}

// TestSignInThroughTheHub runs the whole flow through the runtime's routes:
// start, poll while waiting, approve, and the token comes back once.
func TestSignInThroughTheHub(t *testing.T) {
	p := newPlexTV(t)
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	rand.Read(key)
	v, err := vault.New(st, key)
	if err != nil {
		t.Fatal(err)
	}
	reg := integration.NewRegistry()
	if err := reg.Register(Integration{}); err != nil {
		t.Fatal(err)
	}
	m, err := runtime.New(runtime.Options{Store: st, Vault: v, Registry: reg, Topics: topics.New(graph.New(), time.Now()), RootCAs: pool(p)})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.Register(mux, func(h http.Handler) http.Handler { return h })
	hub := httptest.NewServer(mux)
	t.Cleanup(hub.Close)
	call := func(method, path string, dst any) int {
		t.Helper()
		req, _ := http.NewRequest(method, hub.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}

	var start runtime.AuthStart
	if code := call("POST", "/api/integrations/authorize/plex", &start); code != 200 || !strings.HasPrefix(start.URL, "https://app.plex.tv/auth#?clientID=chadash-") || start.Code == "" {
		t.Fatalf("start: %d %+v", code, start)
	}
	if id := p.last().Header.Get("X-Plex-Client-Identifier"); !strings.Contains(start.URL, "clientID="+id+"&") {
		t.Fatalf("the link and the PIN should carry the same client id (%s): %s", id, start.URL)
	}
	var st1 runtime.AuthStatus
	if call("GET", "/api/integrations/authorize/plex/"+start.ID, &st1); st1.Status != runtime.AuthPending {
		t.Fatalf("pending: %+v", st1)
	}
	p.approve()
	var st2 runtime.AuthStatus
	for deadline := time.Now().Add(5 * time.Second); st2.Status != runtime.AuthDone; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never done: %+v", st2)
		}
		call("GET", "/api/integrations/authorize/plex/"+start.ID, &st2) // polls closer than 1 s answer from the last result
	}
	if st2.Secrets["token"] != token || len(st2.Secrets) != 1 {
		t.Fatalf("done: %+v", st2)
	}
	var gone map[string]any
	if code := call("GET", "/api/integrations/authorize/plex/"+start.ID, &gone); code != 404 {
		t.Fatalf("handed over twice: %d %v", code, gone)
	}
}
