# Jellyfin fixtures (synthetic)

**Synthetic.** Nothing here was captured from a real Jellyfin. The responses were written by hand for the Jellyfin integration's contract tests ([`integrations/jellyfin`](../../../integrations/jellyfin/README.md)), in the shapes of Jellyfin 10.11's API as its OpenAPI description and the generated TypeScript SDK (`SystemInfo`, `SessionInfoDto`, `PlayerStateInfo`, `ItemCounts`) define them. Server, user, device and item ids are patterned fakes (`0000…4000 8000…`); users and devices are made up; addresses are the Demo Home's `10.0.11.x` and the documentation range `198.51.100.x`; the one movie title is a Blender Foundation open movie.

The library counts match the Demo Home's Plex (1240 movies, 318 shows), so the Now playing tile reads the same whichever server feeds it.

| File | Endpoint | Notes |
|---|---|---|
| `system_info.json` | `GET /System/Info` | Version 10.11.2, `ProductName` `Jellyfin Server`, a placeholder server id |
| `sessions.json` | `GET /Sessions?activeWithinSeconds=960` | Three sessions: an episode direct-playing on the LAN, a movie transcoding remotely (paused), and a browser that's open but playing nothing (no `NowPlayingItem`) |
| `items_counts.json` | `GET /Items/Counts` | Every `ItemCounts` field |

Every request carries `Authorization: MediaBrowser Token="<key>"`; the fake server in `integrations/jellyfin/jellyfin_test.go` answers 401 without it, and plays the idle (`[]`), Emby and too-old cases inline.
