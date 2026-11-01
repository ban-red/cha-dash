package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/msg"
)

// The morning digest (docs/plan/14-v0.4.md, "Morning digest"; decision
// 12): once a day, at a time an admin sets in the hub's time zone, one
// notification through the digest rule's channels: what opened since the
// last digest, what's open now, updates waiting and stale backups. It
// says nothing when all four are empty, unless "all quiet" is on.
//
// The digest rule ("digest") turns it on and routes it, like every other
// rule; GET and PUT /api/notify/digest hold its time and "all quiet". Its
// Web Push goes to admins and the owner only: it summarises the whole home.
const (
	RuleDigest = "digest"
	KindDigest = "digest"
	trDigest   = "digest"
)

// digestKey is the hub's settings row for the digest: DigestSettings plus
// when it was last sent.
const digestKey = "notify.digest"

// DefaultDigestTime is the digest's time until an admin sets one.
const DefaultDigestTime = "07:30"

// digestLate is how long after its time a digest is still sent (the hub
// was down at the time); later than that, it waits for tomorrow.
const digestLate = 2 * time.Hour

// digestItems bounds the incidents a digest names.
const digestItems = 8

var hhmmRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// DigestSettings are what an admin sets.
type DigestSettings struct {
	// Time is HH:MM, 24-hour, in the hub's time zone (TZ).
	Time string `json:"time"`
	// AllQuiet sends "All quiet" on a day with nothing to say.
	AllQuiet bool `json:"all_quiet"`
}

// DigestView is GET /api/notify/digest.
type DigestView struct {
	DigestSettings
	// Enabled is the digest rule's; PUT /api/notify/rules/digest changes it.
	Enabled bool `json:"enabled"`
	// Zone is the hub's time zone, which Time is in.
	Zone       string     `json:"zone"`
	LastSentAt *time.Time `json:"last_sent_at"`
	// NextAt is when the next one goes, while it's on.
	NextAt *time.Time `json:"next_at"`
}

type storedDigest struct {
	DigestSettings
	LastSent int64 `json:"last_sent,omitempty"` // Unix ms
}

// DigestIncident is one incident that opened since the last digest.
type DigestIncident struct {
	ID       string
	Title    string
	Severity string
	Resolved bool // it has resolved since
}

// DigestSources are where the digest reads from. Each may be nil, and
// each may return nothing: the backups track fills StaleBackups in wave 27.
type DigestSources struct {
	// Overnight lists the incidents that opened over [since, until).
	Overnight func(ctx context.Context, since, until time.Time) ([]DigestIncident, error)
	// Updates counts the containers with an update waiting.
	Updates func(ctx context.Context) (int, error)
	// StaleBackups names what's backed up but stale.
	StaleBackups func(ctx context.Context) ([]string, error)
}

// SetDigestSources installs the digest's sources. Call before Run.
func (s *Service) SetDigestSources(src DigestSources) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.digestSrc = src
}

func (s *Service) loadDigest(ctx context.Context) error {
	d := storedDigest{DigestSettings: DigestSettings{Time: DefaultDigestTime}}
	var raw string
	err := s.st.Reader().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, digestKey).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		_ = json.Unmarshal([]byte(raw), &d)
		if !hhmmRE.MatchString(d.Time) {
			d.Time = DefaultDigestTime
		}
	}
	s.mu.Lock()
	s.digest = d
	s.mu.Unlock()
	return nil
}

func (s *Service) saveDigest(ctx context.Context, d storedDigest) error {
	b, _ := json.Marshal(d)
	_, err := s.st.Writer().ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, digestKey, string(b), s.now().UnixMilli())
	if err == nil {
		s.mu.Lock()
		s.digest = d
		s.mu.Unlock()
	}
	return err
}

// scheduled is the digest's time on now's day in the hub's zone.
func (s *Service) scheduled(d storedDigest, now time.Time) time.Time {
	local := now.In(s.loc)
	var h, m int
	if len(d.Time) == 5 {
		h, m = int(d.Time[0]-'0')*10+int(d.Time[1]-'0'), int(d.Time[3]-'0')*10+int(d.Time[4]-'0')
	}
	return time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, s.loc)
}

