package runtime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dash.cha.sh/internal/integration"
)

// cert is a generated certificate: self-signed, or signed by parent.
type cert struct {
	x509 *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCert(t *testing.T, cn string, ca bool, parent *cert) cert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"pve.lab.example"},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ca {
		tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.x509, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return cert{x509: c, key: key}
}

// tlsServer serves "ok" over TLS with the given chain (leaf first).
func tlsServer(t *testing.T, chain ...cert) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	tc := tls.Certificate{PrivateKey: chain[0].key, Leaf: chain[0].x509}
	for _, c := range chain {
		tc.Certificate = append(tc.Certificate, c.x509.Raw)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{tc}}
	srv.Config.ErrorLog = quiet // refused handshakes are the point
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

var quiet = log.New(io.Discard, "", 0)

func get(c *Client, url string) error {
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	return err
}

func code(err error) string {
	c, _, _ := integration.Classify(err)
	return c
}

func client(t *testing.T, opts ClientOptions) *Client {
	t.Helper()
	c, err := NewHTTPClient(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(t.Context()) })
	return c
}

func TestTLSVerify(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Config.ErrorLog = quiet
	srv.StartTLS()
	defer srv.Close()
	leaf := integration.Fingerprint(srv.Certificate())

	// Not trusted by the system: refused, but the certificate is reported so
	// the UI can offer to trust it.
	c := client(t, ClientOptions{})
	err := get(c, srv.URL)
	if code(err) != integration.CodeTLSUntrusted {
		t.Fatalf("untrusted: %v (%s)", err, code(err))
	}
	if c.Observed() != leaf {
		t.Fatalf("observed %q, want %q", c.Observed(), leaf)
	}
	_, _, msg := integration.Classify(err)
	if !strings.Contains(msg, leaf) {
		t.Fatalf("message should name the fingerprint: %s", msg)
	}

	// Trusted roots: fine, and still observed.
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	c = client(t, ClientOptions{RootCAs: roots})
	if err := get(c, srv.URL); err != nil {
		t.Fatal(err)
	}
	if c.Observed() != leaf {
		t.Fatal("verify mode should record the leaf")
	}

	// The host is checked: the cert names 127.0.0.1 and example.com only.
	c = client(t, ClientOptions{RootCAs: roots})
	u := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	if err := get(c, u); code(err) != integration.CodeTLSUntrusted {
		t.Fatalf("wrong host: %v (%s)", err, code(err))
	}
}

func TestTLSPin(t *testing.T) {
	a := newCert(t, "pluto", false, nil)
	srv := tlsServer(t, a)
	pin := integration.Fingerprint(a.x509)

	c := client(t, ClientOptions{HTTPOptions: integration.HTTPOptions{TLS: integration.TLSPolicy{Mode: integration.TLSPin, Pin: pin}}})
	if err := get(c, srv.URL); err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("0", 64)
	c = client(t, ClientOptions{HTTPOptions: integration.HTTPOptions{TLS: integration.TLSPolicy{Mode: integration.TLSPin, Pin: other}}})
	err := get(c, srv.URL)
	var te *integration.TLSError
	if !errors.As(err, &te) || te.Code != integration.CodeTLSPinMismatch || te.Fingerprint != pin || te.Expected != other {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := NewHTTPClient(ClientOptions{HTTPOptions: integration.HTTPOptions{TLS: integration.TLSPolicy{Mode: integration.TLSPin}}}); err == nil {
		t.Fatal("pin mode without a pin")
	}
}

func TestTLSPinCA(t *testing.T) {
	ca := newCert(t, "PVE Cluster Manager CA", true, nil)
	leaf := newCert(t, "mars-1", false, &ca)
	pin := integration.Fingerprint(ca.x509)
	opts := ClientOptions{HTTPOptions: integration.HTTPOptions{TLS: integration.TLSPolicy{Mode: integration.TLSPin, Pin: pin}}}

	// A leaf signed by the pinned CA, which the server presents: accepted,
	// whatever host the URL names.
	srv := tlsServer(t, leaf, ca)
	if err := get(client(t, opts), strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)); err != nil {
		t.Fatal(err)
	}
	// Presenting the pinned CA next to a leaf it didn't sign proves nothing.
	forged := newCert(t, "mars-1", false, nil)
	srv = tlsServer(t, forged, ca)
	if err := get(client(t, opts), srv.URL); code(err) != integration.CodeTLSPinMismatch {
		t.Fatalf("forged chain: %v", err)
	}
}

