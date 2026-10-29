package weather

import (
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dash.cha.sh/integrations/internal/fixture"
	"dash.cha.sh/internal/integration"
	"dash.cha.sh/internal/integration/integrationtest"
)

// The Royal Observatory, Greenwich: a public landmark.
const (
	lat = 51.4769
	lon = -0.0014
)

func server(t *testing.T) *fixture.Server {
	t.Helper()
	return fixture.New(t, fixture.Dir(t, ID), nil, map[string]fixture.Route{
		"GET /v1/forecast": {File: "forecast.json"},
	})
}

func connect(t *testing.T, h *integrationtest.Harness, url string, extra map[string]any) integration.Instance {
	t.Helper()
	cfg := map[string]any{"latitude": lat, "longitude": lon, "url": url}
	maps.Copy(cfg, extra)
	return h.Connect(Integration{}, cfg, nil)
}

func TestValidate(t *testing.T) {
	ok := []map[string]any{
		{"latitude": lat, "longitude": lon},
		{"latitude": 0, "longitude": 0, "units": "imperial", "name": "Home", "url": "https://weather.lab.example/", "tls": map[string]any{"mode": "tofu"}},
	}
	for _, cfg := range ok {
		if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 0 {
			t.Errorf("%v: %v", cfg, errs)
		}
	}
	for path, cfg := range map[string]map[string]any{
		"/latitude":  {"latitude": 91, "longitude": 0},
		"/longitude": {"latitude": 0},
		"/units":     {"latitude": 0, "longitude": 0, "units": "kelvin"},
		"/url":       {"latitude": 0, "longitude": 0, "url": "ftp://weather.lab.example"},
	} {
		if errs := integrationtest.Validate(Integration{}, cfg, nil); len(errs) != 1 || errs[0].Path != path {
			t.Errorf("%v: %v, want one error at %s", cfg, errs, path)
		}
	}
	_, m, ok2 := integration.Default.Get(ID)
	if !ok2 || len(m.SecretFields()) != 0 || m.Poll.Interval.Minutes() != 15 || m.Poll.Idle.Minutes() != 30 ||
		!reflect.DeepEqual(m.Capabilities, []integration.Capability{"weather.current", "weather.forecast"}) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestSync(t *testing.T) {
	srv := server(t)
	h := integrationtest.New(t) // 2026-10-03 14:05 UTC, 15:05 in London
	inst := connect(t, h, srv.URL, map[string]any{"name": "Greenwich", "units": "imperial"})
	if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}

	// The request: rounded coordinates, metric units, the place's time zone,
	// the runtime's User-Agent.
	reqs := srv.Requests("/v1/forecast")
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	q := reqs[0].URL.Query()
	for k, v := range map[string]string{
		"latitude": "51.477", "longitude": "-0.001", "timezone": "auto", "forecast_days": "5", "forecast_hours": "7",
		"temperature_unit": "celsius", "wind_speed_unit": "kmh", "precipitation_unit": "mm", "timeformat": "iso8601",
		"current": "temperature_2m,apparent_temperature,relative_humidity_2m,is_day,precipitation,weather_code,wind_speed_10m,wind_direction_10m,wind_gusts_10m",
		"hourly":  "temperature_2m,weather_code,precipitation_probability,is_day",
		"daily":   "weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max,sunrise,sunset",
	} {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if ua := reqs[0].Header.Get("User-Agent"); !strings.HasPrefix(ua, "chadash/") {
		t.Errorf("User-Agent = %q", ua)
	}

	rid := "weather:i_test/here"
	r, ok := h.Resource(rid)
	if !ok || r.Kind != "weather" || r.Name != "Test" || r.State != "online" || r.Health != integration.HealthOK || r.Source != "integration:i_test" {
		t.Fatalf("weather = %+v", r)
	}
	want := map[string]any{
		"type": "weather", "provider": "open-meteo", "location": "Greenwich", "units": "imperial",
		"timezone": "Europe/London", "utc_offset_s": 3600.0, "observed_at": "2026-10-03T15:00:00+01:00",
		"code": 3.0, "condition": "overcast", "summary": "Overcast", "is_day": true,
		"high_c": 15.1, "low_c": 9.8, "precip_pct": 60.0,
		"sunrise": "2026-10-03T07:06:00+01:00", "sunset": "2026-10-03T18:38:00+01:00",
		"wind_dir_deg": 247.0, "gusts_kmh": 33.1,
		"attribution": "Weather data by Open-Meteo.com", "attribution_url": "https://open-meteo.com/",
	}
	for k, v := range want {
		if r.Attrs[k] != v {
			t.Errorf("attrs[%s] = %v (%T), want %v", k, r.Attrs[k], r.Attrs[k], v)
		}
	}
	for _, k := range []string{"latitude", "longitude"} {
		if _, ok := r.Attrs[k]; ok {
			t.Errorf("the coordinates stay in the config, not attrs.%s", k)
		}
	}
	if links, _ := r.Attrs["links"].([]any); len(links) != 1 || links[0].(map[string]any)["url"] != "https://open-meteo.com/" {
		t.Errorf("links = %v", r.Attrs["links"])
	}
	if caps, _ := r.Attrs["caps"].([]any); len(caps) != 2 {
		t.Errorf("caps = %v", r.Attrs["caps"])
	}

	// The next six hours after the reading's hour (15:00).
	hourly, _ := r.Attrs["hourly"].([]any)
	if len(hourly) != 6 {
		t.Fatalf("hourly = %v", r.Attrs["hourly"])
	}
	if got, want := hourly[0], map[string]any{"at": "2026-10-03T16:00:00+01:00", "temp_c": 14.0, "code": 3.0, "condition": "overcast", "precip_pct": 15.0, "is_day": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("hourly[0] = %v", got)
	}
	if got := hourly[5].(map[string]any); got["at"] != "2026-10-03T21:00:00+01:00" || got["is_day"] != false || got["condition"] != "mostly_clear" {
		t.Errorf("hourly[5] = %v", got)
	}
	// Five days; a null probability stays null.
	daily, _ := r.Attrs["daily"].([]any)
	if len(daily) != 5 {
		t.Fatalf("daily = %v", r.Attrs["daily"])
	}
	if got, want := daily[0], map[string]any{"date": "2026-10-03", "code": 61.0, "condition": "rain", "summary": "Light rain",
		"high_c": 15.1, "low_c": 9.8, "precip_mm": 2.4, "precip_pct": 60.0}; !reflect.DeepEqual(got, want) {
		t.Errorf("daily[0] = %v", got)
	}
	if got := daily[3].(map[string]any); got["precip_pct"] != nil || got["condition"] != "partly_cloudy" {
		t.Errorf("daily[3] = %v", got)
	}

	for metric, w := range map[string]struct {
		v        float64
		unit     string
		min, max float64
		band     []any
	}{
		"temp.c":       {14.2, "°C", -40, 50, []any{9.8, 15.1}},
		"feels.c":      {12.9, "°C", -40, 50, []any{9.8, 15.1}},
		"humidity.pct": {71, "%", 0, 100, []any{30.0, 70.0}},
		"wind.kmh":     {18.4, "km/h", 0, 150, []any{0.0, 30.0}},
		"precip.mm":    {0, "mm", 0, 50, []any{0.0, 1.0}},
	} {
		se := h.Series(rid, metric)
		if se == nil || se["value"] != w.v || se["unit"] != w.unit || se["min"] != w.min || se["max"] != w.max || !reflect.DeepEqual(se["band"], w.band) {
			t.Errorf("%s = %v", metric, se)
		}
	}
	if h.Graph().Relations["integration:i_test monitors "+rid] == nil {
		t.Fatal("the instance should monitor its weather")
	}
}

func TestDefaults(t *testing.T) {
	srv := server(t)
	h := integrationtest.New(t)
	// No name: the instance's; no units: metric.
	if err := connect(t, h, srv.URL+"/", nil).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	r, _ := h.Resource("weather:i_test/here")
	if r.Attrs["location"] != "Test" || r.Attrs["units"] != "metric" {
		t.Fatalf("attrs = %v", r.Attrs)
	}
	// The forecast endpoint itself works as the URL too.
	if err := connect(t, integrationtest.New(t), srv.URL+"/v1/forecast", nil).Sync(t.Context(), integrationtest.Watched); err != nil {
		t.Fatal(err)
	}
	// Without a url, the public API: Connect doesn't call it.
	inst := integrationtest.New(t).Connect(Integration{}, map[string]any{"latitude": lat, "longitude": lon}, nil)
	if got := inst.(*instance).endpoint; got != "https://api.open-meteo.com/v1/forecast" {
		t.Fatalf("endpoint = %s", got)
	}
}

func TestSevere(t *testing.T) {
	srv := server(t)
	b, err := os.ReadFile(filepath.Join(fixture.Dir(t, ID), "forecast.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	h := integrationtest.New(t)
	inst := connect(t, h, srv.URL, nil)
	for _, c := range []struct{ old, new, reason string }{
		{`"weather_code": [3, 3, 61, 61, 80, 2, 1]`, `"weather_code": [3, 3, 61, 95, 80, 2, 1]`, "Thunderstorm expected around 18:00"},
		{`"weather_code": [3, 3, 61, 61, 80, 2, 1]`, `"weather_code": [3, 3, 66, 99, 80, 2, 1]`, "Freezing rain expected around 17:00"},
		{`"weather_code": 3,`, `"weather_code": 96,`, "Thunderstorm with hail now"},
		// The current hour's own forecast entry isn't "expected".
		{`"weather_code": [3, 3, 61, 61, 80, 2, 1]`, `"weather_code": [95, 3, 61, 61, 80, 2, 1]`, ""},
	} {
		patched := strings.Replace(body, c.old, c.new, 1)
		if patched == body {
			t.Fatalf("%s isn't in the fixture", c.old)
		}
		srv.Set("GET /v1/forecast", fixture.Route{Body: patched})
		if err := inst.Sync(t.Context(), integrationtest.Watched); err != nil {
			t.Fatal(err)
		}
		r, _ := h.Resource("weather:i_test/here")
		wantHealth := integration.HealthAttention
		if c.reason == "" {
			wantHealth = integration.HealthOK
		}
		if string(r.Health) != wantHealth || (c.reason != "" && r.Attrs["health.reason"] != c.reason) {
			t.Errorf("%s: %s %v", c.new, r.Health, r.Attrs["health.reason"])
		}
	}
}

func TestFailures(t *testing.T) {
	srv := server(t)

	// Open-Meteo refuses the parameters: a config error, with its reason.
	srv.Set("GET /v1/forecast", fixture.Route{File: "error_400.json", Status: http.StatusBadRequest})
	err := connect(t, integrationtest.New(t), srv.URL, nil).Sync(t.Context(), integrationtest.Watched)
	if code, class, msg := integration.Classify(err); code != integration.CodeInvalidConfig || class != integration.Permanent ||
		msg != "Open-Meteo refused the request: Latitude must be in range of -90 to 90°. Given: 91.0." {
		t.Fatalf("400: %s %d %q", code, class, msg)
	}
	// Rate-limited: transient, saying so.
	srv.Set("GET /v1/forecast", fixture.Route{Body: `{"error":true,"reason":"Daily API request limit exceeded. Please try again tomorrow."}`, Status: http.StatusTooManyRequests})
	err = connect(t, integrationtest.New(t), srv.URL, nil).Sync(t.Context(), integrationtest.Watched)
	if code, class, msg := integration.Classify(err); code != integration.CodeUpstream || class != integration.Transient || !strings.Contains(msg, "rate limit") {
		t.Fatalf("429: %s %d %q", code, class, msg)
	}
	// Not Open-Meteo.
	res := connect(t, integrationtest.New(t), srv.URL+"/elsewhere", nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("wrong url = %+v", res)
	}
	srv.Set("GET /v1/forecast", fixture.Route{Body: `{"hello":"world"}`})
	res = connect(t, integrationtest.New(t), srv.URL, nil).Test(t.Context())
	if res.OK || res.ErrorCode != integration.CodeAPIChanged {
		t.Fatalf("not a forecast = %+v", res)
	}
}

func TestTest(t *testing.T) {
	srv := server(t)
	h := integrationtest.New(t)
	res := connect(t, h, srv.URL, nil).Test(t.Context())
	if !res.OK || len(res.Grants) != 0 {
		t.Fatalf("test = %+v", res)
	}
	if _, ok := h.Resource("weather:i_test/here"); ok {
		t.Fatal("Test must not emit facts")
	}
}

func TestCodes(t *testing.T) {
	// Every code Open-Meteo documents has words; anything else is unknown.
	for _, c := range []int{0, 1, 2, 3, 45, 48, 51, 53, 55, 56, 57, 61, 63, 65, 66, 67, 71, 73, 75, 77, 80, 81, 82, 85, 86, 95, 96, 99} {
		if w := describe(c); w.condition == "unknown" || w.summary == "" {
			t.Errorf("code %d = %+v", c, w)
		}
	}
	if w := describe(42); w.condition != "unknown" {
		t.Errorf("code 42 = %+v", w)
	}
}
