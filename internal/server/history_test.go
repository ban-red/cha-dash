package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

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
