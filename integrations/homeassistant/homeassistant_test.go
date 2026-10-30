package homeassistant

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// The fixtures are synthetic (testdata/integrations/homeassistant/README.md),
// built from Home Assistant's REST and WebSocket API documentation.
var fixtures = filepath.Join("..", "..", "testdata", "integrations", "homeassistant")

const token = "test-token-0123456789"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fake is a Home Assistant serving the fixtures to the right token.
type fake struct {
	*httptest.Server
	t *testing.T

	mu      sync.Mutex
	files   map[string][]byte // path → body; nil means 404
	hits    map[string]int
	history []string // each history request's path and query
	admin   bool
	noWS    bool
	// calls are the service calls received, "<path> <body>", and onCall
	// answers them (actions_test.go). Reads are all GETs.
	calls  []string
	onCall func(path string, body []byte) []byte
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, hits: map[string]int{}, admin: true, files: map[string][]byte{
		"/api/":       fixture(t, "api.json"),
		"/api/config": fixture(t, "config.json"),
		"/api/states": fixture(t, "states.json"),
		"history":     fixture(t, "history_midnight.json"),
	}}
	f.Server = httptest.NewServer(f)
	t.Cleanup(f.Close)
	return f
}

func (f *fake) set(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = body
}

func (f *fake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fake) historyRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.history)
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.String(), token) {
		f.t.Errorf("the token is in the URL: %s", r.URL)
	}
	if r.URL.Path == "/api/websocket" {
		f.serveWS(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/services/") {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, r.URL.Path+" "+string(b))
		on := f.onCall
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if on == nil {
			w.Write([]byte("[]"))
			return
		}
		w.Write(on(r.URL.Path, b))
		return
	}
	if r.Method != http.MethodGet {
		f.t.Errorf("%s %s: only actions call services; everything else reads", r.Method, r.URL.Path)
	}
	key := r.URL.Path
	if strings.HasPrefix(key, "/api/history/period/") {
		key = "history"
	}
	f.mu.Lock()
	f.hits[key]++
	if key == "history" {
		f.history = append(f.history, r.URL.Path+"?"+r.URL.RawQuery)
	}
	b, ok := f.files[key]
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("unexpected request: %s", r.URL)
	}
	if b == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// serveWS speaks the WebSocket API's auth handshake and answers
