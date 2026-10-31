package server

import (
	"context"
	"net/http"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/topics"
)

// registerReads mounts plain reads of the live topics, for API tokens
// (graph.read, inbox.read) and anyone signed in. The web app reads the same
// documents over the realtime socket.
//
//	GET /api/graph   → 200 {"version", "resources", "relations"}: the Home Graph as the caller's role sees it
//	GET /api/inbox   → 200 {"now", "incidents"}: the Inbox as the caller's role sees it
//	GET /api/causes?resource=<id> → 200 {"resource", "causes": [Cause with path]}: one resource's cause hints;
//	                 household gets none for what its graph leaves out
func registerReads(mux *http.ServeMux, session func(http.Handler) http.Handler, tp *topics.Set, view func(ctx context.Context, role string) topics.InboxView) {
	mux.Handle("GET /api/graph", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		v, doc := tp.Graph.Snapshot(nil)
		if !authz.SeesWholeGraph(id.User.Role) {
			doc = topics.HouseholdGraph(doc) // as the graph topic shows it to household
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"version": v, "resources": doc.Resources, "relations": doc.Relations})
	})))
	mux.Handle("GET /api/inbox", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		_, doc := tp.Inbox.Snapshot()
		httpx.WriteJSON(w, http.StatusOK, topics.ProjectInbox(doc, view(r.Context(), id.User.Role)))
	})))
	mux.Handle("GET /api/causes", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := r.URL.Query().Get("resource")
		if res == "" || len(res) > 512 {
			httpx.Error(w, http.StatusBadRequest, "invalid", "resource must name a resource.")
			return
		}
		id, _ := auth.FromContext(r.Context())
		cs := []topics.Cause{}
		if authz.SeesWholeGraph(id.User.Role) || tp.HouseholdSees(res) {
			cs = append(cs, tp.Causes.Of(res)...)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"resource": res, "causes": cs})
	})))
}
