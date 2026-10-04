# Frigate fixtures: dogfood NVR

Recorded on 2026-10-03 by [spike 4.5](../../../spikes/frigate/README.md) against the owner's Frigate **0.18.0** (LXC on **minig5**: 3 cameras, a Coral USB TPU, an Intel N100 iGPU). The spike used the internal, unauthenticated API port, which answers as `anonymous` with role `admin`. These fixtures seed the v0.1 Frigate integration's contract tests.

**There are no pictures here.** No snapshot, thumbnail, clip or recording was ever written to disk: media endpoints were measured (status, type, size) with their bytes counted into `io.Discard`, and base64 images were stripped from JSON before anything was saved.

## Files

| File | Request | What it is |
|---|---|---|
| `version.txt` | `GET /api/version` | `0.18.0-7e08f7b` (`text/plain`) |
| `profile.json` | `GET /api/profile` | `anonymous`, role `admin`, `allowed_cameras`. With auth on, this is how the integration learns it's camera-limited |
| `config.json` | `GET /api/config` | Full config, 42.6 kB: cameras, detectors (`edgetpu`, USB), go2rtc streams, MQTT, auth, models |
| `stats.json` | `GET /api/stats` | Per-camera fps, `connection_quality`, `reconnects_last_hour`, `stalls_last_hour`; detector inference speed; GPU; storage; uptime; `latest_version` |
| `stats_history.json` | `GET /api/stats/history` | 3 of 80 snapshots. The full response is 780 kB, about 20 minutes at 15 s spacing |
| `events.json` | `GET /api/events?limit=25&include_thumbnails=0` | 25 tracked objects (all `person`, all `max_severity: alert`) over 5.3 days, about 0.9 kB each |
| `events_summary.json` | `GET /api/events/summary` | 30 of 251 rows. The full response is 161 kB, so keep it off the hot path |
| `review.json` | `GET /api/review?limit=25&after=<7 days ago>` | 17 review items (alerts), with detection ids and `thumb_path` |
| `review_default_24h.json` | `GET /api/review?limit=25` | `[]`: without `after`, the window is the last 24 h, and that day was quiet |
| `review_summary.json` | `GET /api/review/summary` | Alerts per day. `last24Hours` is all `null`, and days are bucketed in UTC unless a `timezone` is passed |
| `recordings_storage.json` | `GET /api/recordings/storage` | Per camera: `usage` (MB), `bandwidth` (MB/h), `usage_percent` |
| `labels.json`, `sub_labels.json` | `GET /api/labels`, `/api/sub_labels` | `["person"]`, `[]` |
| `timeline.json` | `GET /api/timeline?limit=25` | 10 of 25. Without `before`/`after` it returns the **oldest** entries (from 2023) |
| `ws_onconnect.json` | websocket `/ws`, 45 s, after sending `{"topic":"onConnect"}` | The state snapshot (`camera_activity`, `birdseye_layout`, `model_state`, …) plus live `<camera>/status/detect` heartbeats and `<camera>/motion` |
| `ws_passive.json` | websocket `/ws`, 55 s, nothing sent | Heartbeats, motion, and one `stats` push (11.3 kB) |
| `media.json` | snapshot, thumbnail and clip endpoints | Status, content type, byte count and headers only |
| `endpoints.json` | every metadata GET | Status, size, latency, headers |

## How these were sanitized

| Original | Fixture |
|---|---|
| Camera names | `driveway`, `side_deck`, `backyard`, the Demo Home's names |
| The one zone | `walkway` |
| go2rtc stream names | the camera's neutral name (plus `_sub`), or `stream_01` … `stream_07` for the 7 streams no Frigate camera uses |
| LAN addresses (MQTT broker, Frigate's own WebRTC candidate) | `10.0.11.x`, last octet kept |
| Camera addresses (a separate VLAN) | `10.0.21.x`, last octet kept. Matches the OPNsense fixtures, where Frigate (`10.0.11.202`) pulls ~13 Mb/s from `10.0.21.16/.17/.18` |
| MQTT username | `mqtt-user` |
| Process command lines in `cpu_usages` | Program name or Frigate's process title (`frigate.capture:driveway`), with arguments dropped. ffmpeg arguments carry stream URLs |
| Credentials | **Already masked by Frigate** and kept as Frigate sends them: `rtsp://*:*@…`, `user=*&password=*`, and `"password": "__FRIGATE_SAVED_CREDENTIAL__"` (Frigate's redaction sentinel). Nothing secret was present in any response |

Kept verbatim: all numbers, timestamps, event and review ids, labels, boxes, stream paths (vendor URL shapes such as `Preview_01_main` and `live0`), ffmpeg arguments in `config.ffmpeg_cmds`, and the websocket framing.

A leak check (`spikes/frigate/leakcheck_test.go`) scanned these files for every real camera, zone and stream name, address, MQTT identity and `.env.local` value seen during capture, and found nothing. Names on the allowed neutral list (`driveway`, `side_deck`, `backyard`) are not scanned for.

## Facts worth testing against

- **Health per camera without MQTT.** The websocket sends `<camera>/status/detect` = `"online"` every 10 s. `stats.cameras.<c>` has `camera_fps` against `expected_fps`, `connection_quality` (`"excellent"`), and `reconnects_last_hour`/`stalls_last_hour`.
- **Stats cadence.** Frigate computes stats every ~15 s (see `stats_history`), but pushes them on the websocket only every `mqtt.stats_interval` (60 s).
- **Media sizes** (`media.json`):

  | Request | Size | Caching |
  |---|---|---|
  | `latest.jpg` | 48–80 kB | `no-store` |
  | `?height=180` | 8.1 kB JPEG, 4.9 kB WebP | `no-store` |
  | Event `snapshot.jpg` | 97 kB | `private, max-age=31536000` |
  | Cropped to 180 px | 7.9 kB | `private, max-age=31536000` |
  | Event `thumbnail` | 8.0 kB `.jpg`, 5.8 kB `.webp` | `private, max-age=2592000` |
  | Event `clip.mp4` | 5.3 MB | Chunked, no `Content-Length`, served through nginx's cache (`X-Cache-Status: MISS`) |

- **`HEAD`** returns `405` on API routes such as `latest.jpg`, but `200` on `clip.mp4`, which nginx serves.
