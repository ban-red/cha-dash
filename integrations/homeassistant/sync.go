package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// Scopes: the service's health comes from every sync, the readings only
// from syncs that read /api/states.
const (
	scopeGraph  = "graph"
	scopeStates = "states"
)

// instance is one Home Assistant. Sync runs on one goroutine and Test on a
// separate instance, so the state needs no lock.
type instance struct {
	d            integration.Deps
	http         *http.Client
	base         *url.URL
	token        string
	name         string
	link         string
	appID        string
	powerEntity  string
	energyEntity string

	needFull  bool
	version   string
	state     string
	health    integration.Health
	tzName    string
	tz        *time.Location
	statesAt  time.Time
	derived   derived
	day       string               // the local date the baselines belong to
	baselines map[string]float64   // energy meters' readings at local midnight, kWh
	tried     map[string]time.Time // when each meter's baseline was last asked for
}

// haConfig is the part of /api/config the integration reads.
type haConfig struct {
	Version      string `json:"version"`
	State        string `json:"state"`
	SafeMode     bool   `json:"safe_mode"`
	RecoveryMode bool   `json:"recovery_mode"`
	TimeZone     string `json:"time_zone"`
}

// entity is one item of /api/states, with only the attributes the
// derivation uses.
type entity struct {
	EntityID   string `json:"entity_id"`
	State      string `json:"state"`
	Attributes struct {
		DeviceClass string          `json:"device_class"`
		StateClass  string          `json:"state_class"`
		Unit        string          `json:"unit_of_measurement"`
		Members     json.RawMessage `json:"entity_id"`
		IsHueGroup  bool            `json:"is_hue_group"`
		LastReset   *string         `json:"last_reset"`
	} `json:"attributes"`
}

func (e entity) domain() string {
	d, _, _ := strings.Cut(e.EntityID, ".")
	return d
}

// group reports a group of other entities: a light or switch group lists
// its members in attributes.entity_id; a Hue room says is_hue_group.
func (e entity) group() bool {
	if e.Attributes.IsHueGroup {
		return true
	}
	var members []string
	return json.Unmarshal(e.Attributes.Members, &members) == nil && len(members) > 0
}