// auth/current_user.
func (f *fake) serveWS(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	noWS, admin := f.noWS, f.admin
	f.mu.Unlock()
	if noWS {
		http.NotFound(w, r)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	wsjson.Write(ctx, conn, map[string]string{"type": "auth_required", "ha_version": "2026.9.3"})
	var auth struct {
		Type        string `json:"type"`
		AccessToken string `json:"access_token"`
	}
	if wsjson.Read(ctx, conn, &auth) != nil {
		return
	}
	if auth.Type != "auth" || auth.AccessToken != token {
		wsjson.Write(ctx, conn, map[string]string{"type": "auth_invalid", "message": "Invalid access token or password"})
		return
	}
	wsjson.Write(ctx, conn, map[string]string{"type": "auth_ok", "ha_version": "2026.9.3"})
	var cmd struct {
		ID   int    `json:"id"`
		Type string `json:"type"`
	}
	if wsjson.Read(ctx, conn, &cmd) != nil {
		return
	}
	if cmd.Type != "auth/current_user" {
		f.t.Errorf("the integration sent %q; it only asks who it is", cmd.Type)
		return
	}
	var res map[string]any
	json.Unmarshal(fixture(f.t, "ws_current_user.json"), &res)
	res["id"] = cmd.ID
	res["result"].(map[string]any)["is_admin"] = admin
	wsjson.Write(ctx, conn, res)
	conn.Read(ctx) // until the client closes
}

func connect(t *testing.T, h *integrationtest.Harness, f *fake, extra map[string]any) integration.Instance {
	t.Helper()
	cfg := map[string]any{"url": f.URL}
	maps.Copy(cfg, extra)
	return h.Connect(Integration{}, cfg, map[string]string{"token": token})
}

func mustSync(t *testing.T, inst integration.Instance, want integration.Interest) {
	t.Helper()
	if err := inst.Sync(t.Context(), want); err != nil {
		t.Fatal(err)
	}
}

func value(t *testing.T, h *integrationtest.Harness, key string) any {
	t.Helper()
	se := h.Series("homeassistant:i_test/app", key)
	if se == nil {
		t.Fatalf("no %s series", key)
	}
	return se["value"]
}

func TestRegistered(t *testing.T) {
	_, m, ok := integration.Default.Get(ID)
	if !ok || m.Name != "Home Assistant" || !slices.Equal(m.SecretFields(), []string{"token"}) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidate(t *testing.T) {
	// The Homepage importer's shape; the token arrives resolved.
	imported := map[string]any{"tls": map[string]any{"mode": "verify"}, "url": "https://home.lab.example"}
	if errs := integrationtest.Validate(Integration{}, imported, map[string]string{"token": "x"}); len(errs) != 0 {
		t.Fatalf("importer shape: %v", errs)
	}
	ok := map[string]any{"url": "http://10.0.11.203:8123", "power_entity": "sensor.house_power", "energy_entity": "sensor.house_energy"}
	if errs := integrationtest.Validate(Integration{}, ok, map[string]string{"token": "x"}); len(errs) != 0 {
		t.Fatalf("with entities: %v", errs)
	}
	cases := map[string]struct {
		config  map[string]any
		secrets map[string]string
		path    string
	}{
		"no token":         {map[string]any{"url": "http://a.lab.example"}, nil, "/token"},
		"no url":           {map[string]any{}, map[string]string{"token": "x"}, "/url"},
		"not a sensor":     {map[string]any{"url": "http://a.lab.example", "power_entity": "light.kitchen"}, map[string]string{"token": "x"}, "/power_entity"},
		"websocket scheme": {map[string]any{"url": "ws://a.lab.example"}, map[string]string{"token": "x"}, "/url"},
	}
	for name, c := range cases {
		errs := integrationtest.Validate(Integration{}, c.config, c.secrets)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v, want an error at %s", name, errs, c.path)
		}
	}
}

func TestSync(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	inst := connect(t, h, f, nil)
	mustSync(t, inst, integrationtest.Watched)

	app, ok := h.Resource("homeassistant:i_test/app")
	if !ok || app.Kind != "app" || app.Name != "Test" || app.State != "running" || app.Health != integration.HealthOK || app.Source != "integration:i_test" {
		t.Fatalf("app = %+v", app)
	}
	for k, want := range map[string]any{
		"type": "homeassistant", "version": "2026.9.3", "url": f.URL, "entities": 55.0,
		"people_total": 3.0, "lights_total": 6.0, "switches_total": 4.0,
		"power_entity": "sensor.house_power", "energy_entity": "sensor.house_energy_daily",
		"locks_total": 2.0, "openings_total": 4.0, "batteries_total": 3.0, "alarm": "armed_home",
		"safety_total": 3.0, "media_total": 3.0, "temperature_unit": "°C",
	} {
		if app.Attrs[k] != want {
			t.Errorf("app.attrs.%s = %v, want %v", k, app.Attrs[k], want)
		}
	}
	// What needs a look: pending updates (an unavailable one isn't), open
	// doors, windows and garage doors (not blinds or motion), low batteries
	// lowest first with "battery" dropped from the name.
	lists := map[string]string{
		"update_items":  `[{"installed":"2.5.0","latest":"2.6.1","name":"Music Assistant"},{"installed":"1.2.0","latest":"1.3.0","name":"Hallway sensor firmware"}]`,
		"open_items":    `[{"kind":"door","name":"Back door"},{"kind":"window","name":"Bedroom window"},{"kind":"garage","name":"Garage door"}]`,
		"battery_items": `[{"level":12,"name":"Hallway motion"},{"name":"Smoke alarm"}]`,
		// Every person with where they are (a zone's name, "away" for
		// not_home); the climates that report a temperature (the unavailable
		// one doesn't), with the action, else the mode; what's playing (not
		// idle or off); nothing tripped.
		"people_items":  `[{"name":"Resident A","since":"2026-10-03T13:58:41Z","where":"home"},{"name":"Resident B","since":"2026-10-03T13:58:41Z","where":"away"},{"name":"Resident C","since":"2026-10-03T13:58:41Z","where":"home"}]`,
		"climate_items": `[{"action":"heating","current":21.5,"humidity":46,"name":"Living room","target":22},{"action":"idle","current":18.9,"name":"Bedroom","target":17}]`,
		"media_items":   `[{"artist":"Marconi Union","name":"Living room speaker","title":"Weightless"}]`,
		"safety_items":  `[]`,
	}
	for k, want := range lists {
		if got, _ := json.Marshal(app.Attrs[k]); string(got) != want {
			t.Errorf("app.attrs.%s = %s, want %s", k, got, want)
		}
	}
	if h.Graph().Relations["integration:i_test monitors homeassistant:i_test/app"] == nil {
		t.Error("the instance should monitor the app")
	}
	// Groups (light.downstairs, the Hue room, switch.all_plugs) don't count
	// twice; the unavailable porch light counts as a light that's off.
	// The porch light, the fridge's power, the garden pump's firmware and the
	// garage thermostat are unavailable.
	for key, want := range map[string]any{"people_home": 2.0, "lights_on": 3.0, "switches_on": 2.0, "power.kw": 1.234, "energy.kwh": 8.25,
		"open": 3.0, "locks_unlocked": 1.0, "batteries_low": 2.0, "updates": 2.0, "unavailable": 4.0,
		"safety_tripped": 0.0, "media_playing": 1.0} {
		if got := value(t, h, key); got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	if se := h.Series("homeassistant:i_test/app", "lights_on"); se["max"] != 6.0 || se["unit"] != "count" {
		t.Errorf("lights_on scale = %v", se)
	}
	if se := h.Series("homeassistant:i_test/app", "power.kw"); se["max"] != 12.0 || se["unit"] != "kW" {
		t.Errorf("power.kw scale = %v", se)
	}

	// One history request for the meters that don't reset daily, for the
	// minute after local midnight (Europe/Berlin: 22:00 UTC the day before).
	reqs := f.historyRequests()
	if len(reqs) != 1 || !strings.HasPrefix(reqs[0], "/api/history/period/2026-10-02T22:00:00Z?") ||
		!strings.Contains(reqs[0], "filter_entity_id=sensor.coffee_machine_energy%2Csensor.house_energy&") ||
		!strings.Contains(reqs[0], "end_time=2026-10-02T22%3A01%3A00Z") || !strings.Contains(reqs[0], "minimal_response") {
		t.Fatalf("history requests = %v", reqs)
	}
	// The same day doesn't ask again.
	h.Advance(time.Minute)
	mustSync(t, inst, integrationtest.Watched)
	if len(f.historyRequests()) != 1 {
		t.Fatal("baselines are read once a day")
	}
	// After local midnight (22:00 UTC), a new day: new baselines.
	h.Advance(8 * time.Hour)
	mustSync(t, inst, integrationtest.Watched)
	if reqs := f.historyRequests(); len(reqs) != 2 || !strings.HasPrefix(reqs[1], "/api/history/period/2026-10-03T22:00:00Z?") {
		t.Fatalf("history requests = %v", reqs)
	}
}

// A tripped smoke or leak sensor is listed and counted.
func TestSafetyTripped(t *testing.T) {
	f := newFake(t)
	states := bytes.Replace(fixture(t, "states.json"),
		[]byte(`"state": "off",
    "attributes": {
      "device_class": "smoke"`),
		[]byte(`"state": "on",
    "attributes": {
      "device_class": "smoke"`), 1)
	if bytes.Equal(states, fixture(t, "states.json")) {
		t.Fatal("the fixture's smoke sensor wasn't found")
	}
	f.set("/api/states", states)
	h := integrationtest.New(t)
	mustSync(t, connect(t, h, f, nil), integrationtest.Watched)
	if got := value(t, h, "safety_tripped"); got != 1.0 {
		t.Fatalf("safety_tripped = %v", got)
	}
	app, _ := h.Resource("homeassistant:i_test/app")
	if got, _ := json.Marshal(app.Attrs["safety_items"]); string(got) != `[{"kind":"smoke","name":"Kitchen smoke"}]` {
		t.Fatalf("safety_items = %s", got)
	}
}

func TestChosenEntities(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	inst := connect(t, h, f, map[string]any{"power_entity": "sensor.solar_power", "energy_entity": "sensor.house_energy"})
	mustSync(t, inst, integrationtest.Watched)
	// The lifetime meter minus its reading at midnight.
	if got := value(t, h, "energy.kwh"); got != 8.2 {
		t.Fatalf("energy.kwh = %v", got)
	}
	if got := value(t, h, "power.kw"); got != 0.8 {
		t.Fatalf("power.kw = %v", got)
	}
	if reqs := f.historyRequests(); len(reqs) != 1 || !strings.Contains(reqs[0], "filter_entity_id=sensor.house_energy&") {
		t.Fatalf("only the chosen meter's history: %v", reqs)
	}

	// Without history (no recorder), energy today is unknown, never 0.
	f2 := newFake(t)
	f2.set("history", nil)
	h2 := integrationtest.New(t)
	mustSync(t, connect(t, h2, f2, map[string]any{"energy_entity": "sensor.house_energy"}), integrationtest.Watched)
	if got := value(t, h2, "energy.kwh"); got != nil {
		t.Fatalf("energy.kwh without history = %v", got)
	}
	// A chosen sensor that doesn't exist leaves power unknown too.
	h3 := integrationtest.New(t)
	mustSync(t, connect(t, h3, f2, map[string]any{"power_entity": "sensor.missing"}), integrationtest.Watched)
	if got := value(t, h3, "power.kw"); got != nil {
		t.Fatalf("power.kw from a missing sensor = %v", got)
	}
}

func TestIdle(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	inst := connect(t, h, f, nil)
	mustSync(t, inst, integration.Interest{})
	h.Advance(5 * time.Minute)
	mustSync(t, inst, integration.Interest{})
	if f.count("/api/config") != 2 || f.count("/api/states") != 1 {
		t.Fatalf("idle: config %d, states %d", f.count("/api/config"), f.count("/api/states"))
	}
	h.Advance(25 * time.Minute)
	mustSync(t, inst, integration.Interest{})
	if f.count("/api/states") != 2 {
		t.Fatal("idle syncs read the states every 30 minutes")
	}
	mustSync(t, inst, integrationtest.Watched)
	if f.count("/api/states") != 3 {
		t.Fatal("watched syncs always read the states")
	}
}

func TestHealth(t *testing.T) {
	for name, c := range map[string]struct {
		patch  map[string]any
		state  string
		reason string
	}{
		"starting": {map[string]any{"state": "STARTING"}, "starting", "Home Assistant is starting"},
		"recovery": {map[string]any{"recovery_mode": true}, "running", "Home Assistant started in recovery mode: its configuration didn't load"},
		"safe":     {map[string]any{"safe_mode": true}, "running", "Home Assistant is running in safe mode, with custom integrations off"},
	} {
		f := newFake(t)
		var cfg map[string]any
		json.Unmarshal(fixture(t, "config.json"), &cfg)
		maps.Copy(cfg, c.patch)
		b, _ := json.Marshal(cfg)
		f.set("/api/config", b)
		h := integrationtest.New(t)
		mustSync(t, connect(t, h, f, nil), integrationtest.Watched)
		if app, _ := h.Resource("homeassistant:i_test/app"); app.State != c.state || app.Health != integration.HealthAttention || app.Attrs["health.reason"] != c.reason {
			t.Errorf("%s: %+v", name, app)
		}
	}
}

func TestAuth(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	inst := h.Connect(Integration{}, map[string]any{"url": f.URL}, map[string]string{"token": "revoked"})
	err := inst.Sync(t.Context(), integrationtest.Watched)
	if code, class, _ := integration.Classify(err); code != integration.CodeAuth || class != integration.NeedsRepair {
		t.Fatalf("a revoked token = %v", err)
	}
	if res := inst.Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAuth {
		t.Fatalf("test = %+v", res)
	}
	if strings.Contains(h.Logs(), "revoked") {
		t.Fatal("the token leaked into the logs")
	}
}

func TestTest(t *testing.T) {
	f := newFake(t)
	h := integrationtest.New(t)
	res := connect(t, h, f, nil).Test(t.Context())
	if !res.OK || res.Version != "2026.9.3" || len(res.Grants) != 1 || res.Grants[0].Access != integration.AccessAdmin ||
		!strings.Contains(res.Grants[0].Detail, "Dashboard") || len(res.Warnings) != 0 {
		t.Fatalf("admin = %+v", res)
	}
	if _, ok := h.Resource("homeassistant:i_test/app"); ok {
		t.Fatal("Test must not emit facts")
	}
	// A non-admin user's token can still call services: a write grant.
	f.mu.Lock()
	f.admin = false
	f.mu.Unlock()
	res = connect(t, h, f, nil).Test(t.Context())
	if !res.OK || res.Grants[0].Access != integration.AccessReadWrite || !res.Grants[0].CanWrite() || !strings.Contains(res.Grants[0].Detail, "no read-only tokens") {
		t.Fatalf("user = %+v", res)
	}
	// Without the WebSocket API, the grant says what any token can do.
	f.mu.Lock()
	f.noWS = true
	f.mu.Unlock()
	res = connect(t, h, f, nil).Test(t.Context())
	if !res.OK || !res.Grants[0].CanWrite() || len(res.Warnings) != 1 || res.Warnings[0].Code != "ha_user_unknown" {
		t.Fatalf("no websocket = %+v", res)
	}
	// Not Home Assistant.
	f.set("/api/", nil)
	if res := connect(t, h, f, nil).Test(t.Context()); res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("not HA = %+v", res)
	}
}
