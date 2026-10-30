package homeassistant

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"dash.cha.sh/integrations/internal/act/acttest"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// An entity's state, in /api/states/<entity_id>'s shape (REST API docs).
func stateBody(entity, state, attrs string) []byte {
	if attrs == "" {
		attrs = `{}`
	}
	return []byte(`{"entity_id":"` + entity + `","state":"` + state + `","attributes":` + attrs + `,"last_changed":"2026-10-03T12:00:00+00:00"}`)
}

func ids(defs []action.Def) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.ID
	}
	return out
}

func TestNoControlNoDeviceActions(t *testing.T) {
	in := connect(t, integrationtest.New(t), newFake(t), nil)
	if defs := integration.ActionDefs(in.(integration.Actor)); len(defs) != 0 {
		t.Fatalf("defs = %v", ids(defs))
	}
	for _, bad := range []any{[]any{"lock"}, []any{"cover"}, []any{"alarm_control_panel"}, "light"} {
		if errs := integrationtest.Validate(Integration{}, map[string]any{"url": "http://ha.home.arpa:8123", "control": bad}, map[string]string{"token": token}); len(errs) == 0 {
			t.Errorf("control %v passed", bad)
		}
	}
}

func TestControlDefs(t *testing.T) {
	f := newFake(t)
	in := connect(t, integrationtest.New(t), f, map[string]any{"control": []any{"light", "media_player"}})
	defs := integration.ActionDefs(in.(integration.Actor))
	if got := ids(defs); !slices.Equal(got, []string{ActEntityOn, ActEntityOff, ActMediaPlay, ActMediaPause}) {
		t.Fatalf("defs = %v", got)
	}
	for _, d := range defs {
		if d.Tier != action.T1 || !d.Household {
			t.Errorf("%s: tier %v, household %v", d.ID, d.Tier, d.Household)
		}
	}
	// With switches allowed, household may not switch: a switch can be a
	// server's plug.
	in = connect(t, integrationtest.New(t), f, map[string]any{"control": []any{"light", "switch", "climate"}})
	defs = integration.ActionDefs(in.(integration.Actor))
	if got := ids(defs); !slices.Equal(got, []string{ActEntityOn, ActEntityOff, ActClimateSet}) || defs[0].Household || !defs[2].Household {
		t.Fatalf("defs = %v", got)
	}
}

func TestTurnOnAndOff(t *testing.T) {
	f := newFake(t)
	f.set("/api/states/light.kitchen", stateBody("light.kitchen", "off", `{"friendly_name":"Kitchen"}`))
	f.onCall = func(path string, body []byte) []byte {
		switch path {
		case "/api/services/light/turn_on":
			f.set("/api/states/light.kitchen", stateBody("light.kitchen", "on", `{"friendly_name":"Kitchen"}`))
		case "/api/services/light/turn_off":
			f.set("/api/states/light.kitchen", stateBody("light.kitchen", "off", `{"friendly_name":"Kitchen"}`))
		}
		return []byte("[]")
	}
	h := integrationtest.New(t)
	in := connect(t, h, f, map[string]any{"control": []any{"light"}})
	mustSync(t, in, integrationtest.Watched)
	env := acttest.New(t, h.Set.Graph, integration.ActionDefs(in.(integration.Actor))...)
	app := "homeassistant:i_test/app"

	// The listing offers none: they need an entity, which the tile's panel names.
	offers, err := env.Engine.ActionsFor(t.Context(), acttest.Owner, app)
	if err != nil || len(offers) != 0 {
		t.Fatalf("offers = %+v, %v", offers, err)
	}

	_, plan := env.Succeeded(ActEntityOn, app, map[string]any{"entity": "light.kitchen"})
	if plan.Label != "Turn on light.kitchen" || plan.Undo == nil || plan.Undo.Action != ActEntityOff || string(plan.Undo.Params) != `{"entity":"light.kitchen"}` {
		t.Fatalf("plan = %+v, undo %+v", plan, plan.Undo)
	}
	// Already on.
	if r := env.Refusal(ActEntityOn, app, map[string]any{"entity": "light.kitchen"}); r.Code != "precondition" || r.Message != "Kitchen is already on." {
		t.Fatalf("refusal = %+v", r)
	}
	env.Succeeded(ActEntityOff, app, map[string]any{"entity": "light.kitchen"})

	// Only allowed domains, and nothing but an entity.
	for _, p := range []map[string]any{{"entity": "switch.server_rack"}, {"entity": "lock.front_door"}, {"entity": "light.kitchen", "service": "lock.unlock"}, {"entity": "light.Kitchen; x"}, {}} {
		if r := env.Refusal(ActEntityOn, app, p); r.Code != "invalid" || r.Status != 422 {
			t.Fatalf("%v: %+v", p, r)
		}
	}
	// Home Assistant has no such light, or it's unavailable.
	f.set("/api/states/light.gone", nil)
	if r := env.Refusal(ActEntityOn, app, map[string]any{"entity": "light.gone"}); r.Code != "precondition" || !strings.Contains(r.Message, "has no light.gone") {
		t.Fatalf("refusal = %+v", r)
	}
	f.set("/api/states/light.porch", stateBody("light.porch", "unavailable", `{"friendly_name":"Porch"}`))
	if r := env.Refusal(ActEntityOn, app, map[string]any{"entity": "light.porch"}); r.Code != "precondition" || r.Message != "Porch is unavailable in Home Assistant." {
		t.Fatalf("refusal = %+v", r)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if want := []string{`/api/services/light/turn_on {"entity_id":"light.kitchen"}`, `/api/services/light/turn_off {"entity_id":"light.kitchen"}`}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v", f.calls)
	}
	for _, e := range env.Events() {
		if strings.Contains(string(e.Params), token) {
			t.Fatalf("event = %+v", e)
		}
	}
}

