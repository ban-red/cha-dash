# Pi-hole fixtures (synthetic)

**Synthetic.** Nothing here was captured from a real Pi-hole. The responses were written by hand for the Pi-hole integration's contract tests ([`integrations/pihole`](../../../integrations/pihole/README.md)), in the shapes of Pi-hole v6's REST API as its OpenAPI description defines them (FTL's `src/api/docs/content/specs/auth.yaml`, `stats.yaml`, `dns.yaml`, `info.yaml`, and docs.pi-hole.net/api). The v5 answer follows the PHP API (`/admin/api.php?versions`) that v5 served. The numbers and hashes are made up; the session id and CSRF token are base64 placeholders.

| File | Endpoint | Notes |
|---|---|---|
| `auth_ok.json` | `POST /api/auth` `{"password": …}` | A valid session: `sid`, `csrf`, `validity` 1800 s |
| `auth_no_password.json` | `POST /api/auth` on a Pi-hole without a password | `valid` with `sid: null` |
| `unauthorized.json` | any, with a wrong password or no session | Sent with HTTP 401 |
| `seats_exceeded.json` | `POST /api/auth` | Sent with HTTP 429 when every API session is taken |
| `stats_summary.json` | `GET /api/stats/summary` | 48,210 queries in the last 24 h, 9,020 blocked (18.7 %), 182,544 domains on the blocklist, 17 of 24 clients active, gravity updated 2026-09-28 03:12 UTC |
| `dns_blocking.json` | `GET /api/dns/blocking` | `enabled`, no timer |
| `info_version.json` | `GET /api/info/version` | Core v6.1.4, web v6.2.1, FTL v6.2.3 with v6.3 available, not in Docker |
| `v5_versions.json` | `GET /admin/api.php?versions` | A Pi-hole v5.18.4, which has no `/api` |

The fake Pi-hole in `integrations/pihole/pihole_test.go` keeps the session ids it handed out, answers 401 without `X-FTL-SID`, frees the session on `DELETE /api/auth`, and plays the paused, failed and empty-blocklist cases inline.
