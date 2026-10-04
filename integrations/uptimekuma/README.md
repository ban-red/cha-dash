# Uptime Kuma (`uptimekuma`)

One Uptime Kuma status page: its monitors as check resources, and the service's count of monitors up and down. It reads the public status page API, and Prometheus `/metrics` when an API key is set.

**Tier:** silver (repairs, fixture tests; no version: none of these endpoints tells it). **Capability:** `checks.uptime`. **Secrets:** `key` (optional).

## Config

```json
{"url": "https://uptime.lab.example", "tls": {"mode": "verify"}, "slug": "current", "key": "${secret:instance/<id>/key}"}
```

| Key | Required | Meaning |
|---|---|---|
| `url` | yes | Uptime Kuma's address, `http://` or `https://` (a path prefix is kept) |
| `tls` | no | The usual `{"mode", "pin"}` |
| `slug` | yes | The status page: the end of its address, `/status/<slug>`. Letters, digits, `-` and `_`; Uptime Kuma lowercases it |
| `key` | no | An API key (Settings → API Keys). Adds live status, response times and certificate expiry from `/metrics` |

**The status page decides which monitors appear**, with or without a key. It's the view the owner already curated, and the Homepage widget the importer replaces showed the same page. `/metrics` covers every monitor, but with a key it only enriches the page's monitors; the others are ignored.

## What it reads

| Endpoint | Auth | Why |
|---|---|---|
| `GET /api/status-page/<slug>` | none (public) | The page's title, groups and monitors (`id`, `name`, `type`, `url` when the page shows it, certificate expiry when the page shows it). Cached for 5 minutes by Uptime Kuma |
| `GET /api/status-page/heartbeat/<slug>` | none (public) | Up to 100 beats per monitor (`status`, `time`, `msg`, `ping`) and the 24-hour uptime. Cached for 1 minute by Uptime Kuma |
| `GET /metrics` | basic auth, empty username, the key as the password; only when `key` is set | `monitor_status`, `monitor_response_time`, `monitor_cert_days_remaining`, `monitor_cert_is_valid`, and the target from the labels. The windowed averages (`window` label) and process metrics are skipped |

The status page endpoints never get a credential.

**Poll plan.** `Poll.Interval` 60 s while watched (the heartbeat endpoint is cached for a minute, so faster is pointless), `Poll.Idle` 5 min otherwise, 30 s timeout. Each sync reads all of them: they're small (the heartbeat list is ~6 kB per monitor).

**Merging.** A monitor's status, message and ping come from its newest beat; with a key, `/metrics`' live values win (the heartbeat endpoint can be a minute behind). Monitors match `/metrics` by `monitor_id` (Uptime Kuma 2), or by name when the labels have no ID (1.x) and the name is unique.

## What it publishes

**`uptimekuma:<id>/app`**, kind `app`, named after the instance, state `running`, health `ok` while the status page answers. `caps: ["checks.uptime"]`; links to Uptime Kuma and to the status page.

| Attr | From |
|---|---|
| `type` | `uptimekuma` |
| `status_page` | the slug |
| `title` | the status page's title |
| `monitors` | how many monitors the page shows (the widget's total) |
| `url` | the configured URL |

**`uptimekuma:<id>/monitor/<monitor id>`**, one per monitor on the page, keyed by Uptime Kuma's numeric ID (stable across renames), **kind `check`**, named after the monitor, `caps: ["checks.uptime"]`.

*Why `check`:* an Uptime Kuma monitor is exactly what the built-in checks integration publishes as a check: a probe of one target with an up/down health and a latency. Using the same kind means the web app finds a tile's check by kind and name whichever integration probes it, inbox copy and widgets treat them alike, and the correlator (wave 6) can merge a Kuma monitor and a built-in check of the same URL. A new kind (`monitor`) would have needed its own widget code for the same thing.

| Attr | From |
|---|---|
| `check` | the monitor type (`http`, `keyword`, `port`, `ping`, `dns`, `docker`, `push`, …), like the built-in checks' `http`/`tcp` |
| `monitor_id` | Uptime Kuma's ID |
| `group` | the status page group |
| `target` | the URL, or `host:port`, when the page or `/metrics` says |
| `ip` | the host, when it's an address (a natural key) |
| `uptime_24h` | percent, one decimal |
| `cert_days` | days until the certificate expires, when it's valid |

| Status | State | Health |
|---|---|---|
| 1 up | `online` | `ok` |
| 0 down | `error` | `down`; `health.since` is the first beat of the down streak |
| 2 pending (failing, retrying) | `error` | `attention`, "Failing; Uptime Kuma is retrying" |
| 3 maintenance | `maintenance` | `maintenance` |
| no beats yet | `unknown` | `unknown` |

The reason is the beat's message when there is one; Uptime Kuma 2 no longer publishes messages on status pages.

**Relations:** each monitor is `part_of` the app (as a Traefik route is `part_of` Traefik in the Demo Home); `integration:<id> monitors` the app and every monitor.

**Metrics:**

| Series | Value |
|---|---|
| `uptimekuma:<id>/app#monitors.up` | monitors with status up (count, 0 – total, band total – total) |
| `uptimekuma:<id>/app#monitors.down` | monitors with status down (count, 0 – total, band 0 – 0) |
| `uptimekuma:<id>/monitor/<n>#http.ms` | response time of `http`, `keyword`, `json-query`, `real-browser` and `grpc-keyword` monitors (1–5000 ms, band 1–300, warn 800, crit 2000: the built-in checks' scale) |
| `…#tcp.ms` | `port` monitors (band 1–50, warn 200, crit 1000) |
| `…#ping.ms` | `ping` monitors (same scale as `tcp.ms`). Not a simulator key |
| `…#response.ms` | every other type (the `http.ms` scale). Not a simulator key |

Pending and maintenance monitors are in neither count, so `up + down` can be less than `attrs.monitors`. A monitor's latency is unknown unless it's up.

## Test connection

Reads the status page, its heartbeats and, with a key, `/metrics`. No version is reported. Grants:
- `status page <slug>`, read: public, anyone who reaches Uptime Kuma can read it;
- with a key, `metrics`, read: an Uptime Kuma API key only opens `/metrics`.

Neither can change anything. A page with no monitors passes with an info warning.

## Repairs

- **Unknown slug** (404) → a config error on `slug`: no Repair, the instance shows the message.
- **Wrong API key** (401 or 403 on `/metrics`) → an auth Repair on `key`. A 404 on `/metrics` → an `api_changed` Repair on `key`.
- **403 on a status page endpoint** is Uptime Kuma's catch-all for its own errors, not a credential problem (there is none): a transient error with Uptime Kuma's message. 503 (its database is busy) is transient too.
- **Not Uptime Kuma** (an HTML page, JSON without a config or monitors, an unparseable `/metrics`) → an `api_changed` Repair.

## Limits

- **No incidents from monitors.** Uptime Kuma notifies on its own; raising a problem per monitor would alert twice. The widget shows the counts, and each check its health.
- **Not read:** the page's pinned incidents and maintenance windows, tags, monitors that aren't on the page, the version, and the windowed (30 d, 365 d) uptime.
- **Unpublished status pages** may still answer the API; the integration doesn't check `published`.
