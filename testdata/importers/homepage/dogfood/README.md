# Homepage importer fixture: dogfood home

The first acceptance test for the Homepage importer. It is derived from the owner's real Homepage config (`services.yaml`, `docker.yaml`, `settings.yaml`) as of 2026-10-03.

## How this fixture was sanitized

The repo is public. Everything except the values below is copied verbatim, including field names, ordering, comments, quirks and container names.

| Original | Fixture |
|---|---|
| Personal domains | `*.lab.example`, `*.family.example` (RFC 2606 reserved names) |
| LAN IPs (private range) | `10.0.11.x` (the last octet is kept, so host identity still lines up) |
| Usernames | `admin` |
| Every secret (API keys, tokens, passwords, PVE token secrets, the HA JWT) | **Fake values in the same format.** UUIDs stay UUIDs, 32-hex keys stay 32-hex, prefixes such as `uk2_` and `ak1_` are kept, and the JWT is still JWT-shaped |
| A password reused for both Traefik and qBittorrent | Reused here too (`FAKE-REUSED-PASSWORD`), so the reused-secret hint can be tested |

The fixture contains no real secret, domain or public IP.

## Expected import result

### Layout
- **Sections**, in the original order, with their column hints. Homepage `columns: N` means N service cards per row; it maps to the section's internal column count.

  | Section | Columns | Notes |
  |---|---|---|
  | Home | 4 | |
  | Servers | 4 | |
  | Services | 5 | |
  | Media | 5 | |
  | Data | 2 | `header: false` → untitled section |
  | Archive/Backup | 4 | |
  | Downloads | 4 | |
  | Apps | 4 | Only in `settings.layout`. Its services come from container labels, so it's created empty and fills in through discovery |

- **`"Data" → ""` (a service with no name)** becomes a chrome-less **media calendar widget**.
  - It binds to the `media.calendar` capability of the imported Sonarr and Radarr instances, so it doesn't duplicate their config.
  - `unmonitored: true` is preserved.
  - `firstDayInWeek: sunday` becomes a locale setting.
- **`quicklaunch`** → ⌘K, which is always on. `searchDescriptions: true` is honored.
- **`title`** → board name ("Alpaca Homepage").
- **`instanceName: pvedocker`** → a hint that this config lives on pvedocker, so the hub probably runs there too. It's used for self-awareness, and confirmed later.
- **Not imported** (each one reported in the import summary):
  - `background` (Cha Dash doesn't put content over a wallpaper)
  - `color`, `statusStyle`, `useEqualHeights`, `headerStyle`, `disableCollapse`, `hideVersion`
  - the placeholder weather `providers`

### Integration instances (18 secrets moved into the vault and replaced with `${secret:…}` refs)

| Homepage entry | Cha Dash instance | Notes |
|---|---|---|
| HomeAssistant | `homeassistant` | Long-lived token |
| Frigate | `frigate` | No auth. `enableRecentEvents` → camera events on |
| OPNSense | `opnsense` | Key and secret. **Hint:** sent over plain `http://` |
| ProxmoxPluto · ProxmoxMiniG5 · ProxmoxMarsCluster · ProxmoxIO · JupiterNas | `proxmox` × **5** | `api@pam!homepage` token on each. Detected as **read-only (PVEAuditor)**; the Inbox offers a control-upgrade recipe per instance. The Mars instance discovers both cluster hosts |
| Traefik | `traefik` | Basic auth. **Hint:** plain `http://`. **Hint:** password reused with qBittorrent |
| UptimeKuma | `uptimekuma` | Status-page slug `current` plus a `uk2_` key |
| Plex | `plex` | Token. `fields` → tile stats. *Live run: expect a 401 → Repair, reconnect via PIN* |
| Radarr · Sonarr · Bazarr · Prowlarr | one instance each | API keys. `fields` → tile stats |
| Tautulli | `tautulli` (spec) | API key |
| Hoarder/Karakeep | `karakeep` (spec) | API key. *Live run: expect a 404 → Repair, API changed* |
| qBittorrent | `qbittorrent` | Username and password. **Hint:** reused password |

### Links and checks (no integration)
- **Jupiter, JupiterNasCockpit:** `siteMonitor` → built-in HTTP checks plus tiles that deep-link to their UIs.
- **Linkding:** a tile and an HTTP check. Its `description` is kept.
- **Linkding and Karakeep run on pvedocker** (confirmed by the owner), but the YAML has no `server`/`container` for them. On a live run, the correlator should link both to their pvedocker containers by itself, using Traefik routes and published ports.

### Docker endpoints and container links
- `my-docker` → socket-proxy endpoint `tcp://dockerproxy:2375`. It's on pvedocker, on the same compose network as Homepage today.
- `my-unraid` → socket-proxy endpoint `tcp://10.0.11.120:2375`, which is jupiter. **The owner confirmed it's a socket-proxy container.**
  - The importer runs a **safe, read-only probe**: does the endpoint behave like a filtering proxy or like a raw Docker API?
  - **Expected: jupiter (`my-unraid`) is a coarse filtering proxy and raises the warning tier:** its container reads are open, so env, logs and files are exposed to the LAN ([spike 4.4](../../../../spikes/docker-proxy/README.md)). `my-docker` is only reachable from pvedocker's compose network, so it's probed from the hub there. A raw API would raise the critical tier: "anyone on the LAN would have root on this host."
  - It never probes write endpoints.
- `server` + `container` pairs become explicit **runs-as relations**. Container names don't match app names, which is exactly why these explicit links matter:

  | Endpoint | Containers |
  |---|---|
  | `my-docker` | `traefik`, `uptimekuma` |
  | `my-unraid` | `Plex-Current`, `radarr`, `sonarr`, `bazarr`, `tautulli`, `binhex-qbittorrentvpn`, `binhex-prowlarr` |

- Later (v0.3), the Inbox suggests **replacing both endpoints with nodes**.

### Services defined only by container labels (not in this YAML)
- **Immich and Mealie** (group Apps) and **Proxmox Backup Server** (group Backup).
- They appear on the rendered page, so they come from `homepage.*` labels on running containers. Mealie's label points at jupiter (`:9925`), and so does PBS (`:8007`).
- The import summary says: "3 services are defined by container labels and will appear through discovery."
- A live run against the socket proxies must produce them, and the Backup group must be created even though it isn't in `settings.layout`.

### Not imported
These are commented out in the YAML, and comments aren't config:
- Mini-Titan PVE
- Overseerr
- NZBGet
- Linkwarden
- the HA `custom` block

### Pass criteria
1. Every active entry lands in a section, as a tile, an integration or a check. **Zero unmapped items.**
2. **No secret value appears** in logs, the import summary, the exported YAML or the browser.
3. The Inbox contains exactly:
   - the read-only PVE upgrade offers
   - the reused-password hint
   - two plain-HTTP credential hints (OPNsense, Traefik)
   - no Docker-endpoint security notice (both endpoints are proxies)

   On a live run it also contains the Plex and Karakeep repairs, plus the 3 label-discovered services.
4. Re-importing the same files gives a **diff with no changes** (idempotent).
