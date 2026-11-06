# Technitium DNS fixtures

**Synthetic.** Responses for `specs/technitium.yaml`, written by hand from Technitium DNS Server's API documentation (`APIDOCS.md` on the master branch, 15.6.2, fetched 2026-11-06: "API Authentication", "Get Session Info" and "Get Stats"). No real server was contacted; names and numbers are made up, with `*.home.example` names and `10.20.30.x` addresses. The token is fake; `session.json` echoes it back the way Technitium does, and the test checks it never leaves the response.

| File | Endpoint | Notes |
|---|---|---|
| `session.json` | `GET /api/user/session/get` (`Authorization: Bearer …`) | A user with Dashboard: View only, version 15.6.2 |
| `stats_day.json` | `GET /api/dashboard/stats/get?type=LastDay&utc=true` | 48 210 queries, 8 033 blocked; chart data and top lists trimmed |
| `stats_hour.json` | `…?type=LastHour&utc=true` | 9 of 1 987 queries SERVFAIL |
| `stats_hour_failing.json` | The same | 552 of 1 840 SERVFAIL (30%) |
| `invalid_token.json` | Any call, with a token Technitium doesn't know | `status: invalid-token` in a 200 |
| `denied.json` | Stats, for a user without Dashboard: View | `status: error`; the exact message is assumed |

Not checked against a live server: the error message for a missing permission (the spec matches "access", "denied" or "permission"), and how a pre-15.0 server answers a header-only token.
