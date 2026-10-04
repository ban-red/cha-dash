package labkit

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ErrRefused is returned, without touching the network, for any request the
// read-only client won't send.
var ErrRefused = errors.New("read-only client refused the request")

// Guard can refuse a request (already known to be GET or HEAD) before it is
// sent, e.g. a Docker path deny-list.
type Guard func(*http.Request) error

type readOnly struct {
	base  http.RoundTripper
	guard Guard
	pace  time.Duration

	mu   sync.Mutex
	last time.Time
}

func (t *readOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, fmt.Errorf("%w: method %s is not GET or HEAD", ErrRefused, r.Method)
	}
	if r.Body != nil && r.Body != http.NoBody {
		return nil, fmt.Errorf("%w: request has a body", ErrRefused)
	}
	if t.guard != nil {
		if err := t.guard(r); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRefused, err)
		}
	}
	// Be gentle: one request at a time, at most one per pace interval.
	t.mu.Lock()
	defer t.mu.Unlock()
	if wait := t.pace - time.Since(t.last); wait > 0 {
		time.Sleep(wait)
	}
	t.last = time.Now()
	return t.base.RoundTrip(r)
}

// NewReadOnlyClient wraps base in a transport that refuses every method except
// GET and HEAD, applies guard, serializes requests with a pace delay, and never
// follows redirects (so a 3xx can't send us, or our auth header, elsewhere).
// timeout 0 means no client-wide timeout (use per-request contexts).
func NewReadOnlyClient(base http.RoundTripper, guard Guard, pace, timeout time.Duration) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{
		Transport:     &readOnly{base: base, guard: guard, pace: pace},
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
