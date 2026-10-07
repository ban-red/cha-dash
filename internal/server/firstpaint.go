package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/firstpaint"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/topics"
)

// firstPaint returns the snapshot index.html embeds for r: for any valid
// session, never without one, and nil when building it fails or it's over
// the cap (the page then loads from the socket as usual). The inbox in it is
// the one the role's inbox topic shows (Builder.Inbox); a builder without
// that view inlines nothing for roles that don't see the Inbox in full. The
// graph likewise follows Builder.WholeGraph (household's view below
// viewer), and a builder without it inlines nothing for household.
func firstPaint(d Deps, r *http.Request) []byte {
	id, ok := d.Auth.Identify(r)
	if !ok || !authz.SignedIn.Allows(id.User.Role) {
		return nil
	}
	if d.FirstPaint.Inbox == nil && !authz.SeesInboxDetail(id.User.Role) {
		return nil
	}
	if d.FirstPaint.WholeGraph == nil && !authz.SeesWholeGraph(id.User.Role) {
		return nil // no household view of the graph to inline
	}
	b, err := d.FirstPaint.Build(r.Context(), firstpaint.User{ID: id.User.ID, Username: id.User.Username, Role: id.User.Role})
	var big *firstpaint.TooLargeError
	switch {
	case errors.As(err, &big):
		d.Log.Debug("first-paint snapshot not inlined", "err", err)
		return nil
	case err != nil:
		d.Log.Warn("first-paint snapshot failed", "err", err)
		return nil
	}
	return b
}

// InboxView is how each role sees the inbox, for the inbox topic and the
// first paint alike. authz decides who sees it in full; the household role
// sees topics.ProjectIncident's projection of the incidents about what the
// boards show (boards.Scope, read once per call), with each resource's kind
// read from g. Anyone else sees nothing.
func InboxView(b *boards.Service, g *graph.Graph, log *slog.Logger) func(ctx context.Context, role string) topics.InboxView {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return func(ctx context.Context, role string) topics.InboxView {
		if authz.SeesInboxDetail(role) {
			return topics.InboxView{Detail: true, Approvals: authz.SeesApprovals(role)}
		}
		if !authz.SignedIn.Allows(role) {
			return topics.InboxView{}
		}
		sc, err := b.Scope(ctx)
		if err != nil {
			log.Warn("reading what the boards show failed; the household inbox is empty", "err", err)
			return topics.InboxView{}
		}
		return topics.InboxView{Relevant: func(id string) bool {
			if sc.All || sc.IDs[id] {
				return true
			}
			r, ok := g.Get(graph.RID(id))
			return ok && sc.Shows(id, string(r.Kind))
		}}
	}
}

// GraphViewer says whether a realtime connection sees the whole graph
// (authz.SeesWholeGraph for the role of the identity on its context); the
// graph topic gives the others household's view (topics.Set.GraphSource).
func GraphViewer() func(ctx context.Context) bool {
	return func(ctx context.Context) bool {
		id, ok := auth.FromContext(ctx)
		return ok && authz.SeesWholeGraph(id.User.Role)
	}
}

// InboxViewer is InboxView for a realtime connection: the role of the
// identity auth.Require attached to its context.
func InboxViewer(b *boards.Service, g *graph.Graph, log *slog.Logger) func(ctx context.Context) topics.InboxView {
	view := InboxView(b, g, log)
	return func(ctx context.Context) topics.InboxView {
		id, ok := auth.FromContext(ctx)
		if !ok {
			return topics.InboxView{}
		}
		return view(ctx, id.User.Role)
	}
}
