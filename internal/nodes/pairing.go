package nodes

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/nodeproto"
)

// Pairing is v0.2's local pairing: a secret the hub generates on first run
// and writes, with its pin, into a volume it shares with the node on the
// compose network (cdp_<secret>.<hub pin>). A node that presents the
// secret, over a channel where it already checked the hub's pin, is
// enrolled and approved at once. Anything else is refused: a stray
// container on the network has neither the secret nor a known key.
//
// The file is 0640 in a 0750 directory, both owned by the hub's user, so the
// node reads it through the group (compose gives it group_add: [65532]).
// Remote enrollment (join tokens, approval in the Inbox) is v0.3; it will
// be another way in beside this one (see Enroller).
type Pairing struct {
	path string

	mu     sync.Mutex
	secret string
}

// PairingFileMode is the pairing file's mode.
const PairingFileMode = 0o640

// OpenPairing reads the pairing file at path, creating it (with a fresh
// secret) when it doesn't exist, and rewriting it when hubPin changed. A
// file that exists but doesn't parse is an error: it's never silently
// replaced, since the node would then hold a different secret.
func OpenPairing(path, hubPin string) (*Pairing, error) {
	p := &Pairing{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		p.secret = base64.RawURLEncoding.EncodeToString(raw)
		return p, p.write(hubPin)
	case err != nil:
		return nil, fmt.Errorf("nodes: pairing file: %w", err)
	}
	secret, pin, err := nodeproto.ParsePairing(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("nodes: pairing file %s: %w", path, err)
	}
	p.secret = secret
	if pin != hubPin {
		return p, p.write(hubPin)
	}
	return p, nil
}

func (p *Pairing) write(pin string) error {
	p.mu.Lock()
	tok := nodeproto.FormatPairing(p.secret, pin)
	p.mu.Unlock()
	if err := nodeproto.WriteFileAtomic(p.path, []byte(tok+"\n"), PairingFileMode); err != nil {
		return fmt.Errorf("nodes: writing the pairing file: %w", err)
	}
	return nil
}

// Path is where the pairing file lives.
func (p *Pairing) Path() string { return p.path }

// Check compares a presented secret with the hub's, in constant time.
func (p *Pairing) Check(secret string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return secret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(p.secret)) == 1
}

// limiter bounds failed enrollments per client address: a few, then one a
// minute, like sign-in's attempt limiting.
type limiter struct {
	mu    sync.Mutex
	seen  map[string]*attempts
	burst int
	every time.Duration
	now   func() time.Time
}

type attempts struct {
	n    int
	next time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{seen: map[string]*attempts{}, burst: 5, every: time.Minute, now: now}
}

// allowed reports whether addr may try now.
func (l *limiter) allowed(addr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.seen[addr]
	return a == nil || a.n < l.burst || !l.now().Before(a.next)
}

// failed counts a failure from addr.
func (l *limiter) failed(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) > 4096 {
		clear(l.seen) // bounded memory; an attacker gains a few tries at most
	}
	a := l.seen[addr]
	if a == nil {
		a = &attempts{}
		l.seen[addr] = a
	}
	a.n++
	a.next = l.now().Add(l.every)
}
