package backups

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/store"
	"dash.cha.sh/internal/topics"
)

type env struct {
	st  *store.Store
	tp  *topics.Set
	s   *Service
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "chadash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer().Exec(`INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_admin', 'admin', 'admin', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, tp: topics.New(graph.New(), now), now: now}
	l := baseLab()
	_, err = e.tp.Graph.Update(func(tx *graph.Tx) error {
		for _, r := range l.doc.Resources {
			if err := tx.Upsert(*r); err != nil {
				return err
			}
		}
		for _, r := range l.doc.Relations {
			if err := tx.Relate(*r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.s = e.open(t)
	return e
}

func (e *env) open(t *testing.T) *Service {
	t.Helper()
	s, err := New(t.Context(), Options{Topics: e.tp, Store: e.st, Now: func() time.Time { return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) item(t *testing.T, id string) (Item, bool) {
	t.Helper()
	raw := e.tp.Backups.Item(topics.BackupItems, id)
	if raw == nil {
		return Item{}, false
	}
	var it Item
	if err := json.Unmarshal(raw, &it); err != nil {
		t.Fatal(err)
	}
	return it, true
}

// The topic holds the map, follows the clock, and objectives persist.
func TestServiceWritesTheTopic(t *testing.T) {
	e := newEnv(t)
	if it, ok := e.item(t, guest); !ok || it.State != Fresh {
		t.Fatalf("guest: %+v", it)
	}
	if it, ok := e.item(t, ctr); !ok || it.Through != guest {
		t.Fatalf("container: %+v", it)
	}
	e.now = e.now.Add(20 * time.Hour) // 30 hours since the backup
	if err := e.s.Sync(); err != nil {
		t.Fatal(err)
	}
	if it, _ := e.item(t, guest); it.State != Stale {
		t.Fatalf("after 30 hours: %+v", it)
	}
	if _, err := e.s.SetObjective(t.Context(), guest, 36*time.Hour, "u_admin"); err != nil {
		t.Fatal(err)
	}
	if it, _ := e.item(t, guest); it.State != Fresh || it.Objective != FromOverride {
		t.Fatalf("with 36 h: %+v", it)
	}
	if _, err := e.s.SetObjective(t.Context(), guest, time.Minute, ""); err != ErrOutOfRange {
		t.Fatalf("a minute: %v", err)
	}
	if _, err := e.s.SetObjective(t.Context(), "proxmox:i_pve/qemu/999", time.Hour*30, ""); err != ErrNotProtectable {
		t.Fatalf("not on the map: %v", err)
	}
	// A new service reads it back.
	s2 := e.open(t)
	if obs := s2.Objectives(); len(obs) != 1 || obs[0].Resource != guest || obs[0].SetBy != "u_admin" || obs[0].MaxAgeMS != (36*time.Hour).Milliseconds() {
		t.Fatalf("reloaded: %+v", obs)
	}
	if _, err := s2.ClearObjective(t.Context(), guest); err != nil {
		t.Fatal(err)
	}
	if it, _ := e.item(t, guest); it.State != Stale || it.Objective != FromSchedule {
		t.Fatalf("cleared: %+v", it)
	}
	if _, err := s2.ClearObjective(t.Context(), guest); err != ErrNotSet {
		t.Fatalf("clear twice: %v", err)
	}
	// A resource that leaves the graph leaves the topic.
	if _, err := e.tp.Graph.Update(func(tx *graph.Tx) error { tx.Remove(share); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s2.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.item(t, share); ok {
		t.Fatal("a removed share stayed")
	}
}

// TestObjectiveRoutes: setting and clearing are audited
// (server.TestAuditedRoutesRecord names this test); refusals aren't.
func TestObjectiveRoutes(t *testing.T) {
	e := newEnv(t)
	log := audit.New(e.st)
	e.s.Audit = &authz.Recorder{Log: log}
	mux := http.NewServeMux()
	e.s.Register(mux, func(h http.Handler) http.Handler { return h })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
		return w
	}
	path := "/api/backups/objectives/" + url.PathEscape(guest)
	if w := do("PUT", path, `{"max_age_ms": 1000}`); w.Code != 422 || !strings.Contains(w.Body.String(), `"id":"backups.objective_range"`) {
		t.Fatalf("too short %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "/api/backups/objectives/"+url.PathEscape("proxmox:i_pve/qemu/999"), `{"max_age_ms": 86400000}`); w.Code != 404 {
		t.Fatalf("unknown %d %s", w.Code, w.Body)
	}
	if w := do("PUT", path, `{"max_age_ms": 172800000}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"max_age_ms":172800000`) {
		t.Fatalf("set %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", path, ""); w.Code != 200 {
		t.Fatalf("clear %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", path, ""); w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"not_set"`) {
		t.Fatalf("clear twice %d %s", w.Code, w.Body)
	}
	page, err := log.Query(t.Context(), audit.Filter{Action: "backups."})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range page.Events {
		got = append(got, ev.Action+" "+ev.Target+" "+string(ev.Params))
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "backups.objective.clear "+guest) || !strings.HasPrefix(got[1], "backups.objective.set "+guest) ||
		!strings.Contains(got[1], `"max_age_ms":172800000`) || !strings.Contains(got[1], `"name":"pvedocker"`) {
		t.Fatalf("audit %q", got)
	}
}
