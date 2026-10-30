// Package arrtest is the contract test the media *arrs share for their
// search-missing action, run by each integration against its synthetic
// fixtures.
package arrtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/integrations/internal/act/acttest"
	"dash.cha.sh/integrations/internal/arr"
	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// cmdBody is a /command answer in the shape the *arrs' API documentation
// gives (CommandResource), trimmed to what's read.
func cmdBody(name, status string) string {
	return `{"id":1234,"name":"` + name + `","commandName":"Missing Search","status":"` + status + `","priority":"normal","trigger":"manual"}`
}

// SearchMissing runs integ's search-missing action against its fixtures
// (dir, served under api, such as "/sonarr/api/v3", with key): it plans
// with the right words, posts the app's command (named cmd) with the
// key, reads it back, and refuses when nothing is missing or a search is
// already queued, or fails when the search did. With SearchMissingMax or
// more missing it's unavailable and offers the app's Missing page instead.
// noun is what's missing ("episodes").
func SearchMissing(t *testing.T, integ integration.Integration, dir, api, key, cmd, noun string) {
	t.Helper()
	var mu sync.Mutex
	var posted []string
	auth := func(r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == api+"/command" {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(b))
			var body struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(b, &body)
			mu.Lock()
			posted = append(posted, body.Name)
			mu.Unlock()
		}
		return r.Header.Get("X-Api-Key") == key
	}
	srv := fixture.New(t, dir, auth, map[string]fixture.Route{
		"GET " + api + "/system/status":  {File: "system_status.json"},
		"GET " + api + "/health":         {File: "health.json"},
		"GET " + api + "/queue/status":   {File: "queue_status.json"},
		"GET " + api + "/wanted/missing": {File: "wanted_missing.json"},
		"GET " + api + "/wanted/cutoff":  {File: "wanted_cutoff.json"},
		"GET " + api + "/calendar":       {File: "calendar.json"},
		"GET " + api + "/command":        {Body: `[]`},
		"POST " + api + "/command":       {Status: http.StatusCreated, Body: cmdBody(cmd, "queued")},
		"GET " + api + "/command/1234":   {Body: cmdBody(cmd, "started")},
	})
	base, _, _ := strings.Cut(api, "/api/")
	h := integrationtest.New(t)
	inst := h.Connect(integ, map[string]any{"url": srv.URL + base, "tls": map[string]any{"mode": "verify"}}, map[string]string{"api_key": key})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	actor, ok := inst.(integration.Actor)
	if !ok {
		t.Fatal("not an Actor")
	}
	defs := integration.ActionDefs(actor)
	id := integ.Manifest().ID + ".app.search-missing"
	if len(defs) != 1 || defs[0].ID != id || defs[0].Tier != action.T1 || defs[0].Household {
		t.Fatalf("defs = %+v", defs)
	}
	env := acttest.New(t, h.Set.Graph, defs...)
	app := integ.Manifest().ID + ":i_test/app"
	missing := func(n int) {
		srv.Set("GET "+api+"/wanted/missing", fixture.Route{Body: fmt.Sprintf(`{"page":1,"pageSize":1,"totalRecords":%d,"records":[]}`, n)})
	}

	// As many as the limit, or more: the app's Missing page, not a search.
	missing(arr.SearchMissingMax)
	r := env.Refusal(id, app, nil)
	wantMsg := fmt.Sprintf("%d monitored %s are missing in Test. Cha Dash searches for fewer than %d at once; pick them in %s's Missing page instead.",
		arr.SearchMissingMax, noun, arr.SearchMissingMax, integ.Manifest().Name)
	if r.Code != "unavailable" || r.Message != wantMsg || r.Instead == nil ||
		r.Instead.Title != "Open Missing in Test" || r.Instead.URL != srv.URL+base+"/wanted/missing" {
		t.Fatalf("refusal at the limit = %+v (instead %+v)", r, r.Instead)
	}
	missing(arr.SearchMissingMax - 1)

	_, plan := env.Succeeded(id, app, nil)
	if plan.Label != "Search for missing "+noun+" in Test" || plan.Undo != nil || plan.Confirm != "undo" || len(plan.Steps) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	mu.Lock()
	if !slices.Equal(posted, []string{cmd}) {
		t.Errorf("posted %v, want %s", posted, cmd)
	}
	mu.Unlock()

	// A search already queued.
	srv.Set("GET "+api+"/command", fixture.Route{Body: "[" + cmdBody(cmd, "queued") + "]"})
	if r := env.Refusal(id, app, nil); r.Code != "precondition" || r.Message != "Test is already searching for missing "+noun+"." {
		t.Fatalf("refusal = %+v", r)
	}
	srv.Set("GET "+api+"/command", fixture.Route{Body: `[]`})

	// A search that failed at once.
	srv.Set("GET "+api+"/command/1234", fixture.Route{Body: cmdBody(cmd, "failed")})
	task, _ := env.Run(id, app, nil)
	if task.State != action.StateFailed || task.Error == nil || task.Error.Code != "search_failed" {
		t.Fatalf("task = %+v", task)
	}

	// Nothing missing.
	missing(0)
	if r := env.Refusal(id, app, nil); r.Code != "precondition" || r.Message != "Nothing monitored is missing in Test." {
		t.Fatalf("refusal = %+v", r)
	}
	// Another instance's app isn't this one's to search.
	if r := env.Refusal(id, integ.Manifest().ID+":i_other/app", nil); r.Code != "not_found" {
		t.Fatalf("refusal = %+v", r)
	}
	// The key never reaches the audit log.
	for _, e := range env.Events() {
		if strings.Contains(string(e.Params), key) {
			t.Fatalf("event = %+v", e)
		}
	}
}
