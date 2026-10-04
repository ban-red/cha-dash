package sim

import (
	"math"
	"time"
)

// The Demo Home's weather comes from the built-in weather integration
// (integrations/weather): one instance, "demo", reading Open-Meteo for
// Greenwich, the integration's own example place. Its coordinates live in
// the instance config, so nothing here or in the graph carries them.
//
// The resource has the integration's exact shape. The conditions are made
// up, plausibly for a London autumn, and depend on the seed like any other
// noise. The integration polls every 15 minutes while someone watches; here
// the current readings (the series) move hourly, and the resource (the
// reading's time, conditions and forecast) refreshes every three hours,
// because each refresh is a whole-resource event.

const (
	weatherID  = "weather:demo/here"
	weatherInt = "integration:demo"
	wxReading  = time.Hour     // how often the current readings move
	wxRefresh  = 3 * time.Hour // how often the resource's attrs do
)

// wxCode is a WMO weather code with its icon slug and phrase, as the
// weather integration maps them.
type wxCode struct {
	code              int
	condition, phrase string
	wet               bool
}

var wxCodes = map[int]wxCode{
	1:  {1, "mostly_clear", "Mainly clear", false},
	2:  {2, "partly_cloudy", "Partly cloudy", false},
	3:  {3, "overcast", "Overcast", false},
	51: {51, "drizzle", "Light drizzle", true},
	61: {61, "rain", "Light rain", true},
	80: {80, "rain_showers", "Light rain showers", true},
}

// dayCodes are the kinds of days to pick from: mostly grey, sometimes wet,
// now and then bright.
var dayCodes = []int{3, 61, 2, 80, 3, 1, 51, 2}

// wxDay is one local day's weather.
type wxDay struct {
	date           time.Time // the local date, at 00:00 UTC
	code           wxCode
	high, low      float64
	precipMM       float64
	precipPct      int
	wetFrom, wetTo float64 // local hours when its rain falls
}

