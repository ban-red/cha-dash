package httpx

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

// ProxyNote is a deployment problem ProxyCheck noticed, for Settings →
// Security.
type ProxyNote struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Proxy note codes.
const (
	// NoteUntrustedProxy: requests carry X-Forwarded-* headers from a peer
	// that isn't in CHADASH_TRUSTED_PROXIES.
	NoteUntrustedProxy = "untrusted_proxy"
	// NotePlainSignIn: CHADASH_PUBLIC_URL is https://, but a sign-in arrived
	// over plain HTTP.
	NotePlainSignIn = "plain_sign_in"
)

// ProxyCheck notices the most likely reverse-proxy mistake: a proxy in front
// of the hub that CHADASH_TRUSTED_PROXIES doesn't list. The hub then can't
// tell clients apart (every sign-in shares the proxy's rate limit, so a few
// wrong passwords lock everyone out) and doesn't mark the session cookie
// Secure. Each problem is logged once, at warn, and kept as a note until the
// hub restarts.
//
// Anyone can send X-Forwarded-For, so a note is a hint, not proof: the log
// names the peer so the owner can tell whether it's their proxy. The note
// never suggests trusting that address.
type ProxyCheck struct {
	log      *slog.Logger
	proxies  Proxies
	httpsURL bool

	mu    sync.Mutex
	notes []ProxyNote
	seen  map[string]bool
}

// NewProxyCheck checks requests against proxies and the hub's public URL.
func NewProxyCheck(log *slog.Logger, proxies Proxies, publicURL string) *ProxyCheck {
	return &ProxyCheck{
		log:      log,
		proxies:  proxies,
		httpsURL: strings.HasPrefix(strings.ToLower(publicURL), "https://"),
		seen:     map[string]bool{},
	}
}

// Middleware inspects each request; it never changes one.
func (c *ProxyCheck) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.inspect(r)
		next.ServeHTTP(w, r)
	})
}

func (c *ProxyCheck) inspect(r *http.Request) {
	peer := PeerAddr(r)
	forwarded := r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Forwarded-Proto") != ""
	if forwarded && peer.IsValid() && !c.proxies.isTrusted(peer) {
		c.note(NoteUntrustedProxy,
			"Requests are arriving through what looks like a reverse proxy that Cha Dash doesn't trust. If you use one, add its address to CHADASH_TRUSTED_PROXIES: until then every sign-in shares one rate limit, and the session cookie isn't marked Secure.",
			"peer", peer.String())
	}
	if c.httpsURL && r.Method == http.MethodPost && isSignIn(r.URL.Path) && !c.proxies.IsHTTPS(r) {
		c.note(NotePlainSignIn,
			"A sign-in reached Cha Dash over plain HTTP, although CHADASH_PUBLIC_URL is https://. The password crossed the network unencrypted. Open Cha Dash at its https:// address, and if a proxy terminates TLS, list it in CHADASH_TRUSTED_PROXIES.",
			"peer", peer.String())
	}
}

func isSignIn(path string) bool {
	return path == "/api/auth/login" || path == "/api/setup"
}

func (c *ProxyCheck) note(code, msg string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[code] {
		return
	}
	c.seen[code] = true
	c.notes = append(c.notes, ProxyNote{Code: code, Message: msg})
	c.log.Warn(msg, args...)
}

// Notes are the problems seen since the hub started, oldest first.
func (c *ProxyCheck) Notes() []ProxyNote {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ProxyNote(nil), c.notes...)
}