func TestTLSTOFU(t *testing.T) {
	a := newCert(t, "minig5", false, nil)
	b := newCert(t, "impostor", false, nil)
	srvA, srvB := tlsServer(t, a), tlsServer(t, b)

	var pins []string
	c := client(t, ClientOptions{HTTPOptions: integration.HTTPOptions{
		TLS:   integration.TLSPolicy{Mode: integration.TLSTOFU},
		OnPin: func(p string) { pins = append(pins, p) },
	}})
	if err := get(c, srvA.URL); err != nil {
		t.Fatal(err)
	}
	want := integration.Fingerprint(a.x509)
	if len(pins) != 1 || pins[0] != want || c.Pin() != want {
		t.Fatalf("pinned %v (client %q), want %q", pins, c.Pin(), want)
	}
	// A fresh connection to the same server matches the pin; nothing is
	// recorded twice.
	c.tr.CloseIdleConnections()
	if err := get(c, srvA.URL); err != nil {
		t.Fatal(err)
	}
	// A different certificate is refused from then on.
	err := get(c, srvB.URL)
	if code(err) != integration.CodeTLSPinMismatch {
		t.Fatalf("after pinning, another certificate: %v", err)
	}
	if len(pins) != 1 {
		t.Fatalf("pins = %v", pins)
	}

	// A stored pin from an earlier run: a mismatch is refused immediately.
	c = client(t, ClientOptions{HTTPOptions: integration.HTTPOptions{TLS: integration.TLSPolicy{Mode: integration.TLSTOFU, Pin: want}}})
	if err := get(c, srvB.URL); code(err) != integration.CodeTLSPinMismatch {
		t.Fatalf("stored pin, other certificate: %v", err)
	}
	if err := get(c, srvA.URL); err != nil {
		t.Fatal(err)
	}
}

func TestTLSInsecure(t *testing.T) {
	srv := tlsServer(t, newCert(t, "anything", false, nil))
	c := client(t, ClientOptions{HTTPOptions: integration.HTTPOptions{TLS: integration.TLSPolicy{Mode: integration.TLSInsecure}}})
	if err := get(c, srv.URL); err != nil {
		t.Fatal(err)
	}
	if c.Observed() == "" {
		t.Fatal("insecure still records what it saw")
	}
}

func TestRedirectsAndHeaders(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("followed a redirect to another origin")
	}))
	defer other.Close()
	var ua, extra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/away":
			http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
		case "/final":
			ua, extra = r.Header.Get("User-Agent"), r.Header.Get("X-Extra")
		}
	}))
	defer srv.Close()

	c := client(t, ClientOptions{HTTPOptions: integration.HTTPOptions{Headers: map[string]string{"X-Extra": "1"}}})
	if err := get(c, srv.URL+"/same"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ua, "chadash/") || extra != "1" {
		t.Fatalf("headers: ua %q extra %q", ua, extra)
	}
	if err := get(c, srv.URL+"/away"); err == nil || !strings.Contains(err.Error(), "another origin") {
		t.Fatalf("cross-origin redirect: %v", err)
	}
	nc := client(t, ClientOptions{HTTPOptions: integration.HTTPOptions{Redirects: integration.NoRedirects}})
	resp, err := nc.Get(srv.URL + "/same")
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("no redirects: %v %v", resp, err)
	}
	resp.Body.Close()
}

func TestResponseCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 2048))
	}))
	defer srv.Close()
	if err := get(client(t, ClientOptions{MaxBody: 1024}), srv.URL); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
	if err := get(client(t, ClientOptions{MaxBody: 2048}), srv.URL); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
}

// session logs in with a fake form login that hands out a token.
type session struct {
	logins, logouts atomic.Int32
	token           atomic.Value
}

func (s *session) Login(ctx context.Context, c *http.Client) error {
	resp, err := c.Post(loginURL.Load().(string), "application/json", strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := integration.CheckResponse(resp); err != nil {
		return err
	}
	b, _ := io.ReadAll(resp.Body)
	s.token.Store(string(b))
	s.logins.Add(1)
	return nil
}

func (s *session) Apply(r *http.Request) { r.Header.Set("X-Session", s.token.Load().(string)) }

func (s *session) Logout(ctx context.Context, c *http.Client) error {
	s.logouts.Add(1)
	return nil
}

var loginURL atomic.Value

func TestSession(t *testing.T) {
	var issued atomic.Int32
	var valid atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			tok := "tok" + string(rune('0'+issued.Add(1)))
			valid.Store(tok)
			io.WriteString(w, tok)
		default:
			if r.Header.Get("X-Session") != valid.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			io.WriteString(w, "data")
		}
	}))
	defer srv.Close()
	loginURL.Store(srv.URL + "/login")

	s := &session{}
	c, err := NewHTTPClient(ClientOptions{HTTPOptions: integration.HTTPOptions{Session: s}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := get(c, srv.URL+"/api"); err != nil {
			t.Fatal(err)
		}
	}
	if s.logins.Load() != 1 {
		t.Fatalf("logged in %d times, want once", s.logins.Load())
	}
	// The upstream forgets the session (a restart): one 403, a new login, a
	// retry that works.
	valid.Store("expired")
	if err := get(c, srv.URL+"/api"); err != nil {
		t.Fatal(err)
	}
	if s.logins.Load() != 2 {
		t.Fatalf("logins after expiry = %d", s.logins.Load())
	}
	c.Close(t.Context())
	if s.logouts.Load() != 1 {
		t.Fatal("Close should log out")
	}
}
