# AdGuard Home (`adguard`)

Reads AdGuard Home through its control API: the last 24 hours' queries, how many were blocked, how big the blocklist is, and whether protection is on. It publishes the same metric keys as Pi-hole, so one DNS tile reads either.

**Tier:** silver, Tier 0 (Go). **Capability:** `dns.filter`. **Secret:** `password` (optional).

**Why Tier 0 and not a spec:** basic auth needs `base64(user:password)` in the header, which a spec's templates can't compute; and the 24-hour totals are sums over the last 24 entries of hourly arrays, with a fallback when the stats interval is shorter, which gjson paths can't express.

## Config

```json
{"url": "https://adguard.lab.example", "tls": {"mode": "verify"}, "username": "admin", "password": "${secret:instance/<id>/password}"}
```

| Key | Meaning |
|---|---|
| `url` | The web interface's address, with its path if it's proxied under one |
| `tls` | The usual `{"mode", "pin"}` |
| `username` | An AdGuard Home user (`users` in `AdGuardHome.yaml`): an identifier, so plain config |
| `password` | That user's password: write-only, kept in the vault |

Both or neither: leave both out when AdGuard Home has no users. Every request carries `Authorization: Basic …`; nothing goes in the URL.

## What it reads

All `GET`:

| Endpoint | When | For |
|---|---|---|
| `/control/status` | every sync | version, `running`, protection and its pause |
| `/control/stats?recent=86400000` | every sync | the last 24 hours' queries and blocks |
| `/control/filtering/status` | the first sync, then every 30 minutes while watched | whether filtering is on, and the blocklist size |

**The 24-hour window.** `/control/stats` covers AdGuard Home's statistics interval (24 hours by default). Up to 7 days, its arrays hold one entry per hour, oldest first; beyond, one per day.
- The integration asks for `recent=86400000` (24 hours, AdGuard Home v0.107.72 and later), so the answer is exactly the last 24 hours.
- Older releases ignore `recent`; the integration then sums the last 24 hourly entries itself, which is the same window.
- An interval shorter than 24 hours makes `recent` a 400; the integration asks again without it and sums what there is. `attrs.stats_hours` says how many hours the figures cover (24 normally).
- An interval over 7 days on a release before v0.107.72 answers in days, so the 24-hour figures are unknown, and Test adds an info warning (`stats_days`) saying to update or shorten the interval.

**Poll plan:** every 60 s while someone watches, every 5 minutes while nobody does, 30 s per sync.

## What it publishes

- **`adguard:<instance id>/app`**, kind `app`, named after the instance, state `running`, with:
  - `attrs.type = "adguard"`, `attrs.url`, `attrs.version` (without the `v`);
  - `attrs.running` (the DNS server), `attrs.protection_enabled`, `attrs.protection_paused_until` (RFC 3339, UTC, while a timed pause runs), `attrs.filtering_enabled`;
  - `attrs.filters` (how many filter lists are enabled) and `attrs.stats_hours`;
  - a link to the configured URL and `caps: ["dns.filter"]`.
- `integration:<instance id> monitors` it.
- **Health:** `down` with "AdGuard Home's DNS server isn't running"; else `attention` with "Protection is paused for another 10 min" or "Protection is off"; else `attention` with "Filtering is off, so nothing is blocked"; else `ok`.

| Metric | Unit | Scale | Band | Value |
|---|---|---|---|---|
| `queries.24h` | count | 0–10⁷ | 0–50,000 | the last 24 hourly `dns_queries` |
| `blocked.24h` | count | 0–10⁷ | 0–15,000 | the same hours of `blocked_filtering` + `replaced_safebrowsing` + `replaced_parental` (Safe Search rewrites aren't blocks) |
| `blocked.pct` | % | 0–100 | 5–35 | `blocked.24h` / `queries.24h`, to 0.1; 0 with no queries. **The Service tile's primary stat** |
| `blocklist` | count | 0–10⁷ | 50,000–5·10⁶ | the `rules_count` of the enabled filter lists (allowlists and user rules not counted). **The tile's context line** |

The keys are Pi-hole's (neither is in the Demo Home). A quiet resolver is 0, never unknown.

## Grants

Every AdGuard Home user is an administrator, so Test reports one grant, `AdGuard Home user`, access `admin` ("can change filters, clients and settings"; without users, "anyone who reaches it has full control"), and the hub warns about it.

## Repairs

| What happened | Error | Field |
|---|---|---|
| 401 or 403 with credentials | `auth` Repair: "AdGuard Home rejected the username or password" | `password` |
| 401 or 403 without | `auth` Repair: "enter its username and password" | `username` |
| 404, or an answer that isn't JSON | `api_changed` Repair: check that the URL is AdGuard Home's web interface | `url` |
| `/control/status` without a version | `api_changed` Repair | `url` |

## Limits

- Top domains, top clients and the query log aren't read: they're personal, and the tile shows totals.
- Pausing and resuming protection are actions (v0.2).
- `avg_processing_time`, upstream statistics and DHCP aren't published.

## Fixtures

Synthetic, in [`testdata/integrations/adguard/`](../../testdata/integrations/adguard/README.md).