// londonOffset is Europe/London's offset from UTC at t, in seconds: BST from
// 01:00 UTC on the last Sunday of March to 01:00 UTC on the last Sunday of
// October. Worked out here so the simulator needs no time zone database.
func londonOffset(t time.Time) int {
	t = t.UTC()
	lastSunday := func(m time.Month) time.Time {
		d := time.Date(t.Year(), m+1, 1, 1, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
		return d.AddDate(0, 0, -int(d.Weekday()))
	}
	if !t.Before(lastSunday(time.March)) && t.Before(lastSunday(time.October)) {
		return 3600
	}
	return 0
}

// local is t on Greenwich's clock.
func local(t time.Time) time.Time {
	if off := londonOffset(t); off != 0 {
		return t.In(time.FixedZone("BST", off))
	}
	return t.In(time.FixedZone("GMT", 0))
}

// rfc3339 is t as the weather integration writes times: with the place's offset.
func rfc3339(t time.Time) string { return local(t).Format("2006-01-02T15:04:05-07:00") }

// wxDayAt is the weather of the local day containing t.
func wxDayAt(r rng, t time.Time) wxDay {
	l := local(t)
	date := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	n := date.Unix() / 86400
	key := hashKey("wx:day")
	d := wxDay{date: date, code: wxCodes[dayCodes[int(r.u(key, n)*float64(len(dayCodes)))]]}
	d.high = roundTo(12.5+4*r.u(key^1, n), 1)
	d.low = roundTo(d.high-4.2-2*r.u(key^2, n), 1)
	from := 13 + math.Floor(6*r.u(key^3, n))
	mm, pct := r.u(key^4, n), r.u(key^5, n)
	switch d.code.code {
	case 61:
		d.precipMM, d.precipPct, d.wetFrom, d.wetTo = roundTo(1.5+2.5*mm, 1), 55+int(25*pct), from, from+3
	case 80:
		d.precipMM, d.precipPct, d.wetFrom, d.wetTo = roundTo(1+2*mm, 1), 60+int(20*pct), from-3, from-1
	case 51:
		d.precipMM, d.precipPct, d.wetFrom, d.wetTo = roundTo(0.3+0.6*mm, 1), 40+int(20*pct), 6, 10
	case 3:
		d.precipMM, d.precipPct = roundTo(0.4*mm, 1), 20+int(15*pct)
	default:
		d.precipPct = 5 + int(15*pct)
	}
	return d
}

// sunTimes approximates sunrise and sunset at Greenwich on a date: solar
// declination, the equation of time, and the standard refraction angle.
// Close to a minute or so, which is plenty for sun or moon icons.
func sunTimes(date time.Time) (rise, set time.Time) {
	doy := float64(date.YearDay())
	decl := 23.44 * math.Pi / 180 * math.Sin(2*math.Pi*(284+doy)/365)
	lat := 51.48 * math.Pi / 180
	cosH := (math.Sin(-0.833*math.Pi/180) - math.Sin(lat)*math.Sin(decl)) / (math.Cos(lat) * math.Cos(decl))
	h := math.Acos(clamp(cosH, -1, 1)) * 180 / math.Pi / 15 // hours either side of solar noon
	b := 2 * math.Pi * (doy - 81) / 364
	eot := 9.87*math.Sin(2*b) - 7.53*math.Cos(b) - 1.5*math.Sin(b) // minutes
	noon := date.Add(12*time.Hour - time.Duration(eot*float64(time.Minute)))
	half := time.Duration(h * float64(time.Hour))
	return noon.Add(-half).Truncate(time.Minute), noon.Add(half).Truncate(time.Minute)
}

// hourWx is the weather in the hour starting at at.
type hourWx struct {
	at        time.Time
	code      wxCode
	tempC     float64
	precipPct int
	isDay     bool
}

func hourWxAt(r rng, at time.Time) hourWx {
	d := wxDayAt(r, at)
	h := float64(local(at).Hour())
	rise, set := sunTimes(d.date)
	mid := at.Add(30 * time.Minute)
	w := hourWx{at: at, isDay: !mid.Before(rise) && mid.Before(set), code: wxCodes[3]}
	// Coolest before dawn, warmest mid-afternoon.
	w.tempC = roundTo(d.low+(d.high-d.low)*(0.5+0.5*math.Cos(2*math.Pi*(h-15)/24)), 1)
	if !d.code.wet {
		w.code = d.code
	}
	w.precipPct = max(0, d.precipPct-30)
	if d.code.wet && h >= d.wetFrom && h < d.wetTo {
		w.code, w.precipPct = d.code, d.precipPct-5+int(5*r.u(hashKey("wx:hour"), at.Unix()/3600))
	}
	return w
}

// wxSlot is the start of the resource refresh t falls in.
func wxSlot(t time.Time) time.Time { return t.Truncate(wxRefresh) }

// windAt is the wind at the refresh starting at at, before noise: stronger
// in the afternoon.
func windAt(at time.Time) float64 {
	return 11 + 9*(0.5+0.5*math.Cos(2*math.Pi*(float64(local(at).Hour())-14)/24))
}

func (b *builder) weather() {
	src := weatherInt
	b.add(&Resource{ID: weatherID, Kind: KindWeather, Name: "Weather", Source: src, State: StateOnline,
		Links:  []Link{{Title: "Open-Meteo", URL: "https://open-meteo.com/"}},
		Caps:   []string{"weather.current", "weather.forecast"},
		Health: Health{Status: HealthOK, Since: b.start.Add(-9*24*time.Hour + 2*time.Minute)}})
	// The temperature bands are today's low to high: the simulated day's.
	today := wxDayAt(b.rng, b.start.Add(12*time.Hour))
	tband := [2]float64{today.low, today.high}
	noise := func(e *eval, key string, at time.Time) float64 { return e.s.rng.s(hashKey("wx:"+key), at.Unix()/3600) }
	// Each series holds the hour's reading.
	cur := func(fn func(e *eval, w hourWx) float64) valueFn {
		return func(e *eval, m *metricSpec, t time.Time) float64 {
			return fn(e, hourWxAt(e.s.rng, t.Truncate(wxReading)))
		}
	}
	temp := func(e *eval, w hourWx) float64 { return w.tempC + 0.3*noise(e, "temp", w.at) }
	wind := func(e *eval, w hourWx) float64 { return windAt(w.at) + 3*noise(e, "wind", w.at) }
	b.metric(&metricSpec{res: weatherID, key: "temp.c", unit: "°C", min: -40, max: 50, band: tband, dec: 1, source: src, fn: cur(temp)})
	b.metric(&metricSpec{res: weatherID, key: "feels.c", unit: "°C", min: -40, max: 50, band: tband, dec: 1, source: src,
		fn: cur(func(e *eval, w hourWx) float64 {
			v := temp(e, w) - 0.12*wind(e, w)
			if w.code.wet {
				v -= 0.6
			}
			return v
		})})
	b.metric(&metricSpec{res: weatherID, key: "humidity.pct", unit: "%", min: 0, max: 100, band: [2]float64{30, 70}, dec: 0, source: src,
		fn: cur(func(e *eval, w hourWx) float64 {
			v := 90 - 22*(0.5+0.5*math.Cos(2*math.Pi*(float64(local(w.at).Hour())-15)/24)) + 2*noise(e, "rh", w.at)
			if w.code.wet {
				v += 7
			}
			return min(v, 99)
		})})
	b.metric(&metricSpec{res: weatherID, key: "wind.kmh", unit: "km/h", min: 0, max: 150, band: [2]float64{0, 30}, dec: 1, source: src, fn: cur(wind)})
	b.metric(&metricSpec{res: weatherID, key: "precip.mm", unit: "mm", min: 0, max: 50, band: [2]float64{0, 1}, dec: 1, source: src,
		fn: cur(func(e *eval, w hourWx) float64 {
			d := wxDayAt(e.s.rng, w.at)
			if !w.code.wet || d.wetTo <= d.wetFrom {
				return 0
			}
			return d.precipMM / (d.wetTo - d.wetFrom) / 4 * (1 + 0.3*noise(e, "rain", w.at)) // over one 15-minute reading
		})})
}

// weatherHour is one entry of attrs.hourly.
type weatherHour struct {
	At        string  `json:"at"`
	TempC     float64 `json:"temp_c"`
	Code      int     `json:"code"`
	Condition string  `json:"condition"`
	PrecipPct int     `json:"precip_pct"`
	IsDay     bool    `json:"is_day"`
}

// weatherDay is one entry of attrs.daily.
type weatherDay struct {
	Date      string  `json:"date"`
	Code      int     `json:"code"`
	Condition string  `json:"condition"`
	Summary   string  `json:"summary"`
	HighC     float64 `json:"high_c"`
	LowC      float64 `json:"low_c"`
	PrecipMM  float64 `json:"precip_mm"`
	PrecipPct int     `json:"precip_pct"`
}

// applyWeather sets the weather resource's attrs as of its last refresh.
func (s *Sim) applyWeather(w *world) {
	r := w.res[weatherID]
	if r == nil {
		return
	}
	at := wxSlot(w.t)
	cur := hourWxAt(s.rng, at)
	d := wxDayAt(s.rng, at)
	rise, set := sunTimes(d.date)
	hourly := make([]weatherHour, 0, 6)
	for i := 1; i <= 6; i++ {
		h := hourWxAt(s.rng, at.Add(time.Duration(i)*time.Hour))
		hourly = append(hourly, weatherHour{At: rfc3339(h.at), TempC: h.tempC, Code: h.code.code, Condition: h.code.condition, PrecipPct: h.precipPct, IsDay: h.isDay})
	}
	daily := make([]weatherDay, 0, 5)
	for i := range 5 {
		dd := wxDayAt(s.rng, at.Add(time.Duration(i)*24*time.Hour))
		daily = append(daily, weatherDay{Date: dd.date.Format(time.DateOnly), Code: dd.code.code, Condition: dd.code.condition, Summary: dd.code.phrase,
			HighC: dd.high, LowC: dd.low, PrecipMM: dd.precipMM, PrecipPct: dd.precipPct})
	}
	r.Attrs = map[string]any{
		"type": "weather", "provider": "open-meteo", "location": "Greenwich", "units": "metric",
		"observed_at": rfc3339(at), "timezone": "Europe/London", "utc_offset_s": londonOffset(at),
		"code": cur.code.code, "condition": cur.code.condition, "summary": cur.code.phrase, "is_day": cur.isDay,
		"high_c": d.high, "low_c": d.low, "precip_pct": d.precipPct,
		"sunrise": rfc3339(rise), "sunset": rfc3339(set),
		"wind_dir_deg": 200 + int(60*s.rng.u(hashKey("wx:dir"), at.Unix()/(6*3600))), "gusts_kmh": roundTo(1.7*windAt(at), 1),
		"hourly": hourly, "daily": daily,
		"attribution": "Weather data by Open-Meteo.com", "attribution_url": "https://open-meteo.com/",
	}
}

// weatherChangePoints are the forecast refreshes in (from, to].
func weatherChangePoints(from, to time.Time) []time.Time {
	var out []time.Time
	for t := wxSlot(from).Add(wxRefresh); !t.After(to); t = t.Add(wxRefresh) {
		out = append(out, t)
	}
	return out
}
