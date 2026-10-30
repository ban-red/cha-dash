package ical

import (
	"bytes"
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	goical "github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"

	"dash.cha.sh/internal/integration"
)

// Limits on what a feed can make the hub do.
const (
	maxIterations = 100_000 // recurrence steps per event, from its first instance
	maxOnsets     = 500     // transitions per VTIMEZONE observance
	maxText       = 200     // runes kept of a title or a location
)

// loadLocation reads the zone database. The hub's image has none, so in
// production most zones come from the feed's own VTIMEZONEs; tests swap it
// to check that path.
var loadLocation = time.LoadLocation

// Value formats (RFC 5545 §3.3.4, §3.3.5).
const (
	dateFormat     = "20060102"
	localFormat    = "20060102T150405"
	utcFormat      = "20060102T150405Z"
	wallKeyFormat  = "20060102T150405"
	dateKeyFormat  = "20060102"
	dateAttrFormat = "2006-01-02"
)

// zone turns wall-clock times into instants and back. A wall-clock time is
// a time.Time whose fields, read in UTC, are the local date and time.
type zone struct {
	name string         // the IANA name, when known: attrs.time_zone
	loc  *time.Location // from the zone database
	obs  []observance   // from the feed's VTIMEZONE, when loc is nil
}

// observance is one of a VTIMEZONE's STANDARD or DAYLIGHT parts.
type observance struct {
	from, to int         // UTC offsets in seconds, before and after each onset
	onsets   []time.Time // wall-clock times, in the offset before (from)
}

// utc is the fallback zone. Its name is empty: a calendar that falls back to
// it doesn't know its zone.
var utc = &zone{loc: time.UTC}

// instant is the moment wall-clock time w names in z.
func (z *zone) instant(w time.Time) time.Time {
	if z.loc != nil {
		return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, z.loc)
	}
	return w.Add(-time.Duration(z.offset(w, false)) * time.Second)
}

// wall is t's wall-clock time in z.
func (z *zone) wall(t time.Time) time.Time {
	if z.loc != nil {
		l := t.In(z.loc)
		return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
	}
	return t.UTC().Add(time.Duration(z.offset(t, true)) * time.Second)
}

// location is where to show instant t: z itself, or its offset at t.
func (z *zone) location(t time.Time) *time.Location {
	if z.loc != nil {
		return z.loc
	}
	return time.FixedZone("", z.offset(t, true))
}

// offset is the UTC offset in force at t: an instant when isInstant, else a
// wall-clock time. It's the offset after the latest onset at or before t;
// before every onset, the earliest observance's offset before it.
func (z *zone) offset(t time.Time, isInstant bool) int {
	var best, first time.Time
	off, firstOff := 0, 0
	for _, o := range z.obs {
		for _, on := range o.onsets {
			at := on
			if isInstant {
				at = on.Add(-time.Duration(o.from) * time.Second)
			}
			if first.IsZero() || at.Before(first) {
				first, firstOff = at, o.from
			}
			if !at.After(t) && (best.IsZero() || at.After(best)) {
				best, off = at, o.to
			}
		}
	}
	if best.IsZero() {
		return firstOff
	}
	return off
}

// calendar is a parsed feed.
type calendar struct {
	name    string // X-WR-CALNAME
	zone    *zone  // the calendar's zone: all-day and floating times, and the output's offsets
	events  []*goical.Component
	vtz     map[string]*goical.Component
	zones   map[string]*zone // resolved TZIDs; nil for one that isn't known
	unknown []string         // TZIDs read as UTC
	horizon time.Time        // how far VTIMEZONE onsets are expanded
}

