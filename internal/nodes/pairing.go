package nodes

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/nodeproto"
)

// Pairing is v0.2's local pairing: a secret the hub generates and writes,
// with its pin, into a volume it shares with the node on the compose
// network (cdp_<secret>.<hub pin>). A node that presents the secret, over a
// channel where it already checked the hub's pin, is enrolled and approved
// at once. Anything else is refused: a stray container on the network has
// neither the secret nor a known key.
//
// The secret is single-use. The first enrollment it admits consumes it, and
// the hub writes a fresh one at once; the node goes on with its enrolled
// key and never needs the secret again. The hub also writes a fresh one
// when a node is revoked, when an admin asks (Regenerate), and when it
// starts after any node has paired, so a secret copied out of the volume
// stops working.
//
// The file is 0640 in a 0750 directory, both owned by the hub's user, so the
// node reads it through the group (compose gives it group_add: [65532]).
// Nodes on other hosts join with a join token instead (join.go), and wait
// for an admin's approval.
type Pairing struct {
	path string

	mu     sync.Mutex
	secret string
	pin    string
}

// PairingFileMode is the pairing file's mode.
const PairingFileMode = 0o640

// OpenPairing reads the pairing file at path, creating it (with a fresh
// secret) when it doesn't exist, and rewriting it when hubPin changed. A
// file that exists but doesn't parse is an error: it's never silently
// replaced, since the node would then hold a different secret.
func OpenPairing(path, hubPin string) (*Pairing, error) {
	p := &Pairing{path: path, pin: hubPin}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return p, p.Regenerate()
	case err != nil:
		return nil, fmt.Errorf("nodes: pairing file: %w", err)
	}
	secret, pin, err := nodeproto.ParsePairing(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("nodes: pairing file %s: %w", path, err)
	}
	p.secret = secret
	if pin != hubPin {
		return p, p.setPin(hubPin)
	}
	return p, nil
}

func newSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// writeLocked writes the file; p.mu is held.
func (p *Pairing) writeLocked() error {
	tok := nodeproto.FormatPairing(p.secret, p.pin)
	if err := nodeproto.WriteFileAtomic(p.path, []byte(tok+"\n"), PairingFileMode); err != nil {
		return fmt.Errorf("nodes: writing the pairing file: %w", err)
	}
	return nil
}

// setPin rewrites the file with the hub's new pin, keeping the secret.
func (p *Pairing) setPin(pin string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pin = pin
	return p.writeLocked()
}

// Regenerate replaces the secret with a fresh one and rewrites the file.
// The old secret stops working at once, even if the file can't be written.
func (p *Pairing) Regenerate() error {
	s, err := newSecret()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.secret = s
	return p.writeLocked()
}

// Path is where the pairing file lives.
func (p *Pairing) Path() string { return p.path }

// Check compares a presented secret with the hub's, in constant time.
func (p *Pairing) Check(secret string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checkLocked(secret)
}

func (p *Pairing) checkLocked(secret string) bool {
	return secret != "" && p.secret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(p.secret)) == 1
}

// errBadSecret is a presented secret that isn't the current one.
var errBadSecret = errors.New("nodes: the pairing secret doesn't match")

// Redeem spends the secret: if it matches, enroll runs, and when enroll
// succeeds the secret is replaced (and the file rewritten) before anyone
// else can present it. Two nodes racing with one secret can't both get
// in: the lock is held throughout. redeemed reports that enroll ran and
// succeeded; err is then only a failure to rewrite the file, which leaves
// the old secret dead all the same.
func (p *Pairing) Redeem(secret string, enroll func() error) (redeemed bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.checkLocked(secret) {
		return false, errBadSecret
	}
	if err := enroll(); err != nil {
		return false, err
	}
	next, err := newSecret()
	if err != nil {
		p.secret = "" // nothing pairs until a regenerate succeeds
		return true, err
	}
	p.secret = next
	return true, p.writeLocked()
}

// DefaultPairingFrom are the client addresses a node may pair from when
// Options.PairingFrom is nil: loopback, private (RFC 1918, ULA), link-local
// and CGNAT ranges, which is where a compose network or a LAN puts a node.
// Pairing from a public address, even with the secret, is refused.
var DefaultPairingFrom = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}

func addrIn(addr string, prefixes []netip.Prefix) bool {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// limiter bounds failed handshakes per client address (bad secrets, unknown
// keys, malformed hellos): a few, then one a minute, like sign-in's attempt
// limiting.
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

// succeeded forgets addr's failures: an admitted node proved a key the
// registry trusts.
func (l *limiter) succeeded(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.seen, addr)
}

// gate bounds handshakes in progress: before a node is admitted, holding a
// connection open costs the hub a goroutine and buffers, so each address
// gets a few at once and everyone together a few dozen.
type gate struct {
	mu       sync.Mutex
	perAddr  map[string]int
	total    int
	maxPer   int
	maxTotal int
}

// Handshake limits.
const (
	maxHandshakesPerAddr = 4
	maxHandshakes        = 64
)

func newGate(perAddr, total int) *gate {
	return &gate{perAddr: map[string]int{}, maxPer: perAddr, maxTotal: total}
}

// enter takes a slot for addr, or reports false when it's over a limit.
func (g *gate) enter(addr string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total >= g.maxTotal || g.perAddr[addr] >= g.maxPer {
		return false
	}
	g.total++
	g.perAddr[addr]++
	return true
}

// leave gives addr's slot back.
func (g *gate) leave(addr string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.total--
	if g.perAddr[addr]--; g.perAddr[addr] <= 0 {
		delete(g.perAddr, addr)
	}
}

// sampler lets one log line through per interval and reports how many it
// held back, so a flood of refusals from strangers doesn't flood the log.
type sampler struct {
	mu      sync.Mutex
	every   time.Duration
	next    time.Time
	dropped int
	now     func() time.Time
}

// take reports whether to log now, and how many lines were held back since
// the last one.
func (s *sampler) take() (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Before(s.next) {
		s.dropped++
		return false, 0
	}
	n := s.dropped
	s.dropped, s.next = 0, now.Add(s.every)
	return true, n
}
