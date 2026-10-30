package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
)

func TestMetricsHandler(t *testing.T) {
	tp := topics.New(graph.New(), time.Now())
	if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
		if err := tx.Upsert(graph.Resource{ID: "host:pve", Kind: "host", Name: "pve", Source: "integration:i_pve", Health: graph.HealthOK}); err != nil {
			return err
		}
		return tx.Upsert(graph.Resource{ID: `app:odd"name`, Kind: "app", Name: "odd", Source: "integration:i_x", Health: "down"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tp.Metrics.Update(func(tx *livedoc.Tx) error {
		tx.Put(topics.Series, "host:pve#cpu", map[string]any{"resource": "host:pve", "metric": "cpu", "unit": "%", "value": 12.5})
		tx.Put(topics.Series, "host:pve#mem", map[string]any{"resource": "host:pve", "metric": "mem", "unit": "MiB", "value": 2048, "stale": true})
		tx.Put(topics.Series, "host:pve#temp", map[string]any{"resource": "host:pve", "metric": "temp", "unit": "°C", "value": nil})
		tx.Put(topics.Series, `app:odd"name#rx`, map[string]any{"resource": `app:odd"name`, "metric": "rx", "unit": "B/s", "value": 1.5e9})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	hst, err := store.OpenHistory(t.Context(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hst.Close() })
	if _, err := hst.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec, err := history.Open(t.Context(), history.Options{Store: hst, Level: history.Standard})
	if err != nil {
		t.Fatal(err)
	}
	rec.Record("host:pve#cpu", "i_pve", "%", time.Now(), 12.5)

	srv := httptest.NewServer(MetricsHandler(tp, rec, ""))
	t.Cleanup(srv.Close)
	get := func(path, token string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header
	}
	status, body, h := get("/metrics", "")
	if status != http.StatusOK || !strings.HasPrefix(h.Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("%d %s", status, h.Get("Content-Type"))
	}
	for _, want := range []string{
		"# TYPE chadash_series_value gauge\n",
		`chadash_series_value{resource="app:odd\"name",metric="rx",unit="B/s",source="integration:i_x"} 1.5e+09` + "\n",
		`chadash_series_value{resource="host:pve",metric="cpu",unit="%",source="integration:i_pve"} 12.5` + "\n",
		`chadash_resource_health{resource="host:pve",kind="host",source="integration:i_pve",status="ok"} 1` + "\n",
		`chadash_resource_health{resource="app:odd\"name",kind="app",source="integration:i_x",status="down"} 1` + "\n",
		"chadash_history_series 1\n",
		"# TYPE chadash_go_gc_cycles_total counter\n",
		"chadash_build_info{version=",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	// Stale and unknown values are left out, and the order is stable.
	if strings.Contains(body, `metric="mem"`) || strings.Contains(body, `metric="temp"`) {
		t.Errorf("a stale or unknown value was served:\n%s", body)
	}
	if strings.Index(body, `resource="app:odd`) > strings.Index(body, `resource="host:pve",metric="cpu"`) {
		t.Error("series aren't sorted by key")
	}
	if _, again, _ := get("/metrics", ""); stripGo(again) != stripGo(body) {
		t.Error("two scrapes differ beyond the Go runtime's numbers")
	}
	// Nothing else is on this listener.
	if status, _, _ := get("/api/graph", ""); status != http.StatusNotFound {
		t.Errorf("/api/graph on the metrics listener: %d", status)
	}

	// With a token.
	locked := httptest.NewServer(MetricsHandler(tp, nil, "fake-scrape-token"))
	t.Cleanup(locked.Close)
	srv = locked
	if status, _, h := get("/metrics", ""); status != http.StatusUnauthorized || h.Get("WWW-Authenticate") == "" {
		t.Fatalf("no token: %d", status)
	}
	if status, _, _ := get("/metrics", "wrong"); status != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", status)
	}
	if status, body, _ := get("/metrics", "fake-scrape-token"); status != http.StatusOK || strings.Contains(body, "chadash_history_") {
		t.Fatalf("right token, no history: %d\n%s", status, body)
	}
}

// stripGo drops the Go runtime's lines, which change between scrapes.
func stripGo(s string) string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if !strings.Contains(line, "chadash_go_") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