// parse decodes a feed. Anything that isn't an iCalendar object is
// errNotICal; its content never reaches the message.
func parse(body []byte, now time.Time) (*calendar, error) {
	body = bytes.TrimLeft(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")), " \t\r\n")
	if len(body) < 15 || !strings.EqualFold(string(body[:15]), "BEGIN:VCALENDAR") {
		return nil, errNotICal
	}
	cal, err := goical.NewDecoder(bytes.NewReader(body)).Decode()
	if err != nil {
		return nil, errNotICal
	}
	c := &calendar{zone: utc, vtz: map[string]*goical.Component{}, zones: map[string]*zone{},
		horizon: now.AddDate(1, 0, 0)}
	c.name = text(cal.Component, "X-WR-CALNAME")
	var firstVTZ string
	for _, child := range cal.Children {
		switch child.Name {
		case goical.CompTimezone:
			id := strings.TrimSpace(child.Props.Get(goical.PropTimezoneID).Value)
			if child.Props.Get(goical.PropTimezoneID) == nil || id == "" {
				continue
			}
			c.vtz[id] = child
			if firstVTZ == "" {
				firstVTZ = id
			}
		case goical.CompEvent:
			c.events = append(c.events, child)
		}
	}

	// The calendar's zone: what it says it is, else its first VTIMEZONE,
	// else the first event's TZID.
	var tzid string
	if p := cal.Props.Get("X-WR-TIMEZONE"); p != nil {
		tzid = p.Value
	}
	if tzid == "" {
		tzid = firstVTZ
	}
	for _, e := range c.events {
		if tzid != "" {
			break
		}
		if p := e.Props.Get(goical.PropDateTimeStart); p != nil {
			tzid = p.Params.Get(goical.PropTimezoneID)
		}
	}
	if tzid != "" {
		if z := c.resolve(tzid); z != nil {
			c.zone = z
		}
	}
	return c, nil
}

// warnings are the instance's warnings about this feed.
func (c *calendar) warnings() []integration.Warning {
	if len(c.unknown) == 0 {
		return nil
	}
	names := slices.Clone(c.unknown)
	for i, n := range names {
		names[i] = clip(n, 64)
	}
	more := ""
	if len(names) > 3 {
		names, more = names[:3], fmt.Sprintf(" and %d more", len(c.unknown)-3)
	}
	return []integration.Warning{{Code: "unknown_time_zone", Severity: integration.WarnWarning,
		Message: fmt.Sprintf("The feed uses time zones Cha Dash doesn't know (%s%s), so their times are read as UTC.", strings.Join(names, ", "), more)}}
}

// ianaShaped matches names that look like IANA zones.
var ianaShaped = regexp.MustCompile(`^(UTC|[A-Za-z]+(/[A-Za-z0-9_+\-]+)+)$`)

// resolve finds a TZID's zone: by IANA name from the zone database (also a
// vendor-prefixed one, like "/mozilla.org/20050126_1/Europe/Berlin"), by
// Windows name through the table, else from the feed's VTIMEZONE. A TZID
// none of them knows is nil, and noted for the warning.
func (c *calendar) resolve(tzid string) *zone {
	tzid = strings.Trim(strings.TrimSpace(tzid), `"`)
	if z, ok := c.zones[tzid]; ok {
		return z
	}
	z := c.lookup(tzid)
	c.zones[tzid] = z
	if z == nil {
		c.unknown = append(c.unknown, tzid)
	}
	return z
}

func (c *calendar) lookup(tzid string) *zone {
	// The IANA candidates: the TZID, and each suffix of a path-like one.
	var names []string
	if w, ok := windowsZones[tzid]; ok {
		names = append(names, w)
	} else {
		trimmed := strings.TrimPrefix(tzid, "/")
		names = append(names, trimmed)
		for i := range len(trimmed) {
			if trimmed[i] == '/' && strings.Contains(trimmed[i+1:], "/") {
				names = append(names, trimmed[i+1:])
			}
		}
	}
	for _, n := range names {
		if n == "" || n == "Local" || !ianaShaped.MatchString(n) {
			continue
		}
		if loc, err := loadLocation(n); err == nil {
			return &zone{name: n, loc: loc}
		}
	}
	vt := c.vtz[tzid]
	if vt == nil {
		return nil
	}
	z := c.vtimezone(vt)
	if z == nil {
		return nil
	}
	for _, n := range names {
		if n != "Local" && ianaShaped.MatchString(n) {
			z.name = n
			break
		}
	}
	return z
}

// vtimezone reads a VTIMEZONE's observances, their onsets expanded up to the
// horizon.
func (c *calendar) vtimezone(vt *goical.Component) *zone {
	z := &zone{}
	for _, part := range vt.Children {
		if part.Name != goical.CompTimezoneStandard && part.Name != goical.CompTimezoneDaylight {
			continue
		}
		from, ok1 := utcOffset(part.Props.Get(goical.PropTimezoneOffsetFrom))
		to, ok2 := utcOffset(part.Props.Get(goical.PropTimezoneOffsetTo))
		start := part.Props.Get(goical.PropDateTimeStart)
		if !ok1 || !ok2 || start == nil {
			continue
		}
		first, err := time.Parse(localFormat, strings.TrimSuffix(strings.TrimSpace(start.Value), "Z"))
		if err != nil {
			continue
		}
		// Onsets older than a few years only matter as the last of them:
		// Outlook starts its rules in 1601.
		o := observance{from: from, to: to}
		horizon := c.horizon.UTC().Add(time.Duration(to) * time.Second)
		cutoff := horizon.AddDate(-3, 0, 0)
		older := first
		if r := part.Props.Get(goical.PropRecurrenceRule); r != nil {
			if opt, err := rrule.StrToROption(r.Value); err == nil {
				opt.Dtstart = first
				if untilUTC(r.Value) {
					opt.Until = opt.Until.Add(time.Duration(from) * time.Second)
				}
				if rule, err := rrule.NewRRule(*opt); err == nil {
					next := rule.Iterator()
					for range maxIterations {
						t, ok := next()
						if !ok || t.After(horizon) || len(o.onsets) == maxOnsets {
							break
						}
						if t.Before(cutoff) {
							older = t
						} else if !t.Equal(first) {
							o.onsets = append(o.onsets, t)
						}
					}
				}
			}
		}
		o.onsets = append([]time.Time{older}, o.onsets...)
		for _, p := range part.Props.Values(goical.PropRecurrenceDates) {
			for v := range strings.SplitSeq(p.Value, ",") {
				if t, err := time.Parse(localFormat, strings.TrimSuffix(strings.TrimSpace(v), "Z")); err == nil && len(o.onsets) < maxOnsets {
					o.onsets = append(o.onsets, t)
				}
			}
		}
		z.obs = append(z.obs, o)
	}
	if len(z.obs) == 0 {
		return nil
	}
	return z
}

// utcOffset reads "+0100", "-0500" or "+053000" as seconds.
func utcOffset(p *goical.Prop) (int, bool) {
	if p == nil {
		return 0, false
	}
	v := strings.TrimSpace(p.Value)
	if len(v) != 5 && len(v) != 7 {
		return 0, false
	}
	sign := 1
	switch v[0] {
	case '-':
		sign = -1
	case '+':
	default:
		return 0, false
	}
	n := 0
	for _, r := range v[1:] {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	h, m, s := n/100, n%100, 0
	if len(v) == 7 {
		h, m, s = n/10000, n/100%100, n%100
	}
	return sign * (h*3600 + m*60 + s), true
}

// untilUTC reports whether an RRULE's UNTIL is a UTC time, which a rule on
// wall-clock times must convert.
func untilUTC(rule string) bool {
	for part := range strings.SplitSeq(strings.ToUpper(rule), ";") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "UNTIL="); ok {
			return strings.HasSuffix(v, "Z")
		}
	}
	return false
}

