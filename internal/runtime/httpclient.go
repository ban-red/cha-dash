package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/integration"
)

// HTTP client defaults.
const (
	DefaultRequestTimeout = 15 * time.Second
	// MaxResponseBytes caps every response body an integration reads. Reading
	// past it fails with ErrResponseTooLarge.
	MaxResponseBytes = 16 << 20
	maxRedirects     = 5
)

// ErrResponseTooLarge is returned when a response body exceeds MaxResponseBytes.
var ErrResponseTooLarge = errors.New("runtime: response body too large")

// UserAgent is what every integration request says it is.
func UserAgent() string { return "chadash/" + buildinfo.Version + " (+https://dash.cha.sh)" }

// ClientOptions configure NewHTTPClient.
type ClientOptions struct {
	integration.HTTPOptions
	UserAgent string         // default UserAgent()
	RootCAs   *x509.CertPool // roots for TLSVerify; nil means the system's (tests set it)
	MaxBody   int64          // response body cap; default MaxResponseBytes
}

// Client is an instance's HTTP client plus what the runtime needs to know
// about it: the certificate it last saw, and how to shut it down.
type Client struct {
	*http.Client
	guard   *tlsGuard
	session *sessionTransport
	tr      *http.Transport
}

// Observed returns the SHA-256 of the last leaf certificate the client saw
// (accepted or not), or "".
func (c *Client) Observed() string { return c.guard.observedPin() }

// Pin returns the pin the client enforces: configured, or recorded by TOFU.
func (c *Client) Pin() string { return c.guard.currentPin() }

// Close logs out of the session (if any) and drops idle connections.
func (c *Client) Close(ctx context.Context) {
	if c.session != nil {
		c.session.logout(ctx)
	}
	c.tr.CloseIdleConnections()
}

// NewHTTPClient builds a client with a TLS policy, timeouts, a User-Agent, a
// response size cap, a redirect policy and an optional session hook.
//
// Every mode checks certificates in a VerifyConnection callback except
// verify, which uses crypto/tls's own verification (with the URL host); the
// others set InsecureSkipVerify only to hand chain checking to that
// callback. No proxy is used: integrations talk to the LAN directly.
func NewHTTPClient(opts ClientOptions) (*Client, error) {
	pol := opts.TLS
	if pol.Mode == "" {
		pol.Mode = integration.TLSVerify
	}
	if pol.Mode == integration.TLSPin && pol.Pin == "" {
		return nil, &integration.ConfigError{Message: "tls mode pin needs a pin"}
	}
	g := &tlsGuard{mode: pol.Mode, pin: pol.Pin, onPin: opts.OnPin}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if pol.Mode == integration.TLSVerify {
		tc.RootCAs = opts.RootCAs
		tc.VerifyConnection = g.observe // runs after crypto/tls verified the chain and host
	} else {
		tc.InsecureSkipVerify = true // the chain is checked by g.check instead
		tc.VerifyConnection = g.check
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	dial := dialer.DialContext
	if path := opts.UnixSocket; path != "" {
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) { return dialer.DialContext(ctx, "unix", path) }
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial,
		TLSClientConfig:       tc,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   2,
		ForceAttemptHTTP2:     true,
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = UserAgent()
	}
	maxBody := opts.MaxBody
	if maxBody <= 0 {
		maxBody = MaxResponseBytes
	}
	base := &decorate{next: tr, ua: ua, headers: opts.Headers, guard: g, maxBody: maxBody}
	redirect := func(req *http.Request, via []*http.Request) error {
		if opts.Redirects == integration.NoRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if origin(req.URL) != origin(via[0].URL) {
			return fmt.Errorf("refused a redirect to another origin (%s)", origin(req.URL))
		}
		return nil
	}
	c := &Client{guard: g, tr: tr}
	var rt http.RoundTripper = base
	if opts.Session != nil {
		login := &http.Client{Transport: base, Timeout: timeout, CheckRedirect: redirect}
		c.session = &sessionTransport{next: base, login: login, s: opts.Session}
		rt = c.session
	}
	c.Client = &http.Client{Transport: rt, Timeout: timeout, CheckRedirect: redirect}
	return c, nil
}

func origin(u *url.URL) string {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(host, port)
}

// tlsGuard enforces an instance's TLS policy and remembers what it saw.
type tlsGuard struct {
	mu       sync.Mutex
	mode     integration.TLSMode
	pin      string
	onPin    func(string)
	observed string
}

func (g *tlsGuard) observedPin() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.observed
}

func (g *tlsGuard) currentPin() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pin
}

func (g *tlsGuard) setObserved(fp string) {
	g.mu.Lock()
	g.observed = fp
	g.mu.Unlock()
}

// observe records the leaf in verify mode, where crypto/tls has already
// checked the chain and the host.
func (g *tlsGuard) observe(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) > 0 {
		g.setObserved(integration.Fingerprint(cs.PeerCertificates[0]))
	}
	return nil
}