// number is the state as a finite number.
func (e entity) number() (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(e.State), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// Unit factors to kW and kWh.
var (
	powerUnits  = map[string]float64{"mW": 1e-6, "W": 1e-3, "kW": 1, "MW": 1e3, "GW": 1e6}
	energyUnits = map[string]float64{"mWh": 1e-6, "Wh": 1e-3, "kWh": 1, "MWh": 1e3, "GWh": 1e6, "J": 1 / 3.6e6, "kJ": 1 / 3600.0, "MJ": 1 / 3.6, "GJ": 1000 / 3.6}
)

// derived is what the readings say.
type derived struct {
	read          bool
	entities      int
	people        int
	peopleTotal   int
	lights        int
	lightsTotal   int
	switches      int
	switchesTotal int
	powerKW       float64
	powerEntity   string
	energyKWh     float64
	energyEntity  string
}

// meter is an energy sensor that accumulates.
type meter struct {
	id         string
	kwh        float64
	factor     float64
	resetToday bool // it starts from zero at local midnight (a daily utility meter)
}

// Sync implements integration.Instance. Every sync reads /api/config (the
// health); /api/states (which can be megabytes) is read on every watched
// sync and every 30 minutes while idle.
func (in *instance) Sync(ctx context.Context, want integration.Interest) (err error) {
	defer func() {
		if err != nil {
			in.needFull = true
		}
	}()
	now := in.d.Now()
	var cfg haConfig
	if err := in.getJSON(ctx, "/api/config", nil, &cfg); err != nil {
		return err
	}
	if cfg.Version == "" {
		return &integration.RepairError{Code: integration.CodeAPIChanged, Message: "GET /api/config has no version: this isn't an API this integration knows"}
	}
	in.applyConfig(cfg)
	readStates := in.needFull || want.Watched || in.statesAt.IsZero() || now.Sub(in.statesAt) >= statesEveryIdle
	if readStates {
		var states []entity
		if err := in.getJSON(ctx, "/api/states", nil, &states); err != nil {
			return err
		}
		in.derive(ctx, states, now)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		in.statesAt = now
	}
	in.needFull = false
	in.d.Emit.SetVersion(in.version)
	if err := in.d.Emit.Replace(scopeGraph, in.graphFacts()); err != nil {
		return err
	}
	if readStates {
		return in.d.Emit.Replace(scopeStates, in.statesFacts())
	}
	return nil
}

func (in *instance) applyConfig(cfg haConfig) {
	in.version = cfg.Version
	in.state = "running"
	in.health = integration.Health{Status: integration.HealthOK}
	attention := func(reason string) {
		in.health = integration.Health{Status: integration.HealthAttention, Reason: reason}
	}
	switch strings.ToUpper(cfg.State) {
	case "", "RUNNING":
	case "NOT_RUNNING", "STARTING":
		in.state = "starting"
		attention("Home Assistant is starting")
	case "STOPPING", "FINAL_WRITE", "STOPPED":
		attention("Home Assistant is shutting down")
	default:
		attention("Home Assistant reports its state as " + strings.ToLower(cfg.State))
	}
	switch {
	case cfg.RecoveryMode:
		attention("Home Assistant started in recovery mode: its configuration didn't load")
	case cfg.SafeMode:
		attention("Home Assistant is running in safe mode, with custom integrations off")
	}
	if cfg.TimeZone != in.tzName {
		in.tzName, in.tz = cfg.TimeZone, time.UTC
		if loc, err := time.LoadLocation(cfg.TimeZone); err == nil {
			in.tz = loc
		} else if cfg.TimeZone != "" {
			in.d.Log.Warn("unknown time zone; energy today is counted from midnight UTC", "time_zone", cfg.TimeZone)
		}
	}
}

// derive turns the entity states into the readings. See README.md for the
// rules.
func (in *instance) derive(ctx context.Context, states []entity, now time.Time) {
	d := derived{read: true, entities: len(states), powerKW: integration.Unknown(), energyKWh: integration.Unknown()}
	local := now.In(in.tz)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, in.tz)
	var meters []meter
	for _, e := range states {
		switch e.domain() {
		case "person":
			d.peopleTotal++
			if e.State == "home" {
				d.people++
			}
		case "light":
			if !e.group() {
				d.lightsTotal++
				if e.State == "on" {
					d.lights++
				}
			}
		case "switch":
			if !e.group() {
				d.switchesTotal++
				if e.State == "on" {
					d.switches++
				}
			}
		case "sensor":
			v, ok := e.number()
			if !ok {
				continue
			}
			switch e.Attributes.DeviceClass {
			case "power":
				f, ok := powerUnits[e.Attributes.Unit]
				if !ok || (in.powerEntity != "" && e.EntityID != in.powerEntity) {
					continue
				}
				kw := v * f
				if in.powerEntity == "" && kw < 0 {
					continue // net metering exporting: not the house's draw
				}
				if math.IsNaN(d.powerKW) || kw > d.powerKW {
					d.powerKW, d.powerEntity = kw, e.EntityID
				}
			case "energy":
				f, ok := energyUnits[e.Attributes.Unit]
				if !ok || (e.Attributes.StateClass != "total_increasing" && e.Attributes.StateClass != "total") ||
					(in.energyEntity != "" && e.EntityID != in.energyEntity) {
					continue
				}
				m := meter{id: e.EntityID, kwh: v * f, factor: f}
				if r := e.Attributes.LastReset; r != nil {
					if t, err := time.Parse(time.RFC3339Nano, *r); err == nil && !t.Before(midnight) {
						m.resetToday = true
					}
				}
				meters = append(meters, m)
			}
		}
	}
	if in.powerEntity != "" {
		d.powerEntity = in.powerEntity
	}
	if !math.IsNaN(d.powerKW) {
		d.powerKW = math.Round(d.powerKW*1000) / 1000
	}
	in.baselinesFor(ctx, meters, midnight, now)
	for _, m := range meters {
		inc := m.kwh
		if !m.resetToday {
			b, ok := in.baselines[m.id]
			if !ok {
				continue
			}
			if inc = m.kwh - b; inc < 0 {
				inc = m.kwh // the meter was reset since midnight
			}
		}
		if math.IsNaN(d.energyKWh) || inc > d.energyKWh {
			d.energyKWh, d.energyEntity = inc, m.id
		}
	}
	if in.energyEntity != "" {
		d.energyEntity = in.energyEntity
	}
	if !math.IsNaN(d.energyKWh) {
		d.energyKWh = math.Round(d.energyKWh*100) / 100
	}
	in.derived = d
}

