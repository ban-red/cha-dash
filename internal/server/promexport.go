package server

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"

	"dash.cha.sh/internal/buildinfo"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/topics"
)

// MetricsHandler serves /metrics in the Prometheus text format (0.0.4), for
// a listener of its own that's off by default (CHADASH_METRICS_LISTEN;
// 09 · Roadmap 2026 → E9). Nothing else is on that listener: never the API,
// never pprof. It has no session; with token set, a scrape needs
// "Authorization: Bearer <token>". Anyone who can reach it reads every
// series and every resource's health, so bind it to a private address.
//
// What it serves, sorted so a scrape is stable:
//
//	chadash_build_info{version}                         1
//	chadash_series_value{resource,metric,unit,source}   each series' current value; unknown and stale ones are left out
//	chadash_resource_health{resource,kind,source,status} 1, one line a resource, its current status
//	chadash_history_series, chadash_history_file_bytes  what history.db holds (with history on)
//	chadash_go_goroutines, chadash_go_heap_bytes, chadash_go_memory_bytes, chadash_go_gc_cycles_total
//
// Series are what the boards show (the Demo Home's in demo mode). source is
// the publishing integration's resource ID, "integration:<instance id>".
func MetricsHandler(tp *topics.Set, rec *history.Recorder, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="chadash metrics"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		bw := bufio.NewWriter(w)
		writeMetrics(r.Context(), bw, tp, rec)
		_ = bw.Flush()
	})
	return mux
}

// promSeries is the part of a metrics-topic series the exporter reads.
type promSeries struct {
	Resource string   `json:"resource"`
	Metric   string   `json:"metric"`
	Unit     string   `json:"unit"`
	Value    *float64 `json:"value"`
	Stale    bool     `json:"stale"`
}

func writeMetrics(ctx context.Context, w *bufio.Writer, tp *topics.Set, rec *history.Recorder) {
	family := func(name, typ, help string) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	family("chadash_build_info", "gauge", "The hub's version, as a label.")
	fmt.Fprintf(w, "chadash_build_info{version=%s} 1\n", promLabel(buildinfo.Version))

	_, gdoc := tp.Graph.Snapshot(nil)
	sourceOf := func(id string) string {
		if r := gdoc.Resources[graph.RID(id)]; r != nil {
			return r.Source
		}
		return ""
	}

	_, mdoc := tp.Metrics.Snapshot()
	items, _ := mdoc[topics.Series].(map[string]json.RawMessage)
	family("chadash_series_value", "gauge", "Each series' current value, as the boards show it. Unknown and stale values are left out.")
	for _, key := range slices.Sorted(maps.Keys(items)) {
		var s promSeries
		if json.Unmarshal(items[key], &s) != nil || s.Value == nil || s.Stale || math.IsNaN(*s.Value) || math.IsInf(*s.Value, 0) {
			continue
		}
		fmt.Fprintf(w, "chadash_series_value{resource=%s,metric=%s,unit=%s,source=%s} %s\n",
			promLabel(s.Resource), promLabel(s.Metric), promLabel(s.Unit), promLabel(sourceOf(s.Resource)), strconv.FormatFloat(*s.Value, 'g', -1, 64))
	}

	family("chadash_resource_health", "gauge", "1 for each resource's current health status.")
	ids := make([]string, 0, len(gdoc.Resources))
	for id := range gdoc.Resources {
		ids = append(ids, string(id))
	}
	slices.Sort(ids)
	for _, id := range ids {
		r := gdoc.Resources[graph.RID(id)]
		status := string(r.Health)
		if status == "" {
			status = "unknown"
		}
		fmt.Fprintf(w, "chadash_resource_health{resource=%s,kind=%s,source=%s,status=%s} 1\n",
			promLabel(id), promLabel(string(r.Kind)), promLabel(r.Source), promLabel(status))
	}

	if rec != nil {
		family("chadash_history_series", "gauge", "Series the metrics history knows.")
		fmt.Fprintf(w, "chadash_history_series %d\n", rec.SeriesCount())
		if b, err := rec.FileBytes(ctx); err == nil {
			family("chadash_history_file_bytes", "gauge", "The size of history.db, without its WAL.")
			fmt.Fprintf(w, "chadash_history_file_bytes %d\n", b)
		}
	}

	samples := []metrics.Sample{
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/total:bytes"},
		{Name: "/gc/cycles/total:gc-cycles"},
	}
	metrics.Read(samples)
	for i, f := range []struct{ name, typ, help string }{
		{"chadash_go_goroutines", "gauge", "Goroutines in the hub."},
		{"chadash_go_heap_bytes", "gauge", "Bytes of live and not-yet-swept heap objects."},
		{"chadash_go_memory_bytes", "gauge", "All memory the Go runtime has mapped for the hub."},
		{"chadash_go_gc_cycles_total", "counter", "Completed garbage collections."},
	} {
		if samples[i].Value.Kind() != metrics.KindUint64 {
			continue
		}
		family(f.name, f.typ, f.help)
		fmt.Fprintf(w, "%s %d\n", f.name, samples[i].Value.Uint64())
	}
}

// promLabel quotes a label value: backslash, double quote and newline are escaped.
func promLabel(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}
