package specstore

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/audit"
	"dash.cha.sh/internal/authz"
	"dash.cha.sh/internal/integration/integrationtest"
)

// communityRepo is a fixture of the community spec repository
// (ban-red/cha-dash-specs): one folder per spec with spec.yaml, fixtures/
// and a README. Its spec is Seerr's under another ID, two actions (T1 and
// T2), with Seerr's synthetic fixtures. The hub doesn't fetch the
// repository (docs/plan/09-v0.3.md, decision 31): the owner copies the
// spec from it into Settings, so the test fetches it the way the owner's
// browser would and pastes it into the install route.
type communityRepo struct {
	srv *httptest.Server
	mu  sync.Mutex
	fs  fstest.MapFS
}

func newCommunityRepo(t *testing.T) *communityRepo {
	t.Helper()
	repo := &communityRepo{fs: fstest.MapFS{
		"myseerr/README.md": {Data: []byte("# myseerr\n\nSeerr's requests, approve and decline.\n")},
	}}
	repo.publish(community(t))
	dir := filepath.Join("..", "..", "specs", "testdata", "seerr")
	for _, f := range []string{"status.json", "request_count.json", "request_pending.json", "request_42.json", "request_43.json"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		repo.fs["myseerr/fixtures/"+f] = &fstest.MapFile{Data: b}
	}
	repo.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repo.mu.Lock()
		files := fstest.MapFS{}
		maps.Copy(files, repo.fs)
		repo.mu.Unlock()
		http.FileServerFS(files).ServeHTTP(w, r)
	}))
	t.Cleanup(repo.srv.Close)
	return repo
}

// publish merges a new spec.yaml into the repository.
func (c *communityRepo) publish(yaml string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fs["myseerr/spec.yaml"] = &fstest.MapFile{Data: []byte(yaml)}
}

func (c *communityRepo) get(t *testing.T, path string) []byte {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, c.srv.URL+"/"+path, nil)
	res, err := c.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", path, res.StatusCode)
	}
	return b
}

// fakeSeerr answers the spec's requests from the repository's fixtures and
// records every write; approve and decline change a request's status.
func fakeSeerr(t *testing.T, repo *communityRepo) (*httptest.Server, *integrationtest.Writes) {
	t.Helper()
	writes := &integrationtest.Writes{}
	var mu sync.Mutex
	status := map[string]int{"42": 1, "43": 1}
	fixture := func(name string) []byte {
		b, err := fs.ReadFile(repo.fs, "myseerr/fixtures/"+name)
		if err != nil {
			t.Error(err)
		}
		return b
	}
	serve := func(w http.ResponseWriter, b []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, _ *http.Request) { serve(w, fixture("status.json")) })
	mux.HandleFunc("GET /api/v1/request/count", func(w http.ResponseWriter, _ *http.Request) { serve(w, fixture("request_count.json")) })
	mux.HandleFunc("GET /api/v1/request", func(w http.ResponseWriter, _ *http.Request) { serve(w, fixture("request_pending.json")) })
	mux.HandleFunc("GET /api/v1/request/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		mu.Lock()
		st, ok := status[id]
		mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		var doc map[string]any
		_ = json.Unmarshal(fixture("request_"+id+".json"), &doc)
		doc["status"] = st
		b, _ := json.Marshal(doc)
		serve(w, b)
	})
	mux.HandleFunc("POST /api/v1/request/{id}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		mu.Lock()
		defer mu.Unlock()
		if _, ok := status[id]; !ok {
			http.NotFound(w, r)
			return
		}
		switch r.PathValue("verb") {
		case "approve":
			status[id] = 2
		case "decline":
			status[id] = 3
		}
		serve(w, []byte(`{"id":`+id+`}`))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "fake-seerr-key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writes.Wrap(mux).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, writes
}

