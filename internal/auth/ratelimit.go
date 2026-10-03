package auth

import (
	"math"
	"sync"
	"time"
)

// limiter is a per-key token bucket for setup and login attempts. It is
// deliberately simple: in-memory, per process, keyed by client IP.
type limiter struct {
	burst float64
	every time.Duration // one token is refilled per interval
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
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
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+float64(now.Sub(b.last))/float64(l.every))
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) * float64(l.every))
}

// sweep drops buckets that have refilled completely; they carry no state.
// If every bucket is still draining, it drops them all rather than grow.
func (l *limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+float64(now.Sub(b.last))/float64(l.every) >= l.burst {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) >= maxBuckets {
		clear(l.buckets)
	}
}
