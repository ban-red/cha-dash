package server

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/auth"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/httpx"
	"dash.cha.sh/internal/topics"
)

const (
	// historySeries is how many series one request may ask for.
	historySeries = 20
	// historyPoints is the most points a series may come back with, and
	// historyDefaultPoints the number when the request doesn't say.
	historyPoints        = 2000
	historyDefaultPoints = 300
)

// historySeriesJSON is one series of GET /api/history. Point i is at
// start + i × step (Unix ms); null is a gap.
type historySeriesJSON struct {
	Unit  string     `json:"unit"`
	Start int64      `json:"start"`
	Step  int64      `json:"step"`
	Avg   []*float32 `json:"avg"`
	Min   []*float32 `json:"min"`
	Max   []*float32 `json:"max"`
}

// registerHistory mounts the metrics history (internal/history, ADR-0031).
//
//	GET /api/history?series=<resource>#<metric>[&series=…]&from=<ms>&to=<ms>&points=<n>
//	  → 200 {"level", "series": {"<key>": {"unit", "start", "step", "avg", "min", "max"}}}
//
// from defaults to a day before to, and to to now. The tier is the finest
// that gives at most points points (default 300, at most 2,000). A series
// nobody recorded is left out. With history off (rec nil) the level is
// "off" and series is empty. Household reads only the series of resources
// its graph shows.
func registerHistory(mux *http.ServeMux, session func(http.Handler) http.Handler, rec *history.Recorder, g *graph.Graph) {
	mux.Handle("GET /api/history", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		keys := q["series"]
		if len(keys) == 0 || len(keys) > historySeries {
			httpx.Error(w, http.StatusBadRequest, "bad_series", "ask for 1 to 20 series, as series=<resource>#<metric>")
			return
		}
		now := time.Now()
		to, ok := msParam(q.Get("to"), now)
		from, ok2 := msParam(q.Get("from"), to.Add(-24*time.Hour))
		if !ok || !ok2 || !from.Before(to) {
			httpx.Error(w, http.StatusBadRequest, "bad_range", "from and to are Unix milliseconds, from before to")
			return
		}
		points := historyDefaultPoints
		if s := q.Get("points"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > historyPoints {
				httpx.Error(w, http.StatusBadRequest, "bad_points", "points is 1 to 2000")
				return
			}
			points = n
		}
		out := map[string]historySeriesJSON{}
		if rec == nil {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"level": history.Off, "series": out})
			return
		}
		id, _ := auth.FromContext(r.Context())
		whole := authz.SeesWholeGraph(id.User.Role)
		for _, key := range keys {
			if _, dup := out[key]; dup || !rec.Known(key) {
				continue
			}
			if !whole {
				res, ok := g.Get(graph.RID(strings.SplitN(key, "#", 2)[0]))
				if !ok || !topics.HouseholdFilter(&res) {
					continue
				}
			}
			rg, err := rec.Query(r.Context(), key, from, to, points)
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "couldn't read the history")
				return
			}
			out[key] = toHistoryJSON(rg)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"level": rec.Level(), "series": out})
	})))
}

func msParam(s string, def time.Time) (time.Time, bool) {
	if s == "" {
		return def, true
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil || ms < 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}

func toHistoryJSON(rg history.Range) historySeriesJSON {
	n := len(rg.Points)
	out := historySeriesJSON{Unit: rg.Unit, Start: rg.Start.UnixMilli(), Step: rg.Step.Milliseconds(),
		Avg: make([]*float32, n), Min: make([]*float32, n), Max: make([]*float32, n)}
	finite := func(v float32) *float32 {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil
		}
		return &v
	}
	for i, p := range rg.Points {
		if p.N > 0 {
			out.Avg[i], out.Min[i], out.Max[i] = finite(p.Avg), finite(p.Min), finite(p.Max)
		}
	}
	return out
}
