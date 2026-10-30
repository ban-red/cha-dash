package server

import (
	"math"
	"net/http"
	"slices"
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
	// pulsePoints bounds the slots of GET /api/history/pulse; 96 is 24
	// hours by the quarter hour.
	pulsePoints        = 288
	pulseDefaultPoints = 96
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
//	GET /api/history/pulse?from=<ms>&to=<ms>&points=<n>
//	  → 200 {"level", "start", "step", "slots": [{"counts": {"<status>": n}, "issues": {"<resource>": "<status>"}}]}
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
//
// pulse is the whole home's health for the Briefing's Pulse strip: the
// range in points equal slots (default 96, at most 288; from defaults to a
// day before to). Each slot counts the resources by the worst they were
// during it (ok, attention, repair, down; unwatched ones aren't counted),
// and issues names the ones that weren't ok. Household counts only what its
// graph shows.
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

	mux.Handle("GET /api/history/pulse", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		now := time.Now()
		to, ok := msParam(q.Get("to"), now)
		from, ok2 := msParam(q.Get("from"), to.Add(-24*time.Hour))
		if !ok || !ok2 || !from.Before(to) {
			httpx.Error(w, http.StatusBadRequest, "bad_range", "from and to are Unix milliseconds, from before to")
			return
		}
		points := pulseDefaultPoints
		if s := q.Get("points"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > pulsePoints {
				httpx.Error(w, http.StatusBadRequest, "bad_points", "points is 1 to 288")
				return
			}
			points = n
		}
		type slot struct {
			Counts map[string]int    `json:"counts"`
			Issues map[string]string `json:"issues"`
		}
		if rec == nil {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"level": history.Off, "start": from.UnixMilli(), "step": 0, "slots": []slot{}})
			return
		}
		p, err := rec.Pulse(r.Context(), from, to, points, func(id string) bool { return visible(r, id) })
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "history_failed", "couldn't read the history")
			return
		}
		slots := make([]slot, len(p.Slots))
		for i, s := range p.Slots {
			slots[i] = slot{Counts: s.Counts, Issues: s.Issues}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"level": rec.Level(), "start": p.Start.UnixMilli(), "step": p.Step.Milliseconds(), "slots": slots})
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
		if to.After(now) {
			to = now // nothing is recorded ahead of now
		}
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
	Locked    bool              `json:"locked"`
	Demo      bool              `json:"demo"`
	MaxBytes  int64             `json:"max_bytes"`  // 0: no limit
	TrimmedAt *int64            `json:"trimmed_at"` // when points were last dropped to stay under it
	Series    int               `json:"series"`
	Resources int               `json:"resources"`
	FileBytes int64             `json:"file_bytes"`
	Tiers     []historyTierJSON `json:"tiers"`
	// Instances is what each instance's series hold, most series first;
	// Excluded, the instances whose metrics aren't recorded.
	Instances []historyInstanceJSON `json:"instances"`
	Excluded  []string              `json:"excluded"`
	// ExcludeKeys are the metric patterns left out, with how many series
	// each matches.
	ExcludeKeys []historyPatternJSON    `json:"exclude_keys"`
	Estimates   map[history.Level]int64 `json:"estimates"`
}

type historyPatternJSON struct {
	Pattern string `json:"pattern"`
	Series  int    `json:"series"`
}

type historyInstanceJSON struct {
	ID     string `json:"id"`
	Series int    `json:"series"`
	Bytes  int64  `json:"bytes"`
}

type historyTierJSON struct {
	Tier   string `json:"tier"`
	StepMs int64  `json:"step_ms"`
	KeepMs int64  `json:"keep_ms"` // 0: forever
	// DefaultKeepMs is what the level keeps, and Custom whether keep_ms
	// was set instead; ChoicesMs are the retentions to offer, shortest
	// first (0: forever).
	DefaultKeepMs int64   `json:"default_keep_ms"`
	Custom        bool    `json:"custom"`
	ChoicesMs     []int64 `json:"choices_ms"`
	Chunks        int     `json:"chunks"`
	Bytes         int64   `json:"bytes"`
	Oldest        *int64  `json:"oldest"` // Unix ms of the oldest chunk's start
}

// historyExcludeMax bounds the exclusions, a list of instance IDs.
const historyExcludeMax = 500

// historyMinLimit is the smallest size limit: below it, a busy hub would
// trim away most of its 48 hours.
const historyMinLimit = 64 << 20

// historyEstimateFloor is the series count estimates assume at least, so a
// new hub sees what a typical one grows to.
const historyEstimateFloor = 300

