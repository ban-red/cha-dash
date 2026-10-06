package auth

import (
	"math"
	"net/netip"
	"sync"
	"time"
)

// limiter is a per-key token bucket for setup and login attempts. It is
// deliberately simple: in-memory, per process, keyed by client network (see
// clientKey) or, for password failures, by user ID.
type limiter struct {
	burst float64
	every time.Duration // one token is refilled per interval
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	// overflow is shared by every new key once buckets is full of keys still
	// draining. Clearing the map instead would hand every client, an attacker
	// rotating addresses included, a fresh budget.
	overflow bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// maxBuckets bounds memory if many distinct clients show up.
const maxBuckets = 4096

func newLimiter(burst int, every time.Duration) *limiter {
	return &limiter{burst: float64(burst), every: every, now: time.Now, buckets: map[string]*bucket{}}
}

// allow takes a token for key. When none is left it reports how long until one is.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			l.sweep(now)
		}
		if len(l.buckets) < maxBuckets {
			b = &bucket{tokens: l.burst, last: now}
			l.buckets[key] = b
		} else {
			b = &l.overflow
		}
	}
	b.tokens = l.refilled(b, now)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) * float64(l.every))
}

// wait reports how long until key has a token, without taking one. A key
// with no bucket has a full one.
func (l *limiter) wait(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		return 0
	}
	if t := l.refilled(b, l.now()); t < 1 {
		return time.Duration((1 - t) * float64(l.every))
	}
	return 0
}

// refilled is b's token count at now. A zero bucket (the overflow's first
// use) comes out full.
func (l *limiter) refilled(b *bucket, now time.Time) float64 {
	return math.Min(l.burst, b.tokens+float64(now.Sub(b.last))/float64(l.every))
}

// sweep drops buckets that have refilled completely; they carry no state.
func (l *limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if l.refilled(b, now) >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// clientKey is the rate-limit key for a client address. IPv6 clients are
// keyed by their /64, the smallest block a home or hosting connection is
// usually given, so one connection can't mint unlimited fresh budgets.
func clientKey(a netip.Addr) string {
	if a.Is6() {
		if p, err := a.WithZone("").Prefix(64); err == nil {
			return p.String()
		}
	}
	return a.String()
}
