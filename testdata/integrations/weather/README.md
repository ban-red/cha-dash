# Weather (Open-Meteo) fixtures (synthetic)

**Synthetic.** Nothing here was fetched from Open-Meteo. The answers were written by hand for the weather integration's contract tests ([`integrations/weather`](../../../integrations/weather/README.md)), in the shape of Open-Meteo's `/v1/forecast` as its [documentation](https://open-meteo.com/en/docs) describes it: `current`, `hourly` and `daily` blocks with their `*_units`, local times in `timezone=auto`, WMO weather codes. The place is a public landmark, the Royal Observatory in Greenwich, London; the weather is made up.

| File | Request | Notes |
|---|---|---|
| `forecast.json` | `GET /v1/forecast?latitude=51.477&longitude=-0.001&current=…&hourly=…&daily=…&timezone=auto&forecast_days=5&forecast_hours=7&temperature_unit=celsius&wind_speed_unit=kmh&precipitation_unit=mm&timeformat=iso8601` | Europe/London, UTC+1 (summer time). Current reading at 15:00 local (14:00 UTC, the 15-minute slot before the harness's 14:05 UTC): 14.2 °C, feels 12.9, 71 %, overcast (code 3), wind 18.4 km/h from 247°, gusts 33.1, no rain. Seven hourly entries from 15:00 (light rain at 17:00–18:00, showers at 19:00, dark from 19:00). Five days from 2026-10-03, one with a `null` precipitation probability |
| `error_400.json` | the same, with a latitude out of range | Open-Meteo's error shape, `{"error": true, "reason": …}`, sent with HTTP 400 |

The tests patch the weather codes in `forecast.json` inline for the thunderstorm and freezing-rain cases.
