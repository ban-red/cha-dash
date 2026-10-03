package sim

import (
	"math"
	"time"
)

// The simulator's randomness is counter-based: every random number is a hash
// of (seed, stream key, counter). Nothing depends on how many numbers were
// drawn before, so the world at time t is the same no matter how the clock got
// there, and adding a resource never shifts another resource's noise.

// splitmix64 is the finalizer of the SplitMix64 generator, used as a hash.
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// hashKey is FNV-1a over s.
func hashKey(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// rng is a stateless, seeded source of uniform numbers.
type rng struct{ seed uint64 }

func newRNG(seed int64) rng { return rng{seed: splitmix64(uint64(seed))} }

// u returns a uniform number in [0, 1) for (key, n).
func (r rng) u(key uint64, n int64) float64 {
	h := splitmix64(r.seed ^ splitmix64(key^splitmix64(uint64(n))))
	return float64(h>>11) / (1 << 53)
}

// s returns a uniform number in [-1, 1) for (key, n).
func (r rng) s(key uint64, n int64) float64 { return 2*r.u(key, n) - 1 }

// smooth is value noise in [-1, 1]: random knots every period, joined with
// cosine interpolation, so it wanders slowly instead of flickering.
func (r rng) smooth(key uint64, t time.Time, period time.Duration) float64 {
	p := int64(period / time.Second)
	x := t.Unix()
	k := floorDiv(x, p)
	f := float64(x-k*p) / float64(p)
	a, b := r.s(key, k), r.s(key, k+1)
	w := (1 - math.Cos(math.Pi*f)) / 2
	return a*(1-w) + b*w
}

// jitter is fast noise in [-1, 1), different every second.
func (r rng) jitter(key uint64, t time.Time) float64 { return r.s(key^0x6a09e667f3bcc908, t.Unix()) }

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// hourOf is the time of day in hours. The Demo Home keeps UTC as its clock.
func hourOf(t time.Time) float64 {
	u := t.UTC()
	return float64(u.Hour()) + float64(u.Minute())/60 + float64(u.Second())/3600
}

// dayStart is 00:00 UTC of t's day.
func dayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// diurnal is a raised cosine in [0, 1] that peaks at the given hour.
func diurnal(t time.Time, peak float64) float64 {
	return 0.5 + 0.5*math.Cos(2*math.Pi*(hourOf(t)-peak)/24)
}

// bump is a raised-cosine bump in [0, 1] centred on an hour of the day, with
// the given half-width in hours. It wraps around midnight.
func bump(t time.Time, center, halfWidth float64) float64 {
	d := math.Mod(hourOf(t)-center+36, 24) - 12
	if math.Abs(d) >= halfWidth {
		return 0
	}
	return 0.5 + 0.5*math.Cos(math.Pi*d/halfWidth)
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func smoothstep(x float64) float64 {
	x = clamp(x, 0, 1)
	return x * x * (3 - 2*x)
}

// lin is t's linear position between a and b, clamped to [0, 1].
func lin(t, a, b time.Time) float64 {
	if !t.After(a) {
		return 0
	}
	if !t.Before(b) {
		return 1
	}
	return float64(t.Sub(a)) / float64(b.Sub(a))
}

// ease is lin with smoothstep easing.
func ease(t, a, b time.Time) float64 { return smoothstep(lin(t, a, b)) }

// within reports a <= t < b.
func within(t, a, b time.Time) bool { return !t.Before(a) && t.Before(b) }

func roundTo(v float64, dec int) float64 {
	p := math.Pow10(dec)
	r := math.Round(v*p) / p
	if r == 0 {
		return 0
	}
	return r
}

func hours(d time.Duration) float64 { return float64(d) / float64(time.Hour) }

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
