package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/topics"
	"dash.cha.sh/internal/updates"
)

// The home at a glance, for a reader that isn't a person at a board: Home
// Assistant's Cha Dash integration (plan 15, "Home Assistant both ways",
// W4) polls it with an inbox.read API token and makes entities of it, so an
// automation can flash a light when something breaks.
//
//	GET /api/summary → 200 Summary, as the caller's role sees the Inbox

// SummaryTop is how many open problems Summary lists, worst first.
const SummaryTop = 5

// Summary is GET /api/summary's answer.
type Summary struct {
	// Now is the inbox clock (RFC 3339).
	Now string `json:"now"`
	// Health is the home's: down when an open problem nobody has
	// acknowledged is down, attention when any problem is open, else ok.
	Health string `json:"health"`
	// Incidents counts the open problems the caller sees.
	Incidents SummaryCounts `json:"incidents"`
	// Top are the worst open problems: unacknowledged first, down before
	// attention, newest first.
	Top []SummaryIncident `json:"top"`
	// Updates counts the updates waiting; null for a role that doesn't see
	// Settings → Updates, or a hub without it.
	Updates *SummaryUpdates `json:"updates"`
}

// SummaryCounts counts open problems.
type SummaryCounts struct {
	Open         int `json:"open"`
	Down         int `json:"down"`
	Attention    int `json:"attention"`
	Acknowledged int `json:"acknowledged"`
}

// SummaryIncident is one open problem.
type SummaryIncident struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Severity       string `json:"severity"`
	Target         string `json:"target,omitempty"`
	OpenedAt       string `json:"opened_at,omitempty"`
	AcknowledgedBy string `json:"acknowledged_by,omitempty"`
}

// SummaryUpdates counts the containers with an update available.
type SummaryUpdates struct {
	Available int `json:"available"`
}

func registerSummary(mux *http.ServeMux, session func(http.Handler) http.Handler, tp *topics.Set, view func(ctx context.Context, role string) topics.InboxView, up *updates.Service, log *slog.Logger) {
	mux.Handle("GET /api/summary", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		_, doc := tp.Inbox.Snapshot()
		doc = topics.ProjectInbox(doc, view(r.Context(), id.User.Role))
		s := summarize(doc)
		if up != nil && authz.SeesWholeGraph(id.User.Role) {
			rows, err := up.List(r.Context())
			if err != nil {
				log.Warn("summary: listing updates failed", "err", err)
			} else {
				n := 0
				for _, row := range rows {
					if row.State == "available" {
						n++
					}
				}
				s.Updates = &SummaryUpdates{Available: n}
			}
		}
		httpx.WriteJSON(w, http.StatusOK, s)
	})))
}

// summarize reads an inbox document as the caller sees it.
func summarize(doc map[string]any) Summary {
	s := Summary{Health: "ok", Top: []SummaryIncident{}}
	s.Now, _ = doc[topics.Now].(string)
	items, _ := doc[topics.Incidents].(map[string]json.RawMessage)
	type open struct {
		SummaryIncident
		acked bool
	}
	var all []open
	for _, raw := range items {
		var in struct {
			ID           string `json:"id"`
			Class        string `json:"class"`
			Severity     string `json:"severity"`
			Status       string `json:"status"`
			Title        string `json:"title"`
			Target       string `json:"target"`
			OpenedAt     string `json:"opened_at"`
			Acknowledged *struct {
				By string `json:"by"`
			} `json:"acknowledged"`
		}
		if json.Unmarshal(raw, &in) != nil || in.Class != "problem" || in.Status != "open" {
			continue
		}
		if in.Severity != "down" && in.Severity != "attention" {
			continue
		}
		o := open{SummaryIncident: SummaryIncident{ID: in.ID, Title: in.Title, Severity: in.Severity, Target: in.Target, OpenedAt: in.OpenedAt}}
		if in.Acknowledged != nil {
			o.acked, o.AcknowledgedBy = true, in.Acknowledged.By
			s.Incidents.Acknowledged++
		}
		s.Incidents.Open++
		if in.Severity == "down" {
			s.Incidents.Down++
			if !o.acked {
				s.Health = "down"
			}
		} else {
			s.Incidents.Attention++
		}
		all = append(all, o)
	}
	if s.Health == "ok" && s.Incidents.Open > 0 {
		s.Health = "attention"
	}
	rank := func(o open) int {
		r := 0
		if o.acked {
			r += 2
		}
		if o.Severity != "down" {
			r++
		}
		return r
	}
	slices.SortStableFunc(all, func(a, b open) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		if a.OpenedAt != b.OpenedAt {
			if a.OpenedAt > b.OpenedAt {
				return -1
			}
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	for i, o := range all {
		if i == SummaryTop {
			break
		}
		s.Top = append(s.Top, o.SummaryIncident)
	}
	return s
}