// check is the VerifyConnection callback for pin, tofu and insecure.
func (g *tlsGuard) check(cs tls.ConnectionState) error {
	certs := cs.PeerCertificates
	if len(certs) == 0 {
		return &integration.TLSError{Code: integration.CodeTLSUntrusted, Err: errors.New("no certificate")}
	}
	fp := integration.Fingerprint(certs[0])
	g.mu.Lock()
	g.observed = fp
	mode, pin := g.mode, g.pin
	record := mode == integration.TLSTOFU && pin == ""
	if record {
		g.pin = fp // later handshakes, even concurrent ones, must match it
	}
	g.mu.Unlock()

	switch {
	case mode == integration.TLSInsecure:
		return nil
	case record:
		if g.onPin != nil {
			g.onPin(fp)
		}
		return nil
	}
	return checkPin(pin, certs)
}

// checkPin accepts the chain if the leaf is the pinned certificate, or if a
// pinned CA appears in the presented chain and the leaf verifies against it.
// A CA merely being present proves nothing (anyone can send it), hence the
// verification. The host isn't checked: the pin is the identity, and users
// reach Proxmox by aliases and IPs its certificate doesn't name.
func checkPin(pin string, certs []*x509.Certificate) error {
	leaf := certs[0]
	fp := integration.Fingerprint(leaf)
	if fp == pin {
		return nil
	}
	for i, c := range certs[1:] {
		if integration.Fingerprint(c) != pin {
			continue
		}
		roots := x509.NewCertPool()
		roots.AddCert(c)
		inter := x509.NewCertPool()
		for _, ic := range certs[1 : i+1] {
			inter.AddCert(ic)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter}); err == nil {
			return nil
		}
	}
	return &integration.TLSError{Code: integration.CodeTLSPinMismatch, Fingerprint: fp, Expected: pin}
}

// decorate adds the User-Agent and default headers, caps response bodies and
// records the certificate behind a failed verification.
type decorate struct {
	next    http.RoundTripper
	ua      string
	headers map[string]string
	guard   *tlsGuard
	maxBody int64
}

func (d *decorate) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req
	if req.Header.Get("User-Agent") == "" || len(d.headers) > 0 {
		r = req.Clone(req.Context())
		if r.Header.Get("User-Agent") == "" {
			r.Header.Set("User-Agent", d.ua)
		}
		for k, v := range d.headers {
			if r.Header.Get(k) == "" {
				r.Header.Set(k, v)
			}
		}
	}
	resp, err := d.next.RoundTrip(r)
	if err != nil {
		var cve *tls.CertificateVerificationError
		if errors.As(err, &cve) && len(cve.UnverifiedCertificates) > 0 {
			d.guard.setObserved(integration.Fingerprint(cve.UnverifiedCertificates[0]))
		}
		return nil, err
	}
	if resp.StatusCode == http.StatusSwitchingProtocols && r.Header.Get("Upgrade") != "" {
		// The body is the upgraded connection (a WebSocket): it must stay
		// writable, and the protocol library caps its messages instead.
		// Only a request that asked to upgrade skips the cap, so a server
		// can't stream past it by answering a plain GET with a 101.
		return resp, nil
	}
	resp.Body = &limitedBody{rc: resp.Body, remaining: d.maxBody}
	return resp, nil
}

type limitedBody struct {
	rc        io.ReadCloser
	remaining int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		var b [1]byte
		if n, err := l.rc.Read(b[:]); n > 0 {
			return 0, ErrResponseTooLarge
		} else {
			return 0, err
		}
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.rc.Read(p)
	l.remaining -= int64(n)
	return n, err
}

func (l *limitedBody) Close() error { return l.rc.Close() }

// sessionTransport implements the session cache: log in once, apply the
// session to each request, and on an expired response log in again and
// retry once (when the body can be replayed).
type sessionTransport struct {
	next  http.RoundTripper
	login *http.Client
	s     integration.Session

	mu    sync.Mutex
	ready bool
	gen   int // bumps on every successful login
}

// ensure logs in if there's no session, or if the session is still the one
// that expired (stale >= 0 is the generation a request saw fail).
func (t *sessionTransport) ensure(ctx context.Context, stale int) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ready && t.gen != stale {
		return t.gen, nil
	}
	if err := t.s.Login(ctx, t.login); err != nil {
		t.ready = false
		return t.gen, err
	}
	t.ready = true
	t.gen++
	return t.gen, nil
}

func (t *sessionTransport) expired(resp *http.Response) bool {
	if e, ok := t.s.(integration.SessionExpiry); ok {
		return e.Expired(resp)
	}
	return resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden
}

func (t *sessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	gen, err := t.ensure(req.Context(), -1)
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	t.s.Apply(r)
	resp, err := t.next.RoundTrip(r)
	replayable := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
	if err != nil || !t.expired(resp) || !replayable {
		return resp, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if _, err := t.ensure(req.Context(), gen); err != nil {
		return nil, err
	}
	r2 := req.Clone(req.Context())
	if req.GetBody != nil {
		if r2.Body, err = req.GetBody(); err != nil {
			return nil, err
		}
	}
	t.s.Apply(r2)
	return t.next.RoundTrip(r2)
}

func (t *sessionTransport) logout(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready {
		return
	}
	t.ready = false
	if l, ok := t.s.(integration.SessionLogout); ok {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = l.Logout(ctx, t.login)
	}
}
