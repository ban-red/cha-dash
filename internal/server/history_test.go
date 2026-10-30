package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/history"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
)

func TestHistoryRoute(t *testing.T) {
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
	// Ten minutes of host:pve's CPU and node:n1's load, one sample a minute,
	// the last one still open.
	now := time.Now().Truncate(time.Minute)
	for i := 10; i >= 0; i-- {
		at := now.Add(-time.Duration(i) * time.Minute)
		rec.Record("host:pve#cpu", "i1", "%", at, float64(i))
		rec.Record("node:n1#load", "i1", "", at, 1)
	}
	tp := topics.New(graph.New(), time.Now())
	if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
		if err := tx.Upsert(graph.Resource{ID: "node:n1", Kind: "node", Name: "local"}); err != nil {
			return err
		}
		return tx.Upsert(graph.Resource{ID: "host:pve", Kind: "host", Name: "pve"})
	}); err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, func(d *Deps) { d.Topics, d.History = tp, rec })
	owner := signIn(t, e)
	_, household := addUser(t, e, owner, "kid", "household")

	type body struct {
		Level  string `json:"level"`
		Series map[string]struct {
			Unit  string     `json:"unit"`
			Start int64      `json:"start"`
			Step  int64      `json:"step"`
			Avg   []*float32 `json:"avg"`
		} `json:"series"`
	}
	get := func(c *http.Client, query string, want int) body {
		t.Helper()
		status, _, b := e.call(t, c, "GET", "/api/history?"+query, nil)
		if status != want {
			t.Fatalf("GET /api/history?%s: %d %s", query, status, b)
		}
		var out body
		_ = json.Unmarshal(b, &out)
		return out
	}
	from, to := now.Add(-10*time.Minute).UnixMilli(), now.UnixMilli()
	q := fmt.Sprintf("series=host:pve%%23cpu&series=node:n1%%23load&series=app:none%%23x&from=%d&to=%d", from, to)
	got := get(owner, q, http.StatusOK)
	cpu, ok := got.Series["host:pve#cpu"]
	if got.Level != "standard" || len(got.Series) != 2 || !ok {
		t.Fatalf("owner got %+v", got)
	}
	// By the minute, oldest first: 10, 9, … 1; the open minute isn't in yet.
	if cpu.Unit != "%" || cpu.Start != from || cpu.Step != 60_000 || len(cpu.Avg) != 10 || *cpu.Avg[0] != 10 || *cpu.Avg[9] != 1 {
		t.Fatalf("cpu = %+v", cpu)
	}
	// Household doesn't see the node's series, as its graph doesn't show the node.
	if hh := get(household, q, http.StatusOK); len(hh.Series) != 1 || hh.Series["host:pve#cpu"].Step == 0 {
		t.Fatalf("household got %+v", hh)
	}
	// A week at 300 points comes from the quarter-hour tier.
	if wk := get(owner, fmt.Sprintf("series=host:pve%%23cpu&from=%d&to=%d", to-7*24*3600_000, to), http.StatusOK); wk.Series["host:pve#cpu"].Step != 900_000 {
		t.Fatalf("a week = %+v", wk)
	}
	for _, bad := range []string{"", "series=a%23b&points=0", "series=a%23b&from=5&to=4", "series=a%23b&to=soon"} {
		get(owner, bad, http.StatusBadRequest)
	}
}

func TestHistoryRouteWhenOff(t *testing.T) {
	e := newEnvWith(t, func(d *Deps) { d.Topics = topics.New(graph.New(), time.Now()) })
	owner := signIn(t, e)
	status, _, b := e.call(t, owner, "GET", "/api/history?series=a%23b", nil)
	if status != http.StatusOK || string(b) != `{"level":"off","series":{}}`+"\n" {
		t.Fatalf("%d %s", status, b)
	}
}

