package specs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// seerrUpstream is a fake Seerr that remembers what approve and decline
// did to each request (synthetic fixtures; see testdata/seerr/README.md).
type seerrUpstream struct {
	srv    *httptest.Server
	writes *integrationtest.Writes
	reads  atomic.Int64 // GETs of one request

	mu     sync.Mutex
	status map[string]int // request id → status
	ignore bool           // accept writes without changing anything
}

func newSeerrUpstream(t *testing.T) *seerrUpstream {
	t.Helper()
	u := &seerrUpstream{writes: &integrationtest.Writes{}, status: map[string]int{"42": 1, "43": 1}}
	read := func(name string) []byte {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Error(err)
		}
		return b
	}
	mux := http.NewServeMux()
	serve := func(w http.ResponseWriter, b []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, _ *http.Request) { serve(w, read("testdata/seerr/status.json")) })
	mux.HandleFunc("GET /api/v1/request/count", func(w http.ResponseWriter, _ *http.Request) {
		serve(w, read("testdata/seerr/request_count.json"))
	})
	mux.HandleFunc("GET /api/v1/request", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("filter") != "pending" || q.Get("take") != "20" || q.Get("sort") != "added" {
			t.Errorf("the pending list was asked as %s", r.URL.RawQuery)
		}
		serve(w, read("testdata/seerr/request_pending.json"))
	})
	mux.HandleFunc("GET /api/v1/request/{id}", func(w http.ResponseWriter, r *http.Request) {
		u.reads.Add(1)
		id := r.PathValue("id")
		u.mu.Lock()
		st, ok := u.status[id]
		u.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		var doc map[string]any
		_ = json.Unmarshal(read("testdata/seerr/request_"+id+".json"), &doc)
		doc["status"] = st
		b, _ := json.Marshal(doc)
		serve(w, b)
	})
	mux.HandleFunc("POST /api/v1/request/{id}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		id, verb := r.PathValue("id"), r.PathValue("verb")
		u.mu.Lock()
		defer u.mu.Unlock()
		if _, ok := u.status[id]; !ok {
			http.NotFound(w, r)
			return
		}
		if !u.ignore {
			switch verb {
			case "approve":
				u.status[id] = 2
			case "decline":
				u.status[id] = 3
			default:
				http.Error(w, "{}", http.StatusBadRequest)
				return
			}
		}
		serve(w, []byte(`{"id":`+id+`}`))
	})
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != key {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		u.writes.Wrap(mux).ServeHTTP(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func TestSeerrPublishesPendingRequests(t *testing.T) {
	u := newSeerrUpstream(t)
	h := integrationtest.New(t)
	inst := h.Connect(seerrIntegration(t), map[string]any{"url": u.srv.URL + "/"}, map[string]string{"api_key": key})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, ok := h.Resource("seerr:i_test/request/42")
	if !ok || r.Kind != "media_request" || r.Name != "Movie request #42 by viewer-one" || r.State != "pending" ||
		r.Health != integration.HealthOK || r.Attrs["media_type"] != "movie" || r.Attrs["requested_by"] != "viewer-one" {
		t.Fatalf("request 42 = %+v", r)
	}
	if links := r.Attrs["links"].([]any); links[0].(map[string]any)["url"] != u.srv.URL+"/requests?filter=pending" {
		t.Fatalf("links = %v", links)
	}
	if r, ok := h.Resource("seerr:i_test/request/43"); !ok || r.Name != "TV request #43 by viewer-two" {
		t.Fatalf("request 43 = %+v", r)
	}
	rels := h.Graph().Relations
	if rels["seerr:i_test/request/42 part_of seerr:i_test/app"] == nil || rels["integration:i_test monitors seerr:i_test/request/43"] == nil {
		t.Fatalf("relations = %v", rels)
	}
}

func TestSeerrApproveAndDecline(t *testing.T) {
	u := newSeerrUpstream(t)
	h := integrationtest.New(t)
	acts := h.Actions(seerrIntegration(t), map[string]any{"url": u.srv.URL}, map[string]string{"api_key": key})
	if ids := strings.Join(acts.IDs(), ","); ids != "seerr.request.approve,seerr.request.decline" {
		t.Fatalf("actions = %s", ids)
	}
	const r42, r43 = "seerr:i_test/request/42", "seerr:i_test/request/43"

	// What ⌘K lists for a pending request: both, by label.
	offers, err := acts.Engine.ActionsFor(t.Context(), acts.Actor, r42)
	if err != nil || len(offers) != 2 || offers[0].Label != "Approve Movie request #42 by viewer-one" || offers[0].Tier != action.T1 ||
		offers[1].ID != "seerr.request.decline" || offers[1].Tier != action.T2 {
		t.Fatalf("offers = %+v, %v", offers, err)
	}

	p, err := acts.Plan("seerr.request.approve", r42, nil)
	if err != nil || p.Tier != action.T1 || p.Confirm != "undo" || p.Undo != nil || p.Label != "Approve Movie request #42 by viewer-one" {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	task, err := acts.Approve(p)
	if err != nil {
		t.Fatal(err)
	}
	if done := acts.Wait(task.ID); done.State != action.StateSucceeded {
		t.Fatalf("approve: %s %+v", done.State, done.Error)
	}
	u.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api/v1/request/42/approve", Body: ""})
	if ev := acts.LastEvent(); ev.Action != "seerr.request.approve" || ev.Target != r42 {
		t.Fatalf("audit = %+v", ev)
	}

	// Now approved: nothing to offer, and planning says why.
	if offers, _ := acts.Engine.ActionsFor(t.Context(), acts.Actor, r42); len(offers) != 0 {
		t.Fatalf("offers once approved = %+v", offers)
	}
	if ref := acts.Refused("seerr.request.approve", r42, nil); ref.Code != "precondition" || ref.Message != "Movie request #42 by viewer-one is already approved." {
		t.Fatalf("approve twice: %+v", ref)
	}

	// Decline is T2: held to confirm.
	p, err = acts.Plan("seerr.request.decline", r43, nil)
	if err != nil || p.Tier != action.T2 || p.Confirm != "hold" {
		t.Fatalf("decline plan = %+v, %v", p, err)
	}
	acts.Succeeded("seerr.request.decline", r43, nil)
	u.writes.Expect(t, integrationtest.Write{Method: "POST", Path: "/api/v1/request/43/decline"})
	if ref := acts.Refused("seerr.request.approve", r43, nil); ref.Code != "precondition" || !strings.Contains(ref.Message, "was declined") {
		t.Fatalf("approve a declined request: %+v", ref)
	}
	u.writes.Expect(t)
}

func TestSeerrActionTargets(t *testing.T) {
	u := newSeerrUpstream(t)
	h := integrationtest.New(t)
	acts := h.Actions(seerrIntegration(t), map[string]any{"url": u.srv.URL}, map[string]string{"api_key": key})

	// IDs the pattern refuses never reach Seerr.
	for _, target := range []string{
		"seerr:i_test/request/..", "seerr:i_test/request/4%2F2", "seerr:i_test/request/42/approve", "seerr:i_test/request/abc",
		"seerr:i_test/request/", "seerr:i_test/request/0", "seerr:i_test/app", "seerr:other/request/42", "sonarr:i_test/request/42",
	} {
		if ref := acts.Refused("seerr.request.approve", target, nil); ref.Code != "not_found" {
			t.Errorf("%s: %+v", target, ref)
		}
	}
	if n := u.reads.Load(); n != 0 {
		t.Fatalf("refused IDs sent %d reads", n)
	}
	// One Seerr doesn't know is not found too.
	if ref := acts.Refused("seerr.request.approve", "seerr:i_test/request/77", nil); ref.Code != "not_found" {
		t.Fatalf("unknown request: %+v", ref)
	}
	u.writes.Expect(t)
}

func TestSeerrActionFailures(t *testing.T) {
	u := newSeerrUpstream(t)

	// A wrong key: unavailable, naming the instance, never quoting the key.
	h := integrationtest.New(t)
	acts := h.Actions(seerrIntegration(t), map[string]any{"url": u.srv.URL}, map[string]string{"api_key": "wrong-seerr-key"})
	ref := acts.Refused("seerr.request.approve", "seerr:i_test/request/42", nil)
	if ref.Code != "unavailable" || ref.Recipe != "i_test" || strings.Contains(ref.Message, "wrong-seerr-key") {
		t.Fatalf("wrong key: %+v", ref)
	}
	for _, ev := range acts.Events() {
		if b, _ := json.Marshal(ev); strings.Contains(string(b), "wrong-seerr-key") || strings.Contains(string(b), key) {
			t.Fatalf("the audit log holds the key: %s", b)
		}
	}

	// The request disappears between plan and approve: the approval finds
	// the target gone.
	h = integrationtest.New(t)
	acts = h.Actions(seerrIntegration(t), map[string]any{"url": u.srv.URL}, map[string]string{"api_key": key})
	p, err := acts.Plan("seerr.request.approve", "seerr:i_test/request/42", nil)
	if err != nil {
		t.Fatal(err)
	}
	u.mu.Lock()
	u.status["42"] = 2 // someone approved it in Seerr meanwhile
	u.mu.Unlock()
	if _, err := acts.Approve(p); err == nil {
		t.Fatal("approving a plan whose target changed should be refused")
	}
	u.writes.Expect(t)
}
