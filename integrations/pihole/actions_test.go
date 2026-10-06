package pihole

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/integrations/internal/act/acttest"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// setBlocking is POST /api/dns/blocking, from Pi-hole v6's API docs:
// {blocking, timer} in, the new state out. Called with p.mu held.
func (p *pihole) setBlocking(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body struct {
		Blocking *bool    `json:"blocking"`
		Timer    *float64 `json:"timer"`
	}
	if json.Unmarshal(b, &body) != nil || body.Blocking == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p.posts = append(p.posts, string(b))
	state := `{"blocking":"enabled","timer":null,"took":0.0001}`
	if !*body.Blocking {
		state = `{"blocking":"disabled","timer":300,"took":0.0001}`
	}
	p.bodies["/api/dns/blocking"] = state
	w.Write([]byte(state))
}

func TestPauseBlocking(t *testing.T) {
	p := newPihole(t)
	h := integrationtest.New(t)
	in := connect(t, h, p.URL, pass)
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
	app := "pihole:i_test/app"

	_, plan := env.Succeeded(ActPause, app, nil)
	if plan.Label != "Pause blocking in Test for 5 minutes" || plan.Undo == nil || plan.Undo.Action != ActResume || plan.Undo.Label != "Resume blocking in Test" {
		t.Fatalf("plan = %+v", plan)
	}
	if r := env.Refusal(ActPause, app, nil); r.Code != "precondition" || r.Message != "Blocking in Test is already paused for another 5 min." {
		t.Fatalf("refusal = %+v", r)
	}
	_, plan = env.Succeeded(ActResume, app, nil)
	if plan.Label != "Resume blocking in Test" || plan.Undo != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if r := env.Refusal(ActResume, app, nil); r.Code != "precondition" || r.Message != "Blocking in Test is already on." {
		t.Fatalf("refusal = %+v", r)
	}
	p.with(func(p *pihole) {
		if want := []string{`{"blocking":false,"timer":300}`, `{"blocking":true,"timer":null}`}; !slices.Equal(p.posts, want) {
			t.Errorf("posts = %v", p.posts)
		}
	})
	for _, e := range env.Events() {
		if strings.Contains(string(e.Params), pass) {
			t.Fatalf("event = %+v", e)
		}
	}
}

func TestPauseVerifies(t *testing.T) {
	p := newPihole(t)
	in := connect(t, integrationtest.New(t), p.URL, pass).(*instance)
	var pause action.Def
	for _, d := range integration.ActionDefs(in) {
		if d.ID == ActPause {
			pause = d
		}
	}
	ctx, cancel := acttest.Short(t)
	defer cancel()
	// Blocking stays on (the fixture): the check fails, saying what it saw.
	if err := pause.Verify(ctx, action.Request{Action: ActPause, Target: action.Target{ID: "pihole:i_test/app", Name: "Test"}}, nil); err == nil || err.Error() != "Pi-hole reports blocking enabled" {
		t.Fatalf("verify = %v", err)
	}
}
