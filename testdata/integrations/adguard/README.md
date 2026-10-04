# AdGuard Home fixtures (synthetic)

**Synthetic.** Nothing here was captured from a real AdGuard Home. The responses were written by hand for the AdGuard Home integration's contract tests ([`integrations/adguard`](../../../integrations/adguard/README.md)), in the shapes of AdGuard Home's control API as its OpenAPI description (`openapi/openapi.yaml`: `ServerStatus`, `Stats`, `FilterStatus`) and source (`internal/stats/http.go`, where `recent` is parsed and the hourly or daily arrays are built) define them, as of v0.107.79. Domains are `.example` names; client addresses are the Demo Home's `10.0.11.x`; the counts are made up. The first filter list is AdGuard Home's default DNS filter; the others are invented.

| File | Endpoint | Notes |
|---|---|---|
| `control_status.json` | `GET /control/status` | v0.107.79, running, protection on |
| `control_stats.json` | `GET /control/stats?recent=86400000` | `time_units` `hours` and 24 hourly entries, oldest first: 33,750 queries, 6,115 blocked by filters and 16 by Safe Browsing (18.2 %). The `num_*` totals match the arrays, as they do when the window is exactly 24 hours |
| `control_filtering_status.json` | `GET /control/filtering/status` | Filtering on; two enabled lists (79,412 and 51,208 rules) and one disabled (402,117 rules, not counted); two user rules |

The tests in `integrations/adguard/adguard_test.go` serve these behind basic auth under a `/adguard` path, and build the other stats answers inline: 168 hourly entries (a 7-day interval on a release that ignores `recent`), 6 (a 6-hour interval, where `recent` is refused with a 400), and 90 daily entries (a 90-day interval on an old release).
