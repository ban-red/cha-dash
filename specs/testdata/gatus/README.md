# Gatus fixtures

**Synthetic.** Responses for `specs/gatus.yaml`, written by hand from Gatus's README (its "API" section) and its source ([TwiN/gatus](https://github.com/TwiN/gatus)): the endpoint status and result types (`config/endpoint/` in Gatus 5, `core/` before it), where `duration` is a Go `time.Duration`, so nanoseconds in JSON, and the key helper `ConvertGroupAndEndpointNameToKey`. No real instance was contacted; names, hosts and numbers are made up, with `*.home.example` and `10.20.30.x` addresses.

| File | Endpoint | Notes |
|---|---|---|
| `statuses.json` | `GET /api/v1/endpoints/statuses?page=1&pageSize=1` (no credential) | Four endpoints: one passing, one failing on its conditions (a 502), one with no group failing with an error (a TCP timeout), and one never checked yet |
| `statuses_ok.json` | The same | One endpoint, passing |

Not checked against a live Gatus: whether `group` comes back as `""` or is left out for an endpoint without one (the spec handles both), and the exact key Gatus makes then (`_nas-ssh` here).
