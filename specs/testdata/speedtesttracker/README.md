# Speedtest Tracker fixtures

**Synthetic.** Responses for `specs/speedtesttracker.yaml`, written by hand from Speedtest Tracker's API documentation (docs.speedtest-tracker.dev, "API") and its source (`app/Http/Controllers/Api/V1/` and `app/Http/Resources/V1/ResultResource.php` in [alexjustesen/speedtest-tracker](https://github.com/alexjustesen/speedtest-tracker)), in the shape the v1 API returns. The nested `data` object is Ookla's CLI result as Speedtest Tracker stores it. No real instance was contacted; the numbers, names and addresses are made up (documentation ranges).

| File | Endpoint | Notes |
|---|---|---|
| `results.json` | `GET /api/v1/results?page[size]=1` (needs `Authorization: Bearer …`) | One result per page, `meta.total` 4812 |
| `latest.json` | `GET /api/v1/results/latest` | A completed, healthy scheduled test: 941 234 567 bit/s down (941.2 Mb/s), 41 234 567 up (41.2), ping 12.345 ms, jitter 1.87 ms, no packet loss |

Before the first test, `latest` answers 404; the test covers that with an empty route.
