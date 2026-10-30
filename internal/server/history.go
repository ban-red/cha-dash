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

// registerHistory mounts the metrics and health history (internal/history,
// ADR-0031).
//
//	GET /api/history?series=<resource>#<metric>[&series=…]&from=<ms>&to=<ms>&points=<n>
//	  → 200 {"level", "series": {"<key>": {"unit", "start", "step", "avg", "min", "max"}}}
//	GET /api/history/states?resource=<id>&from=<ms>&to=<ms>
//	  → 200 {"level", "states": [{"at", "status"}], "totals": {"<status>": ms}, "up": share or null}
//
// from defaults to a day before to, and to to now. The tier is the finest
// that gives at most points points (default 300, at most 2,000). A series
// nobody recorded is left out. With history off (rec nil) the level is
// "off" and series is empty. Household reads only the series of resources
// its graph shows.
//
// states is a resource's status changes: the one in effect at from (moved
// to from), then each change, oldest first. from defaults to a week before
// to. totals is the milliseconds in each status over the range, up to now;
// up is the share of the watched time it was ok or attention (null when
// none was watched). Stopped means the hub wasn't running; gone, that the
// resource had left the graph.
func registerHistory(mux *http.ServeMux, session func(http.Handler) http.Handler, rec *history.Recorder, g *graph.Graph) {
	visible := func(r *http.Request, resource string) bool {
		id, _ := auth.FromContext(r.Context())
		if authz.SeesWholeGraph(id.User.Role) {
			return true
		}
		res, ok := g.Get(graph.RID(resource))
		return ok && topics.HouseholdFilter(&res)
	}
	mux.Handle("GET /api/history/states", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		resource := q.Get("resource")
		if resource == "" {
			httpx.Error(w, http.StatusBadRequest, "bad_resource", "resource is a resource ID")
			return
		}
		now := time.Now()
		to, ok := msParam(q.Get("to"), now)
		from, ok2 := msParam(q.Get("from"), to.Add(-7*24*time.Hour))
		if !ok || !ok2 || !from.Before(to) {
			httpx.Error(w, http.StatusBadRequest, "bad_range", "from and to are Unix milliseconds, from before to")
			return
		}
		type change struct {
			At     int64  `json:"at"`
			Status string `json:"status"`
		}
		out := map[string]any{"level": history.Off, "states": []change{}, "totals": map[string]int64{}, "up": nil}
		if rec == nil || !visible(r, resource) {
			if rec != nil {
				out["level"] = rec.Level()
			}
			httpx.WriteJSON(w, http.StatusOK, out)
			return
		}
		changes, err := rec.States(r.Context(), resource, from, to)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "history_failed", "couldn't read the history")
			return
		}
		list := make([]change, len(changes))
		for i, c := range changes {
			list[i] = change{At: c.At.UnixMilli(), Status: c.Status}
		}
		end := to
		if end.After(now) {
			end = now
		}
		sum := history.Summarize(changes, from, end)
		totals := map[string]int64{}
		for k, d := range sum.Totals {
			totals[k] = d.Milliseconds()
		}
		out["level"], out["states"], out["totals"] = rec.Level(), list, totals
		if sum.Up >= 0 {
			out["up"] = sum.Up
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	})))

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
		for _, key := range keys {
			if _, dup := out[key]; dup || !rec.Known(key) || !visible(r, strings.SplitN(key, "#", 2)[0]) {
				continue
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
