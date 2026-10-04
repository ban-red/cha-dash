package runtime

import (
	"slices"
	"strings"
	"sync"
	"time"

	"dash.cha.sh/internal/integration"
)

// Instance statuses, as the API reports them.
const (
	StatusOK       = "ok"       // the last sync worked
	StatusDegraded = "degraded" // working with warnings, starting, or a sync or two failed
	StatusRepair   = "repair"   // needs the user: credential rejected, certificate changed, API changed
	StatusError    = "error"    // can't read the source (repeated failures, bad config, unknown integration)
	StatusDisabled = "disabled"
)

// errorAfter is how many consecutive transient failures turn degraded into error.
const errorAfter = 3

// attemptLog is how many recent sync attempts diagnostics keep.
const attemptLog = 20

// ErrorInfo is the last failure: when, a stable code, and a message with
// secrets redacted.
type ErrorInfo struct {
	At      time.Time `json:"at"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
}

// Attempt is one sync (or connect) attempt, for diagnostics.
type Attempt struct {
	At         time.Time `json:"at"`
	DurationMS int64     `json:"duration_ms"`
	OK         bool      `json:"ok"`
	Code       string    `json:"code,omitempty"`
	Message    string    `json:"message,omitempty"`
}

// state is an instance's live status. It's shared by the coordinator (which
// records attempts), the emitter (version and warnings) and the API.
type state struct {
	mu       sync.Mutex
	disabled bool
	unknown  string // set when the integration isn't in this build
	started  bool   // an attempt has finished
	version  string
	lastOK   time.Time
	lastErr  *ErrorInfo
	class    integration.Class // of the last failure, when failures > 0
	failures int               // consecutive
	warnings []integration.Warning
	attempts []Attempt
	watched  bool
	interval time.Duration
	nextSync time.Time
}

// snapshot is a consistent copy of state for readers.
type snapshot struct {
	Status   string
	Version  string
	LastOK   time.Time
	LastErr  *ErrorInfo
	Warnings []integration.Warning
	Attempts []Attempt
	Failures int
	Watched  bool
	Interval time.Duration
	NextSync time.Time
}

func (s *state) snapshot() snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := snapshot{
		Status: s.statusLocked(), Version: s.version, LastOK: s.lastOK,
		Warnings: slices.Clone(s.warnings), Attempts: slices.Clone(s.attempts), Failures: s.failures,
		Watched: s.watched, Interval: s.interval, NextSync: s.nextSync,
	}
	if s.lastErr != nil {
		e := *s.lastErr
		snap.LastErr = &e
	}
	if !s.started && !s.disabled && s.unknown == "" {
		snap.Warnings = append(snap.Warnings, integration.Warning{Code: "starting", Severity: integration.WarnInfo, Message: "Waiting for the first sync."})
	}
	if snap.Warnings == nil {
		snap.Warnings = []integration.Warning{}
	}
	return snap
}

func (s *state) status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *state) statusLocked() string {
	switch {
	case s.disabled:
		return StatusDisabled
	case s.unknown != "":
		return StatusError
	case s.failures > 0 && s.class == integration.NeedsRepair:
		return StatusRepair
	case s.failures > 0 && (s.class == integration.Permanent || s.failures >= errorAfter):
		return StatusError
	case s.failures > 0, !s.started, len(s.warnings) > 0:
		return StatusDegraded
	}
	return StatusOK
}

func (s *state) logAttempt(a Attempt) {
	s.attempts = append(s.attempts, a)
	if len(s.attempts) > attemptLog {
		s.attempts = slices.Delete(s.attempts, 0, len(s.attempts)-attemptLog)
	}
}

func (s *state) success(at time.Time, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
	s.lastOK = at
	s.failures = 0
	s.logAttempt(Attempt{At: at, DurationMS: d.Milliseconds(), OK: true})
}

func (s *state) failure(at time.Time, d time.Duration, code string, class integration.Class, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
	s.failures++
	s.class = class
	s.lastErr = &ErrorInfo{At: at, Code: code, Message: msg}
	s.logAttempt(Attempt{At: at, DurationMS: d.Milliseconds(), Code: code, Message: msg})
}

func (s *state) report(version string, warnings []integration.Warning, setVersion, setWarnings bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if setVersion {
		s.version = version
	}
	if setWarnings {
		s.warnings = warnings
	}
}

func (s *state) schedule(watched bool, interval time.Duration, next time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watched, s.interval, s.nextSync = watched, interval, next
}

// reset forgets the previous run's failures when an instance restarts with a
// new config, keeping last_ok and the attempt log.
func (s *state) reset(disabled bool, unknown string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabled, s.unknown = disabled, unknown
	s.started = false
	s.failures = 0
	s.warnings = nil
	s.nextSync = time.Time{}
	if unknown != "" {
		s.lastErr = &ErrorInfo{At: time.Now().UTC(), Code: integration.CodeInvalidConfig, Message: unknown}
	}
}

// redactor scrubs secret values out of text that may reach logs or the API.
type redactor struct{ values []string }

// minRedactLen keeps tiny values (a one-letter password) from shredding
// every message; they're too short to be worth a leak hunt and too common to
// replace safely.
const minRedactLen = 4

func newRedactor(secrets []string) redactor {
	var vals []string
	for _, s := range secrets {
		if len(s) < minRedactLen {
			continue
		}
		vals = append(vals, s)
		// Also the forms a value takes inside URLs and JSON.
		if q := urlQueryEscape(s); q != s {
			vals = append(vals, q)
		}
	}
	// Longest first, so a secret containing another is replaced whole.
	slices.SortFunc(vals, func(a, b string) int { return len(b) - len(a) })
	return redactor{values: vals}
}

func (r redactor) scrub(s string) string {
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}
