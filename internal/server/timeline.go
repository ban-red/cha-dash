package server

import (
	"context"
	"net/http"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/incidents"
	"dash.cha.sh/internal/msg"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/timeline"
	"dash.cha.sh/internal/topics"
)

const (
	// timelineDefault is the range when the request names no from.
	timelineDefault = 7 * 24 * time.Hour
	// timelineMaxDays bounds one request: a little more than the default
	// incident retention.
	timelineMaxDays = 92
)

// registerTimeline mounts the timeline (internal/timeline).
//
//	GET /api/timeline?resource=<id>&from=<ms>&to=<ms>
//	  → 200 {"from", "to", "resource"?, "entries": [Entry], "truncated", "history"}
//
// resource narrows it to one resource (its incidents, the actions on it,
// its health, the windows on its hosts); without it, the whole home. to
// defaults to now and from to a week before to; at most 92 days apart.
// Entries are oldest first, at most 1,000 (the newest; truncated says so).
// Viewer and up see everything; household sees its inbox's projection
// (problems on its boards, no causes, no security findings), the health of
// what its graph shows, and only its own actions.
func registerTimeline(mux *http.ServeMux, session func(http.Handler) http.Handler, src timeline.Sources, g *graph.Graph,
	view func(ctx context.Context, role string) topics.InboxView) {
	mux.Handle("GET /api/timeline", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		resource := q.Get("resource")
		if len(resource) > 512 {
			httpx.Say(w, http.StatusBadRequest, "bad_resource", msg.New("timeline.bad_resource"))
			return
		}
		now := time.Now()
		to, ok := msParam(q.Get("to"), now)
		from, ok2 := msParam(q.Get("from"), to.Add(-timelineDefault))
		if !ok || !ok2 || !from.Before(to) || to.Sub(from) > timelineMaxDays*24*time.Hour {
			httpx.Say(w, http.StatusBadRequest, "bad_range", msg.New("timeline.bad_range", "days", timelineMaxDays))
			return
		}
		id, _ := auth.FromContext(r.Context())
		v := timeline.View{Whole: authz.SeesInboxDetail(id.User.Role), User: id.User.ID}
		if !v.Whole {
			v.Relevant = view(r.Context(), id.User.Role).Relevant
			v.Visible = func(res string) bool {
				x, ok := g.Get(graph.RID(res))
				return ok && topics.HouseholdFilter(&x)
			}
		}
		tl, err := timeline.Build(r.Context(), src, timeline.Query{Resource: resource, From: from, To: to}, v)
		if err != nil {
			httpx.Say(w, http.StatusInternalServerError, "timeline_failed", msg.New("timeline.failed"))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, tl)
	})))
}

// timelineSources wires the timeline to the hub's stores: names from the
// graph, action titles from the engine, a resource's hosts along runs_on.
func timelineSources(inc *incidents.Store, log *audit.Log, rec *history.Recorder, st *store.Store, g *graph.Graph, eng *action.Engine) timeline.Sources {
	src := timeline.Sources{Incidents: inc, Audit: log, DB: st.Reader()}
	if rec != nil {
		src.History = rec
	}
	if g != nil {
		src.Name = func(id string) string {
			if x, ok := g.Get(graph.RID(id)); ok {
				return x.Name
			}
			return ""
		}
		src.Hosts = func(id string) []string {
			_, doc := g.Snapshot(nil)
			next := map[graph.RID]graph.RID{}
			for _, rel := range doc.Relations {
				if rel.Type == "runs_on" {
					next[rel.From] = rel.To
				}
			}
			var out []string
			cur := graph.RID(id)
			for range 4 { // container → engine → guest → host
				to, ok := next[cur]
				if !ok {
					break
				}
				out = append(out, string(to))
				cur = to
			}
			return out
		}
	}
	if eng != nil {
		reg := eng.Registry()
		src.ActionTitle = func(id string) string {
			if d, ok := reg.Get(id); ok {
				return d.Title
			}
			return ""
		}
	}
	return src
}
