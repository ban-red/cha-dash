package correlate

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
)

func TestRunFollowsTheGraph(t *testing.T) {
	set := topics.New(graph.New(), t0)
	c := New(Options{Topics: set, Debounce: 10 * time.Millisecond, Log: slog.New(slog.NewTextHandler(testWriter{t}, nil))})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	h := jupiter(t)
	if _, err := set.Graph.Update(func(tx *graph.Tx) error {
		for _, r := range h.doc.Resources {
			if err := tx.Upsert(*r); err != nil {
				return err
			}
		}
		for _, rel := range h.doc.Relations {
			if err := tx.Relate(*rel); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := relation(set, "unraid:i_u/container/sonarr", SameAs, "docker:i_d/container/sonarr"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never correlated the new facts")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return st
}

// Decisions survive a restart, and go with the instance that published
// one of the pair.
func TestDecisionsPersist(t *testing.T) {
	st := openStore(t)
	ctx := t.Context()
	if _, err := st.Writer().ExecContext(ctx, `INSERT INTO integration_instances (id, integration, name, created_at, updated_at) VALUES ('i_u', 'unraid', 'NAS', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	h := jupiter(t)
	set, _, _ := live(t, h)
	c := New(Options{Topics: set, Store: st, Now: func() time.Time { return t0 }})
	d, err := c.Decide(ctx, "unraid:i_u/container/sonarr", SameAs, "docker:i_d/container/sonarr", Reject, "u_missing")
	if err != nil {
		t.Fatal(err)
	}
	var fromInst, toInst, by any
	if err := st.Reader().QueryRowContext(ctx, `SELECT from_instance, to_instance, created_by FROM correlation_decisions WHERE id = ?`, d.ID).Scan(&fromInst, &toInst, &by); err != nil {
		t.Fatal(err)
	}
	// docker:… sorts first; its instance has no row, and neither has the user.
	if fromInst != nil || toInst != "i_u" || by != nil {
		t.Fatalf("instances = %v, %v; by %v", fromInst, toInst, by)
	}

	// A restart: a new correlator over a fresh graph reads the decision.
	set2, _, _ := live(t, h)
	c2 := New(Options{Topics: set2, Store: st})
	syncOK(t, c2)
	if _, ok := relation(set2, "unraid:i_u/container/sonarr", SameAs, "docker:i_d/container/sonarr"); ok {
		t.Fatal("the rejected merge came back after a restart")
	}
	if ds, err := c2.Decisions(ctx); err != nil || len(ds) != 1 || ds[0].ID != d.ID || !ds[0].CreatedAt.Equal(t0) {
		t.Fatalf("decisions = %+v, %v", ds, err)
	}

	// The Unraid instance is deleted: the decision goes with it.
	if _, err := st.Writer().ExecContext(ctx, `DELETE FROM integration_instances WHERE id = 'i_u'`); err != nil {
		t.Fatal(err)
	}
	syncOK(t, c2)
	if _, ok := relation(set2, "unraid:i_u/container/sonarr", SameAs, "docker:i_d/container/sonarr"); !ok {
		t.Fatal("the decision outlived its instance")
	}
}

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func (c client) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var r *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	} else {
		r = strings.NewReader("")
	}
	req, _ := http.NewRequestWithContext(c.t.Context(), method, c.srv.URL+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, b
}

func TestAPI(t *testing.T) {
	set, c, _ := live(t, proxyHop(t))
	syncOK(t, c)
	auditSt, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auditSt.Close() })
	if _, err := auditSt.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	auditLog := audit.New(auditSt)
	c.Audit = &authz.Recorder{Log: auditLog}
	mux := http.NewServeMux()
	wrapped := 0
	c.Register(mux, func(h http.Handler) http.Handler { wrapped++; return h }, nil)
	if wrapped != 7 {
		t.Fatalf("%d routes wrapped, want every one of 7", wrapped)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	api := client{t, srv}

	code, body := api.do("GET", "/api/correlate/suggestions", nil)
	var ss []Suggestion
	if err := json.Unmarshal(body, &ss); code != 200 || err != nil || len(ss) != 1 || ss[0].Title == "" || ss[0].Evidence == "" {
		t.Fatalf("suggestions: %d %s", code, body)
	}
	code, body = api.do("GET", "/api/correlate/links", nil)
	var ls []Link
	if err := json.Unmarshal(body, &ls); code != 200 || err != nil || len(ls) == 0 || ls[0].Rule == "" {
		t.Fatalf("links: %d %s", code, body)
	}

	if code, _ := api.do("POST", "/api/correlate/suggestions/correlate.nope/confirm", nil); code != 404 {
		t.Fatalf("unknown suggestion: %d", code)
	}
	code, body = api.do("POST", "/api/correlate/suggestions/"+ss[0].ID+"/confirm", nil)
	var d Decision
	if err := json.Unmarshal(body, &d); code != 200 || err != nil || d.Verdict != Confirm || d.From != "sonarr:i_s/app" {
		t.Fatalf("confirm: %d %s", code, body)
	}
	if in, _ := incident(t, set, ss[0].ID); in.Status != integration.StatusResolved {
		t.Fatalf("confirmed suggestion = %+v", in)
	}

	if code, body := api.do("POST", "/api/correlate/decisions", map[string]any{"from": "sonarr:i_s/app", "type": "owns", "to": "docker:i_d/engine", "verdict": "confirm"}); code != 422 || !strings.Contains(string(body), `"invalid"`) {
		t.Fatalf("invalid decision: %d %s", code, body)
	}
	if code, _ := api.do("POST", "/api/correlate/decisions", map[string]any{"from": "nope:x", "type": "runs_on", "to": "docker:i_d/engine", "verdict": "confirm"}); code != 404 {
		t.Fatalf("unknown resource: %d", code)
	}
	code, body = api.do("POST", "/api/correlate/decisions", map[string]any{"from": "unraid:i_u/container/radarr", "type": "same_as", "to": "docker:i_d/container/radarr", "verdict": "reject"})
	if code != 201 {
		t.Fatalf("split: %d %s", code, body)
	}
	var split Decision
	_ = json.Unmarshal(body, &split)

	code, body = api.do("GET", "/api/correlate/decisions", nil)
	var ds []Decision
	if err := json.Unmarshal(body, &ds); code != 200 || err != nil || len(ds) != 2 {
		t.Fatalf("decisions: %d %s", code, body)
	}
	if code, _ := api.do("DELETE", "/api/correlate/decisions/"+split.ID, nil); code != 204 {
		t.Fatalf("forget: %d", code)
	}
	if code, _ := api.do("DELETE", "/api/correlate/decisions/"+split.ID, nil); code != 404 {
		t.Fatalf("forget twice: %d", code)
	}
	if code, body := api.do("GET", "/api/correlate/suggestions", nil); code != 200 || string(body) != "[]\n" {
		t.Fatalf("empty suggestions: %d %q", code, body)
	}

	// Each decision that went through is audited, with the pair and the
	// verdict; refusals (404, 422) record nothing.
	page, err := auditLog.Query(t.Context(), audit.Filter{Action: "correlate."})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range page.Events {
		got = append(got, ev.Action+" "+ev.Target)
	}
	want := []string{"correlate.decision.delete decision:" + split.ID, "correlate.decision.create decision:" + split.ID, "correlate.suggestion.confirm suggestion:" + ss[0].ID}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audited %v, want %v", got, want)
	}
	if p := string(page.Events[1].Params); !strings.Contains(p, `"verdict":"reject"`) || !strings.Contains(p, `"type":"same_as"`) {
		t.Fatalf("decision params = %s", p)
	}
}
