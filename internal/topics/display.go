package topics

import (
	"context"

	"dash.cha.sh/internal/realtime"
)

// DisplaySource routes the topics a display (wave 33) reads: household's
// view of each. The graph, its causes and the metrics are household's
// whole, because a tile's widget reads neighbours (runs_on, part_of,
// same-as links) a board's items don't name, the reason household's graph
// isn't scoped either (internal/README.md, "Realtime topics"). The inbox
// and backups are the views the server builds for the display's board.
// There's no tasks topic: a display starts nothing.
func (s *Set) DisplaySource(inbox func(ctx context.Context) InboxView, backups func(ctx context.Context) BackupsView) realtime.Source {
	household := func(context.Context) bool { return false }
	return realtime.Mux{
		Graph:   s.GraphSource(household),
		Metrics: metricsSource{doc: s.Metrics},
		Causes:  s.CausesSource(household),
		Inbox:   s.InboxSource(inbox),
		Backups: s.BackupsSource(backups),
	}
}
