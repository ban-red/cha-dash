package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"dash.cha.sh/integrations/internal/act"
	"dash.cha.sh/internal/action"
	"dash.cha.sh/internal/integration"
)

// Device control (opt-in): turn lights and switches on and off, play and
// pause media players, set a thermostat's target. All T1. Only the domains
// the owner lists in the instance's `control` can be acted on; with none
// listed, there are no device actions. Locks, covers and the alarm are
// never offered: opening a door from a dashboard tile is too easy to do by
// accident.
//
// Every action targets the app and names the entity in its params, so the
// tile's panel can act on any entity it lists. The listing (⌘K, the sheet)
// leaves them out: with no entity, Preconditions refuse before any read.

// Action IDs.
const (
	ActEntityOn   = "homeassistant.entity.on"
	ActEntityOff  = "homeassistant.entity.off"
	ActMediaPlay  = "homeassistant.media.play"
	ActMediaPause = "homeassistant.media.pause"
	ActClimateSet = "homeassistant.climate.set"
)

// The domains `control` may list.
const (
	domLight   = "light"
	domSwitch  = "switch"
	domMedia   = "media_player"
	domClimate = "climate"
)

var controlDomains = []string{domLight, domSwitch, domMedia, domClimate}

// controlTitles label controlDomains in the form.
var controlTitles = []string{"Lights", "Switches", "Media players", "Thermostats"}

// entityState is the part of /api/states/<entity> the device actions read.
type entityState struct {
	State      string `json:"state"`
	Attributes struct {
		FriendlyName string   `json:"friendly_name"`
		Temperature  *float64 `json:"temperature"`
		MinTemp      *float64 `json:"min_temp"`
		MaxTemp      *float64 `json:"max_temp"`
	} `json:"attributes"`
}

// controls reports whether the owner lets Cha Dash act on domain.
func (in *instance) controls(domain string) bool { return slices.Contains(in.control, domain) }

// entityParam is the params' entity, restricted to domains by pattern.
func entityParam(domains ...string) *integration.Schema {
	return &integration.Schema{Type: "string", Title: "Entity", Pattern: `^(` + strings.Join(domains, "|") + `)\.[a-z0-9_]+$`, MaxLength: new(255)}
}

// controlDefs are the device actions for the domains the owner allows.
func (in *instance) controlDefs() []action.Def {
	var defs []action.Def
	var onOff []string
	for _, d := range []string{domLight, domSwitch} {
		if in.controls(d) {
			onOff = append(onOff, d)
		}
	}
	if len(onOff) > 0 {
		// Household may switch lights, but not switches: a switch can be a
		// server's smart plug.
		household := !slices.Contains(onOff, domSwitch)
		params := integration.Params(integration.Object(integration.Prop("entity", entityParam(onOff...))).Require("entity"))
		defs = append(defs,
			in.deviceDef(ActEntityOn, ActEntityOff, "Turn on", "Turn off", params, household),
			in.deviceDef(ActEntityOff, ActEntityOn, "Turn off", "Turn on", params, household),
		)
	}
	if in.controls(domMedia) {
		params := integration.Params(integration.Object(integration.Prop("entity", entityParam(domMedia))).Require("entity"))
		defs = append(defs,
			in.deviceDef(ActMediaPlay, ActMediaPause, "Play", "Pause", params, true),
			in.deviceDef(ActMediaPause, ActMediaPlay, "Pause", "Play", params, true),
		)
	}
	if in.controls(domClimate) {
		params := integration.Params(integration.Object(
			integration.Prop("entity", entityParam(domClimate)),
			integration.Prop("temperature", &integration.Schema{Type: "number", Title: "Target", Minimum: new(-50.0), Maximum: new(100.0)}),
		).Require("entity", "temperature"))
		d := in.deviceDef(ActClimateSet, "", "Set", "", params, true)
		d.Label = func(r action.Request) string {
			p := deviceOf(r)
			if p.Entity == "" {
				return "Set a thermostat in " + r.Target.Name
			}
			return "Set " + p.Entity + " to " + strconv.FormatFloat(p.Temperature, 'f', -1, 64) + "°"
		}
		defs = append(defs, d)
	}
	return defs
}

// deviceDef is one device action; undo, when set, is the opposite one with
// the same params.
func (in *instance) deviceDef(id, undo, verb, undoVerb string, params action.Schema, household bool) action.Def {
	d := action.Def{
		ID: id, Title: verb, Targets: []string{"app"}, Tier: action.T1, Household: household,
		Params:        params,
		Resolve:       act.App(in.appID, in.name),
		Preconditions: in.devicePreconditions,
		Label: func(r action.Request) string {
			if e := deviceOf(r).Entity; e != "" {
				return verb + " " + e
			}
			return verb + " a device in " + r.Target.Name
		},
		Steps: func(r action.Request) []action.Step {
			return []action.Step{{Label: "Ask " + r.Target.Name + " to " + strings.ToLower(verb) + " " + deviceOf(r).Entity}, {Label: "Check that it changed"}}
		},
		Verify: in.deviceVerify,
	}
	if undo != "" {
		d.Undo = func(r action.Request) *action.Undo {
			return &action.Undo{Action: undo, Params: r.Params, Label: undoVerb + " " + deviceOf(r).Entity}
		}
	}
	return d
}

type deviceParams struct {
	Entity      string  `json:"entity"`
	Temperature float64 `json:"temperature"`
}

func deviceOf(r action.Request) deviceParams {
	var p deviceParams
	_ = r.Decode(&p)
	return p
}

