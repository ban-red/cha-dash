// Package weather is the built-in weather source: current conditions, the
// next hours and a short daily forecast for one place, from Open-Meteo's
// keyless forecast API. See README.md for the exact shape the Weather
// widget reads.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dash.cha.sh/internal/integration"
)

// ID is the integration's ID.
const ID = "weather"

// DefaultURL is Open-Meteo's public API. A self-hosted Open-Meteo serves the
// same /v1/forecast.
const DefaultURL = "https://api.open-meteo.com"

// Open-Meteo's free API allows 10,000 calls a day per address and asks for
// fair use; its models update every 15 minutes at best. One call per sync,
// every 15 minutes while someone watches and every 30 while nobody does, is
// at most 96 a day per instance.
const (
	interval = 15 * time.Minute
	idle     = 30 * time.Minute
)

// Forecast lengths.
const (
	days  = 5 // today and four more
	hours = 6 // after the current hour
)

// Attribution is what Open-Meteo's CC BY 4.0 licence asks a display to show.
const (
	Attribution    = "Weather data by Open-Meteo.com"
	AttributionURL = "https://open-meteo.com/"
)

var (
	current = []string{"temperature_2m", "apparent_temperature", "relative_humidity_2m", "is_day", "precipitation",
		"weather_code", "wind_speed_10m", "wind_direction_10m", "wind_gusts_10m"}
	hourly = []string{"temperature_2m", "weather_code", "precipitation_probability", "is_day"}
	daily  = []string{"weather_code", "temperature_2m_max", "temperature_2m_min", "precipitation_sum",
		"precipitation_probability_max", "sunrise", "sunset"}
)

func init() { integration.Register(Integration{}) }

// Integration is the weather integration.
type Integration struct{}

// Manifest implements integration.Integration.
func (Integration) Manifest() integration.Manifest {
	lat, lon := -90.0, 90.0
	lng, lnx := -180.0, 180.0
	return integration.Manifest{
		ID: ID, Name: "Weather", Icon: "weather", Version: "0.1.0", Tier: integration.Bronze,
		Capabilities: []integration.Capability{"weather.current", "weather.forecast"},
		ConfigSchema: integration.Object(
			integration.Prop("latitude", &integration.Schema{Type: "number", Title: "Latitude", Minimum: &lat, Maximum: &lon,
				Description: "Decimal degrees, north positive: 51.4769 for the Royal Observatory, Greenwich."}),
			integration.Prop("longitude", &integration.Schema{Type: "number", Title: "Longitude", Minimum: &lng, Maximum: &lnx,
				Description: "Decimal degrees, east positive: -0.0005 for Greenwich."}),
			integration.Prop("units", integration.Enum("Units", "metric", "imperial").WithDefault("metric").
				Describe("How the widget shows values. The hub always reads °C, km/h and mm.")),
			integration.Prop("name", maxLen(integration.String("Place name"), 64).
				Describe("Shown on the widget, e.g. Home. Defaults to the instance's name.")),
			integration.Prop("url", integration.URL("Open-Meteo server").WithDefault(DefaultURL).
				Describe("Change only for a self-hosted Open-Meteo.")),
			integration.TLSProp(),
		).Require("latitude", "longitude"),
		Poll: integration.Poll{Interval: interval, Idle: idle, Timeout: 30 * time.Second},
	}
}

func maxLen(s *integration.Schema, n int) *integration.Schema {
	s.MaxLength = &n
	return s
}

// ValidateConfig implements integration.ConfigValidator.
func (Integration) ValidateConfig(cfg integration.Config) []integration.FieldError {
	if _, err := endpoint(cfg.String("url")); err != nil {
		return []integration.FieldError{{Path: "/url", Message: err.Error()}}
	}
	return nil
}

// endpoint is the forecast URL under the configured server.
func endpoint(raw string) (string, error) {
	if raw == "" {
		raw = DefaultURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("must be an http:// or https:// URL")
	}
	u.RawQuery, u.Fragment, u.RawPath = "", "", ""
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/v1/forecast") + "/v1/forecast"
	return u.String(), nil
}

// config is an instance's config.
type config struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Units     string  `json:"units"`
	Name      string  `json:"name"`
	URL       string  `json:"url"`
}

