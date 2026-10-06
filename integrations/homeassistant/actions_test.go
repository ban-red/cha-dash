package homeassistant

import (
	"strings"
	"testing"

	"dash.cha.sh/integrations/internal/act/acttest"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// A script's state, in /api/states/<entity_id>'s shape (REST API docs).
func scriptBody(state, last string) []byte {
	lt := "null"
	if last != "" {
		lt = `"` + last + `"`
	}
	return []byte(`{"entity_id":"script.movie_night","state":"` + state + `","attributes":{"friendly_name":"Movie night","mode":"single","last_triggered":` + lt + `},"last_changed":"2026-10-03T12:00:00+00:00"}`)
}

func TestRunScript(t *testing.T) {
	f := newFake(t)
	f.set("/api/states/script.movie_night", scriptBody("off", "2026-10-02T19:30:00+00:00"))
	f.set("/api/states/script.gone", nil) // listed, but deleted in Home Assistant
	f.onCall = func(path string, body []byte) []byte {
		f.set("/api/states/script.movie_night", scriptBody("off", "2026-10-03T14:05:01+00:00"))
		return []byte("[]")
	}
	h := integrationtest.New(t)
	in := connect(t, h, f, map[string]any{"scripts": []any{"script.movie_night", "script.gone"}})
	mustSync(t, in, integrationtest.Watched)
	defs := integration.ActionDefs(in.(integration.Actor))
	if len(defs) != 1 || defs[0].ID != ActRunScript || defs[0].Tier != action.T1 || defs[0].Household {
		t.Fatalf("defs = %+v", defs)
	}
	env := acttest.New(t, h.Set.Graph, defs...)
	app := "homeassistant:i_test/app"

	_, plan := env.Succeeded(ActRunScript, app, map[string]any{"script": "script.movie_night"})
	if plan.Label != "Run script.movie_night in Test" || plan.Undo != nil || len(plan.Steps) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if len(calls) != 1 || calls[0] != `/api/services/script/turn_on {"entity_id":"script.movie_night"}` {
		t.Fatalf("calls = %v", calls)
	}

	// Only listed scripts: anything else is invalid, and nothing is called.
	for _, p := range []map[string]any{{"script": "script.unlock_front_door"}, {"script": "light.kitchen"}, {}, {"script": "script.movie_night", "service": "lock.unlock"}} {
		if r := env.Refusal(ActRunScript, app, p); r.Code != "invalid" || r.Status != 422 {
			t.Fatalf("%v: %+v", p, r)
		}
	}
	// A listed script Home Assistant doesn't have.
	if r := env.Refusal(ActRunScript, app, map[string]any{"script": "script.gone"}); r.Code != "precondition" || !strings.Contains(r.Message, "has no script.gone") {
		t.Fatalf("refusal = %+v", r)
	}
	// Already running.
	f.set("/api/states/script.movie_night", scriptBody("on", "2026-10-03T14:05:01+00:00"))
	if r := env.Refusal(ActRunScript, app, map[string]any{"script": "script.movie_night"}); r.Code != "precondition" || r.Message != "script.movie_night is already running." {
		t.Fatalf("refusal = %+v", r)
	}
	// The audit records the script, never the token.
	for _, e := range env.Events() {
		if strings.Contains(string(e.Params), token) {
			t.Fatalf("event = %+v", e)
		}
	}
	if e := env.Events()[len(env.Events())-1]; string(e.Params) != `{"script":"script.movie_night"}` || e.Result != "ok" {
		t.Fatalf("first event = %+v", e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("service calls after refusals: %v", f.calls)
	}
}

func TestNoScriptsNoActions(t *testing.T) {
	f := newFake(t)
	if defs := integration.ActionDefs(connect(t, integrationtest.New(t), f, nil).(integration.Actor)); len(defs) != 0 {
		t.Fatalf("defs = %+v", defs)
	}
	for _, bad := range []any{[]any{"light.kitchen"}, []any{"script.Bad-Name"}, "script.x"} {
		if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "http://ha.home.arpa:8123", "scripts": bad}, map[string]string{"token": token}); len(errs) == 0 {
			t.Errorf("scripts %v passed", bad)
		}
	}
}

func TestScriptVerifies(t *testing.T) {
	f := newFake(t)
	f.set("/api/states/script.movie_night", scriptBody("off", "2026-10-02T19:30:00+00:00"))
	in := connect(t, integrationtest.New(t), f, map[string]any{"scripts": []any{"script.movie_night"}}).(*instance)
	def := integration.ActionDefs(in)[0]
	ctx, cancel := acttest.Short(t)
	defer cancel()
	in.runs.set("t_1", "2026-10-02T19:30:00+00:00")
	req := action.Request{Action: ActRunScript, Task: "t_1", Params: []byte(`{"script":"script.movie_night"}`), Target: action.Target{ID: "homeassistant:i_test/app", Name: "Test"}}
	if err := def.Verify(ctx, req, nil); err == nil || err.Error() != "script.movie_night hasn't run" {
		t.Fatalf("verify = %v", err)
	}
}