func TestHistoryStatesRoute(t *testing.T) {
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
	now := time.Now().Truncate(time.Minute)
	rec.State("host:pve", "ok", now.Add(-4*time.Hour))
	rec.State("host:pve", "down", now.Add(-3*time.Hour))
	rec.State("host:pve", "ok", now.Add(-2*time.Hour))
	rec.State("node:n1", "ok", now.Add(-4*time.Hour))
	tp := topics.New(graph.New(), time.Now())
	if _, err := tp.Graph.Update(func(tx *graph.Tx) error {
		if err := tx.Upsert(graph.Resource{ID: "node:n1", Kind: "node", Name: "local"}); err != nil {
			return err
		}
		return tx.Upsert(graph.Resource{ID: "host:pve", Kind: "host", Name: "pve"})
	}); err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, func(d *Deps) { d.Topics, d.History = tp, rec })
	owner := signIn(t, e)
	_, household := addUser(t, e, owner, "kid", "household")

	type body struct {
		Level  string `json:"level"`
		States []struct {
			At     int64  `json:"at"`
			Status string `json:"status"`
		} `json:"states"`
		Totals map[string]int64 `json:"totals"`
		Up     *float64         `json:"up"`
	}
	get := func(c *http.Client, query string, want int) body {
		t.Helper()
		status, _, b := e.call(t, c, "GET", "/api/history/states?"+query, nil)
		if status != want {
			t.Fatalf("GET /api/history/states?%s: %d %s", query, status, b)
		}
		var out body
		_ = json.Unmarshal(b, &out)
		return out
	}
	from := now.Add(-5 * time.Hour).UnixMilli()
	got := get(owner, fmt.Sprintf("resource=host:pve&from=%d&to=%d", from, now.UnixMilli()), http.StatusOK)
	if got.Level != "standard" || len(got.States) != 3 || got.States[1].Status != "down" || got.States[0].At != now.Add(-4*time.Hour).UnixMilli() {
		t.Fatalf("owner got %+v", got)
	}
	// Up three of the four watched hours; the hour before the first row isn't counted.
	if got.Up == nil || *got.Up != 0.75 || got.Totals["down"] != 3_600_000 {
		t.Fatalf("up %v, totals %v", got.Up, got.Totals)
	}
	if hh := get(household, "resource=node:n1", http.StatusOK); len(hh.States) != 0 || hh.Up != nil {
		t.Fatalf("household sees the node's states: %+v", hh)
	}
	if hh := get(household, "resource=host:pve", http.StatusOK); len(hh.States) != 3 {
		t.Fatalf("household got %+v", hh)
	}
	get(owner, "", http.StatusBadRequest)
	get(owner, "resource=host:pve&from=9&to=3", http.StatusBadRequest)
}

