# qBittorrent WebUI API fixture (synthetic)

**Synthetic.** Nothing here was captured from a real qBittorrent. The responses were written from the public [WebUI API documentation](https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-(qBittorrent-5.0)) (sync/maindata, the torrent states, server_state), in the shape qBittorrent 5.1 returns. Torrents are Linux ISOs and the Blender Foundation's open movies; hashes are patterned fakes (`0101…`, `0202…`); paths are made up.

The speeds match the Demo Home at 2026-10-03 14:05 UTC (18.8 Mb/s down, 7.6 up); the counts are smaller.

| File | Endpoint | Notes |
|---|---|---|
| `app_version.txt` | `GET /api/v2/app/version` | `v5.1.2` (plain text) |
| `webapi_version.txt` | `GET /api/v2/app/webapiVersion` | `2.11.4` |
| `maindata_full.json` | `GET /api/v2/sync/maindata?rid=0` | `full_update`, rid 1: 12 torrents (3 leeching, 5 seeding, 2 queued, one stopped, one `missingFiles`), the full `server_state` |
| `maindata_delta.json` | `GET /api/v2/sync/maindata?rid=1` | rid 2, changes only: Debian finished (now `uploading`), one torrent with only `num_seeds` changed, Rocky Linux added whole, the broken torrent removed, and only `dl_info_speed` and `dht_nodes` changed on the server |

The login (`POST /api/v2/auth/login` → `Ok.` with a `QBT_SID_8080` cookie, or `Fails.`) and the 403 for a missing session are played by the fake WebUI in `integrations/qbittorrent/qbittorrent_test.go`, which also answers `{"rid":3}` (nothing changed) after the delta.