// stamp is one date or date-time value.
type stamp struct {
	wall time.Time // the date (at midnight) or the wall-clock time
	date bool
	z    *zone // the zone of the wall-clock time; the calendar's for dates and floating times
}

func (s stamp) instant() time.Time { return s.z.instant(s.wall) }

// stamps reads a property's values: one, or a comma-separated list
// (EXDATE, RDATE). A PERIOD keeps its start.
func (c *calendar) stamps(p *goical.Prop) []stamp {
	if p == nil {
		return nil
	}
	var z *zone
	if tzid := p.Params.Get(goical.PropTimezoneID); tzid != "" {
		z = c.resolve(tzid)
		if z == nil {
			z = utc
		}
	}
	isDate := strings.EqualFold(p.Params.Get(goical.ParamValue), string(goical.ValueDate))
	var out []stamp
	for v := range strings.SplitSeq(p.Value, ",") {
		v = strings.TrimSpace(v)
		v, _, _ = strings.Cut(v, "/")
		switch {
		case isDate || len(v) == len(dateFormat):
			if t, err := time.Parse(dateFormat, v[:min(len(v), len(dateFormat))]); err == nil {
				out = append(out, stamp{wall: t, date: true, z: c.zone})
			}
		case strings.HasSuffix(v, "Z"):
			if t, err := time.Parse(utcFormat, v); err == nil {
				out = append(out, stamp{wall: t, z: utc})
			}
		default:
			if t, err := time.Parse(localFormat, v); err == nil {
				out = append(out, stamp{wall: t, z: cmp.Or(z, c.zone)})
			}
		}
	}
	return out
}

