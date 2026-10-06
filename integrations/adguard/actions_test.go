package adguard

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"dash.cha.sh/integrations/internal/act/acttest"
	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// protection is a fake AdGuard Home whose POST /control/protection
// ({enabled, duration} from the OpenAPI document) changes what
// /control/status says.
type protection struct {
	srv   *fixture.Server
	mu    sync.Mutex
	posts []string
}

func newProtection(t *testing.T) *protection {
	p := &protection{}
	status, err := os.ReadFile(filepath.Join(fixture.Dir(t, ID), "control_status.json"))
	if err != nil {
		t.Fatal(err)
	}
	auth := func(r *http.Request) bool {
		u, pw, ok := r.BasicAuth()
		if !ok || u != user || pw != pass {
			return false
		}
		if r.Method == http.MethodPost && r.URL.Path == "/adguard/control/protection" {
			b, _ := io.ReadAll(r.Body)
			var body struct {
				Enabled bool `json:"enabled"`
			}
			_ = json.Unmarshal(b, &body)
			p.mu.Lock()
			p.posts = append(p.posts, string(b))
			p.mu.Unlock()
			next := bytes.Replace(status, []byte(`"protection_enabled": true`), []byte(`"protection_enabled": false`), 1)
			if body.Enabled {
				next = status
			}
			p.srv.Set("GET /adguard/control/status", fixture.Route{Body: string(next)})
		}
		return true
	}
	p.srv = fixture.New(t, fixture.Dir(t, ID), auth, map[string]fixture.Route{
		"GET /adguard/control/status":           {File: "control_status.json"},
		"GET /adguard/control/stats":            {File: "control_stats.json"},
		"GET /adguard/control/filtering/status": {File: "control_filtering_status.json"},
		"POST /adguard/control/protection":      {Body: "OK\n", ContentType: "text/plain; charset=utf-8"},
	})
	return p
}

func TestPauseProtection(t *testing.T) {
	p := newProtection(t)
	h := integrationtest.New(t)
	in := connect(t, h, p.srv.URL+"/adguard", pass)
	if err := in.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	defs := integration.ActionDefs(in.(integration.Actor))
	for _, d := range defs {
		if !d.Household || d.Tier != action.T1 {
			t.Fatalf("%s: household %v, %s", d.ID, d.Household, d.Tier)
		}
	}
	env := acttest.New(t, h.Set.Graph, defs...)
	app := "adguard:i_test/app"
	_, plan := env.Succeeded(ActPause, app, nil)
	if plan.Label != "Pause protection in Test for 5 minutes" || plan.Undo == nil || plan.Undo.Action != ActResume {
		t.Fatalf("plan = %+v", plan)
	}
	if r := env.Refusal(ActPause, app, nil); r.Code != "precondition" || r.Message != "Protection in Test is already off." {
		t.Fatalf("refusal = %+v", r)
	}
	env.Succeeded(ActResume, app, nil)
	if r := env.Refusal(ActResume, app, nil); r.Code != "precondition" || r.Message != "Protection in Test is already on." {
		t.Fatalf("refusal = %+v", r)
	}
	p.mu.Lock()
	if want := []string{`{"duration":300000,"enabled":false}`, `{"enabled":true}`}; !slices.Equal(p.posts, want) {
		t.Errorf("posts = %v", p.posts)
	}
	p.mu.Unlock()
	for _, e := range env.Events() {
		if strings.Contains(string(e.Params), pass) {
			t.Fatalf("event = %+v", e)
		}
	}
}

func TestPauseVerifies(t *testing.T) {
	srv := server(t) // POSTs go nowhere: protection stays on
	in := connect(t, integrationtest.New(t), srv.URL+"/adguard", pass).(*instance)
	var pause action.Def
	for _, d := range integration.ActionDefs(in) {
		if d.ID == ActPause {
			pause = d
		}
	}
	ctx, cancel := acttest.Short(t)
	defer cancel()
	if err := pause.Verify(ctx, action.Request{Action: ActPause, Target: action.Target{ID: "adguard:i_test/app", Name: "Test"}}, nil); err == nil || err.Error() != "AdGuard Home reports protection on" {
		t.Fatalf("verify = %v", err)
	}
}
