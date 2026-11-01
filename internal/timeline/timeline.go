// Package timeline merges what happened in the home onto one time axis
// (docs/plan/14-v0.4.md, "The incident timeline"): incidents' lifecycles
// and their causes (internal/incidents), the actions that ran (the audit
// log), state changes (history's health rows) and maintenance windows
// (maintenance_windows, which the rollouts track writes; it may be empty).
//
// It's a query, not a topic: GET /api/timeline (internal/server) builds one
// per request, as the caller's role may see it. Every entry carries its
// sentence as a catalog message (internal/msg) and its English text, so
// the web can copy a range as Markdown without wording anything itself.
package timeline

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/incidents"
	"dash.cha.sh/internal/msg"
)

// Entry kinds.
const (
	KindIncident = "incident" // an incident event: opened, changed, expected, resolved
	KindCause    = "cause"    // an incident's cause, as the correlator named it
	KindAction   = "action"   // an action that ran (a task)
	KindState    = "state"    // a resource's health changed
	KindWindow   = "window"   // a maintenance window began or ended
)

// MaxEntries bounds one timeline; the newest are kept and Truncated says so.
const MaxEntries = 1000

// Entry is one thing on the time axis.
type Entry struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	// Event is what happened, by kind: an incident event's kind (opened,
	// changed, caused, acknowledged, expected, resolved); an action's
	// result (ok, failed); a window's start or end. Empty for a state.
	Event    string `json:"event,omitempty"`
	Resource string `json:"resource,omitempty"`
	Incident string `json:"incident,omitempty"`
	// Severity: an incident's, or a state's status as a severity (down,
	// attention, ok); what the web colours the mark with.
	Severity string `json:"severity,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Action   string `json:"action,omitempty"` // the action's ID
	Task     string `json:"task,omitempty"`
	Actor    string `json:"actor,omitempty"`
	Window   string `json:"window,omitempty"`
	// Text is the sentence in English; Message and Args name it in the
	// catalog.
	Text    string         `json:"text"`
	Message string         `json:"message"`
	Args    map[string]any `json:"args,omitempty"`
}

// Timeline is GET /api/timeline's answer.
type Timeline struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Resource string    `json:"resource,omitempty"`
	// Entries are oldest first.
	Entries []Entry `json:"entries"`
	// Truncated: there were more than MaxEntries; the oldest were left out.
	Truncated bool `json:"truncated"`
	// History is the metrics history's level ("off" without one): with
	// it off, the timeline has no state changes.
	History string `json:"history"`
}

// Sources are where the entries come from. Any may be nil.
type Sources struct {
	Incidents *incidents.Store
	Audit     *audit.Log
	History   *history.Recorder
	// DB holds maintenance_windows (the hub's database).
	DB *sql.DB
	// Name is a resource's name, for the sentences; nil or "" uses the ID.
	Name func(resource string) string
	// ActionTitle is an action's title; nil or "" uses its ID.
	ActionTitle func(id string) string
	// Hosts is what a resource runs on, for the maintenance windows that
	// cover it (a container's engine, the engine's host). Nil: only windows
	// naming the resource itself.
	Hosts func(resource string) []string
}

// View is what the caller may see.
type View struct {
	// Whole: viewer and up see everything.
	Whole bool
	// For household (Whole false):
	// Relevant says whether a resource is on one of its boards: only
	// problems (not repairs, never a security finding) whose target is
	// relevant, and no causes, as the household's inbox shows them.
	Relevant func(resource string) bool
	// Visible says whether its graph shows a resource (state changes,
	// windows).
	Visible func(resource string) bool
	// User is the caller: household sees only its own actions, as on the
	// tasks topic.
	User string
}

// Query is one request.
type Query struct {
	Resource string // "" for the whole home
	From, To time.Time
}

// Build merges the sources over q as v sees them.
func Build(ctx context.Context, src Sources, q Query, v View) (Timeline, error) {
	tl := Timeline{From: q.From.UTC(), To: q.To.UTC(), Resource: q.Resource, Entries: []Entry{}, History: string(history.Off)}
	b := builder{src: src, v: v}
	if src.Incidents != nil {
		evs, err := src.Incidents.Events(ctx, incidents.Query{Resource: q.Resource, From: q.From, To: q.To, Limit: MaxEntries + 1})
		if err != nil {
			return tl, err
		}
		for _, e := range evs {
			b.incident(e)
		}
	}
	if src.Audit != nil {
		evs, err := src.Audit.Ran(ctx, q.Resource, q.From, q.To, audit.MaxLimit)
		if err != nil {
			return tl, err
		}
		for _, e := range evs {
			b.action(e)
		}
	}
	if src.History != nil {
		tl.History = string(src.History.Level())
		changes, err := src.History.Changes(ctx, q.Resource, q.From, q.To, MaxEntries+1)
		if err != nil {
			return tl, err
		}
		for _, c := range changes {
			b.state(c)
		}
	}
	if src.DB != nil {
		if err := b.windows(ctx, q); err != nil {
			return tl, err
		}
	}
	order := map[string]int{KindWindow: 0, KindState: 1, KindIncident: 2, KindCause: 3, KindAction: 4}
	slices.SortStableFunc(b.out, func(a, c Entry) int {
		return cmp.Or(a.At.Compare(c.At), cmp.Compare(order[a.Kind], order[c.Kind]))
	})
	if len(b.out) > MaxEntries {
		b.out = b.out[len(b.out)-MaxEntries:]
		tl.Truncated = true
	}
	tl.Entries = b.out
	return tl, nil
}

type builder struct {
	src Sources
	v   View
	out []Entry
}

func (b *builder) add(e Entry, m msg.M) {
	e.At = e.At.UTC()
	e.Text, e.Message, e.Args = m.Text(), m.ID, m.Args
	b.out = append(b.out, e)
}

func (b *builder) name(id string) string {
	if b.src.Name != nil {
		if n := b.src.Name(id); n != "" {
			return n
		}
	}
	return id
}

func (b *builder) incident(e incidents.Event) {
	d := incidents.DataOf(e)
	if !b.v.Whole {
		// The household's inbox: problems on its boards, never a security
		// finding, no causes.
		if d.Class != "problem" || d.Category == "security" || b.v.Relevant == nil || !b.v.Relevant(d.Target) || e.Kind == incidents.Caused {
			return
		}
	}
	title := cmp.Or(d.Title, d.Target, e.IncidentID)
	en := Entry{At: e.At, Kind: KindIncident, Event: string(e.Kind), Resource: d.Target, Incident: e.IncidentID, Severity: d.Severity}
	switch e.Kind {
	case incidents.Opened:
		b.add(en, msg.New("timeline.opened", "title", title))
	case incidents.Changed:
		en.From, en.To = d.From, d.To
		if d.Field == "phase" {
			b.add(en, msg.New("timeline.phase", "title", title, "phase", d.To))
			return
		}
		b.add(en, msg.New("timeline.severity", "title", title, "from", d.From, "to", d.To))
	case incidents.Caused:
		en.Kind = KindCause
		if d.Resource != "" {
			en.Resource = d.Resource
		}
		b.add(en, msg.New("timeline.caused", "title", title, "cause", cmp.Or(d.Text, b.name(d.Resource))))
	case incidents.Acknowledged:
		b.add(en, msg.New("timeline.acknowledged", "title", title))
	case incidents.Expected:
		en.Window = d.Window
		b.add(en, msg.New("timeline.expected", "title", title))
	case incidents.Resolved:
		en.Severity = "ok"
		if d.By == "action" {
			b.add(en, msg.New("timeline.resolved_by", "title", title))
			return
		}
		b.add(en, msg.New("timeline.resolved", "title", title))
	}
}

func (b *builder) action(e audit.Event) {
	if !b.v.Whole && (b.v.User == "" || (e.Actor.ID != b.v.User && e.Actor.OnBehalfOf != b.v.User)) {
		return
	}
	if e.Result != audit.OK && e.Result != audit.Failed {
		return
	}
	title := e.Action
	if b.src.ActionTitle != nil {
		title = cmp.Or(b.src.ActionTitle(e.Action), e.Action)
	}
	actor := cmp.Or(e.Actor.Name, e.Actor.Kind)
	en := Entry{At: e.At.Time, Kind: KindAction, Event: e.Result, Resource: e.Target, Action: e.Action, Task: e.Task, Actor: actor}
	if e.Result == audit.Failed {
		en.Severity = "down"
		b.add(en, msg.New("timeline.action_failed", "actor", actor, "action", title, "target", b.name(e.Target)))
		return
	}
	en.Severity = "ok"
	b.add(en, msg.New("timeline.action_ok", "actor", actor, "action", title, "target", b.name(e.Target)))
}

// severityOf maps a health status to the timeline's colours.
func severityOf(status string) string {
	switch status {
	case "ok":
		return "ok"
	case "down":
		return "down"
	case "attention", "repair", "degraded":
		return "attention"
	}
	return "info"
}

func (b *builder) state(c history.Change) {
	if !b.v.Whole && (b.v.Visible == nil || !b.v.Visible(c.Resource)) {
		return
	}
	en := Entry{At: c.At, Kind: KindState, Resource: c.Resource, From: c.From, To: c.Status, Severity: severityOf(c.Status)}
	if c.From == "" {
		b.add(en, msg.New("timeline.state", "name", b.name(c.Resource), "status", c.Status))
		return
	}
	b.add(en, msg.New("timeline.state_from", "name", b.name(c.Resource), "from", c.From, "status", c.Status))
}

// week is a weekly window's period.
const week = 7 * 24 * time.Hour

// windows adds the starts and ends of maintenance windows that fall in the
// range and cover the resource (or any, for the whole home).
func (b *builder) windows(ctx context.Context, q Query) error {
	rows, err := b.src.DB.QueryContext(ctx, `SELECT id, name, hosts, starts_at, ends_at, weekly FROM maintenance_windows
		WHERE weekly = 1 OR (starts_at < ? AND ends_at >= ?)`, q.To.UnixMilli(), q.From.UnixMilli())
	if err != nil {
		return fmt.Errorf("timeline: %w", err)
	}
	defer rows.Close()
	want := map[string]bool{}
	if q.Resource != "" {
		want[q.Resource] = true
		if b.src.Hosts != nil {
			for _, h := range b.src.Hosts(q.Resource) {
				want[h] = true
			}
		}
	}
	for rows.Next() {
		var id, name, rawHosts string
		var start, end int64
		var weekly bool
		if err := rows.Scan(&id, &name, &rawHosts, &start, &end, &weekly); err != nil {
			return fmt.Errorf("timeline: %w", err)
		}
		var hosts []string
		_ = json.Unmarshal([]byte(rawHosts), &hosts)
		host := ""
		for _, h := range hosts {
			if (len(want) == 0 || want[h]) && (b.v.Whole || (b.v.Visible != nil && b.v.Visible(h))) {
				host = h
				break
			}
		}
		if host == "" {
			continue
		}
		s, e := time.UnixMilli(start).UTC(), time.UnixMilli(end).UTC()
		if weekly && s.Before(q.From) {
			// Move to the first occurrence that may reach the range.
			n := q.From.Sub(e) / week
			if n > 0 {
				s, e = s.Add(n*week), e.Add(n*week)
			}
		}
		for i := 0; i < 60 && s.Before(q.To); i++ {
			en := Entry{Kind: KindWindow, Resource: host, Window: id, Severity: "info"}
			if !s.Before(q.From) {
				en.At, en.Event = s, "start"
				b.add(en, msg.New("timeline.window_start", "name", strings.TrimSpace(name)))
			}
			if !e.Before(q.From) && e.Before(q.To) {
				en.At, en.Event = e, "end"
				b.add(en, msg.New("timeline.window_end", "name", strings.TrimSpace(name)))
			}
			if !weekly {
				break
			}
			s, e = s.Add(week), e.Add(week)
		}
	}
	return rows.Err()
}
