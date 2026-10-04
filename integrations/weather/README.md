# Weather (`weather`)

The built-in weather source: current conditions, the next six hours and a five-day forecast for one place, from [Open-Meteo](https://open-meteo.com/)'s keyless forecast API. It feeds the Weather widget.

**Tier:** bronze, Tier 0 (Go). **Capabilities:** `weather.current`, `weather.forecast`. **Secrets:** none.

The hub calls Open-Meteo only once someone adds a weather instance: registering the integration does nothing on its own, and Connect does no I/O.

## Config

```json
{"latitude": 51.4769, "longitude": -0.0005, "units": "metric", "name": "Greenwich"}
```

| Key | Required | Meaning |
|---|---|---|
| `latitude` | yes | Decimal degrees, −90 to 90, north positive |
| `longitude` | yes | Decimal degrees, −180 to 180, east positive |
| `units` | no | `metric` (default) or `imperial`: **how the widget shows values**. The hub always reads °C, km/h and mm, whatever this says, because the metric keys name their units |
| `name` | no | The place's name on the widget (up to 64 characters). Defaults to the instance's name |
| `url` | no | The Open-Meteo server, default `https://api.open-meteo.com`. Change it only for a self-hosted Open-Meteo. `…/v1/forecast` itself is accepted too |
| `tls` | no | The usual `{"mode", "pin"}`, for a self-hosted server |

**Privacy.** The coordinates are rounded to three decimals (about 100 m, finer than Open-Meteo's grid) before they leave the hub, and they stay in the instance config: they aren't published in the graph.

## Open-Meteo's terms

- **Polling:** one request per sync; every **15 minutes** while someone watches and every **30 minutes** while nobody does, so at most 96 calls a day per instance. Open-Meteo's free tier allows 10,000 a day per address, and its models update every 15 minutes at best, so polling faster would only spend the shared quota.
- **User-Agent:** every request carries the runtime's `chadash/<version> (+https://dash.cha.sh)`.
- **Licence and attribution:** the data is CC BY 4.0; the resource carries `attribution` and `attribution_url` for the widget to show, and links to Open-Meteo.
- **Non-commercial:** the free API is for non-commercial use, which a home dashboard is. Anyone else should point `url` at a self-hosted Open-Meteo or a paid plan's server.
- A 429 (rate limit) is transient and says so; the coordinator backs off from the 15-minute interval.

## The request

```
GET <url>/v1/forecast?latitude=51.477&longitude=-0.001
  &current=temperature_2m,apparent_temperature,relative_humidity_2m,is_day,precipitation,weather_code,wind_speed_10m,wind_direction_10m,wind_gusts_10m
  &hourly=temperature_2m,weather_code,precipitation_probability,is_day
  &daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max,sunrise,sunset
  &timezone=auto&forecast_days=5&forecast_hours=7
  &temperature_unit=celsius&wind_speed_unit=kmh&precipitation_unit=mm&timeformat=iso8601
```

`timezone=auto` makes Open-Meteo answer in the place's local time. The hub turns local times into RFC 3339 with the place's offset, using the time zone by name when the hub has the zone database, else the answer's current offset (`utc_offset_seconds`; only an hour off across a daylight-saving change). `forecast_hours=7` starts at the current hour, so six whole hours follow it.

## What it publishes: the shape the Weather widget reads

One resource per instance, **`weather:<instance id>/here`**, kind **`weather`**, named after the instance, state `online`, source `integration:<instance id>` (which `monitors` it). The widget finds it by kind `weather` (or by `attrs.type = "weather"`); with several instances, by ID.

### Attrs

Top-level attrs are left out when Open-Meteo doesn't give the value. Every temperature is °C, every speed km/h, every amount mm, whatever `units` says.

| Attr | Type | Meaning |
|---|---|---|
| `type` | string | always `"weather"` |
| `provider` | string | always `"open-meteo"` |
| `location` | string | the place's name: `name` from the config, else the instance's name |
| `units` | string | `"metric"` or `"imperial"`: the display preference from the config |
| `observed_at` | string | RFC 3339 with the place's offset: the current reading's time (Open-Meteo's 15-minute slot), e.g. `"2026-10-03T15:00:00+01:00"` |
| `timezone` | string | IANA name, e.g. `"Europe/London"` |
| `utc_offset_s` | number | the place's offset from UTC now, in seconds |
| `code` | number | the current WMO weather code (table below) |
| `condition` | string | that code as a slug for icons (table below) |
| `summary` | string | that code as a short phrase, e.g. `"Overcast"` |
| `is_day` | boolean | daylight now (pick sun or moon icons) |
| `high_c` | number | today's high |
| `low_c` | number | today's low |
| `precip_pct` | number | today's highest chance of precipitation, 0–100 |
| `sunrise`, `sunset` | string | today's, RFC 3339 with the place's offset |
| `wind_dir_deg` | number | where the wind comes from, degrees (0 = north, 90 = east) |
| `gusts_kmh` | number | wind gusts |
| `hourly` | array | the next hours, below; `[]` when there are none |
| `daily` | array | the days, below; `[]` when there are none |
| `attribution` | string | `"Weather data by Open-Meteo.com"`: show it with the data (CC BY 4.0) |
| `attribution_url` | string | `"https://open-meteo.com/"` |

Plus what every resource carries: `health.reason` (when not `ok`), `health.since`, `links` (`[{"title": "Open-Meteo", "url": "https://open-meteo.com/"}]`) and `caps` (`["weather.current", "weather.forecast"]`).

**`hourly`**: up to 6 entries, oldest first, for the whole hours after the current reading's hour (15:00 → 16:00 … 21:00). Each entry always has every key; a value Open-Meteo didn't give is `null`.

| Key | Type | Meaning |
|---|---|---|
| `at` | string | RFC 3339 with the place's offset, on the hour |
| `temp_c` | number \| null | temperature |
| `code` | number \| null | WMO code |
| `condition` | string \| null | its slug |
| `precip_pct` | number \| null | chance of precipitation, 0–100 |
| `is_day` | boolean \| null | daylight in that hour |

**`daily`**: 5 entries, today first. Every key always present; missing values `null`.

| Key | Type | Meaning |
|---|---|---|
| `date` | string | the local date, `"2026-10-03"` |
| `code` | number \| null | the day's WMO code (its most significant weather) |
| `condition` | string \| null | its slug |
| `summary` | string \| null | its phrase |
| `high_c`, `low_c` | number \| null | the day's high and low |
| `precip_mm` | number \| null | the day's precipitation sum |
| `precip_pct` | number \| null | the day's highest chance of precipitation, 0–100 |

### Health

`attention` when a thunderstorm or freezing precipitation is happening now ("Thunderstorm now", "Thunderstorm with hail now", "Freezing rain now", "Freezing drizzle now"), or else forecast in the next six hours ("Thunderstorm expected around 18:00", local time, the first such hour); `ok` otherwise. Open-Meteo issues no official warnings: this is a hint read from the forecast's codes 95, 96, 99, 56, 57, 66 and 67. An unreachable Open-Meteo is the framework's: the series go stale with their last values, and the resource keeps its last attrs ("last forecast, hatched").

### Metrics

Series keyed `weather:<instance id>/here#<key>`, current conditions only:

| Key | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `temp.c` | °C | −40–50 | today's low–high | `current.temperature_2m` |
| `feels.c` | °C | −40–50 | today's low–high | `current.apparent_temperature` |
| `humidity.pct` | % | 0–100 | 30–70 | `current.relative_humidity_2m` |
| `wind.kmh` | km/h | 0–150 | 0–30 | `current.wind_speed_10m` |
| `precip.mm` | mm | 0–50 | 0–1 | `current.precipitation`: over the current reading's slot (the preceding 15 minutes) |

A missing value is unknown (`null`), never 0. `temp.c` is the simulator's key for CPU temperatures; here it's the air's. The others aren't in the Demo Home.

### WMO codes

| Code | `condition` | `summary` |
|---|---|---|
| 0 | `clear` | Clear sky |
| 1 | `mostly_clear` | Mainly clear |
| 2 | `partly_cloudy` | Partly cloudy |
| 3 | `overcast` | Overcast |
| 45, 48 | `fog` | Fog; Depositing rime fog |
| 51, 53, 55 | `drizzle` | Light drizzle; Drizzle; Dense drizzle |
| 56, 57 | `freezing_drizzle` | Light / Dense freezing drizzle |
| 61, 63, 65 | `rain` | Light rain; Rain; Heavy rain |
| 66, 67 | `freezing_rain` | Light / Heavy freezing rain |
| 71, 73, 75 | `snow` | Light snow; Snow; Heavy snow |
| 77 | `snow_grains` | Snow grains |
| 80, 81, 82 | `rain_showers` | Light rain showers; Rain showers; Violent rain showers |
| 85, 86 | `snow_showers` | Light / Heavy snow showers |
| 95 | `thunderstorm` | Thunderstorm |
| 96, 99 | `hail` | Thunderstorm with light / heavy hail |
| anything else | `unknown` | Unknown |

### Example

The resource from the synthetic fixture (Greenwich, 2026-10-03 15:05 local), as the `graph` topic carries it (`hourly` and `daily` shortened):

```json
{
  "id": "weather:i_x/here", "kind": "weather", "name": "Weather", "source": "integration:i_x",
  "state": "online", "health": "ok",
  "attrs": {
    "type": "weather", "provider": "open-meteo", "location": "Greenwich", "units": "metric",
    "observed_at": "2026-10-03T15:00:00+01:00", "timezone": "Europe/London", "utc_offset_s": 3600,
    "code": 3, "condition": "overcast", "summary": "Overcast", "is_day": true,
    "high_c": 15.1, "low_c": 9.8, "precip_pct": 60,
    "sunrise": "2026-10-03T07:06:00+01:00", "sunset": "2026-10-03T18:38:00+01:00",
    "wind_dir_deg": 247, "gusts_kmh": 33.1,
    "hourly": [
      {"at": "2026-10-03T16:00:00+01:00", "temp_c": 14, "code": 3, "condition": "overcast", "precip_pct": 15, "is_day": true},
      {"at": "2026-10-03T17:00:00+01:00", "temp_c": 13.6, "code": 61, "condition": "rain", "precip_pct": 45, "is_day": true}
    ],
    "daily": [
      {"date": "2026-10-03", "code": 61, "condition": "rain", "summary": "Light rain", "high_c": 15.1, "low_c": 9.8, "precip_mm": 2.4, "precip_pct": 60},
      {"date": "2026-10-06", "code": 2, "condition": "partly_cloudy", "summary": "Partly cloudy", "high_c": 13.9, "low_c": 7.9, "precip_mm": 0.2, "precip_pct": null}
    ],
    "attribution": "Weather data by Open-Meteo.com", "attribution_url": "https://open-meteo.com/",
    "health.since": "2026-10-03T14:05:00Z",
    "links": [{"title": "Open-Meteo", "url": "https://open-meteo.com/"}],
    "caps": ["weather.current", "weather.forecast"]
  }
}
```

and the series `weather:i_x/here#temp.c` = `{"unit": "°C", "min": -40, "max": 50, "band": [9.8, 15.1], "value": 14.2, …}`.

## Grants

None: there's no credential. Test makes one forecast call and reports no version (Open-Meteo doesn't give one).

## Repairs and errors

| What happened | Error | Field |
|---|---|---|
| 400 (Open-Meteo refused a parameter) | `invalid_config`, with Open-Meteo's reason | |
| 429 (rate limit) | transient, saying the quota is shared by the address | |
| 404, or an answer without a `current` block | `api_changed` Repair: check the Open-Meteo server URL | `url` |

## Limits

- No official weather warnings, air quality, UV index or radar: the attention hint above is derived from the forecast.
- One place per instance; add another instance for a second place.
- `units` doesn't change what the hub reads or publishes; converting for display is the widget's job.

## Fixtures

Synthetic, in [`testdata/integrations/weather/`](../../testdata/integrations/weather/README.md).
