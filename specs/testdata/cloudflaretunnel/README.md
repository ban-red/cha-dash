# Cloudflare Tunnel fixtures

**Synthetic.** Responses for `specs/cloudflaretunnel.yaml`, written by hand from Cloudflare's API reference ("List Cloudflare Tunnels", `GET /accounts/{account_id}/cfd_tunnel`, and the tunnel connections call), fetched 2026-11-06. No real account was contacted; the account ID, tunnel and connector UUIDs are made up, and connector addresses are from a documentation range (`198.51.100.x`), which the test checks never reach the graph.

| File | Endpoint | Notes |
|---|---|---|
| `tunnels.json` | `GET /accounts/<id>/cfd_tunnel?is_deleted=false&per_page=50` (`Authorization: Bearer …`) | Four tunnels: healthy (four connections, one connector), degraded, down and inactive |
| `tunnels_ok.json` | The same | One healthy tunnel, with an empty (deprecated) `connections` list |
| `forbidden.json` | The 401/403 | Cloudflare's 10000 "Authentication error" envelope |
| `error.json` | The same, in a 200 | `success: false` |

Not checked against a live account: whether the deprecated `connections` list still comes back filled, and the exact status Cloudflare answers a token without the permission (403 is assumed).