// A spec from the community repository installs through the API with its
// actions off. Planning one is refused with a sentence and nothing reaches
// the app, until the owner turns them on naming the version reviewed. A
// newer version with the same actions keeps them on; one whose actions
// changed turns them off again until the owner reviews that version.
func TestCommunitySpecActionsWaitForTheOwner(t *testing.T) {
	e := newEnv(t)
	repo := newCommunityRepo(t)
	upstream, writes := fakeSeerr(t, repo)
	e.s.Audit = &authz.Recorder{Log: audit.New(e.st)}
	owner := authz.Actor{Kind: authz.KindUser, ID: "u_owner", Name: "owner", Role: "owner", Session: "s_1", StepUpUntil: time.Now().Add(time.Hour)}
	e.s.actorOf = func(*http.Request) (authz.Actor, bool) { return owner, true }
	mux := http.NewServeMux()
	e.s.Register(mux, func(h http.Handler) http.Handler { return h })
	api := func(method, path string, body any) (int, Installed, string) {
		t.Helper()
		b, _ := json.Marshal(body)
		r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var v Installed
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return w.Code, v, w.Body.String()
	}
	source := repo.srv.URL + "/myseerr/spec.yaml"
	install := func(replace bool) Installed {
		t.Helper()
		code, v, body := api("POST", "/api/specs/installed", map[string]any{"yaml": string(repo.get(t, "myseerr/spec.yaml")), "source": source, "replace": replace})
		if code != http.StatusOK {
			t.Fatalf("install: %d %s", code, body)
		}
		return v
	}
	// actions attaches an instance of what's registered now, as the
	// runtime does on start and after a replace (Rebind), to an action
	// engine over the fake app.
	actions := func() *integrationtest.Actions {
		t.Helper()
		integ, _, ok := e.reg.Get("myseerr")
		if !ok {
			t.Fatal("myseerr isn't registered")
		}
		return integrationtest.New(t).Actions(integ, map[string]any{"url": upstream.URL}, map[string]string{"api_key": "fake-seerr-key"})
	}
	const r42, r43 = "myseerr:i_test/request/42", "myseerr:i_test/request/43"

	// Installed from the repository: its actions are listed, and off.
	v1 := install(false)
	if v1.ActionsOn || v1.Source != source || len(v1.Summary.Actions) != 2 {
		t.Fatalf("installed %+v", v1)
	}
	acts := actions()
	if ids := strings.Join(acts.IDs(), ","); ids != "myseerr.request.approve,myseerr.request.decline" {
		t.Fatalf("actions %s", ids)
	}
	for _, id := range acts.IDs() {
		ref := acts.Refused(id, r42, nil)
		if ref.Code != "unavailable" || ref.Message != Off("Seerr") {
			t.Fatalf("%s while off: %+v", id, ref)
		}
	}
	writes.Expect(t)

	// Turning them on names the version reviewed; anything else is refused.
	if code, _, body := api("PUT", "/api/specs/installed/myseerr/actions", map[string]any{"on": true, "sha256": strings.Repeat("0", 64)}); code != http.StatusConflict || !strings.Contains(body, `"changed"`) {
		t.Fatalf("on with another version: %d %s", code, body)
	}
	if code, v, body := api("PUT", "/api/specs/installed/myseerr/actions", map[string]any{"on": true, "sha256": v1.SHA256}); code != http.StatusOK || !v.ActionsOn {
		t.Fatalf("on: %d %s", code, body)
	}
	if task := acts.Succeeded("myseerr.request.approve", r42, nil); task.Action != "myseerr.request.approve" {
		t.Fatalf("task %+v", task)
	}
	writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api/v1/request/42/approve"})

	// A newer version with the same actions: still on.
	newer := strings.Replace(community(t), "version: 0.1.0", "version: 0.2.0", 1)
	if newer == community(t) {
		t.Fatal("no version to bump")
	}
	repo.publish(newer)
	v2 := install(true)
	if !v2.ActionsOn || v2.SHA256 == v1.SHA256 || v2.Summary.Version != "0.2.0" {
		t.Fatalf("replaced with the same actions: %+v", v2)
	}
	acts = actions()
	if p, err := acts.Plan("myseerr.request.decline", r43, nil); err != nil || p.Tier != action.T2 {
		t.Fatalf("decline with the same actions: %+v %v", p, err)
	}
	writes.Expect(t)

	// A version whose decline claims T1: off again, for the old instance
	// and the restarted one, until the owner reviews this version.
	changed := strings.Replace(newer, "version: 0.2.0", "version: 0.3.0", 1)
	changed = strings.Replace(changed, "tier: T2", "tier: T1", 1)
	if !strings.Contains(changed, "version: 0.3.0") || strings.Contains(changed, "tier: T2") {
		t.Fatal("the changed version didn't change")
	}
	repo.publish(changed)
	v3 := install(true)
	if v3.ActionsOn || v3.Summary.Actions[1].Tier != "T1" {
		t.Fatalf("replaced with changed actions: %+v", v3)
	}
	old := acts
	acts = actions()
	for _, a := range []*integrationtest.Actions{old, acts} {
		if ref := a.Refused("myseerr.request.decline", r43, nil); ref.Code != "unavailable" || ref.Message != Off("Seerr") {
			t.Fatalf("decline after a changed version: %+v", ref)
		}
	}
	if code, _, body := api("PUT", "/api/specs/installed/myseerr/actions", map[string]any{"on": true, "sha256": v2.SHA256}); code != http.StatusConflict || !strings.Contains(body, `"changed"`) {
		t.Fatalf("on with the version before: %d %s", code, body)
	}
	writes.Expect(t)
	if code, _, body := api("PUT", "/api/specs/installed/myseerr/actions", map[string]any{"on": true, "sha256": v3.SHA256}); code != http.StatusOK {
		t.Fatalf("on for the new version: %d %s", code, body)
	}
	acts.Succeeded("myseerr.request.decline", r43, nil)
	writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api/v1/request/43/decline"})

	// A restart keeps the owner's choice for that version.
	e.restart()
	if !e.s.ActionsOn("myseerr") {
		t.Fatal("actions off after a restart")
	}
}
