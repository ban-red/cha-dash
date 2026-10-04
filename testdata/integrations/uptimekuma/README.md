# Uptime Kuma fixtures: synthetic

**These fixtures are synthetic.** Nothing here was recorded from a real Uptime Kuma. They were written on 2026-10-03 for the v0.1 Uptime Kuma integration's contract tests, from Uptime Kuma 2's source (`louislam/uptime-kuma`, `master`):
- `server/routers/status-page-router.js`: the two public status page routes, their caches (5 minutes and 1 minute), and `sendHttpError` (404 for "not found", 503 for a busy database, 403 for anything else, with a `{"status": "fail", "msg"}` body);
- `server/model/status_page.js`, `group.js` and `monitor.js`: `getStatusPageData` (`config`, `incidents`, `publicGroupList`, `maintenanceList`) and the public monitor shape (`id`, `name`, `sendUrl`, `type`, `url` only with `sendUrl`, certificate expiry only with `showCertificateExpiry`);
- `server/model/heartbeat.js`: the public heartbeat (`status`, `time`, `msg` (always empty since 2.0), `ping`), up to 100 per monitor, oldest first, and `uptimeList` keyed `<monitor id>_24`;
- `server/prometheus.js`: the `/metrics` gauges and their labels (`monitor_id` is new in 2.0; 1.x has only `monitor_name`, `monitor_type`, `monitor_url`, `monitor_hostname`, `monitor_port`), plus the wiki's "Prometheus API Keys" page (basic auth, empty username, the key as the password).

The monitors mirror the Demo Home's nine (`sim/topology.go`, `kumaMonitors`) at 14:05 on its day, with qBittorrent down. Hosts are `*.lab.example` and `10.0.11.x`.

## Files

| File | Request | What it is |
|---|---|---|
| `status_page.json` | `GET /api/status-page/current` | Title "Lab status", three groups, nine monitors (`http`, `ping`, `keyword`, `port`), certificate expiry shown |
| `heartbeat.json` | `GET /api/status-page/heartbeat/current` | Five beats a minute apart per monitor (the real endpoint returns up to 100), ending before 14:05 UTC. qBittorrent (8) has been down since 14:03:33, Mealie (5) is pending; the rest are up. 24-hour uptime per monitor |
| `metrics.txt` | `GET /metrics` with the API key | Uptime Kuma 2's gauges: status, response time, certificate days and validity, the windowed uptime and response time, and one process metric. Mealie is up here (the live value, newer than the heartbeat cache), and "Internal DNS" (10) isn't on the status page |

## What they should derive

| | Without a key | With the key |
|---|---|---|
| `monitors.up` | 7 | 8 (Mealie recovered) |
| `monitors.down` | 1 (qBittorrent) | 1 |
| Home Assistant `http.ms` | 41 (newest beat) | 42 (`monitor_response_time`) |
| qBittorrent target | unknown (not on the page) | `10.0.11.30:8080` |
| Internal DNS | not published | not published |

The tests also strip `monitor_id` from `metrics.txt` to stand in for Uptime Kuma 1.x, where monitors match by name.
