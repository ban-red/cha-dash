package notify

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Message kinds: an incident transition, a group of them, a critical
// action, or a test. The webhook payload's "kind".
const (
	KindDown      = "incident.down"
	KindAttention = "incident.attention"
	KindRepair    = "repair"
	KindStuck     = "stack.stuck"
	KindResolved  = "incident.resolved"
	KindAction    = "action.t4"
	KindGroup     = "group"
	KindTest      = "test"
)

// PayloadVersion is the webhook payload's "version".
const PayloadVersion = 1

// Message is one notification as channels send it, and the webhook's JSON
// payload (README.md documents it as version 1).
type Message struct {
	Version  int          `json:"version"`
	ID       string       `json:"id"` // the event's ID (ne_…), or the group's (ng_…)
	Kind     string       `json:"kind"`
	Severity string       `json:"severity"` // down, attention or info
	Title    string       `json:"title"`
	Body     string       `json:"body"`
	URL      string       `json:"url,omitempty"` // the hub's page for it; only with CHADASH_PUBLIC_URL
	At       time.Time    `json:"at"`
	Incident *IncidentRef `json:"incident,omitempty"`
	Task     *TaskRef     `json:"task,omitempty"`
	Items    []Item       `json:"items,omitempty"` // a group's messages, at most maxGroupItems
	// Path is where a click opens inside the app; channels turn it into URL.
	Path string `json:"-"`
	// Tag groups messages about one thing (an incident), for Web Push.
	Tag string `json:"-"`
}

// IncidentRef is the incident a message is about.
type IncidentRef struct {
	ID       string `json:"id"`
	Target   string `json:"target"`
	Scenario string `json:"scenario"`
	Class    string `json:"class"`
	Severity string `json:"severity"`
	Status   string `json:"status"`
	Category string `json:"category,omitempty"`
}

// TaskRef is the action a message is about.
type TaskRef struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Label  string `json:"label"`
	Target string `json:"target"`
	State  string `json:"state"`
	Actor  string `json:"actor"`
	Audit  string `json:"audit,omitempty"`
}

// Item is one message inside a group.
type Item struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Incident string `json:"incident,omitempty"`
	URL      string `json:"url,omitempty"`
}

// Grouping: this many messages arriving together become one.
const (
	groupMin      = 3
	maxGroupItems = 20
	maxTitle      = 200
	maxBody       = 1000
)

// incidentPath opens the Inbox on an incident.
func incidentPath(id string) string { return "/?incident=" + url.QueryEscape(id) }

// inboxPath opens the Inbox.
const inboxPath = "/?inbox=1"

// auditPath opens Settings → Audit.
const auditPath = "/settings/audit"

// messageOf is the message for one event.
func messageOf(e Event) Message {
	m := Message{Version: PayloadVersion, ID: e.ID, Kind: e.Kind, Severity: e.Severity, Title: clip(e.Title, maxTitle), Body: clip(e.Body, maxBody),
		At: e.At.UTC(), Path: e.Path, Tag: e.Tag, Incident: e.Incident, Task: e.Task}
	return m
}

// compose turns events bound for one destination into messages: one each,
// except that groupMin or more incident messages become one group (a
// backup night), keeping openings and resolutions apart. Critical actions
// always go alone.
func compose(evs []Event, groupID func() string) []Message {
	var opens, resolved, alone []Event
	for _, e := range evs {
		switch e.Kind {
		case KindAction, KindTest:
			alone = append(alone, e)
		case KindResolved:
			resolved = append(resolved, e)
		default:
			opens = append(opens, e)
		}
	}
	var out []Message
	for _, e := range alone {
		out = append(out, messageOf(e))
	}
	for _, set := range [][]Event{opens, resolved} {
		if len(set) < groupMin {
			for _, e := range set {
				out = append(out, messageOf(e))
			}
			continue
		}
		out = append(out, group(set, groupID()))
	}
	return out
}

// group is one message for many.
func group(evs []Event, id string) Message {
	worst := "info"
	counts := map[string]int{}
	for _, e := range evs {
		if rank(e.Severity) < rank(worst) {
			worst = e.Severity
		}
		counts[e.Kind]++
	}
	m := Message{Version: PayloadVersion, ID: id, Kind: KindGroup, Severity: worst, At: evs[len(evs)-1].At.UTC(), Path: inboxPath, Tag: "group"}
	resolvedOnly := counts[KindResolved] == len(evs)
	if resolvedOnly {
		m.Title = fmt.Sprintf("%d problems resolved", len(evs))
	} else {
		var parts []string
		if n := counts[KindDown] + counts[KindStuck]; n > 0 {
			parts = append(parts, plural(n, "down", "down"))
		}
		if n := counts[KindAttention]; n > 0 {
			parts = append(parts, plural(n, "needs attention", "need attention"))
		}
		if n := counts[KindRepair]; n > 0 {
			parts = append(parts, plural(n, "integration to reconnect", "integrations to reconnect"))
		}
		m.Title = fmt.Sprintf("%d new problems: %s", len(evs), strings.Join(parts, ", "))
	}
	var lines []string
	for i, e := range evs {
		if i < maxGroupItems {
			m.Items = append(m.Items, Item{ID: e.ID, Kind: e.Kind, Severity: e.Severity, Title: clip(e.Title, maxTitle), Incident: incidentID(e)})
		}
		if i < 6 {
			lines = append(lines, "· "+clip(e.Title, 120))
		}
	}
	if len(evs) > 6 {
		lines = append(lines, fmt.Sprintf("and %d more in the Inbox.", len(evs)-6))
	}
	m.Body = clip(strings.Join(lines, "\n"), maxBody)
	return m
}

func incidentID(e Event) string {
	if e.Incident != nil {
		return e.Incident.ID
	}
	return ""
}

// plural: "1 down", "2 need attention".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func rank(sev string) int {
	switch sev {
	case "down":
		return 0
	case "attention":
		return 1
	}
	return 2
}

// withURLs fills URL from Path when the hub knows its public address.
func withURLs(m Message, publicURL string) Message {
	if publicURL == "" {
		return m
	}
	base := strings.TrimSuffix(publicURL, "/")
	if m.Path != "" {
		m.URL = base + m.Path
	}
	if len(m.Items) > 0 {
		items := make([]Item, len(m.Items))
		copy(items, m.Items)
		for i := range items {
			if items[i].Incident != "" {
				items[i].URL = base + incidentPath(items[i].Incident)
			}
		}
		m.Items = items
	}
	return m
}

// clip shortens s to at most n bytes on a rune boundary, with an ellipsis.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return ""
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " ") + "…"
}

// hhmm is a time as messages say it: "14:02 UTC". The hub doesn't know the
// reader's time zone.
func hhmm(t time.Time) string { return t.UTC().Format("15:04") + " UTC" }

// encodeMessage is the stored and sent form.
func encodeMessage(m Message) []byte {
	b, _ := json.Marshal(storedMessage{Message: m, Path: m.Path, Tag: m.Tag})
	return b
}

// storedMessage keeps Path and Tag, which the payload leaves out.
type storedMessage struct {
	Message
	Path string `json:"_path,omitempty"`
	Tag  string `json:"_tag,omitempty"`
}

func decodeMessage(b []byte) (Message, error) {
	var sm storedMessage
	if err := json.Unmarshal(b, &sm); err != nil {
		return Message{}, err
	}
	m := sm.Message
	m.Path, m.Tag = sm.Path, sm.Tag
	return m, nil
}