// registerHistorySettings mounts Settings → History (admin).
//
//	GET  /api/settings/history         → historySettings
//	PUT  /api/settings/history         {level?, max_bytes?, exclude?, exclude_keys?, keep?} → historySettings; 409 history_locked
//	POST /api/settings/history/clear   deletes every point and health row → historySettings;
//	                                   ?instance=<id>: only that instance's series and points
//
// The level and the size limit (max_bytes, 0 for none, else at least
// 64 MB) are kept in the hub's settings (history.LevelKey, LimitKey) and
// apply at once; retention enforces the limit within the hour. exclude
// replaces the instances whose metrics aren't recorded (their health still
// is); what they recorded stays until it ages out or is cleared.
// exclude_keys replaces the metric patterns left out ("docker:*#net_rx", *
// matching any run). keep replaces per-tier retention, tier name to
// milliseconds (0: forever), within each tier's choices; a tier left out or
// null keeps its level's. Estimates are how big history.db grows at each
// level with the series recorded so far (at least 300).
func registerHistorySettings(mux *http.ServeMux, session func(http.Handler) http.Handler, rec *history.Recorder, st *store.Store, demo bool, audit *authz.Recorder) {
	view := func(w http.ResponseWriter, r *http.Request) {
		out := historySettings{Level: history.Off, Locked: rec == nil, Demo: demo, Tiers: []historyTierJSON{}, Estimates: map[history.Level]int64{},
			Instances: []historyInstanceJSON{}, Excluded: []string{}, ExcludeKeys: []historyPatternJSON{}}
		if rec != nil {
			s, err := rec.Stats(r.Context())
			if err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "couldn't read the history")
				return
			}
			out.Level, out.Series, out.Resources, out.FileBytes, out.MaxBytes = s.Level, s.Series, s.Resources, s.FileBytes, s.MaxBytes
			if !s.TrimmedAt.IsZero() {
				ms := s.TrimmedAt.UnixMilli()
				out.TrimmedAt = &ms
			}
			for _, t := range s.Tiers {
				tj := historyTierJSON{Tier: t.Tier, StepMs: t.Step.Milliseconds(), KeepMs: t.Keep.Milliseconds(), DefaultKeepMs: t.Default.Milliseconds(),
					Custom: t.Custom, Chunks: t.Chunks, Bytes: t.Bytes, ChoicesMs: []int64{}}
				if tier, ok := history.ParseTier(t.Tier); ok {
					for _, c := range history.KeepChoices[tier] {
						tj.ChoicesMs = append(tj.ChoicesMs, c.Milliseconds())
					}
				}
				if !t.Oldest.IsZero() {
					ms := t.Oldest.UnixMilli()
					tj.Oldest = &ms
				}
				out.Tiers = append(out.Tiers, tj)
			}
			for _, in := range s.Instances {
				out.Instances = append(out.Instances, historyInstanceJSON{ID: in.ID, Series: in.Series, Bytes: in.Bytes})
			}
			out.Excluded = append(out.Excluded, rec.Excluded()...)
			for _, p := range rec.ExcludedKeys() {
				out.ExcludeKeys = append(out.ExcludeKeys, historyPatternJSON{Pattern: p, Series: rec.Matching(p)})
			}
			keep := rec.Keep()
			for _, l := range []history.Level{history.Standard, history.LongTerm} {
				out.Estimates[l] = history.EstimateKeep(l, keep, max(s.Series, historyEstimateFloor))
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
			Level       *string            `json:"level"`
			MaxBytes    *int64             `json:"max_bytes"`
			Exclude     *[]string          `json:"exclude"`
			ExcludeKeys *[]string          `json:"exclude_keys"`
			Keep        *map[string]*int64 `json:"keep"`
		}
		if !httpx.DecodeJSON(w, r, &body) || locked(w) {
			return
		}
		if body.Level == nil && body.MaxBytes == nil && body.Exclude == nil && body.ExcludeKeys == nil && body.Keep == nil {
			httpx.Error(w, http.StatusUnprocessableEntity, "nothing_to_change", "send level, max_bytes, exclude, exclude_keys or keep")
			return
		}
		if body.Exclude != nil && len(*body.Exclude) > historyExcludeMax {
			httpx.Error(w, http.StatusUnprocessableEntity, "bad_exclude", "exclude lists at most 500 instances")
			return
		}
		var patterns []string
		if body.ExcludeKeys != nil {
			if len(*body.ExcludeKeys) > history.MaxKeyPatterns {
				httpx.Error(w, http.StatusUnprocessableEntity, "bad_exclude_keys", "exclude_keys lists at most 200 patterns")
				return
			}
			patterns = []string{}
			for _, p := range *body.ExcludeKeys {
				p = strings.TrimSpace(p)
				if err := history.CheckKeyPattern(p); err != nil {
					httpx.Error(w, http.StatusUnprocessableEntity, "bad_exclude_keys", strings.TrimPrefix(err.Error(), "history: "))
					return
				}
				if !slices.Contains(patterns, p) {
					patterns = append(patterns, p)
				}
			}
		}
		var keep map[history.Tier]time.Duration
		if body.Keep != nil {
			keep = map[history.Tier]time.Duration{}
			for name, ms := range *body.Keep {
				t, ok := history.ParseTier(name)
				if !ok || ms != nil && *ms < 0 {
					httpx.Error(w, http.StatusUnprocessableEntity, "bad_keep", "keep maps 1m, 15m, 1h or 1d to milliseconds, or null for the default")
					return
				}
				if ms != nil {
					keep[t] = time.Duration(*ms) * time.Millisecond
				}
			}
			if err := history.CheckKeep(keep); err != nil {
				httpx.Error(w, http.StatusUnprocessableEntity, "bad_keep", strings.TrimPrefix(err.Error(), "history: "))
				return
			}
		}
		var l history.Level
		if body.Level != nil {
			var err error
			if l, err = history.ParseLevel(*body.Level); err != nil {
				httpx.Error(w, http.StatusUnprocessableEntity, "bad_level", "level is off, standard or long-term")
				return
			}
		}
		if m := body.MaxBytes; m != nil && *m != 0 && *m < historyMinLimit {
			httpx.Error(w, http.StatusUnprocessableEntity, "bad_limit", "max_bytes is 0 (no limit) or at least 64 MB")
			return
		}
		params := map[string]any{}
		now := time.Now()
		if body.Level != nil {
			params["level_from"], params["level_to"] = rec.Level(), l
			if err := rec.SetLevel(r.Context(), l); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "couldn't change the history level")
				return
			}
			if err := history.SaveLevel(r.Context(), st, l, now); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "the level applies now but couldn't be saved for the next start")
				return
			}
		}
		if m := body.MaxBytes; m != nil {
			params["max_bytes_from"], params["max_bytes_to"] = rec.Limit(), *m
			rec.SetLimit(*m)
			if err := history.SaveLimit(r.Context(), st, *m, now); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "the limit applies now but couldn't be saved for the next start")
				return
			}
		}
		if body.Exclude != nil {
			ids := slices.Compact(slices.Sorted(slices.Values(*body.Exclude)))
			params["exclude_from"], params["exclude_to"] = rec.Excluded(), ids
			rec.SetExcluded(ids)
			if err := history.SaveExcluded(r.Context(), st, ids, now); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "the exclusions apply now but couldn't be saved for the next start")
				return
			}
		}
		if patterns != nil {
			params["exclude_keys_from"], params["exclude_keys_to"] = rec.ExcludedKeys(), patterns
			rec.SetExcludedKeys(patterns)
			if err := history.SaveExcludedKeys(r.Context(), st, patterns, now); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "the exclusions apply now but couldn't be saved for the next start")
				return
			}
		}
		if keep != nil {
			params["keep_from"], params["keep_to"] = keepParams(rec.Keep()), keepParams(keep)
			rec.SetKeep(keep)
			if err := history.SaveKeep(r.Context(), st, keep, now); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "the retention applies now but couldn't be saved for the next start")
				return
			}
		}
		audit.Change(r, authz.Audited["PUT /api/settings/history"], "settings:history", params, authz.T1, nil)
		view(w, r)
	})))
	mux.Handle("POST /api/settings/history/clear", session(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if locked(w) {
			return
		}
		if instance := r.URL.Query().Get("instance"); instance != "" {
			if err := rec.ClearInstance(r.Context(), instance); err != nil {
				httpx.Error(w, http.StatusInternalServerError, "history_failed", "couldn't clear that integration's history")
				return
			}
			audit.Change(r, authz.Audited["POST /api/settings/history/clear"], "integration:"+instance, nil, authz.T3, nil)
			view(w, r)
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

// keepParams is per-tier retention for the audit log, by tier name.
func keepParams(over map[history.Tier]time.Duration) map[string]int64 {
	out := map[string]int64{}
	for t, k := range over {
		out[history.TierNames[t]] = k.Milliseconds()
	}
	return out
}