// Connect implements integration.Integration.
func (Integration) Connect(_ context.Context, cfg integration.Config, d integration.Deps) (integration.Instance, error) {
	var c config
	if err := cfg.Decode(&c); err != nil {
		return nil, &integration.ConfigError{Message: "the config can't be read: " + err.Error()}
	}
	ep, err := endpoint(c.URL)
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "url " + err.Error()}
	}
	key := cfg.InstanceID
	if key == "" {
		key = "test"
	}
	if c.Units == "" {
		c.Units = "metric"
	}
	if c.Name == "" {
		c.Name = cfg.Name
	}
	return &instance{cfg: cfg, d: d, c: c, endpoint: ep, rid: ID + ":" + key + "/here"}, nil
}

type instance struct {
	cfg      integration.Config
	d        integration.Deps
	c        config
	endpoint string
	rid      string
}

// Close implements integration.Instance.
func (in *instance) Close() error { return nil }

// query is the forecast request. Coordinates are rounded to three decimals
// (about 100 m), finer than Open-Meteo's grid, so no more of the place than
// that leaves the hub. Units are always metric: the metric keys say so.
func (in *instance) query() url.Values {
	coord := func(f float64) string { return strconv.FormatFloat(math.Round(f*1000)/1000, 'f', -1, 64) }
	return url.Values{
		"latitude": {coord(in.c.Latitude)}, "longitude": {coord(in.c.Longitude)},
		"current": {strings.Join(current, ",")}, "hourly": {strings.Join(hourly, ",")}, "daily": {strings.Join(daily, ",")},
		"timezone": {"auto"}, "forecast_days": {strconv.Itoa(days)}, "forecast_hours": {strconv.Itoa(hours + 1)},
		"temperature_unit": {"celsius"}, "wind_speed_unit": {"kmh"}, "precipitation_unit": {"mm"}, "timeformat": {"iso8601"},
	}
}

// forecast is Open-Meteo's answer (the fields read). Local times are
// "2006-01-02T15:04" in the place's time zone, dates "2006-01-02".
type forecast struct {
	UTCOffsetSeconds     int    `json:"utc_offset_seconds"`
	Timezone             string `json:"timezone"`
	TimezoneAbbreviation string `json:"timezone_abbreviation"`
	Current              struct {
		Time          string   `json:"time"`
		Temperature   *float64 `json:"temperature_2m"`
		Apparent      *float64 `json:"apparent_temperature"`
		Humidity      *float64 `json:"relative_humidity_2m"`
		IsDay         *float64 `json:"is_day"`
		Precipitation *float64 `json:"precipitation"`
		Code          *float64 `json:"weather_code"`
		WindSpeed     *float64 `json:"wind_speed_10m"`
		WindDirection *float64 `json:"wind_direction_10m"`
		WindGusts     *float64 `json:"wind_gusts_10m"`
	} `json:"current"`
	Hourly struct {
		Time        []string   `json:"time"`
		Temperature []*float64 `json:"temperature_2m"`
		Code        []*float64 `json:"weather_code"`
		PrecipProb  []*float64 `json:"precipitation_probability"`
		IsDay       []*float64 `json:"is_day"`
	} `json:"hourly"`
	Daily struct {
		Time       []string   `json:"time"`
		Code       []*float64 `json:"weather_code"`
		Max        []*float64 `json:"temperature_2m_max"`
		Min        []*float64 `json:"temperature_2m_min"`
		PrecipSum  []*float64 `json:"precipitation_sum"`
		PrecipProb []*float64 `json:"precipitation_probability_max"`
		Sunrise    []string   `json:"sunrise"`
		Sunset     []string   `json:"sunset"`
	} `json:"daily"`
}

// fetch reads the forecast.
func (in *instance) fetch(ctx context.Context) (*forecast, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.endpoint+"?"+in.query().Encode(), nil)
	if err != nil {
		return nil, &integration.ConfigError{Field: "url", Message: "the URL can't be used: " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := in.d.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest:
		var e struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&e)
		return nil, &integration.ConfigError{Message: "Open-Meteo refused the request: " + cmp(e.Reason, "400 Bad Request")}
	case http.StatusTooManyRequests:
		return nil, errors.New("the free Open-Meteo API's rate limit was reached (10,000 calls a day per address); this instance asks every 15 minutes, so something else on this address is using it too")
	case http.StatusNotFound:
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET /v1/forecast answered 404 Not Found: check the Open-Meteo server URL"}
	}
	if err := integration.CheckResponse(resp); err != nil {
		return nil, err
	}
	var f forecast
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil || f.Current.Time == "" {
		return nil, &integration.RepairError{Code: integration.CodeAPIChanged, Field: "url",
			Message: "GET /v1/forecast didn't answer with Open-Meteo's forecast: check the Open-Meteo server URL"}
	}
	return &f, nil
}

