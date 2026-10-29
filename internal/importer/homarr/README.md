# Homarr importer

Imports a [Homarr](https://homarr.dev) 2 board from its backup: the zip from **Management → Tools → Backup**. The zip holds Homarr's whole SQLite database (`db.sqlite`) and a `metadata.json`. The parser copies `db.sqlite` to a temporary file, opens it read-only and immutable (`store.OpenForeign`), and maps one board onto a plan. Nothing else in the zip is read.

- **Which board.** The one with the most items. Other boards are listed under "Won't come across"; import the backup again to bring another.
- **Secrets.** Homarr encrypts every integration secret with its own `SECRET_ENCRYPTION_KEY`, so none can be read. Each connection arrives with its required secret fields missing: add them in the Review sheet's key panel, or later on the connection. Identifiers Homarr keeps in the same encrypted table (a Proxmox token ID, a username) are listed as fields to set in Cha Dash.
- **Limits.** The zip is at most 24 MB (32 MiB as sent, base64-encoded); `db.sqlite` at most 256 MB once unpacked. Homarr's database carries its icon index, so it's bigger than a dashboard's config.
- **Homarr 0.x configs** (`configs/*.json` with `schemaVersion`) aren't imported. Detection recognises them and says to upgrade to Homarr 2, which imports them, then make a backup.
- **Format source.** The tables and columns are Homarr's `packages/db/schema/sqlite.ts`. Item places come from `item_layout` in the widest layout (Homarr with responsive layouts), else from the item's own `sectionId`, `xOffset` and `yOffset` (earlier 1.x). The fixture is synthetic: [`testdata/importers/homarr`](../../../testdata/importers/homarr/README.md).

## Mapping

| Homarr | Cha Dash |
|---|---|
| Board | A board with the same name |
| Section, `kind: category` | A titled section, 12 columns wide |
| Section, `kind: empty` or `dynamic` | An untitled section |
| Item `app` | A service tile with the app's name, icon, description and link. If an integration points at the app (`integration.appId`), the tile reads that connection; otherwise an http check watches the app's `pingUrl`, else its `href` |
| Item `clock` | A clock tile |
| Items `calendar`, `downloads`, `mediaServer`, `dnsHoleSummary`, `smartHome-*`, `mediaRequests-*`, `indexerManager`, `healthMonitoring` | The matching tile, bound to the integrations the item reads (`integration_item`). The calendar binds Sonarr, Radarr and Lidarr through `media.calendar` |
| Items `weather`, `bookmarks`, `notebook`, `iframe`, `video`, `rssFeed`, others | Won't come across, with the reason. An item's own text is never read into the plan |
| Item size (cells) | `large` at 4×4 and up, `tall` at 2×4, `wide` at 2 wide, else `small`, then fitted to what the tile supports |
| Integration | A connection, `url` and `tls` from the integration's URL; integrations no item uses come across too |

| Homarr integration kind | Integration |
|---|---|
| `sonarr`, `radarr`, `lidarr`, `prowlarr`, `bazarr`, `tautulli` | same |
| `plex`, `jellyfin`, `immich`, `mealie`, `nextcloud`, `unraid`, `transmission` | same |
| `jellyseerr`, `overseerr` | `seerr` |
| `piHole` | `pihole` |
| `adGuardHome` | `adguard` |
| `homeAssistant` | `homeassistant` |
| `proxmox` | `proxmox` |
| `qBittorrent` | `qbittorrent` |
| `nzbGet` | `nzbget` |
| `sabNzbd` | `sabnzbd` |
| anything else | Won't come across: "Cha Dash has no integration for it yet" |
