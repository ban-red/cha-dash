package homeassistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
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
// from syncs that read /api/states, the calendars from syncs that read them.
const (
	scopeGraph     = "graph"
	scopeStates    = "states"
	scopeCalendars = "calendars"
)

// instance is one Home Assistant. Sync runs on one goroutine and Test on a
// separate instance, so the state needs no lock; actions read only the
// fields set at Connect, plus runs, which locks itself.
type instance struct {
	d            integration.Deps
	http         *http.Client
	base         *url.URL
	token        string
	name         string
	link         string
	key          string // the instance ID in resource IDs
	appID        string
	powerEntity  string
	energyEntity string
	scripts      []string // the scripts the owner lets Cha Dash run (actions.go)
	control      []string // the entity domains the owner lets Cha Dash control (control.go)
	runs         runs     // what each script run saw before it started

	needFull  bool
	version   string
	state     string
	health    integration.Health
	tzName    string
	tempUnit  string // the temperature unit Home Assistant is set to, "°C" or "°F"
	tz        *time.Location
	statesAt  time.Time
	derived   derived
	day       string               // the local date the baselines belong to
	baselines map[string]float64   // energy meters' readings at local midnight, kWh
	tried     map[string]time.Time // when each meter's baseline was last asked for

	calendarsAt time.Time  // when the calendars were last read
	calendars   []calendar // calendar entities, by entity ID
}

// haConfig is the part of /api/config the integration reads.
type haConfig struct {
	Version      string `json:"version"`
	State        string `json:"state"`
	SafeMode     bool   `json:"safe_mode"`
	RecoveryMode bool   `json:"recovery_mode"`
	TimeZone     string `json:"time_zone"`
	UnitSystem   struct {
		Temperature string `json:"temperature"`
	} `json:"unit_system"`
}

// entity is one item of /api/states, with only the attributes the
// derivation uses.
type entity struct {
	EntityID    string `json:"entity_id"`
	State       string `json:"state"`
	LastChanged string `json:"last_changed"`
	Attributes  struct {
		DeviceClass string          `json:"device_class"`
		StateClass  string          `json:"state_class"`
		Unit        string          `json:"unit_of_measurement"`
		Members     json.RawMessage `json:"entity_id"`
		IsHueGroup  bool            `json:"is_hue_group"`
		LastReset   *string         `json:"last_reset"`
		Name        string          `json:"friendly_name"`
		Title       string          `json:"title"`             // an update's
		Installed   string          `json:"installed_version"` // an update's
		Latest      string          `json:"latest_version"`    // an update's

		CurrentTemp     *float64 `json:"current_temperature"` // a climate's
		TargetTemp      *float64 `json:"temperature"`         // a climate's setpoint
		CurrentHumidity *float64 `json:"current_humidity"`    // a climate's
		HVACAction      string   `json:"hvac_action"`         // a climate's: heating, cooling, idle, off…
		MediaTitle      string   `json:"media_title"`         // a media player's
		MediaArtist     string   `json:"media_artist"`        // a media player's
		MinTemp         *float64 `json:"min_temp"`            // a climate's
		MaxTemp         *float64 `json:"max_temp"`            // a climate's
		TempStep        *float64 `json:"target_temp_step"`    // a climate's
		Brightness      *float64 `json:"brightness"`          // a light's, 0–255
	} `json:"attributes"`
}

// name is the entity's friendly name, else its ID.
func (e entity) name() string {
	if e.Attributes.Name != "" {
		return e.Attributes.Name
	}
	return e.EntityID
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

	// What needs a look, from the same states.
	unavailable    int
	updates        []map[string]any // update.* that are on
	batteries      []battery        // low ones
	batteriesTotal int
	open           []map[string]any // doors, windows and garage doors that are open
	openingsTotal  int
	unlocked       int
	locksTotal     int
	alarm          string // the first alarm panel's state, "" without one

	// Who and what is where, from the same states.
	peopleItems []map[string]any // every person: name, where (home, away or a zone), since
	climate     []map[string]any // thermostats and heat pumps that report a temperature
	safetyTotal int
	safety      []map[string]any // smoke, CO, gas and leak sensors that are tripped
	mediaTotal  int
	media       []map[string]any // media players that are playing
	players     []map[string]any // every media player, for the tile's panel
	lightItems  []map[string]any // every light (groups excluded), for the tile's panel
	switchItems []map[string]any // every switch (groups excluded), for the tile's panel
	tempUnit    string
}

