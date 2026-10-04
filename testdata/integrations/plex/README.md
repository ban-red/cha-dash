# Plex fixture (synthetic)

**Synthetic.** Nothing here was captured from a real Plex Media Server or from plex.tv. The server responses follow the Plex Media Server's JSON API as its public documentation and clients describe it (`Accept: application/json`, the `MediaContainer` envelope); the PIN responses follow Plex's "Authenticating with Plex" guide on the Plex forums. Titles, ids, users and devices are made up; addresses are the Demo Home's `10.0.11.x` and the documentation range `203.0.113.x`; the token and PIN code are placeholders.

The counts match the Demo Home at 2026-10-03 14:05 UTC (1240 movies, 318 shows); the sessions don't (two streams instead of none), so the tile has something to show.

**Plex Media Server:**

| File | Endpoint | Notes |
|---|---|---|
| `identity.json` | `GET /identity` | Version 1.41.6, a placeholder machine identifier |
| `status_sessions.json` | `GET /status/sessions` | Two sessions: an episode direct-playing on the LAN, a movie transcoding remotely (paused) |
| `status_sessions_idle.json` | `GET /status/sessions` | Nothing playing |
| `library_sections.json` | `GET /library/sections` | Movies (1), TV Shows (2), Music (3), 4K Movies (5) |
| `section_1_all.json` | `GET /library/sections/1/all?X-Plex-Container-Start=0&X-Plex-Container-Size=0` | `totalSize` 1180 |
| `section_2_all.json` | `GET /library/sections/2/all?…` | `totalSize` 318 |
| `section_5_all.json` | `GET /library/sections/5/all?…` | `totalSize` 60 |

**plex.tv (the PIN sign-in):**

| File | Endpoint | Notes |
|---|---|---|
| `pin_created.json` | `POST /api/v2/pins?strong=true` | A strong PIN, valid 30 minutes |
| `pin_waiting.json` | `GET /api/v2/pins/564964751?code=…` | `authToken` still null |
| `pin_approved.json` | `GET /api/v2/pins/564964751?code=…` | `authToken` set |

The tests serve the plex.tv files from an `httptest` TLS server; nothing talks to plex.tv.