func TestMediaAndClimate(t *testing.T) {
	f := newFake(t)
	f.set("/api/states/media_player.bedroom_tv", stateBody("media_player.bedroom_tv", "paused", `{"friendly_name":"Bedroom TV"}`))
	f.set("/api/states/climate.bedroom", stateBody("climate.bedroom", "heat", `{"friendly_name":"Bedroom","temperature":17,"min_temp":7,"max_temp":30}`))
	f.onCall = func(path string, body []byte) []byte {
		switch path {
		case "/api/services/media_player/media_play":
			f.set("/api/states/media_player.bedroom_tv", stateBody("media_player.bedroom_tv", "playing", `{"friendly_name":"Bedroom TV"}`))
		case "/api/services/climate/set_temperature":
			var b struct {
				Temperature float64 `json:"temperature"`
			}
			_ = json.Unmarshal(body, &b)
			t := string(must(json.Marshal(b.Temperature)))
			f.set("/api/states/climate.bedroom", stateBody("climate.bedroom", "heat", `{"friendly_name":"Bedroom","temperature":`+t+`,"min_temp":7,"max_temp":30}`))
		}
		return []byte("[]")
	}
	h := integrationtest.New(t)
	in := connect(t, h, f, map[string]any{"control": []any{"media_player", "climate"}})
	mustSync(t, in, integrationtest.Watched)
	env := acttest.New(t, h.Set.Graph, integration.ActionDefs(in.(integration.Actor))...)
	app := "homeassistant:i_test/app"

	if r := env.Refusal(ActMediaPause, app, map[string]any{"entity": "media_player.bedroom_tv"}); r.Code != "precondition" || r.Message != "Bedroom TV isn't playing." {
		t.Fatalf("refusal = %+v", r)
	}
	_, plan := env.Succeeded(ActMediaPlay, app, map[string]any{"entity": "media_player.bedroom_tv"})
	if plan.Undo == nil || plan.Undo.Action != ActMediaPause {
		t.Fatalf("plan = %+v", plan)
	}

	_, plan = env.Succeeded(ActClimateSet, app, map[string]any{"entity": "climate.bedroom", "temperature": 19.5})
	if plan.Label != "Set climate.bedroom to 19.5°" || plan.Undo != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if r := env.Refusal(ActClimateSet, app, map[string]any{"entity": "climate.bedroom", "temperature": 35}); r.Code != "precondition" || r.Message != "Bedroom goes no higher than 30." {
		t.Fatalf("refusal = %+v", r)
	}
	if r := env.Refusal(ActClimateSet, app, map[string]any{"entity": "climate.bedroom"}); r.Code != "invalid" {
		t.Fatalf("refusal = %+v", r)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if want := []string{`/api/services/media_player/media_play {"entity_id":"media_player.bedroom_tv"}`, `/api/services/climate/set_temperature {"entity_id":"climate.bedroom","temperature":19.5}`}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls = %v", f.calls)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