// battery is a low battery: its device's name and level (nil for a
// binary_sensor that only says "low").
type battery struct {
	name  string
	level *float64
}

// lowBattery is the level, in %, below which a battery is low: Home
// Assistant's own battery card turns red under it.
const lowBattery = 20

// listRows is how many items each list attr keeps.
const listRows = 8

// Longer lists for the tile's panel: every light and switch, every player.
const (
	deviceRows = 200
	playerRows = 30
)

// openings are the binary_sensor device classes that are a door or window,
// and the cover device classes that are one.
var (
	safetySensors  = map[string]bool{"smoke": true, "carbon_monoxide": true, "gas": true, "moisture": true}
	openingSensors = map[string]bool{"door": true, "window": true, "garage_door": true, "opening": true}
	openingCovers  = map[string]bool{"door": true, "garage": true, "gate": true, "window": true}
)

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
	// Calendars on the states' cadence, but at most every 5 minutes.
	readCalendars := readStates && (in.calendarsAt.IsZero() || now.Sub(in.calendarsAt) >= calendarsEvery)
	if readCalendars {
		publish, err := in.readCalendars(ctx, localMidnight(now, in.tz))
		if err != nil {
			return err
		}
		in.calendarsAt = now
		readCalendars = publish
	}
	in.needFull = false
	in.d.Emit.SetVersion(in.version)
	if err := in.d.Emit.Replace(scopeGraph, in.graphFacts()); err != nil {
		return err
	}
	if readStates {
		if err := in.d.Emit.Replace(scopeStates, in.statesFacts()); err != nil {
			return err
		}
	}
	if readCalendars {
		return in.d.Emit.Replace(scopeCalendars, in.calendarFacts())
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
	in.tempUnit = cfg.UnitSystem.Temperature
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
	d := derived{read: true, entities: len(states), powerKW: integration.Unknown(), energyKWh: integration.Unknown(), tempUnit: in.tempUnit}
	local := now.In(in.tz)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, in.tz)
	var meters []meter
	for _, e := range states {
		if e.State == "unavailable" {
			d.unavailable++
		}
		switch e.domain() {
		case "update":
			if e.State == "on" {
				name := e.Attributes.Title
				if name == "" {
					name = e.name()
				}
				d.updates = append(d.updates, map[string]any{"name": name, "installed": e.Attributes.Installed, "latest": e.Attributes.Latest})
			}
		case "lock":
			d.locksTotal++
			if e.State == "unlocked" || e.State == "open" {
				d.unlocked++
			}
		case "alarm_control_panel":
			if d.alarm == "" {
				d.alarm = e.State
			}
		case "binary_sensor":
			switch dc := e.Attributes.DeviceClass; {
			case openingSensors[dc]:
				d.openingsTotal++
				if e.State == "on" {
					d.open = append(d.open, map[string]any{"name": e.name(), "kind": dc})
				}
			case dc == "battery":
				d.batteriesTotal++
				if e.State == "on" {
					d.batteries = append(d.batteries, battery{name: batteryName(e.name())})
				}
			case safetySensors[dc]:
				d.safetyTotal++
				if e.State == "on" {
					d.safety = append(d.safety, map[string]any{"name": e.name(), "kind": dc})
				}
			}
		case "climate":
			if e.Attributes.CurrentTemp == nil || e.State == "unavailable" {
				continue
			}
			row := map[string]any{"entity": e.EntityID, "name": e.name(), "current": round1(*e.Attributes.CurrentTemp), "mode": e.State}
			for k, v := range map[string]*float64{"min": e.Attributes.MinTemp, "max": e.Attributes.MaxTemp, "step": e.Attributes.TempStep} {
				if v != nil {
					row[k] = *v
				}
			}
			if t := e.Attributes.TargetTemp; t != nil {
				row["target"] = round1(*t)
			}
			if h := e.Attributes.CurrentHumidity; h != nil {
				row["humidity"] = math.Round(*h)
			}
			if a := e.Attributes.HVACAction; a != "" {
				row["action"] = a
			} else {
				row["action"] = e.State // the mode, for a device without an action
			}
			d.climate = append(d.climate, row)
		case "media_player":
			d.mediaTotal++
			row := map[string]any{"entity": e.EntityID, "name": e.name()}
			if e.Attributes.MediaTitle != "" {
				row["title"] = e.Attributes.MediaTitle
			}
			if e.Attributes.MediaArtist != "" {
				row["artist"] = e.Attributes.MediaArtist
			}
			if e.State == "playing" {
				d.media = append(d.media, row)
			}
			d.players = append(d.players, withState(row, e.State))
		case "cover":
			if dc := e.Attributes.DeviceClass; openingCovers[dc] {
				d.openingsTotal++
				if e.State == "open" || e.State == "opening" {
					d.open = append(d.open, map[string]any{"name": e.name(), "kind": dc})
				}
			}
		case "person":
			d.peopleTotal++
			if e.State == "home" {
				d.people++
			}
			row := map[string]any{"name": e.name(), "where": personWhere(e.State)}
			if t, err := time.Parse(time.RFC3339Nano, e.LastChanged); err == nil {
				row["since"] = t.UTC().Format(time.RFC3339)
			}
			d.peopleItems = append(d.peopleItems, row)
		case "light":
			if !e.group() {
				d.lightsTotal++
				if e.State == "on" {
					d.lights++
				}
				row := map[string]any{"entity": e.EntityID, "name": e.name(), "state": e.State}
				if b := e.Attributes.Brightness; b != nil && e.State == "on" {
					row["brightness"] = math.Round(*b / 255 * 100)
				}
				d.lightItems = append(d.lightItems, row)
			}
		case "switch":
			if !e.group() {
				d.switchesTotal++
				if e.State == "on" {
					d.switches++
				}
				d.switchItems = append(d.switchItems, map[string]any{"entity": e.EntityID, "name": e.name(), "state": e.State})
			}
		case "sensor":
			v, ok := e.number()
			if !ok {
				continue
			}
			switch e.Attributes.DeviceClass {
			case "battery":
				if e.Attributes.Unit != "%" {
					continue
				}
				d.batteriesTotal++
				if v < lowBattery {
					d.batteries = append(d.batteries, battery{name: batteryName(e.name()), level: &v})
				}
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
	// On first, then by name, so the panel's lists read the same way every
	// time; players likewise, playing first.
	for _, l := range [][]map[string]any{d.lightItems, d.switchItems, d.players} {
		slices.SortStableFunc(l, byStateThenName)
	}
	// Lowest battery first; one that only says "low" goes after the levels.
	slices.SortStableFunc(d.batteries, func(a, b battery) int {
		switch {
		case a.level == nil && b.level == nil:
			return 0
		case a.level == nil:
			return 1
		case b.level == nil:
			return -1
		}
		return cmp.Compare(*a.level, *b.level)
	})
	in.derived = d
}

// withState is a copy of row with state.
func withState(row map[string]any, state string) map[string]any {
	out := make(map[string]any, len(row)+1)
	maps.Copy(out, row)
	out["state"] = state
	return out
}

// stateRank puts what's on or playing first, then paused, then the rest.
func stateRank(state string) int {
	switch state {
	case "on", "playing":
		return 0
	case "paused", "buffering":
		return 1
	case "unavailable", "unknown":
		return 3
	}
	return 2
}

func byStateThenName(a, b map[string]any) int {
	sa, _ := a["state"].(string)
	sb, _ := b["state"].(string)
	na, _ := a["name"].(string)
	nb, _ := b["name"].(string)
	return cmp.Or(cmp.Compare(stateRank(sa), stateRank(sb)), cmp.Compare(strings.ToLower(na), strings.ToLower(nb)))
}

// personWhere is where a person is: "home", "away" for not_home, else the
// zone's name as Home Assistant has it ("work", "school").
func personWhere(state string) string {
	switch state {
	case "not_home":
		return "away"
	case "unavailable", "unknown", "":
		return "unknown"
	}
	return state
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// localMidnight is the start of now's day in tz.
func localMidnight(now time.Time, tz *time.Location) time.Time {
	local := now.In(tz)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, tz)
}

// batteryName drops "battery" and "battery low" from a sensor's name, so
// the list says the device: "Hallway motion Battery" is "Hallway motion".
func batteryName(name string) string {
	for _, suffix := range []string{" battery low", " battery level", " battery"} {
		if len(name) > len(suffix) && strings.EqualFold(name[len(name)-len(suffix):], suffix) {
			return name[:len(name)-len(suffix)]
		}
	}
	return name
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
	if len(in.control) > 0 {
		attrs["control"] = in.control // what the tile's panel may switch
	}
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
		attrs["locks_total"] = d.locksTotal
		attrs["openings_total"] = d.openingsTotal
		attrs["batteries_total"] = d.batteriesTotal
		attrs["update_items"] = head(d.updates)
		attrs["open_items"] = head(d.open)
		low := make([]map[string]any, 0, len(d.batteries))
		for _, b := range d.batteries {
			row := map[string]any{"name": b.name}
			if b.level != nil {
				row["level"] = *b.level
			}
			low = append(low, row)
		}
		attrs["battery_items"] = head(low)
		if d.alarm != "" {
			attrs["alarm"] = d.alarm
		}
		attrs["people_items"] = head(d.peopleItems)
		attrs["climate_items"] = head(d.climate)
		attrs["safety_total"] = d.safetyTotal
		attrs["safety_items"] = head(d.safety)
		attrs["media_total"] = d.mediaTotal
		attrs["media_items"] = head(d.media)
		attrs["player_items"] = headN(d.players, playerRows)
		attrs["light_items"] = headN(d.lightItems, deviceRows)
		attrs["switch_items"] = headN(d.switchItems, deviceRows)
		if d.tempUnit != "" {
			attrs["temperature_unit"] = d.tempUnit
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

// head is the first listRows of a list, never nil.
func head(l []map[string]any) []map[string]any { return headN(l, listRows) }

// headN is the first n of a list, never nil.
func headN(l []map[string]any, n int) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l[:min(len(l), n)]
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
		count("open", len(d.open), d.openingsTotal),
		{Resource: in.appID, Key: "safety_tripped", Unit: "count", Min: 0, Max: float64(max(d.safetyTotal, 1)), Band: [2]float64{0, 0}, Value: float64(len(d.safety))},
		count("media_playing", len(d.media), d.mediaTotal),
		count("locks_unlocked", d.unlocked, d.locksTotal),
		{Resource: in.appID, Key: "batteries_low", Unit: "count", Min: 0, Max: float64(max(d.batteriesTotal, 1)), Band: [2]float64{0, 0}, Value: float64(len(d.batteries))},
		{Resource: in.appID, Key: "updates", Unit: "count", Min: 0, Max: 20, Band: [2]float64{0, 3}, Value: float64(len(d.updates))},
		{Resource: in.appID, Key: "unavailable", Unit: "count", Min: 0, Max: float64(max(d.entities, 1)), Band: [2]float64{0, 5}, Value: float64(d.unavailable)},
	}}
}
