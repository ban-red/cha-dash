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
	"dash.cha.sh/internal/store"
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

// historySettings is the body of GET and PUT /api/settings/history.
type historySettings struct {
	Level history.Level `json:"level"`
	// Locked: history can't be turned on from here (CHADASH_HISTORY=off, or
	// demo mode, where Demo is true).
	Locked    bool                    `json:"locked"`
	Demo      bool                    `json:"demo"`
	Series    int                     `json:"series"`
	Resources int                     `json:"resources"`
	FileBytes int64                   `json:"file_bytes"`
	Tiers     []historyTierJSON       `json:"tiers"`
	Estimates map[history.Level]int64 `json:"estimates"`
}

type historyTierJSON struct {
	Tier   string `json:"tier"`
	StepMs int64  `json:"step_ms"`
	KeepMs int64  `json:"keep_ms"` // 0: forever
	Chunks int    `json:"chunks"`
	Bytes  int64  `json:"bytes"`
	Oldest *int64 `json:"oldest"` // Unix ms of the oldest chunk's start
}

// historyEstimateFloor is the series count estimates assume at least, so a
// new hub sees what a typical one grows to.
const historyEstimateFloor = 300

// registerHistorySettings mounts Settings → History (admin).
//
//	GET  /api/settings/history         → historySettings
//	PUT  /api/settings/history         {level} → historySettings; 409 history_locked
//	POST /api/settings/history/clear   deletes every point and health row → historySettings
//
// The level is kept in the hub's settings (history.LevelKey) and applies at
// once. Estimates are how big history.db grows at each level with the
// series recorded so far (at least 300).
func registerHistorySettings(mux *http.ServeMux, session func(http.Handler) http.Handler, rec *history.Recorder, st *store.Store, demo bool, audit *authz.Recorder) {
	view := func(w http.ResponseWriter, r *http.Request) {
		out := historySettings{Level: history.Off, Locked: rec == nil, Demo: demo, Tiers: []historyTierJSON{}, Estimates: map[history.Level]int64{}}
		if rec != nil {
			s, err := rec.Stats(r.Context())
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "couldn't read the history")
				return
			}
			out.Level, out.Series, out.Resources, out.FileBytes = s.Level, s.Series, s.Resources, s.FileBytes
			for _, t := range s.Tiers {
				tj := historyTierJSON{Tier: t.Tier, StepMs: t.Step.Milliseconds(), KeepMs: t.Keep.Milliseconds(), Chunks: t.Chunks, Bytes: t.Bytes}
				if !t.Oldest.IsZero() {
					ms := t.Oldest.UnixMilli()
					tj.Oldest = &ms
				}
				out.Tiers = append(out.Tiers, tj)
			}
			for _, l := range []history.Level{history.Standard, history.LongTerm} {
				out.Estimates[l] = history.Estimate(l, max(s.Series, historyEstimateFloor))
			}
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
	locked := func(w http.ResponseWriter) bool {
		if rec == nil {
			httpx.Error(w, http.StatusConflict, "history_locked", "History is off for this hub (CHADASH_HISTORY=off, or demo mode).")
			return true
		}
		return false
	}
	mux.Handle("GET /api/settings/history", session(http.HandlerFunc(view)))
	mux.Handle("PUT /api/settings/history", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Level string `json:"level"`
		}
		if !httpx.DecodeJSON(w, r, &body) || locked(w) {
			return
		}
		l, err := history.ParseLevel(body.Level)
		if err != nil {
			httpx.Error(w, http.StatusUnprocessableEntity, "bad_level", "level is off, standard or long-term")
			return
		}
		from := rec.Level()
		if err := rec.SetLevel(r.Context(), l); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "history_failed", "couldn't change the history level")
			return
		}
		if err := history.SaveLevel(r.Context(), st, l, time.Now()); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "history_failed", "the level applies now but couldn't be saved for the next start")
			return
		}
		audit.Change(r, authz.Audited["PUT /api/settings/history"], "settings:history", map[string]string{"from": string(from), "to": string(l)}, authz.T1, nil)
		view(w, r)
	})))
	mux.Handle("POST /api/settings/history/clear", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if locked(w) {
			return
		}
		if err := rec.Clear(r.Context()); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "history_failed", "couldn't clear the history")
			return
		}
		audit.Change(r, authz.Audited["POST /api/settings/history/clear"], "settings:history", nil, authz.T3, nil)
		view(w, r)
	})))
}