func (in *instance) readEntity(ctx context.Context, entity string) (entityState, error) {
	var s entityState
	err := in.getJSON(ctx, "/api/states/"+entity, nil, &s)
	if errors.Is(err, errMissing) {
		return s, action.Precondition("Home Assistant has no " + entity + ".")
	}
	return s, err
}

// devicePreconditions: the entity exists, answers, isn't already as asked
// and, for a thermostat, the target is within its range. The params'
// pattern already holds the entity to the allowed domains.
func (in *instance) devicePreconditions(ctx context.Context, req action.Request) error {
	p := deviceOf(req)
	if p.Entity == "" {
		return action.Precondition("Pick a device.") // the listing: nothing to offer
	}
	s, err := in.readEntity(ctx, p.Entity)
	if err != nil {
		return err
	}
	name := s.Attributes.FriendlyName
	if name == "" {
		name = p.Entity
	}
	if s.State == "unavailable" || s.State == "unknown" {
		return action.Precondition(name + " is " + s.State + " in Home Assistant.")
	}
	switch req.Action {
	case ActEntityOn:
		if s.State == "on" {
			return action.Precondition(name + " is already on.")
		}
	case ActEntityOff:
		if s.State == "off" {
			return action.Precondition(name + " is already off.")
		}
	case ActMediaPlay:
		if s.State == "playing" {
			return action.Precondition(name + " is already playing.")
		}
		if s.State == "off" {
			return action.Precondition(name + " is off.")
		}
	case ActMediaPause:
		if s.State != "playing" {
			return action.Precondition(name + " isn't playing.")
		}
	case ActClimateSet:
		if s.State == "off" {
			return action.Precondition(name + " is off. Turn it on in Home Assistant first.")
		}
		if lo := s.Attributes.MinTemp; lo != nil && p.Temperature < *lo {
			return action.Precondition(fmt.Sprintf("%s goes no lower than %g.", name, *lo))
		}
		if hi := s.Attributes.MaxTemp; hi != nil && p.Temperature > *hi {
			return action.Precondition(fmt.Sprintf("%s goes no higher than %g.", name, *hi))
		}
	}
	return nil
}

// service is the Home Assistant service an action calls, and its body.
func service(req action.Request) (domain, svc string, body map[string]any) {
	p := deviceOf(req)
	d, _, _ := strings.Cut(p.Entity, ".")
	body = map[string]any{"entity_id": p.Entity}
	switch req.Action {
	case ActEntityOn:
		return d, "turn_on", body
	case ActEntityOff:
		return d, "turn_off", body
	case ActMediaPlay:
		return domMedia, "media_play", body
	case ActMediaPause:
		return domMedia, "media_pause", body
	case ActClimateSet:
		body["temperature"] = p.Temperature
		return domClimate, "set_temperature", body
	}
	return "", "", nil
}

// executeDevice calls the action's service.
func (in *instance) executeDevice(ctx context.Context, req action.Request, run *action.TaskRun) error {
	domain, svc, body := service(req)
	if domain == "" {
		return action.Fail("invalid", "Unknown action "+req.Action+".")
	}
	entity := deviceOf(req).Entity
	run.Step("Ask " + req.Target.Name + " to " + strings.ToLower(labelVerb(req.Action)) + " " + entity)
	if err := in.callService(ctx, domain, svc, body); err != nil {
		return err
	}
	run.Logf("Home Assistant took %s.%s for %s", domain, svc, entity)
	return nil
}

func labelVerb(id string) string {
	switch id {
	case ActEntityOn:
		return "Turn on"
	case ActEntityOff:
		return "Turn off"
	case ActMediaPlay:
		return "Play"
	case ActMediaPause:
		return "Pause"
	}
	return "Set"
}

// callService POSTs /api/services/<domain>/<service> with body.
func (in *instance) callService(ctx context.Context, domain, svc string, body map[string]any) error {
	b, _ := json.Marshal(body)
	u := in.base.JoinPath("api", "services", domain, svc)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := in.http.Do(hr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return action.Fail("auth", "Home Assistant refused the token, or its user may not call "+domain+"."+svc+".")
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return action.Fail("upstream", fmt.Sprintf("Home Assistant answered %s.%s with %s: %s", domain, svc, resp.Status, strings.TrimSpace(string(msg))))
	}
	return nil
}

// deviceVerify waits for the entity to show the change.
func (in *instance) deviceVerify(ctx context.Context, req action.Request, run *action.TaskRun) error {
	p := deviceOf(req)
	return act.Until(ctx, run, "Check that it changed", func(ctx context.Context) (bool, error) {
		s, err := in.readEntity(ctx, p.Entity)
		if err != nil {
			return false, err
		}
		var done bool
		switch req.Action {
		case ActEntityOn:
			done = s.State == "on"
		case ActEntityOff:
			done = s.State == "off"
		case ActMediaPlay:
			done = s.State == "playing"
		case ActMediaPause:
			done = s.State != "playing"
		case ActClimateSet:
			if t := s.Attributes.Temperature; t != nil && math.Abs(*t-p.Temperature) < 0.05 {
				return true, nil
			}
			return false, fmt.Errorf("%s's target is still %s", p.Entity, targetWords(s.Attributes.Temperature))
		}
		if done {
			return true, nil
		}
		return false, fmt.Errorf("%s is still %s", p.Entity, s.State)
	})
}

func targetWords(t *float64) string {
	if t == nil {
		return "unset"
	}
	return strconv.FormatFloat(*t, 'f', -1, 64) + "°"
}