func (c *calendar) stamp(comp *goical.Component, name string) (stamp, bool) {
	s := c.stamps(comp.Props.Get(name))
	if len(s) == 0 {
		return stamp{}, false
	}
	return s[0], true
}

// event is one instance in the window, ready to publish.
type event struct {
	from, until time.Time // instants, for ordering and the window
	allDay      bool
	start, end  string
	title       string
	location    string
}

// spec is a VEVENT's start and length.
type spec struct {
	comp  *goical.Component
	start stamp
	dur   time.Duration // timed events
	days  int           // all-day events
}

func (c *calendar) spec(comp *goical.Component) (spec, bool) {
	start, ok := c.stamp(comp, goical.PropDateTimeStart)
	if !ok {
		return spec{}, false
	}
	s := spec{comp: comp, start: start}
	end, hasEnd := c.stamp(comp, goical.PropDateTimeEnd)
	var dur time.Duration
	if p := comp.Props.Get(goical.PropDuration); p != nil && !hasEnd {
		dur, _ = p.Duration()
	}
	if start.date {
		switch {
		case hasEnd:
			s.days = int(end.wall.Sub(start.wall).Hours() / 24)
		case dur > 0:
			s.days = int(dur.Hours() / 24)
		}
		s.days = max(s.days, 1)
		return s, true
	}
	switch {
	case hasEnd:
		s.dur = end.instant().Sub(start.instant())
	case dur > 0:
		s.dur = dur
	}
	s.dur = max(s.dur, 0)
	return s, true
}

// cancelled reports STATUS:CANCELLED.
func cancelled(comp *goical.Component) bool {
	p := comp.Props.Get(goical.PropStatus)
	return p != nil && strings.EqualFold(strings.TrimSpace(p.Value), string(goical.EventCancelled))
}

// key names an instance of s by its start in s's own frame: a date for an
// all-day event, else the wall-clock time in its zone.
func (s spec) key(t stamp) string {
	if s.start.date {
		return t.wall.Format(dateKeyFormat)
	}
	if t.date {
		return "D" + t.wall.Format(dateKeyFormat)
	}
	return s.start.z.wall(t.instant()).Format(wallKeyFormat)
}

// window lays out the events from local midnight today to Days ahead,
// soonest first, at most MaxEvents; total counts them all.
func (c *calendar) window(now time.Time) (out []event, total int) {
	cz := c.zone
	today := cz.wall(now).Truncate(24 * time.Hour)
	from, to := cz.instant(today), cz.instant(today.AddDate(0, 0, Days))

	// Masters by UID, and the instances their overrides replace.
	type master struct {
		spec
		skip map[string]bool
	}
	masters := map[string]*master{}
	var order []*master
	var overrides []*goical.Component
	for _, comp := range c.events {
		if comp.Props.Get(goical.PropRecurrenceID) != nil {
			overrides = append(overrides, comp)
			continue
		}
		s, ok := c.spec(comp)
		if !ok || cancelled(comp) {
			if uid := text(comp, goical.PropUID); uid != "" && ok {
				masters[uid] = nil // a cancelled series: its overrides go too
			}
			continue
		}
		m := &master{spec: s, skip: map[string]bool{}}
		for _, p := range comp.Props.Values(goical.PropExceptionDates) {
			for _, ex := range c.stamps(&p) {
				m.skip[s.key(ex)] = true
			}
		}
		if uid := text(comp, goical.PropUID); uid != "" {
			if _, dup := masters[uid]; !dup {
				masters[uid] = m
			}
		}
		order = append(order, m)
	}

	add := func(s spec, at time.Time) {
		e := event{title: cmp.Or(text(s.comp, goical.PropSummary), "Untitled event"), location: text(s.comp, goical.PropLocation)}
		if s.start.date {
			end := at.AddDate(0, 0, s.days)
			e.allDay, e.from, e.until = true, cz.instant(at), cz.instant(end)
			e.start, e.end = at.Format(dateAttrFormat), end.Format(dateAttrFormat)
		} else {
			e.from = s.start.z.instant(at)
			e.until = e.from.Add(s.dur)
			e.start = e.from.In(cz.location(e.from)).Format(time.RFC3339)
			e.end = e.until.In(cz.location(e.until)).Format(time.RFC3339)
		}
		if e.from.Before(to) && (e.until.After(from) || !e.from.Before(from)) {
			out = append(out, e)
		}
	}

	// An override replaces its instance; a cancelled one only removes it.
	for _, comp := range overrides {
		uid := text(comp, goical.PropUID)
		m, known := masters[uid]
		if known && m == nil {
			continue
		}
		if rid, ok := c.stamp(comp, goical.PropRecurrenceID); ok && m != nil {
			m.skip[m.key(rid)] = true
		}
		if cancelled(comp) {
			continue
		}
		if s, ok := c.spec(comp); ok {
			add(s, s.start.wall)
		}
	}

	for _, m := range order {
		for _, at := range c.instances(m.spec, from, to) {
			k := at.Format(wallKeyFormat)
			if m.start.date {
				k = at.Format(dateKeyFormat)
			}
			if m.skip[k] || m.skip["D"+at.Format(dateKeyFormat)] {
				continue
			}
			add(m.spec, at)
		}
	}

	slices.SortStableFunc(out, func(a, b event) int {
		if d := a.from.Compare(b.from); d != 0 {
			return d
		}
		if a.allDay != b.allDay {
			if a.allDay {
				return -1
			}
			return 1
		}
		return cmp.Or(a.until.Compare(b.until), strings.Compare(a.title, b.title))
	})
	total = len(out)
	if len(out) > MaxEvents {
		out = out[:MaxEvents]
	}
	return out, total
}

