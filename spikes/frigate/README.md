# Spike 4.5 · Frigate: events, stats, cameras, stream options

Throwaway code that answers: **how should the v0.1 Frigate integration feed the Camera Hero widget and the Home Graph**?
- Which endpoints carry camera health, detections and storage?
- Can we get push updates without MQTT credentials?
- What do snapshots and clips cost?

It ran read-only against the dogfood Frigate (**0.18.0**, an LXC on **minig5**) on 2026-10-03.

Not product code. The fixtures are in [`testdata/integrations/frigate/`](../../testdata/integrations/frigate/README.md). The OPNsense half is in [`../opnsense`](../opnsense/README.md).

## How it works

| Command | What it does |
|---|---|
| `probe` | Fetches the metadata endpoints one at a time, 1 s apart. Images and credentials are scrubbed in memory **before** anything is written to the raw directory outside the repo. `--media-only` measures snapshot and clip endpoints |
| `ws` | Connects to `/ws` for ≤ 55 s and records every message. It sends at most one frame, the read-only `{"topic":"onConnect"}` (Frigate answers it by republishing current state). Topics that change state (`*/set`, `*/ptz`, `restart`, …) are never sent |
| `shape` | Prints a capture's structure with camera, zone and stream names masked |
| `sanitize`, `needles` | Fixtures and the leak-check list, as in the OPNsense spike. They share the git-ignored netmap |

### Camera privacy, enforced in code

**Media is never stored.**
- `Client.Do` refuses to keep any `image/*`, `video/*` or HLS body.
- `Client.Measure` counts snapshot, thumbnail and clip bytes into `io.Discard`, and records only status, type and size.
- `scrubImages` removes base64 pictures (JPEG, PNG, WebP, GIF, `data:`) and long `thumbnail`-style fields from JSON and websocket payloads before they're written.
- The tests in [`client_test.go`](client_test.go) cover all three.

The client is GET/HEAD-only and pinned to `FRIGATE_URL`, like the OPNsense one.

```bash
cd spikes/frigate
go run . probe --env ../../../.env.local --out $SCRATCH/frigate-raw --secrets $SCRATCH/frigate-secrets.needles --media=false
go run . probe --env ../../../.env.local --out $SCRATCH/frigate-raw --paths '/api/review?limit=25&after=<now-7d>'
go run . probe --env ../../../.env.local --out $SCRATCH/frigate-raw --media-only
go run . ws    --env ../../../.env.local --out $SCRATCH/frigate-raw --window 45s
go run . ws    --env ../../../.env.local --out $SCRATCH/frigate-raw --window 55s --onconnect=false
go run . needles  --raw $SCRATCH/frigate-raw --env ../../../.env.local --secrets $SCRATCH/frigate-secrets.needles --out $SCRATCH/frigate.needles
go run . sanitize --raw $SCRATCH/frigate-raw --env ../../../.env.local --netmap $SCRATCH/netmap.needles --out ../../testdata/integrations/frigate
LEAKCHECK_NEEDLES=$SCRATCH/frigate.needles LEAKCHECK_ENV=../../../.env.local go test -run LeakCheck -v .
```

## Findings

**Verdict: use the websocket for live state and HTTP for everything else; MQTT isn't needed.** The `/ws` endpoint relays the MQTT topics without broker credentials. It carries camera heartbeats, motion, stats and events. Everything the widget needs is cheap over HTTP. The one real decision is auth: the internal port grants **admin** to anyone who can reach it.

### 1. The instance