func TestHistorySettings(t *testing.T) {
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
	now := time.Now().Truncate(time.Minute)
	for i := 10; i >= 0; i-- {
		rec.Record("host:pve#cpu", "i1", "%", now.Add(-time.Duration(i)*time.Minute), 1)
	}
	var st *store.Store
	e := newEnvWith(t, func(d *Deps) {
		d.Topics, d.History, d.Audit = topics.New(graph.New(), time.Now()), rec, audit.New(d.Store)
		st = d.Store
	})
	owner := signIn(t, e)
	_, viewer := addUser(t, e, owner, "gran", "viewer")

	type body struct {
		Level     string   `json:"level"`
		Locked    bool     `json:"locked"`
		MaxBytes  int64    `json:"max_bytes"`
		Excluded  []string `json:"excluded"`
		Instances []struct {
			ID     string `json:"id"`
			Series int    `json:"series"`
		} `json:"instances"`
		Series    int `json:"series"`
		Estimates map[string]int64
		Tiers     []struct {
			Tier   string `json:"tier"`
			KeepMs int64  `json:"keep_ms"`
		} `json:"tiers"`
	}
	call := func(c *http.Client, method, path string, in any, want int) body {
		t.Helper()
		status, _, b := e.call(t, c, method, path, in)
		if status != want {
			t.Fatalf("%s %s: %d %s", method, path, status, b)
		}
		var out body
		_ = json.Unmarshal(b, &out)
		return out
	}
	got := call(owner, "GET", "/api/settings/history", nil, http.StatusOK)
	if got.Level != "standard" || got.Locked || got.Series != 0 || len(got.Tiers) != 4 || got.Tiers[1].KeepMs != 14*86_400_000 {
		t.Fatalf("GET = %+v", got)
	}
	if got.Estimates["long-term"] <= got.Estimates["standard"] || got.Estimates["standard"] == 0 {
		t.Fatalf("estimates = %v", got.Estimates)
	}
	call(viewer, "GET", "/api/settings/history", nil, http.StatusForbidden)

	got = call(owner, "PUT", "/api/settings/history", map[string]string{"level": "long-term"}, http.StatusOK)
	if got.Level != "long-term" || rec.Level() != history.LongTerm || got.Tiers[1].KeepMs != 30*86_400_000 {
		t.Fatalf("after PUT = %+v", got)
	}
	if l, ok, _ := history.LoadLevel(t.Context(), st); !ok || l != history.LongTerm {
		t.Fatalf("saved level %q, %v", l, ok)
	}
	if ev, ok := lastAudit(t, st, "settings.history.update"); !ok || string(ev.Params) != `{"level_from":"standard","level_to":"long-term"}` {
		t.Fatalf("audit = %+v", ev)
	}
	call(owner, "PUT", "/api/settings/history", map[string]string{"level": "forever"}, http.StatusUnprocessableEntity)
	call(owner, "PUT", "/api/settings/history", map[string]any{}, http.StatusUnprocessableEntity)
	call(owner, "PUT", "/api/settings/history", map[string]any{"max_bytes": 1 << 20}, http.StatusUnprocessableEntity)
	if got = call(owner, "PUT", "/api/settings/history", map[string]any{"max_bytes": 512 << 20}, http.StatusOK); got.MaxBytes != 512<<20 || rec.Limit() != 512<<20 || got.Level != "long-term" {
		t.Fatalf("after setting the limit = %+v", got)
	}
	if l, ok, _ := history.LoadLimit(t.Context(), st); !ok || l != 512<<20 {
		t.Fatalf("saved limit %d, %v", l, ok)
	}
	if ev, _ := lastAudit(t, st, "settings.history.update"); string(ev.Params) != `{"max_bytes_from":0,"max_bytes_to":536870912}` {
		t.Fatalf("audit = %s", ev.Params)
	}
	if got = call(owner, "PUT", "/api/settings/history", map[string]any{"max_bytes": 0}, http.StatusOK); got.MaxBytes != 0 {
		t.Fatalf("no limit = %+v", got)
	}
	// Leaving an integration out.
	if got = call(owner, "PUT", "/api/settings/history", map[string]any{"exclude": []string{"i9", "i2", "i9"}}, http.StatusOK); !slices.Equal(got.Excluded, []string{"i2", "i9"}) || !slices.Equal(rec.Excluded(), []string{"i2", "i9"}) {
		t.Fatalf("excluded = %v", got.Excluded)
	}
	if ids, _ := history.LoadExcluded(t.Context(), st); !slices.Equal(ids, []string{"i2", "i9"}) {
		t.Fatalf("saved exclusions %v", ids)
	}
	if ev, _ := lastAudit(t, st, "settings.history.update"); string(ev.Params) != `{"exclude_from":[],"exclude_to":["i2","i9"]}` {
		t.Fatalf("audit = %s", ev.Params)
	}
	call(owner, "PUT", "/api/settings/history", map[string]any{"exclude": make([]string, 501)}, http.StatusUnprocessableEntity)

	if err := rec.Close(t.Context()); err != nil { // writes the series row
		t.Fatal(err)
	}
	if got = call(owner, "GET", "/api/settings/history", nil, http.StatusOK); got.Series != 1 || len(got.Instances) != 1 || got.Instances[0].ID != "i1" || got.Instances[0].Series != 1 {
		t.Fatalf("before clear = %+v", got)
	}
	// One instance's history; another's is untouched (there is none here).
	if got = call(owner, "POST", "/api/settings/history/clear?instance=i1", nil, http.StatusOK); got.Series != 0 || len(got.Instances) != 0 {
		t.Fatalf("after clearing i1 = %+v", got)
	}
	if ev, _ := lastAudit(t, st, "settings.history.clear"); ev.Target != "integration:i1" {
		t.Fatalf("audit target %q", ev.Target)
	}
	if got = call(owner, "POST", "/api/settings/history/clear", nil, http.StatusOK); got.Series != 0 {
		t.Fatalf("series after clear = %d", got.Series)
	}
	if _, ok := lastAudit(t, st, "settings.history.clear"); !ok {
		t.Fatal("clear wasn't audited")
	}
}

func TestHistorySettingsWhenLocked(t *testing.T) {
	e := newEnvWith(t, func(d *Deps) { d.Topics = topics.New(graph.New(), time.Now()) })
	owner := signIn(t, e)
	status, _, b := e.call(t, owner, "GET", "/api/settings/history", nil)
	if status != http.StatusOK || !json.Valid(b) || !strings.Contains(string(b), `"locked":true`) || !strings.Contains(string(b), `"level":"off"`) {
		t.Fatalf("GET: %d %s", status, b)
	}
	for _, c := range []struct{ method, path string }{{"PUT", "/api/settings/history"}, {"POST", "/api/settings/history/clear"}} {
		if status, _, b := e.call(t, owner, c.method, c.path, map[string]string{"level": "standard"}); status != http.StatusConflict {
			t.Fatalf("%s %s: %d %s", c.method, c.path, status, b)
		}
	}
}