// baselinesFor reads, once a day, what each accumulating meter showed at
// local midnight: one /api/history/period request for all of them, asking
// for the minute after midnight, whose first state is the one in force at
// midnight. It's best effort: without the recorder there's no history, and
// energy today stays unknown.
func (in *instance) baselinesFor(ctx context.Context, meters []meter, midnight, now time.Time) {
	if day := midnight.Format("2006-01-02"); day != in.day {
		in.day = day
		clear(in.baselines)
		clear(in.tried)
	}
	factor := map[string]float64{}
	var ids []string
	for _, m := range meters {
		if m.resetToday {
			continue
		}
		if _, ok := in.baselines[m.id]; ok {
			continue
		}
		if t, ok := in.tried[m.id]; ok && now.Sub(t) < baselineRetry {
			continue
		}
		factor[m.id] = m.factor
		ids = append(ids, m.id)
	}
	if len(ids) == 0 {
		return
	}
	slices.Sort(ids)
	ids = ids[:min(len(ids), maxBaselines)]
	for _, id := range ids {
		in.tried[id] = now
	}
	q := url.Values{
		"filter_entity_id": {strings.Join(ids, ",")},
		"end_time":         {midnight.Add(time.Minute).UTC().Format(time.RFC3339)},
		"minimal_response": {""},
		"no_attributes":    {""},
	}
	var hist [][]struct {
		EntityID string `json:"entity_id"`
		State    string `json:"state"`
	}
	err := in.getJSON(ctx, "/api/history/period/"+midnight.UTC().Format(time.RFC3339), q, &hist)
	if err != nil {
		if ctx.Err() == nil {
			lvl := "no history for energy today"
			if !errors.Is(err, errMissing) {
				lvl = "reading energy history failed"
			}
			in.d.Log.Debug(lvl, "err", err)
		}
		return
	}
	for _, list := range hist {
		if len(list) == 0 {
			continue
		}
		f, ok := factor[list[0].EntityID]
		if !ok {
			continue
		}
		if v, ok := (entity{State: list[0].State}).number(); ok {
			in.baselines[list[0].EntityID] = v * f
		}
	}
}

// graphFacts is the service.
func (in *instance) graphFacts() integration.Facts {
	attrs := map[string]any{"type": ID, "version": in.version, "url": in.link}
	if d := in.derived; d.read {
		attrs["entities"] = d.entities
		attrs["people_total"] = d.peopleTotal
		attrs["lights_total"] = d.lightsTotal
		attrs["switches_total"] = d.switchesTotal
		if d.powerEntity != "" {
			attrs["power_entity"] = d.powerEntity
		}
		if d.energyEntity != "" {
			attrs["energy_entity"] = d.energyEntity
		}
	}
	return integration.Facts{
		Resources: []integration.Resource{{
			ID: in.appID, Kind: "app", Name: in.name, State: in.state, Desired: "running", Health: in.health, Attrs: attrs,
			Links: []integration.Link{{Title: in.name, URL: in.link}},
			Caps:  []string{"home.entities"},
		}},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.appID}},
	}
}

// statesFacts is the readings, on the simulator's scales.
func (in *instance) statesFacts() integration.Facts {
	d := in.derived
	count := func(key string, v, total int) integration.Metric {
		top := float64(max(total, 1))
		return integration.Metric{Resource: in.appID, Key: key, Unit: "count", Min: 0, Max: top, Band: [2]float64{0, top}, Value: float64(v)}
	}
	return integration.Facts{Metrics: []integration.Metric{
		count("people_home", d.people, d.peopleTotal),
		count("lights_on", d.lights, d.lightsTotal),
		count("switches_on", d.switches, d.switchesTotal),
		{Resource: in.appID, Key: "power.kw", Unit: "kW", Min: 0, Max: 12, Band: [2]float64{0.6, 2.2}, Value: d.powerKW},
		{Resource: in.appID, Key: "energy.kwh", Unit: "kWh", Min: 0, Max: 200, Band: [2]float64{0, 40}, Value: d.energyKWh},
	}}
}