| | |
|---|---|
| Version | `0.18.0-7e08f7b`. `stats.service.latest_version` is also `0.18.0`, because version check is on |
| Cameras | 3, each with `record` and `detect` roles on go2rtc restreams (`rtsp://127.0.0.1:8554/<camera>`). Detect at 1280×720 or 640×480, 5 fps. One zone. go2rtc holds 13 streams, 7 of them for cameras Frigate doesn't use |
| Detector | `coral`: `edgetpu`, USB, SSD 320×320. **Inference 10.6 ms** |
| GPU | `N100 iGPU` (QSV decode): 8.7% busy |
| Enrichments | face recognition **on**; LPR, semantic search, GenAI off |
| Storage | `/media/frigate/recordings`: 937,766 MB total, 63,533 MB used (6.8%). Per camera: 4.6–17.3 GB, 1.8–4.5 GB/h |
| Activity | 25 tracked objects in 5.3 days, all `person` (the only label tracked), all alerts: about 5 a day |
| Uptime | 1,155,815 s ≈ 13.4 days, the same as the router's. Probably one shared power event |
| Auth | `auth.enabled: true` (that's the authenticated port). The spike used the internal port: `/api/profile` → `{"username":"anonymous","role":"admin"}` |

### 2. Event stream options

| Channel | Reachable without credentials? | What it carries | Verdict |
|---|---|---|---|
| **MQTT** (`frigate/#` on the broker at `10.0.11.20:1883`) | **No**: it needs broker credentials, and Frigate redacts the password in `/api/config` | Everything, plus binary snapshots per label | Not needed |
| **Websocket `/ws`**, internal port | **Yes**, `101` in 10 ms | Every MQTT topic **except binary payloads** (Frigate skips non-text). After `onConnect`: `camera_activity`, `model_state`, `birdseye_layout`, `audio_detections`, `embeddings_reindex_progress`, `profile/state` | **Use this** |
| Websocket `/ws`, authenticated port | With the `frigate_token` JWT cookie | Same | Use with a viewer account (section 5) |
| HTTP polling | Yes | Stats, events, review, snapshots | Fallback and catch-up |

Measured on `/ws` (100 s in total, a quiet evening):

| Topic | Cadence | Size | Payload |
|---|---|---|---|
| `<camera>/status/detect` | **every 10 s** per camera | 56–62 B | `"online"`. A heartbeat, new in recent versions |
| `<camera>/motion` | on change | 46 B | `"ON"`/`"OFF"` |
| `stats` | **every 60 s** (`mqtt.stats_interval`) | 11.3 kB | Same shape as `/api/stats` |
| `camera_activity` | on `onConnect` | 1.2 kB | Per camera: `config` flags (detect, record, snapshots, alerts, …), `motion`, `objects` |
| `events`, `reviews`, `tracked_object_update` | per detection | — | **Not observed**: no detection happened during the windows. Shapes per Frigate's MQTT docs: `{"type":"new"\|"update"\|"end","before":{…},"after":{…}}` |

### 3. HTTP endpoints and cost

| Endpoint | Size | Latency | Use |
|---|---|---|---|
| `/api/version` | 14 B | 8 ms | Version detection (`text/plain`) |
| `/api/profile` | 99 B | 11–27 ms | Role and `allowed_cameras` → Permission-limited state |
| `/api/config` | 42.6 kB | 8–23 ms | Cameras, zones, detectors, record/snapshot flags. **Sensitive** even with credentials masked: camera IPs, stream paths, MQTT host and user |
| `/api/stats` | 9.8 kB | 10 ms | Health and metrics. Recomputed every ~15 s |
| `/api/stats/history` | **780 kB** | 51 ms | 80 snapshots × 15 s ≈ 20 min. Can backfill sparklines once; never poll it |
| `/api/events?limit=25&include_thumbnails=0` | 22.3 kB | 12 ms | ~0.9 kB per event. Always pass `include_thumbnails=0` |
| `/api/events/summary` | **161 kB** | 42 ms | 251 rows. Too heavy for counts |
| `/api/review?limit=25` | 2 B | 24 ms | `[]`: **defaults to the last 24 h** |
| `/api/review?…&after=<ts>` | 7.6 kB | 23 ms | 17 alerts in 7 days |
| `/api/review/summary` | 0.8 kB | 14 ms | Alerts per day: the cheap source for the "detections today" count. Days are UTC unless you pass `timezone`; `last24Hours` came back all `null` |
| `/api/recordings/storage` | 0.3 kB | 18 ms | Per-camera usage and bandwidth |
| `/api/timeline?limit=25` | 7.4 kB | 13 ms | Returns the **oldest** rows (from 2023) unless bounded |

Snapshot and clip endpoints (sizes only, bytes discarded):

| Request | Status | Type | Size | Cache-Control |
|---|---|---|---|---|
| `/api/<camera>/latest.jpg` (×3) | 200 | image/jpeg | 47.9–79.7 kB | `no-store` |
| `/api/<camera>/latest.jpg?height=180` | 200 | image/jpeg | 8.1 kB | `no-store` |
| `/api/<camera>/latest.webp?height=180` | 200 | image/webp | 4.9 kB | `no-store` |
| `HEAD /api/<camera>/latest.jpg` | **405** | — | — | — |
| `/api/events/<id>/snapshot.jpg` | 200 | image/jpeg | 96.6 kB | `private, max-age=31536000` |
| `/api/events/<id>/snapshot.jpg?crop=1&height=180` | 200 | image/jpeg | 7.9 kB | same |
| `/api/events/<id>/thumbnail.jpg` / `.webp` | 200 | image/* | 8.0 / 5.8 kB | `private, max-age=2592000` |
| `HEAD /api/events/<id>/clip.mp4` | 200 | video/mp4 | no length | `no-store` |
| `/api/events/<id>/clip.mp4` | 200 | video/mp4 | 5.3 MB in 0.43 s, chunked | `no-store`, `X-Cache-Status: MISS` |

### 4. Mapping to the Home Graph and the Camera Hero widget

| Graph item | Source |
|---|---|
| `app:frigate` (`version`, `latest_version`, `uptime`) | `/api/version`, `stats.service` |
| `frigate:camera/<name>` resources | `config.cameras` keys: `enabled`, `detect.{fps,width,height}`, `record.enabled`, `snapshots.enabled`, `audio.enabled`, zones |
| `frigate:detector/<name>` (new kind, or attrs on the app) | `config.detectors` (`type`, `device`), `stats.detectors.<name>` (`inference_speed`, `cpu`, `mem`) |
| `exposes` app → camera | config |
| `depends_on` app → MQTT broker | `config.mqtt.host` |
| `provides_network_for` router → camera | Camera IP from the go2rtc source (`10.0.21.x`), matched to the OPNsense VLAN. The OPNsense top-talkers fixture already shows Frigate pulling ~13 Mb/s from those three addresses |
| `metric camera/fps` | `stats.cameras.<c>.camera_fps` vs `expected_fps`; also `process_fps`, `skipped_fps` |
| `metric camera/detections.24h` | `/api/review/summary` (alerts per day) or `/api/events?after=now-24h` count. `stats.detection_fps` is instantaneous (0 at rest) |
| `metric frigate/inference.ms` | `stats.detectors.coral.inference_speed` |
| `metric frigate/storage.pct` | `stats.service.storage["/media/frigate/recordings"]` `used/total` |
| `metric camera/storage.mb`, `bandwidth` | `/api/recordings/storage` |
| `metric camera/cpu` | `stats.cameras.<c>.{ffmpeg,capture,detect}_cpu` |
| `metric frigate/gpu.pct` | `stats.gpu_usages.<gpu>.gpu` |
| `camera.last_event` / `last_person` | `/api/events?limit=N&include_thumbnails=0`, then live `events` on `/ws` |

**Camera Hero widget:**

| State | Trigger |
|---|---|
| Loading | Camera names from `config`, grey frames |
| Healthy, "3 recording" | Every camera `online` on `/ws` and `camera_fps ≈ expected_fps`; count cameras with `record.enabled` (and `camera_activity.config.record`) |
| Empty, "No detections today" | `review/summary` for today, passing `timezone`, is zero |
| Attention, camera offline | `<camera>/status/detect` ≠ `online`, `camera_fps` 0, or `connection_quality` worse than `excellent`/`reconnects_last_hour` > 0 |
| Attention, storage nearly full | `storage` free < threshold |
| Down | `/api/version` unreachable and `/ws` closed |
| Stale | No heartbeat for > 30 s (3 × the 10 s cadence). Hatch the last frame |
| Needs repair | `401` on the authenticated port (expired JWT), or `404`/schema mismatch after an upgrade |
| Permission-limited | `/api/profile.allowed_cameras` is smaller than `config.cameras`, or role `viewer` |
| Off, expected | `config.cameras.<c>.enabled: false`, or `detection_enabled: false` in stats while `enabled_in_config` is true |

**Frames.**
- Tiles use `latest.webp?height=180` (≈ 5 kB), refreshed only while visible.
- The detection list uses event `thumbnail.webp` (≈ 6 kB). It's cacheable for 30 days by event id, so the hub can cache it.
- Clips stream on tap. They're generated server-side and chunked, so don't prefetch.

### 5. Least privilege

The internal port has no auth, and its anonymous user is **admin**: it can change config, restart Frigate, delete events and manage users.

For v0.1:
1. **Preferred:** the **authenticated port with a dedicated Frigate user, role `viewer`** (or a custom role limited to the cameras the dashboard shows; 0.18 has `auth.roles`).
   - The hub logs in with `POST /api/login` (JSON) and keeps the `frigate_token` cookie. This instance's `config.auth` sets `session_length` 86,400 s and `refresh_time` 1,800 s, so the client must handle token refresh and re-login. A `401` becomes a Repair.
   - `/api/profile` then reports the role and `allowed_cameras`.
   - *Not exercised*: it needs POST and a user, and this spike was GET-only.
2. **Accepted:** the internal port, with a quiet Inbox hint: "Frigate's API is reachable without authentication and grants admin to anyone on your network."

### 6. Surprises

- **Credentials arrive masked.** In `/api/config`, the MQTT password is `"__FRIGATE_SAVED_CREDENTIAL__"` (Frigate's `REDACTED_CREDENTIAL_SENTINEL`), RTSP userinfo is `*:*@` and query-string credentials are `user=*&password=*`. The integration should recognise all three, and must still treat the config as sensitive (addresses, paths, broker identity) and never forward it to the browser.
- **The websocket needs nothing and relays almost everything.** For read-only dashboards it's a better fit than MQTT.
- **Defaults hide data.** `review` defaults to the last 24 h, `timeline` returns the oldest rows, `review/summary` buckets days in UTC and returns `last24Hours: null`.
- **The heavy endpoints are easy to hit by accident:** `stats/history` is 780 kB and `events/summary` 161 kB.
- **Face recognition is on.** `sub_label` can carry the name of a household member (none in this sample). That's personal data: hide it for the Household and kiosk roles, and never put it in logs or diagnostics.
- **`HEAD` isn't uniform:** `405` on API routes, `200` on nginx-served clips.

### 7. Where the Demo Home differs

| Sim (`sim/topology.go`, `sim/routines.go`) | Reality |
|---|---|
| `detector: openvino` | `edgetpu` (Coral USB), plus an Intel N100 iGPU for decode |
| Labels: car, person, dog, cat | Only `person` is tracked |
| 0.4–0.9 detections/h per camera by day (~30/day for the house) | About **5 alerts a day** for the whole house |
| Camera health: `fps`, `detect`, `record` | Also `connection_quality`, `reconnects_last_hour`, `stalls_last_hour`, the 10 s heartbeat, `expected_fps` |
| Cameras on the LAN (`10.0.11.231–233`) | Cameras on a separate VLAN (`10.0.21.x` once sanitized) behind the router |
| `inference.ms` around 9.4 | 10.6 ✓ |
| No MQTT broker | Frigate `depends_on` a broker at `10.0.11.20` |
| No storage metric | Recordings disk 6.8% used, plus per-camera usage and bandwidth |

## Recommendations for v0.1

1. **One `/ws` connection per instance while anything camera-related is watched.** Drive camera status from the 10 s heartbeat and motion from `<camera>/motion`, and react to `events`/`reviews` pushes. On reconnect, catch up with `review?after=<last seen>`.
2. **Poll `/api/stats` every 30 s only when the websocket is down.** Otherwise use the 60 s `stats` push. Fetch `config` on connect and when the `config/*` topics change.
3. **Counts from `review/summary?timezone=<hub tz>`.** Lists from `review`/`events` with `after`, `limit` and `include_thumbnails=0`.
4. **Frames through the hub's media proxy.**
   - `latest.webp?height=180` for tiles; event `thumbnail.webp` for lists, cached by id.
   - Clips are streamed through on tap. They're never stored on the hub's disk.
5. **Wizard: offer the viewer-account route first.** Raise the "unauthenticated admin API" hint when the internal port is used.
6. **Recognise the masking conventions** (`__FRIGATE_SAVED_CREDENTIAL__`, `*:*@`, `user=*&password=*`), and treat `sub_label` as personal data.
