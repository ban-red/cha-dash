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

// limiter bounds failed handshakes per client (httpx.ClientKey: an IPv4
// address or an IPv6 /64): bad secrets and tokens, unknown keys, malformed
// hellos. A few, then one a minute, like sign-in's attempt limiting. It
// applies only to keys the registry doesn't hold: a known node always gets
// through, so strangers behind the same NAT or proxy can't lock it out.
type limiter struct {
	mu     sync.Mutex
	seen   map[string]*attempts
	burst  int
	every  time.Duration
	forget time.Duration // an entry with no failure for this long is dropped
	max    int           // entries kept; the stalest go first
	now    func() time.Time
}

type attempts struct {
	n    int
	next time.Time
	last time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{seen: map[string]*attempts{}, burst: 5, every: time.Minute, forget: 10 * time.Minute, max: 4096, now: now}
}

// entryLocked returns key's attempts, or nil once they're forgotten.
func (l *limiter) entryLocked(key string, now time.Time) *attempts {
	a := l.seen[key]
	if a != nil && now.Sub(a.last) >= l.forget {
		delete(l.seen, key)
		return nil
	}
	return a
}

// allowed reports whether key may try now.
func (l *limiter) allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a := l.entryLocked(key, now)
	return a == nil || a.n < l.burst || !now.Before(a.next)
}

// failed counts a failure from key.
func (l *limiter) failed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a := l.entryLocked(key, now)
	if a == nil {
		if len(l.seen) >= l.max {
			l.evictLocked(now)
		}
		a = &attempts{}
		l.seen[key] = a
	}
	a.n++
	a.next, a.last = now.Add(l.every), now
}

// evictLocked makes room: it drops every forgotten entry and, if that
// frees nothing, the one whose last failure is oldest. Memory stays
// bounded, and a client that keeps failing keeps its entry.
func (l *limiter) evictLocked(now time.Time) {
	var oldest string
	var at time.Time
	for k, a := range l.seen {
		switch {
		case now.Sub(a.last) >= l.forget:
			delete(l.seen, k)
		case oldest == "" || a.last.Before(at):
			oldest, at = k, a.last
		}
	}
	if len(l.seen) >= l.max {
		delete(l.seen, oldest)
	}
}

// succeeded forgets key's failures: a node paired from there, proving the
// secret.
func (l *limiter) succeeded(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.seen, key)
}

// refusalAudit decides which refused enrollments reach the audit log: the
// first per client (ClientKey) in each window, and at most perWindow from
// everyone together, so a flood of bad tokens, even from rotating IPv6
// addresses, writes a bounded number of rows. held counts the refusals
// left out since the last one recorded.
type refusalAudit struct {
	mu        sync.Mutex
	every     time.Duration
	perWindow int
	max       int
	seen      map[string]time.Time // client → when its window started
	start     time.Time            // the overall window
	recorded  int                  // in the overall window
	held      int
	now       func() time.Time
}

// Refusal audit limits.
const (
	refusalWindow   = 10 * time.Minute
	refusalsAudited = 100 // per window, from everyone
	refusalsTracked = 4096
)

func newRefusalAudit(now func() time.Time) *refusalAudit {
	return &refusalAudit{every: refusalWindow, perWindow: refusalsAudited, max: refusalsTracked, seen: map[string]time.Time{}, now: now}
}

// take reports whether to record a refusal from key, and, when it does,
// how many refusals went unrecorded since the last one.
func (r *refusalAudit) take(key string) (bool, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if now.Sub(r.start) >= r.every {
		r.start, r.recorded = now, 0
	}
	if at, ok := r.seen[key]; (ok && now.Sub(at) < r.every) || r.recorded >= r.perWindow {
		r.held++
		return false, 0
	}
	if _, ok := r.seen[key]; !ok && len(r.seen) >= r.max {
		for k, at := range r.seen {
			if now.Sub(at) >= r.every {
				delete(r.seen, k)
			}
		}
		if len(r.seen) >= r.max {
			r.held++ // every slot is a live window: don't make room by forgetting one
			return false, 0
		}
	}
	r.seen[key] = now
	r.recorded++
	n := r.held
	r.held = 0
	return true, n
}

// forget ends key's window, so its next refusal is recorded.
func (r *refusalAudit) forget(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.seen, key)
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