// Test implements integration.Instance: one forecast call. There's no
// credential, so no grants.
func (in *instance) Test(ctx context.Context) integration.TestResult {
	if _, err := in.fetch(ctx); err != nil {
		return integration.Failed(err)
	}
	return integration.TestResult{OK: true}
}

// Sync implements integration.Instance.
func (in *instance) Sync(ctx context.Context, _ integration.Interest) error {
	f, err := in.fetch(ctx)
	if err != nil {
		return err
	}
	loc := location(f)
	at := func(local string) (string, bool) {
		t, err := time.ParseInLocation("2006-01-02T15:04", local, loc)
		if err != nil {
			return "", false
		}
		return t.Format(time.RFC3339), true
	}
	cur := f.Current
	attrs := map[string]any{
		"type": ID, "provider": "open-meteo", "location": in.c.Name, "units": in.c.Units,
		"timezone": f.Timezone, "utc_offset_s": f.UTCOffsetSeconds,
		"attribution": Attribution, "attribution_url": AttributionURL,
	}
	if s, ok := at(cur.Time); ok {
		attrs["observed_at"] = s
	}
	if cur.Code != nil {
		w := describe(int(*cur.Code))
		attrs["code"], attrs["condition"], attrs["summary"] = int(*cur.Code), w.condition, w.summary
	}
	if cur.IsDay != nil {
		attrs["is_day"] = *cur.IsDay == 1
	}
	setNum(attrs, "wind_dir_deg", cur.WindDirection)
	setNum(attrs, "gusts_kmh", cur.WindGusts)

	// Today is the first day; its high and low are also the temperature's band.
	band := [2]float64{}
	var dailyOut []any
	for i, date := range f.Daily.Time {
		code := at2(f.Daily.Code, i)
		d := map[string]any{"date": date, "code": nil, "condition": nil, "summary": nil,
			"high_c": num(at2(f.Daily.Max, i)), "low_c": num(at2(f.Daily.Min, i)),
			"precip_mm": num(at2(f.Daily.PrecipSum, i)), "precip_pct": num(at2(f.Daily.PrecipProb, i))}
		if code != nil {
			w := describe(int(*code))
			d["code"], d["condition"], d["summary"] = int(*code), w.condition, w.summary
		}
		dailyOut = append(dailyOut, d)
		if i == 0 {
			setNum(attrs, "high_c", at2(f.Daily.Max, i))
			setNum(attrs, "low_c", at2(f.Daily.Min, i))
			setNum(attrs, "precip_pct", at2(f.Daily.PrecipProb, i))
			if lo, hi := at2(f.Daily.Min, i), at2(f.Daily.Max, i); lo != nil && hi != nil {
				band = [2]float64{*lo, *hi}
			}
			if i < len(f.Daily.Sunrise) {
				if s, ok := at(f.Daily.Sunrise[i]); ok {
					attrs["sunrise"] = s
				}
			}
			if i < len(f.Daily.Sunset) {
				if s, ok := at(f.Daily.Sunset[i]); ok {
					attrs["sunset"] = s
				}
			}
		}
	}
	attrs["daily"] = orEmpty(dailyOut)

	// The next hours: those after the current reading's hour, at most six.
	var hourlyOut []any
	health := integration.Health{Status: integration.HealthOK}
	if cur.Code != nil {
		if what, ok := severe(int(*cur.Code)); ok {
			health = integration.Health{Status: integration.HealthAttention, Reason: what + " now"}
		}
	}
	for i, local := range f.Hourly.Time {
		if local <= cur.Time || len(hourlyOut) == hours {
			continue
		}
		s, ok := at(local)
		if !ok {
			continue
		}
		code := at2(f.Hourly.Code, i)
		h := map[string]any{"at": s, "temp_c": num(at2(f.Hourly.Temperature, i)), "code": nil, "condition": nil,
			"precip_pct": num(at2(f.Hourly.PrecipProb, i)), "is_day": nil}
		if code != nil {
			h["code"], h["condition"] = int(*code), describe(int(*code)).condition
			if what, ok := severe(int(*code)); ok && health.Status == integration.HealthOK {
				health = integration.Health{Status: integration.HealthAttention,
					Reason: fmt.Sprintf("%s expected around %s", what, local[len(local)-5:])}
			}
		}
		if d := at2(f.Hourly.IsDay, i); d != nil {
			h["is_day"] = *d == 1
		}
		hourlyOut = append(hourlyOut, h)
	}
	attrs["hourly"] = orEmpty(hourlyOut)

	res := integration.Resource{ID: in.rid, Kind: "weather", Name: in.cfg.Name, State: "online", Health: health, Attrs: attrs,
		Caps: []string{"weather.current", "weather.forecast"}, Links: []integration.Link{{Title: "Open-Meteo", URL: AttributionURL}}}
	metric := func(key, unit string, lo, hi float64, b [2]float64, v *float64) integration.Metric {
		val := integration.Unknown()
		if v != nil {
			val = *v
		}
		return integration.Metric{Resource: in.rid, Key: key, Unit: unit, Min: lo, Max: hi, Band: b, Value: val}
	}
	return in.d.Emit.Replace("", integration.Facts{
		Resources: []integration.Resource{res},
		Relations: []integration.Relation{{From: in.d.Emit.Self(), Type: "monitors", To: in.rid}},
		Metrics: []integration.Metric{
			metric("temp.c", "°C", -40, 50, band, cur.Temperature),
			metric("feels.c", "°C", -40, 50, band, cur.Apparent),
			metric("humidity.pct", "%", 0, 100, [2]float64{30, 70}, cur.Humidity),
			metric("wind.kmh", "km/h", 0, 150, [2]float64{0, 30}, cur.WindSpeed),
			metric("precip.mm", "mm", 0, 50, [2]float64{0, 1}, cur.Precipitation),
		},
	})
}