// digestDue says whether the digest should go now.
func (s *Service) digestDue(now time.Time) bool {
	if !s.settings(RuleDigest).Enabled {
		return false
	}
	s.mu.Lock()
	d := s.digest
	s.mu.Unlock()
	at := s.scheduled(d, now)
	return !now.Before(at) && now.Before(at.Add(digestLate)) && d.LastSent < at.UnixMilli()
}

// digestStep sends the digest when it's due.
func (s *Service) digestStep(ctx context.Context, now time.Time) {
	if !s.digestDue(now) {
		return
	}
	s.mu.Lock()
	d := s.digest
	src := s.digestSrc
	s.mu.Unlock()
	since := now.Add(-24 * time.Hour)
	if last := time.UnixMilli(d.LastSent); d.LastSent > 0 && now.Sub(last) < 48*time.Hour {
		since = last
	}
	// Marked sent first: a digest that fails half-way isn't sent twice.
	d.LastSent = now.UnixMilli()
	if err := s.saveDigest(ctx, d); err != nil {
		s.log.Error("notify: recording the digest failed", "err", err)
		return
	}
	in := digestInput{since: since, now: now, loc: s.loc, open: openProblems(s.incs)}
	if src.Overnight != nil {
		list, err := src.Overnight(ctx, since, now)
		if err != nil {
			s.log.Warn("notify: the digest couldn't read the timeline", "err", err)
		}
		in.overnight = list
	}
	if src.Updates != nil {
		n, err := src.Updates(ctx)
		if err != nil {
			s.log.Warn("notify: the digest couldn't count updates", "err", err)
		}
		in.updates = n
	}
	if src.StaleBackups != nil {
		names, err := src.StaleBackups(ctx)
		if err != nil {
			s.log.Warn("notify: the digest couldn't read backups", "err", err)
		}
		in.stale = names
	}
	e, say := composeDigest(in, d.AllQuiet)
	if !say {
		return
	}
	e.ID, e.Rule, e.Kind, e.At, e.Path, e.Tag, e.transition, e.admins = newID("ne_"), RuleDigest, KindDigest, now.UTC(), timelinePath, "digest", trDigest, true
	if err := s.recordEvent(ctx, e, true); err != nil {
		s.log.Error("notify: recording the digest failed", "err", err)
		return
	}
	s.queue(e, now)
}

// timelinePath opens the timeline page.
const timelinePath = "/settings/timeline"

// openProblems are the open incidents worth naming: problems and repairs
// at attention or down (not hints, not the correlator's suggestions or
// approvals).
func openProblems(incs map[string]incident) []incident {
	var out []incident
	for _, id := range sortedIDs(incs) {
		in := incs[id]
		if in.Status == "open" && (in.Class == "problem" || in.Class == "repair") && (in.Severity == "down" || in.Severity == "attention") {
			out = append(out, in)
		}
	}
	return out
}

type digestInput struct {
	since, now time.Time
	loc        *time.Location
	overnight  []DigestIncident
	open       []incident
	updates    int
	stale      []string
}

