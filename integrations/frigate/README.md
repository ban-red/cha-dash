# Frigate (`frigate`)

Frigate NVR: the service, its cameras and their health, the detector's inference time, and tracked objects per camera over the last 24 hours. Live camera state comes over Frigate's `/ws` WebSocket while someone is watching; otherwise it polls.

**Tier:** silver (repairs, version detection, the anonymous-admin hint, fixture tests against spike 4.5's recording). **Capabilities:** `cameras`, `cameras.events`. **Secrets:** `password` (optional).

**It never requests a picture.** No snapshot, thumbnail, clip, recording or `latest.jpg` endpoint is ever called, events are requested with `include_thumbnails=0`, a response with an `image/*` or `video/*` type is refused unread, and binary WebSocket frames are dropped undecoded. The tests' fake Frigate fails on any request outside the metadata API.

## Config

```json
{"url": "https://nvr.lab.example:8971", "tls": {"mode": "tofu"}, "username": "dash", "password": "${secret:instance/<id>/password}"}
```

| Key | Required | Meaning |
|---|---|---|
| `url` | yes | Frigate's address, `http://` or `https://`. A path prefix (Frigate behind a proxy at `/nvr`) is kept |
| `tls` | no | The usual `{"mode", "pin"}` |
| `username` | no | A Frigate user. Give it the `viewer` role (or a custom role limited to the cameras the dashboard shows) |
| `password` | with `username` | Write-only, kept in the vault |

**Two ports, two setups** (spike 4.5, section 5):
- **Preferred: the authenticated port (8971) with a viewer account.** The hub signs in with `POST /api/login` (`{"user", "password"}`) and keeps the JWT cookie Frigate sets (`frigate_token` unless `auth.cookie_name` says otherwise). It's applied to every request and to the WebSocket handshake. When the token expires (`auth.session_length`, a day by default), the next request gets a 401 and the runtime signs in again and retries once.
- **Accepted: the internal port (5000) without credentials.** Frigate answers it as `anonymous` with role `admin`, so it works, but it raises the hint below. This is what the Homepage importer produces (`url` only).

## What it reads

| Endpoint | When | Why |
|---|---|---|
| `GET /api/version` | every sync | Liveness and the version (14 bytes, `text/plain`) |
| `GET /api/profile` | first sync, then every 15 min | Role and `allowed_cameras`; spots the anonymous admin. A 404 means Frigate before 0.14, which has no users |
| `GET /api/config` | first sync, every 15 min, and after the WebSocket (re)connects (Frigate restarts to apply config) | Cameras (`enabled`, `detect`, `record`, detect fps, UI order, `friendly_name`), detector types, `mqtt.stats_interval`, and go2rtc streams to find each camera's address. 42 kB, and sensitive: only the fields named here are decoded, and of the stream URLs only the host is kept |
| `GET /api/stats` | every sync, unless the WebSocket pushed stats within 3 × `stats_interval` | Per camera `camera_fps`, `expected_fps`, `connection_quality`, `reconnects_last_hour`, `detection_enabled`; detector `inference_speed`; `service.latest_version`, `uptime`, `last_updated` |
| `GET /api/events?after=<now − 24 h>&limit=500&include_thumbnails=0` | every 5 min while watched, every 30 min while idle, and 30 s after the WebSocket reports a new object | Counts per camera and each camera's latest object and latest person. `sub_label` (a recognised face or plate) is never decoded |
| `WS /ws` | only while watched (`Subscribe`) | See below |

The heavy endpoints the spike found (`/api/stats/history`, 780 kB; `/api/events/summary`, 161 kB) are never called.

**Poll plan.** `Poll.Interval` 30 s while watched, `Poll.Idle` 5 min otherwise, 30 s timeout. An idle sync is the version and the stats (≈10 kB), plus events every 30 minutes, so the 30-minute history grid gets a point. After a failed sync, the next one reads everything.

**The WebSocket.** It relays Frigate's MQTT topics without broker credentials. On connect the hub sends the one read-only frame `{"topic":"onConnect"}`, which makes Frigate republish its state; it never sends anything else (no `…/set`, `…/ptz` or `restart`). It reads:

| Topic | Effect |
|---|---|
| `<camera>/status/detect` (`online`, `offline`, `disabled`; a heartbeat every 10 s) | Camera state, fresher than stats; published only when it changes |
| `<camera>/motion` (`ON`/`OFF`) | `attrs.motion` |
| `<camera>/enabled/state`, `detect/state`, `recordings/state` | The camera's flags |
| `camera_activity` (answer to `onConnect`) | Flags and motion for every camera |
| `stats` (every `mqtt.stats_interval`, 60 s by default) | Same as `/api/stats`; while it arrives, syncs stop polling stats |
| `events`, type `new` | The camera's latest object now; the count on the next sync |

Messages are capped at 2 MiB. When the socket closes, what only it knew (status, motion) is dropped and polling takes over; the runtime reconnects with backoff while someone is still watching.

## What it publishes

IDs follow the wave 5 convention, `frigate:<instance id>/…`.

**`frigate:<id>/app`**, kind `app`, named after the instance, state `running`, health `ok` while Frigate answers. `caps: ["cameras", "cameras.events"]`, a link to the configured URL.

| Attr | From |
|---|---|
| `type` | `frigate` (how widgets find the service) |
| `version` | `/api/version` without the build hash (`0.18.0`) |
| `latest_version` | `stats.service.latest_version`, only when it differs (Frigate's version check must be on) |
| `cameras` | how many cameras are published |
| `detector` | detector types from the config, comma-separated (`edgetpu`) |
| `started_at` | `last_updated − uptime`, moved only on a restart |
| `url` | the configured URL (a natural key for the correlator) |

**`frigate:<id>/camera/<name>`**, kind `camera`, one per camera in the config (only `allowed_cameras` for a limited role), named after `friendly_name` or the key made readable (`side_deck` → "Side deck"). `caps: ["cameras"]`.

| Attr | From |
|---|---|
| `camera` | Frigate's camera name |
| `detect`, `record` | config, stats' `detection_enabled`, and the WebSocket |
| `fps` | the configured detect fps |
| `ip` (or `host`) | the camera's address: the detect input's host, or, for a go2rtc restream on localhost, the host of that go2rtc stream's first source. Credentials and paths are never kept |
| `motion` | WebSocket only |
| `last_event.at`, `last_event.label`, `last_event.score`; `last_person.at`, `last_person.score` | the newest object (and the newest person) seen in the last 24 h, or since, while the instance runs. Score is `top_score`, to two places |

Camera state and health, freshest source first:

| Case | State | Health |
|---|---|---|
| disabled in config, `enabled/state OFF`, or status `disabled` | `stopped` | `ok`, "Turned off in Frigate" |
| WebSocket status `offline` | `error` | `down` |
| no WebSocket status and `camera_fps` 0 | `error` | `down`, "No frames from the camera" |
| `connection_quality` other than `excellent`/`good`, any `reconnects_last_hour`, or under half the expected fps | `online` | `attention`, with the reasons |
| otherwise | `online` | `ok` |
| nothing read yet | `unknown` | `unknown` |

**Relations:** the app `exposes` each camera (as in the Demo Home); `integration:<id> monitors` the app and every camera.

**Metrics** (the simulator's keys and scales):

| Series | Value |
|---|---|
| `frigate:<id>/app#inference.ms` | mean `inference_speed` of the detectors that report one (ms, 1–500, band 5–20). Unknown when none does |
| `frigate:<id>/camera/<name>#detections.24h` | tracked objects (Frigate events, false positives excluded) that started in the last 24 h (count, 0–500, band 0–40) |

Scopes: `graph` (resources), `stats` (inference), `events` (counts), each published when its source was read, so a series' `as_of` is when its source was.

## Test connection

Reads `/api/version` and `/api/profile`, and reports:
- **`admin`** → a grant with access `admin` ("can change the config, restart Frigate and delete recordings"), plus a `frigate_unauthenticated` warning when it's the anonymous user. The runtime adds its write-grant warning.
- **`viewer`** → a read grant, with the number of allowed cameras.
- **a custom role** → a read grant named after it.
- **no `/api/profile`** (before 0.14) → an `admin` grant: there are no users, so anyone who reaches it can change it.

## Repairs and hints

- **Wrong username or password:** `/api/login` answers 401 → an auth Repair on `password`. 404 on `/api/login` (no sign-in at that URL, Frigate before 0.14) → a config error on `username`. A 429 (Frigate limits sign-ins) is transient.
- **Not Frigate, or the API changed:** a 404 on `/api/version`, `/api/stats`, `/api/config` or `/api/events`, a non-version answer from `/api/version` (an HTML sign-in page from a proxy, say), JSON it can't decode, or stats without `cameras` → an `api_changed` Repair.
- **The hint** (spike 4.5, section 5): while Frigate answers this instance as `anonymous` with role `admin` (the internal port), or has no users at all, an inbox item `<id>.unauthenticated-admin`, class `problem`, severity `info`, scenario `frigate-unauthenticated-admin`: "Frigate grants admin without a sign-in", suggesting a viewer account on port 8971. It resolves itself once that stops (the instance signs in on the authenticated port), checked with the profile every 15 minutes and on every restart: adding credentials restarts the instance, and the new one resolves what the old one raised. The inbox has no separate "hint" class, so it's a problem at the quietest severity.

## Limits

- **Detections are capped at 500 per 24 h** (one `/api/events` page, ≈0.9 kB each). A busier home undercounts.
- **The latest object** is from the last 24 hours, or later while the instance runs: a camera with no event in the last day shows none after a restart.
- **No stale-heartbeat detection.** Frigate older than the 10 s heartbeat doesn't send it, so a missing heartbeat can't mean offline; an explicit `offline` or zero fps does.
- **Not read yet:** storage (`stats.service.storage`, `/api/recordings/storage`), GPU usage, per-camera CPU, review items and summaries, zones, the MQTT broker as a `depends_on` relation, `provides_network_for` from the router (the correlator's job, from `attrs.ip`).
- **The session isn't logged out on stop**: Frigate's `/api/logout` only clears the browser's cookie, and the JWT lapses on its own.
- Faces and plates (`sub_label`) are personal data and are never read.