// instances are the starts of s's instances near the window, in s's frame:
// DTSTART, the RRULE's and the RDATEs.
func (c *calendar) instances(s spec, from, to time.Time) []time.Time {
	frame := s.start.z
	if s.start.date {
		frame = c.zone
	}
	length := s.dur + time.Duration(s.days)*24*time.Hour
	lo := frame.wall(from).Add(-length).AddDate(0, 0, -2)
	hi := frame.wall(to).AddDate(0, 0, 2)

	seen := map[time.Time]bool{}
	var out []time.Time
	keep := func(t time.Time) {
		if !t.Before(lo) && !t.After(hi) && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	keep(s.start.wall)
	if p := s.comp.Props.Get(goical.PropRecurrenceRule); p != nil {
		if opt, err := rrule.StrToROption(p.Value); err == nil {
			opt.Dtstart = s.start.wall
			if untilUTC(p.Value) && !s.start.date {
				opt.Until = frame.wall(opt.Until)
			}
			if rule, err := rrule.NewRRule(*opt); err == nil {
				next := rule.Iterator()
				for range maxIterations {
					t, ok := next()
					if !ok || t.After(hi) {
						break
					}
					keep(t)
				}
			}
		}
	}
	for _, p := range s.comp.Props.Values(goical.PropRecurrenceDates) {
		for _, r := range c.stamps(&p) {
			switch {
			case s.start.date:
				keep(r.wall)
			case r.date: // a date on a timed event: the event's time that day
				w := s.start.wall
				keep(time.Date(r.wall.Year(), r.wall.Month(), r.wall.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.UTC))
			default:
				keep(frame.wall(r.instant()))
			}
		}
	}
	return out
}

// text is a property's text, unescaped, on one line and clipped. Only
// titles, locations and names are read: descriptions, attendees, organisers
// and URLs never leave the parser.
func text(comp *goical.Component, name string) string {
	p := comp.Props.Get(name)
	if p == nil {
		return ""
	}
	return clip(strings.Join(strings.Fields(unescape(p.Value)), " "), maxText)
}

// unescape reads a TEXT value. Prop.Text would stop at the first unescaped
// comma, which some feeds (Outlook's among them) leave in titles.
func unescape(v string) string {
	var sb strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+1 == len(v) {
			sb.WriteByte(v[i])
			continue
		}
		i++
		switch v[i] {
		case 'n', 'N':
			sb.WriteByte(' ')
		default: // \\ \; \, and anything unexpected, as written
			sb.WriteByte(v[i])
		}
	}
	return sb.String()
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
