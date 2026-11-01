package updates

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/graph"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/livedoc"
	"dash.cha.sh/internal/store"
)

type env struct {
	t     *testing.T
	st    *store.Store
	s     *Service
	g     *graph.Graph
	inbox *livedoc.Doc
	now   time.Time
	asked int
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
	if _, err := st.Writer().Exec(`INSERT INTO users (id, username, role, created_at, updated_at) VALUES ('u_op', 'op', 'operator', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, g: graph.New(), inbox: livedoc.New(incidentsColl), now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	e.s, err = New(Options{Store: st, Graph: e.g, Inbox: e.inbox, Now: func() time.Time { return e.now }, Refresh: func() int { e.asked++; return 1 }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.g.Update(func(tx *graph.Tx) error {
		if err := tx.Upsert(graph.Resource{ID: "docker:i_a/engine", Kind: "engine", Name: "pvedocker"}); err != nil {
			return err
		}
		return tx.Upsert(graph.Resource{ID: "docker:i_a/stack/media", Kind: "stack", Name: "media"})
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// container publishes a container with an update attribute, as the Docker
// integration does.
func (e *env) container(name, state, latest string) string {
	e.t.Helper()
	id := graph.RID("docker:i_a/container/" + name)
	_, err := e.g.Update(func(tx *graph.Tx) error {
		u := map[string]any{"state": state, "current": "sha256:aa"}
		if latest != "" {
			u["latest"] = latest
		}
		if err := tx.Upsert(graph.Resource{ID: id, Kind: "container", Name: name, Attrs: map[string]any{"image": "example/" + name + ":1", "update": u}}); err != nil {
			return err
		}
		if err := tx.Relate(graph.Relation{From: id, Type: "runs_on", To: "docker:i_a/engine"}); err != nil {
			return err
		}
		return tx.Relate(graph.Relation{From: id, Type: "part_of", To: "docker:i_a/stack/media"})
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return string(id)
}

func (e *env) list() map[string]Row {
	e.t.Helper()
	rows, err := e.s.List(e.t.Context())
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]Row{}
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

func (e *env) item() (item, bool) {
	raw := e.inbox.Item(incidentsColl, ItemID)
	if raw == nil {
		return item{}, false
	}
	var it item
	if err := json.Unmarshal(raw, &it); err != nil {
		e.t.Fatal(err)
	}
	return it, true
}

func TestListHoldsAndSnoozes(t *testing.T) {
	e := newEnv(t)
	sonarr := e.container("sonarr", integration.UpdateAvailable, "sha256:bb")
	e.container("radarr", integration.UpdateAvailable, "sha256:bb")
	e.container("plex", integration.UpdateCurrent, "sha256:aa")
	e.container("app", integration.UpdateCantCheck, "")
	_, _ = e.g.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: "docker:i_a/container/old", Kind: "container", Name: "old"})
	})

	got := e.list()
	if len(got) != 4 {
		t.Fatalf("rows %v: want the four with an update attribute", got)
	}
	r := got["sonarr"]
	if r.State != integration.UpdateAvailable || r.Host != "pvedocker" || r.Stack != "media" || r.Image != "example/sonarr:1" ||
		r.Update == nil || r.Update.ID != "docker.container.update" || r.Update.Target != sonarr {
		t.Fatalf("sonarr %+v", r)
	}
	if got["plex"].Update != nil || got["app"].Update != nil {
		t.Fatalf("nothing to update offers an update: %+v %+v", got["plex"], got["app"])
	}

	// A hold keeps it held whatever appears.
	if _, err := e.s.Hold(t.Context(), sonarr, KindHold, "u_op"); err != nil {
		t.Fatal(err)
	}
	e.container("sonarr", integration.UpdateAvailable, "sha256:cc")
	if r := e.list()["sonarr"]; r.State != integration.UpdateHeld || r.Hold == nil || r.Hold.CreatedBy != "op" || r.Update == nil {
		t.Fatalf("held %+v", r)
	}
	// It survives a recreate: holds go by engine and name, not ID.
	if r, ok, err := e.s.Release(t.Context(), sonarr); err != nil || !ok || r.State != integration.UpdateAvailable {
		t.Fatalf("release %+v %v %v", r, ok, err)
	}
	if _, ok, _ := e.s.Release(t.Context(), sonarr); ok {
		t.Fatal("released twice")
	}

	// A snooze ends after 7 days…
	if r, err := e.s.Hold(t.Context(), sonarr, KindSnooze, ""); err != nil || r.State != integration.UpdateHeld || r.Hold.Digest != "sha256:cc" || !r.Hold.Until.Equal(e.now.Add(SnoozeFor)) {
		t.Fatalf("snooze %+v %v", r, err)
	}
	e.now = e.now.Add(SnoozeFor - time.Minute)
	if e.list()["sonarr"].State != integration.UpdateHeld {
		t.Fatal("snooze ended early")
	}
	e.now = e.now.Add(2 * time.Minute)
	if e.list()["sonarr"].State != integration.UpdateAvailable {
		t.Fatal("snooze outlived 7 days")
	}
	// …or when a newer digest appears.
	if _, err := e.s.Hold(t.Context(), sonarr, KindSnooze, ""); err != nil {
		t.Fatal(err)
	}
	e.container("sonarr", integration.UpdateAvailable, "sha256:dd")
	if r := e.list()["sonarr"]; r.State != integration.UpdateAvailable || r.Hold != nil {
		t.Fatalf("snooze past a newer digest %+v", r)
	}

	if _, err := e.s.Hold(t.Context(), "docker:i_a/container/nope", KindHold, ""); err != ErrNotFound {
		t.Fatalf("hold of nothing = %v", err)
	}
}

// One quiet info item counts what's available and not held, and resolves
// when nothing is.
func TestInboxItem(t *testing.T) {
	e := newEnv(t)
	sonarr := e.container("sonarr", integration.UpdateAvailable, "sha256:bb")
	radarr := e.container("radarr", integration.UpdateAvailable, "sha256:bb")
	e.container("plex", integration.UpdateCurrent, "sha256:aa")

	e.s.Recount(t.Context())
	it, ok := e.item()
	if !ok || it.Status != "open" || it.Severity != "info" || it.Title != "2 updates available" || !strings.Contains(it.Summary, "radarr and sonarr") || it.Suggested["id"] != ReviewAction {
		t.Fatalf("item %+v", it)
	}
	opened := it.OpenedAt

	if _, err := e.s.Hold(t.Context(), sonarr, KindHold, ""); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	e.s.Recount(t.Context())
	if it, _ := e.item(); it.Title != "1 update available" || !it.OpenedAt.Equal(opened) {
		t.Fatalf("after a hold %+v", it)
	}
	if _, err := e.s.Hold(t.Context(), radarr, KindSnooze, ""); err != nil {
		t.Fatal(err)
	}
	e.s.Recount(t.Context())
	if it, _ := e.item(); it.Status != "resolved" || it.ResolvedAt == nil {
		t.Fatalf("nothing available %+v", it)
	}
}

func TestRoutes(t *testing.T) {
	e := newEnv(t)
	sonarr := e.container("sonarr", integration.UpdateAvailable, "sha256:bb")
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

	if w := do("GET", "/api/updates", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"sonarr"`) {
		t.Fatalf("list %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/api/updates/check", ""); w.Code != 200 || e.asked != 1 || !strings.Contains(w.Body.String(), `"asked":1`) {
		t.Fatalf("check %d %s", w.Code, w.Body)
	}
	hold := "/api/updates/" + url.PathEscape(sonarr) + "/hold"
	if w := do("POST", hold, `{"kind":"forever"}`); w.Code != 400 {
		t.Fatalf("bad kind %d", w.Code)
	}
	if w := do("POST", hold, `{"kind":"snooze"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"held"`) {
		t.Fatalf("snooze %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", hold, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"available"`) {
		t.Fatalf("release %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", hold, ""); w.Code != 404 {
		t.Fatalf("release twice %d", w.Code)
	}
	// Holds and releases are audited (server.TestAuditedRoutesRecord names
	// this test); a refused one isn't a change.
	page, err := log.Query(t.Context(), audit.Filter{Action: "update."})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ev := range page.Events {
		got = append(got, ev.Action+" "+ev.Target+" "+string(ev.Params))
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "update.release "+sonarr) || !strings.HasPrefix(got[1], "update.hold "+sonarr) || !strings.Contains(got[1], `"kind":"snooze"`) {
		t.Fatalf("audit %q", got)
	}
	if w := do("POST", "/api/updates/"+url.PathEscape("docker:i_a/container/nope")+"/hold", `{"kind":"hold"}`); w.Code != 404 {
		t.Fatalf("hold of nothing %d", w.Code)
	}
}

// A hold refuses the update actions however they're asked for; a snooze
// and other actions pass.
func TestGuard(t *testing.T) {
	e := newEnv(t)
	sonarr := e.container("sonarr", integration.UpdateAvailable, "sha256:bb")
	radarr := e.container("radarr", integration.UpdateAvailable, "sha256:bb")
	req := func(act, target string) action.Request {
		return action.Request{Action: act, Target: action.Target{ID: target, Name: "x"}}
	}
	if err := e.s.Guard(t.Context(), req("docker.container.update", sonarr)); err != nil {
		t.Fatalf("not held: %v", err)
	}
	if _, err := e.s.Hold(t.Context(), sonarr, KindHold, "u_op"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Hold(t.Context(), radarr, KindSnooze, ""); err != nil {
		t.Fatal(err)
	}
	err := e.s.Guard(t.Context(), req("docker.container.update", sonarr))
	if r, ok := err.(*action.Refusal); !ok || r.Code != "precondition" || r.Message != "sonarr is held on its image by op. Release it in Settings → Updates to update it." {
		t.Fatalf("held: %v", err)
	}
	if err := e.s.Guard(t.Context(), req("docker.container.restart", sonarr)); err != nil {
		t.Fatalf("a restart of a held container: %v", err)
	}
	if err := e.s.Guard(t.Context(), req("docker.container.update", radarr)); err != nil {
		t.Fatalf("snoozed: %v", err)
	}
	// A pull moves the tag a recreate would take: held too.
	if err := e.s.Guard(t.Context(), req("docker.container.pull", sonarr)); err == nil {
		t.Fatal("a pull of a held container went through")
	}
	// The same container through Unraid: its row is same_as the node's.
	if _, err := e.g.Update(func(tx *graph.Tx) error {
		if err := tx.Upsert(graph.Resource{ID: "unraid:i_u/host", Kind: "host", Name: "tower"}); err != nil {
			return err
		}
		if err := tx.Upsert(graph.Resource{ID: "unraid:i_u/container/sonarr", Kind: "container", Name: "sonarr"}); err != nil {
			return err
		}
		if err := tx.Relate(graph.Relation{From: "unraid:i_u/container/sonarr", Type: "runs_on", To: "unraid:i_u/host"}); err != nil {
			return err
		}
		return tx.Relate(graph.Relation{From: "unraid:i_u/container/sonarr", Type: "same_as", To: graph.RID(sonarr)})
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Guard(t.Context(), req("unraid.container.update", "unraid:i_u/container/sonarr")); err == nil {
		t.Fatal("an update through Unraid went past the hold")
	}
}

// A stack deploy keeps held services on their image: the hub names them by
// compose service, for the engine that reads through the stack's node
// (named by ID or name). A snooze holds nothing.
func TestHeldServices(t *testing.T) {
	e := newEnv(t)
	label := func(name, svc string) {
		_, err := e.g.Update(func(tx *graph.Tx) error {
			r, _ := tx.Get(graph.RID("docker:i_a/container/" + name))
			r.Labels = map[string]string{"com.docker.compose.project": "media", "com.docker.compose.service": svc}
			return tx.Upsert(r)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	db, web := e.container("media-db-1", "available", "sha256:bb"), e.container("media-web-1", "available", "sha256:bb")
	label("media-db-1", "db")
	label("media-web-1", "web")
	if _, err := e.g.Update(func(tx *graph.Tx) error {
		return tx.Upsert(graph.Resource{ID: "docker:i_a/engine", Kind: "engine", Name: "pvedocker", Attrs: map[string]any{"node": "pvedocker"}})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Hold(t.Context(), db, KindHold, "u_op"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Hold(t.Context(), web, KindSnooze, "u_op"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		project string
		node    []string
		want    []string
	}{
		{"media", []string{"n_1", "pvedocker"}, []string{"db"}},
		{"media", []string{"n_2", "elsewhere"}, nil},
		{"other", []string{"pvedocker"}, nil},
	} {
		got, err := e.s.HeldServices(t.Context(), c.project, c.node...)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s on %v: %v, %v; want %v", c.project, c.node, got, err, c.want)
		}
	}
}
