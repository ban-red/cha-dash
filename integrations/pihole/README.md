# Pi-hole (`pihole`)

Reads Pi-hole v6 through its REST API: the last 24 hours' queries, how many were blocked, how big the blocklist is, and whether blocking is on.

**Tier:** silver, Tier 0 (Go). **Capability:** `dns.filter`. **Secret:** `password` (optional).

**Why Tier 0 and not a spec:** v6's API wants a session: `POST /api/auth` with the password, then the session id on every request, a new login when it expires, and a logout so the session's seat is freed (Pi-hole caps concurrent sessions). That's the runtime's `Session` hook, which only Go integrations have.

**Pi-hole v5 isn't supported.** Its PHP API (`/admin/api.php?summaryRaw&auth=<token>`) takes a different credential (the API token, not the password) in the query string, has other field names and needs a second code path, for a release that reached end of life when v6 shipped in February 2025. It wasn't cheap enough. A v5 is recognised and named instead: when `/api` answers 404, the integration asks `/admin/api.php?versions`, and a v5 gets an `unsupported_version` Repair ("Pi-hole v5.18.4 isn't supported … Update the Pi-hole to v6").

## Config

```json
{"url": "https://pihole.lab.example", "tls": {"mode": "verify"}, "password": "${secret:instance/<id>/password}"}
```

| Key | Meaning |
|---|---|
| `url` | The Pi-hole's address. The admin page's (`…/admin/`) works too: a trailing `/admin` is dropped, since the API lives at `/api` on the root |
| `tls` | The usual `{"mode", "pin"}`. Pi-hole v6 serves HTTPS itself with a self-signed certificate, so `tofu` fits |
| `password` | An **app password** (Settings → Web interface / API → Configure app password, in expert mode), or the web password. Write-only, kept in the vault. Leave it out when the Pi-hole has no password |

Use an app password: it works when two-factor authentication is on (the web password then also wants a TOTP code, which a hub can't supply), and it can be revoked without changing the password people sign in with.

## Signing in

A session through the runtime's `Session` hook:
- **Login:** `POST /api/auth` with `{"password": "…"}`. Pi-hole answers `{"session": {"valid": true, "sid": "…", "validity": 1800, …}}`. A Pi-hole without a password answers `valid` with `sid: null`, and requests then go without one.
- **Requests** carry `X-FTL-SID: <sid>` (the header form, so the id never reaches a URL or needs a CSRF token). Each request extends the session; an expired one gets 401, so the framework logs in again and retries once.
- **Logout:** `DELETE /api/auth` with the session id when the instance stops, so its seat is freed at once rather than when it expires.
- **401 on login** is a wrong password; **429** means every API seat is taken (`api_seats_exceeded`, raise `webserver.api.max_sessions` or wait) or sign-ins are rate-limited. The 429 is transient, not a Repair.

## What it reads

All `GET`:

| Endpoint | When | For |
|---|---|---|
| `/api/info/version` | the first sync, then hourly while watched | versions, and whether an update is available |
| `/api/stats/summary` | every sync | queries, blocked, blocklist size, clients, gravity's last update |
| `/api/dns/blocking` | every sync | whether blocking is on, and the pause timer |

**Poll plan:** every 60 s while someone watches, every 5 minutes while nobody does (well inside the session's 30-minute validity), 30 s per sync.

## What it publishes

- **`pihole:<instance id>/app`**, kind `app`, named after the instance, state `running`, with:
  - `attrs.type = "pihole"`, `attrs.url` (the root), `attrs.version` (core, without the `v`), `attrs.ftl_version`, `attrs.web_version`;
  - `attrs.update_available`: the Docker image's tag against the latest when the Pi-hole runs in Docker, else any of core, web and FTL behind its remote version;
  - `attrs.blocking`: `enabled`, `disabled`, `failed` or `unknown`; `attrs.blocking_timer_s` while a pause is counting down;
  - `attrs.gravity_updated_at` (RFC 3339, UTC) and `attrs.clients_total` (clients seen in the window);
  - a link to the configured URL and `caps: ["dns.filter"]`.
- `integration:<instance id> monitors` it.
- **Health:** `attention` with "Blocking is paused for another 5 min" while a timed pause runs, "Blocking is off" when it's off for good, "Pi-hole couldn't turn blocking on" when it `failed`; otherwise `attention` with "The blocklist is empty: update gravity" when gravity holds no domains; else `ok`.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `queries.24h` | count | 0–10⁷ | 0–50,000 | `queries.total`: Pi-hole's window is the last 24 hours |
| `blocked.24h` | count | 0–10⁷ | 0–15,000 | `queries.blocked` |
| `blocked.pct` | % | 0–100 | 5–35 | `queries.percent_blocked`, to 0.1. **The Service tile's primary stat** |
| `blocklist` | count | 0–10⁷ | 50,000–5·10⁶ | `gravity.domains_being_blocked`: the domains on the blocklist. **The tile's context line.** Unknown when Pi-hole reports −1 |
| `clients` | count | 0–1000 | 0–50 | `clients.active` |

None of these keys are in the Demo Home (it has no DNS filter); `queries.24h` follows Prowlarr's naming. AdGuard Home publishes the same keys (except `clients`), so one DNS tile reads either. A quiet resolver is 0, never unknown.

## Grants

Pi-hole has one administrator, and an app password signs in as it, so Test reports one grant, `Pi-hole admin`, access `admin` ("can change settings, lists and blocking"; without a password, "anyone who reaches it has full control"), and the hub warns about it.

## Repairs

| What happened | Error | Field |
|---|---|---|
| Wrong password (401 on login, or `valid: false`) | `auth` Repair, suggesting an app password | `password` |
| 401 with no password configured | `auth` Repair: "Pi-hole wants a password: enter an app password" | `password` |
| 404 on `/api`, and `/admin/api.php` is a v5 | `unsupported_version` Repair naming the version | |
| 404 otherwise, or an answer that isn't JSON | `api_changed` Repair: check the URL, and that it's v6 or later | `url` |
| 429 on login | transient, saying the seats are taken | |

## Actions

| Action | Tier | Undo | Household | What it does | Verify |
|---|---|---|---|---|---|
| `pihole.app.pause` | T1 | `pihole.app.resume` | yes | `POST /api/dns/blocking` with `{"blocking": false, "timer": 300}`: Pi-hole turns blocking back on by itself after 5 minutes | `GET /api/dns/blocking` says `disabled` |
| `pihole.app.resume` | T1 | — | yes | `POST /api/dns/blocking` with `{"blocking": true, "timer": null}` | it says `enabled` |

- **Household:** both are marked `household: true`, so the Household role can pause blocking for a few minutes (and turn it back on) without any other rights.
- **Preconditions** (`409 precondition`): blocking is already paused (with the minutes left) or off, for a pause; already on, for a resume.
- **Credential:** the instance's app password. Pi-hole has one administrator and no scopes, so there's no separate control credential to add.
- **Audit:** the target and empty params; the password never appears in a plan, task log or audit record.

## Limits

- Top domains, top clients and the query log aren't read: they're personal, and the tile shows totals.
- Pausing and resuming blocking are actions (v0.2).
- `blocked.pct` is Pi-hole's own figure, so it counts everything Pi-hole counts as blocked (gravity, regex, denylist, CNAME inspection, upstream-blocked).

## Fixtures

Synthetic, in [`testdata/integrations/pihole/`](../../testdata/integrations/pihole/README.md).
