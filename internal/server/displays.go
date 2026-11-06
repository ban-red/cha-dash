package server

import (
	"context"
	"log/slog"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/boards"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/realtime"
	"dash.cha.sh/internal/topics"
)

// A display (wave 33) reads its one board: household's view of each topic,
// with the incidents and backup coverage narrowed to what
// boards.BoardScope says that board shows (topics.Set.DisplaySource says
// why the graph and metrics aren't). A board whose scope is All (never
// saved, or it has the Incidents widget) shows household's whole view. The
// scope is read when a topic is subscribed.

// displayScope is the scope of the board the display on ctx shows; ok is
// false when there's no display or no such board, and then it shows
// nothing.
func displayScope(ctx context.Context, b *boards.Service, log *slog.Logger) (boards.Scope, bool) {
	id, ok := auth.FromContext(ctx)
	if !ok || id.Display == nil {
		return boards.Scope{}, false
	}
	sc, ok, err := b.BoardScope(ctx, id.Display.BoardID)
	if err != nil {
		log.Warn("reading what a display's board shows failed; it shows nothing", "display", id.Display.ID, "err", err)
		return boards.Scope{}, false
	}
	return sc, ok
}

// DisplaySource is the realtime source of a display's socket
// (GET /api/display/ws): topics.Set.DisplaySource with the display's
// board's scope.
func DisplaySource(tp *topics.Set, b *boards.Service, g *graph.Graph, log *slog.Logger) realtime.Source {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	relevant := func(sc boards.Scope) func(string) bool {
		return func(id string) bool {
			if sc.All || sc.IDs[id] {
				return true
			}
			r, ok := g.Get(graph.RID(id))
			return ok && sc.Shows(id, string(r.Kind))
		}
	}
	inbox := func(ctx context.Context) topics.InboxView {
		sc, ok := displayScope(ctx, b, log)
		if !ok {
			return topics.InboxView{Relevant: func(string) bool { return false }}
		}
		return topics.InboxView{Relevant: relevant(sc)}
	}
	backups := func(ctx context.Context) topics.BackupsView {
		sc, ok := displayScope(ctx, b, log)
		switch {
		case !ok:
			return topics.BackupsView{}
		case sc.All || sc.Backups:
			return topics.BackupsView{Whole: true}
		}
		return topics.BackupsView{Relevant: relevant(sc)}
	}
	return tp.DisplaySource(inbox, backups)
}

// DisplaySocketRecheck is realtime.Options.Recheck for displays' sockets:
// whether the display is still there and not revoked.
func DisplaySocketRecheck(a *auth.Service) func(ctx context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		id, ok := auth.FromContext(ctx)
		if !ok {
			return false, nil
		}
		return a.DisplayStillValid(ctx, id)
	}
}

// closeDisplaySockets closes a revoked display's sockets at once.
func closeDisplaySockets(a *auth.Service, h *realtime.Hub) {
	a.OnEvent(func(e auth.Event) {
		if e.Kind != auth.EventDisplayRevoked {
			return
		}
		go h.CloseWhere(func(ctx context.Context) bool {
			id, ok := auth.FromContext(ctx)
			return ok && id.Display != nil && id.Display.ID == e.Subject
		}, "display revoked")
	})
}