// composeDigest words the digest, and says whether there's anything to
// send: something opened, something is open, an update waits or a backup
// is stale; or nothing, and allQuiet asks for that too.
func composeDigest(in digestInput, allQuiet bool) (Event, bool) {
	quiet := len(in.overnight) == 0 && len(in.open) == 0 && in.updates == 0 && len(in.stale) == 0
	if quiet {
		if !allQuiet {
			return Event{}, false
		}
		return Event{Severity: "info", Title: msg.New("digest.title_quiet").Text(), Body: msg.New("digest.quiet_body").Text()}, true
	}
	since := in.since.In(in.loc).Format("Mon 15:04")
	worst := "info"
	var lines []string
	if len(in.overnight) == 0 {
		lines = append(lines, msg.New("digest.overnight_none", "since", since).Text())
	} else {
		resolved := 0
		for _, o := range in.overnight {
			if o.Resolved {
				resolved++
			}
		}
		lines = append(lines, msg.New("digest.overnight", "since", since, "count", len(in.overnight), "resolved", resolved).Text())
		for i, o := range in.overnight {
			if i == digestItems {
				lines = append(lines, msg.New("digest.more", "count", len(in.overnight)-digestItems).Text())
				break
			}
			lines = append(lines, msg.New("digest.overnight_item", "title", clip(o.Title, 120)).Text())
		}
	}
	if len(in.open) == 0 {
		lines = append(lines, msg.New("digest.open_none").Text())
	} else {
		lines = append(lines, msg.New("digest.open", "count", len(in.open)).Text())
		for i, o := range in.open {
			if rank(o.Severity) < rank(worst) {
				worst = o.Severity
			}
			if i == digestItems {
				lines = append(lines, msg.New("digest.more", "count", len(in.open)-digestItems).Text())
				break
			}
			lines = append(lines, msg.New("digest.overnight_item", "title", clip(o.Title, 120)).Text())
		}
	}
	if in.updates > 0 {
		lines = append(lines, msg.New("digest.updates", "count", in.updates).Text())
	}
	if len(in.stale) > 0 {
		names := slices.Clone(in.stale)
		if len(names) > digestItems {
			names = append(names[:digestItems], msg.New("digest.more", "count", len(in.stale)-digestItems).Text())
		}
		lines = append(lines, msg.New("digest.backups_stale", "names", strings.Join(names, ", ")).Text())
		if worst == "info" {
			worst = "attention"
		}
	}
	return Event{Severity: worst, Title: msg.New("digest.title").Text(), Body: clip(strings.Join(lines, "\n"), maxBody)}, true
}

// Digest is the digest's settings as GET /api/notify/digest shows them.
func (s *Service) Digest() DigestView {
	s.mu.Lock()
	d := s.digest
	s.mu.Unlock()
	v := DigestView{DigestSettings: d.DigestSettings, Enabled: s.settings(RuleDigest).Enabled, Zone: s.loc.String()}
	if d.LastSent > 0 {
		t := time.UnixMilli(d.LastSent).UTC()
		v.LastSentAt = &t
	}
	if v.Enabled {
		now := s.now()
		next := s.scheduled(d, now)
		if !now.Before(next) {
			next = s.scheduled(d, now.Add(24*time.Hour))
		}
		next = next.UTC()
		v.NextAt = &next
	}
	return v
}

// DigestUpdate is PUT /api/notify/digest: fields left out keep their value.
type DigestUpdate struct {
	Time     *string `json:"time"`
	AllQuiet *bool   `json:"all_quiet"`
}

func (s *Service) handleDigest(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.Digest())
}

func (s *Service) handleUpdateDigest(w http.ResponseWriter, r *http.Request) {
	var u DigestUpdate
	if !httpx.DecodeJSON(w, r, &u) {
		return
	}
	if u.Time == nil && u.AllQuiet == nil {
		httpx.Say(w, http.StatusUnprocessableEntity, "nothing_to_change", msg.New("digest.nothing_to_change"))
		return
	}
	s.mu.Lock()
	d := s.digest
	s.mu.Unlock()
	before := d.DigestSettings
	if u.Time != nil {
		t := strings.TrimSpace(*u.Time)
		if !hhmmRE.MatchString(t) {
			httpx.Say(w, http.StatusUnprocessableEntity, "bad_time", msg.New("digest.bad_time"))
			return
		}
		if t != d.Time && d.LastSent >= s.scheduled(d, s.now()).UnixMilli() {
			// Sent today already: a later time today doesn't send twice.
			d.LastSent = max(d.LastSent, s.scheduled(storedDigest{DigestSettings: DigestSettings{Time: t}}, s.now()).UnixMilli())
		}
		d.Time = t
	}
	if u.AllQuiet != nil {
		d.AllQuiet = *u.AllQuiet
	}
	if err := s.saveDigest(r.Context(), d); err != nil {
		s.log.Error("notify: saving the digest failed", "err", err)
		httpx.Say(w, http.StatusInternalServerError, "failed", msg.New("digest.failed"))
		return
	}
	s.Audit.Change(r, authz.Audited["PUT /api/notify/digest"], "settings:notify.digest",
		map[string]any{"time_from": before.Time, "time_to": d.Time, "all_quiet_from": before.AllQuiet, "all_quiet_to": d.AllQuiet}, authz.T1, nil)
	httpx.WriteJSON(w, http.StatusOK, s.Digest())
}
