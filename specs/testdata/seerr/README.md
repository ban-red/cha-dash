# Seerr fixtures

Synthetic responses for `specs/seerr.yaml`, written by hand in the shape of Seerr's API, which Overseerr and Jellyseerr share. No real instance was contacted; the numbers are made up.

| File | Endpoint |
|---|---|
| `status.json` | `GET /api/v1/status` (a Jellyseerr 2.x-style version) |
| `status_overseerr.json` | `GET /api/v1/status` as Overseerr 1.33 answers it, with an update available |
| `request_count.json` | `GET /api/v1/request/count` (needs `X-Api-Key`) |
