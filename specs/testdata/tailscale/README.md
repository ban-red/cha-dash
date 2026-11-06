# Tailscale fixtures

**Synthetic.** Responses for `specs/tailscale.yaml`, written by hand from Tailscale's API reference: the OpenAPI document `https://api.tailscale.com/api/v2?outputOpenapiSchema=true`, fetched 2026-11-06 (the `Device` schema and the `fields` parameter's default set). No real tailnet was contacted; names, addresses (`100.101.x`, `100.102.x`) and keys are made up, with `*.example` users and a made-up tailnet name.

| File | Endpoint | Notes |
|---|---|---|
| `devices.json` | `GET /api/v2/tailnet/-/devices` (`Authorization: Bearer tskey-api-…`) | Seven devices: one with key expiry off, one expiring soon, one expired and offline, one phone asleep, one gone for weeks, one waiting for approval, and one shared in from another tailnet |
| `devices_ok.json` | The same | One connected device with key expiry off |
| `unauthorized.json` | The 401 | Tailscale's `{"message"}` error |

The spec compares dates with the clock, so `tailscale_test.go` moves `lastSeen` and `expires` to around now before serving `devices.json`. Not checked against a live tailnet: that a key-expiry-disabled device's `expires` is the zero time (the spec handles a missing one too), and the 401's message.
