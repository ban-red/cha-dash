package registry

import (
	"context"
	"errors"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
)

// CheckEvery is how long an answer for a tag is kept: a tag is asked about
// at most this often, whoever asks.
const CheckEvery = 6 * time.Hour

// CheckRetry is how long a failed answer is kept: a registry that was down
// is asked again sooner than one that answered.
const CheckRetry = 15 * time.Minute

// FreshAfter is how old an answer must be for a fresh check (Check now) to
// ask again, so pressing it twice doesn't ask twice.
const FreshAfter = time.Minute

// Login states of an answer (stackproto.LoginNeeded and LoginUsed).
const (
	LoginNeeded = "needed"
	LoginUsed   = "used"
)

// Answer is what a registry said about a tag, and when.
type Answer struct {
	Digest string // the tag's manifest digest, "sha256:…"
	Reason string // why there's no digest, a sentence
	Login  string // "", LoginNeeded or LoginUsed
	At     time.Time
	keep   time.Duration
}

// Checker asks registries about tags and keeps their answers, so a tag is
// asked about at most once every CheckEvery. It's safe for concurrent use.
type Checker struct {
	Client
	// Now is the clock (time.Now when nil).
	Now func() time.Time
	// Fallback asks Docker for a tag's digest when the registry can't be
	// reached from here (plain http, a registry only the daemon's config
	// knows). It may count as a pull, so it's only for those. Nil: none.
	Fallback func(ctx context.Context, ref string) (string, error)

	mu   sync.Mutex
	tags map[string]Answer
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Ask is the registry's answer for the tag, from memory while it's fresh:
// for its keep, or with fresh for FreshAfter.
func (c *Checker) Ask(ctx context.Context, tagged reference.NamedTagged, fresh bool) Answer {
	ref := tagged.String()
	domain := reference.Domain(tagged)
	_, have := c.Logins.For(domain)
	// A login added or removed asks again.
	key := ref
	if have {
		key += " (login)"
	}
	now := c.now()
	c.mu.Lock()
	a, ok := c.tags[key]
	c.mu.Unlock()
	keep := a.keep
	if fresh {
		keep = min(keep, FreshAfter)
	}
	if ok && now.Sub(a.At) < keep {
		return a
	}
	a = Answer{At: now, keep: CheckEvery}
	d, used, err := c.Head(ctx, tagged)
	var le *LoginError
	if err != nil && ctx.Err() == nil && c.Fallback != nil && !errors.As(err, &le) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrAnswered) {
		// Not reachable from here: Docker's lookup, but only for such
		// registries. A registry that answered (even a 429) is never asked
		// again this way.
		fctx, cancel := context.WithTimeout(ctx, Timeout)
		d, err = c.Fallback(fctx, ref)
		cancel()
		used = err == nil && have
		switch {
		case err != nil && IsAuthError(err):
			err = &LoginError{Registry: domain, Had: have}
		case cerrdefs.IsNotFound(err):
			err = ErrNotFound
		}
	}
	if err != nil && ctx.Err() != nil {
		// The caller gave up (a sync's budget): say so, but keep nothing,
		// so the next check asks the registry instead of repeating this.
		a.Reason = "The check was cut short before its registry answered."
		return a
	}
	switch {
	case err == nil:
		a.Digest = d
		if used {
			a.Login = LoginUsed
		}
	case errors.As(err, &le):
		// A login won't appear by itself: kept as long as an answer.
		a.Reason = le.Error()
		a.Login = LoginNeeded
		if le.Had {
			a.Login = LoginUsed
		}
	case errors.Is(err, ErrNotFound):
		a.Reason, a.keep = "Its registry doesn't have that tag any more.", CheckRetry
	default:
		a.Reason, a.keep = "Its registry didn't answer: "+err.Error(), CheckRetry
	}
	c.mu.Lock()
	if c.tags == nil {
		c.tags = map[string]Answer{}
	}
	c.tags[key] = a
	c.mu.Unlock()
	return a
}