// location is the place's time zone: by name when the hub has the zone
// database, else the answer's current offset (exact except across a change
// to or from daylight saving time).
func location(f *forecast) *time.Location {
	if f.Timezone != "" {
		if loc, err := time.LoadLocation(f.Timezone); err == nil {
			return loc
		}
	}
	return time.FixedZone(cmp(f.TimezoneAbbreviation, "local"), f.UTCOffsetSeconds)
}

// at2 is xs[i], or nil past its end.
func at2(xs []*float64, i int) *float64 {
	if i < len(xs) {
		return xs[i]
	}
	return nil
}

// num is a forecast value for attrs, or null when it's missing.
func num(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func setNum(attrs map[string]any, k string, v *float64) {
	if v != nil {
		attrs[k] = *v
	}
}

func orEmpty(xs []any) []any {
	if xs == nil {
		return []any{}
	}
	return xs
}

func cmp(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// wmo is a WMO weather interpretation code as words: condition is a stable
// slug for icons, summary a short phrase for people.
type wmo struct{ condition, summary string }

// codes are the WMO codes Open-Meteo reports (its documentation's table).
var codes = map[int]wmo{
	0:  {"clear", "Clear sky"},
	1:  {"mostly_clear", "Mainly clear"},
	2:  {"partly_cloudy", "Partly cloudy"},
	3:  {"overcast", "Overcast"},
	45: {"fog", "Fog"},
	48: {"fog", "Depositing rime fog"},
	51: {"drizzle", "Light drizzle"},
	53: {"drizzle", "Drizzle"},
	55: {"drizzle", "Dense drizzle"},
	56: {"freezing_drizzle", "Light freezing drizzle"},
	57: {"freezing_drizzle", "Dense freezing drizzle"},
	61: {"rain", "Light rain"},
	63: {"rain", "Rain"},
	65: {"rain", "Heavy rain"},
	66: {"freezing_rain", "Light freezing rain"},
	67: {"freezing_rain", "Heavy freezing rain"},
	71: {"snow", "Light snow"},
	73: {"snow", "Snow"},
	75: {"snow", "Heavy snow"},
	77: {"snow_grains", "Snow grains"},
	80: {"rain_showers", "Light rain showers"},
	81: {"rain_showers", "Rain showers"},
	82: {"rain_showers", "Violent rain showers"},
	85: {"snow_showers", "Light snow showers"},
	86: {"snow_showers", "Heavy snow showers"},
	95: {"thunderstorm", "Thunderstorm"},
	96: {"hail", "Thunderstorm with light hail"},
	99: {"hail", "Thunderstorm with heavy hail"},
}

func describe(code int) wmo {
	if w, ok := codes[code]; ok {
		return w
	}
	return wmo{"unknown", "Unknown"}
}

// severe names the conditions worth a warning: thunderstorms and freezing
// precipitation. Open-Meteo has no official warnings; this is a hint read
// from the forecast.
func severe(code int) (string, bool) {
	switch code {
	case 95:
		return "Thunderstorm", true
	case 96, 99:
		return "Thunderstorm with hail", true
	case 56, 57:
		return "Freezing drizzle", true
	case 66, 67:
		return "Freezing rain", true
	}
	return "", false
}
