# 05 · Dogfood Home

> The owner's real homelab is Cha Dash's first customer. **v0.1 is done when this home's current Homepage gets replaced, and nobody wants it back.**
> Sources: a rendered snapshot of the owner's Homepage and its config files (`services.yaml`, `docker.yaml`, `settings.yaml`), both from 2026-10-03. A sanitized copy of the config is the importer fixture at [`testdata/importers/homepage/dogfood/`](../../testdata/importers/homepage/dogfood/README.md).
> **Privacy:** this repo is public, so domains, IPs, personal names and all secrets are deliberately left out. Host nicknames are kept.

---

## Inventory

### Hosts and platforms

| Host | Platform | Role | What runs there (per the snapshot) |
|---|---|---|---|
| **pluto** | Proxmox VE | Network core | **OPNsense VM (the router)** and 1 LXC. Memory at **80%** |
| **minig5** | Proxmox VE | Home automation | Home Assistant and Frigate (1 VM + 1 LXC) |
| **mars** | Proxmox VE, **2-host cluster** | Docker | **`pvedocker` LXC**, a Docker host. Runs **Homepage itself**, a `dockerproxy` socket proxy, **Traefik** and **Uptime Kuma**, **Linkding** and **Karakeep** |
| **io** | Proxmox VE | Workstation | Windows gaming VM, Ubuntu VM, 1 more VM (1 of 3 running) |
| **jupiter** | **Unraid** | NAS and main app host | Docker reachable through a **socket-proxy container** on `:2375`. Containers: `Plex-Current`, `radarr`, `sonarr`, `bazarr`, `tautulli`, `binhex-qbittorrentvpn`, `binhex-prowlarr`, plus **Mealie**, **PBS** and very likely **Immich**, all defined by `homepage.*` labels |
| **jupiter-nas** | Proxmox VE | NAS / misc | 1 LXC (Debian, managed through Cockpit), 1 VM (stopped). Memory at **95%** |
| *(mini-titan)* | Proxmox VE | Windows mini PC | Commented out in the config, so presumably offline or retired. Not imported |

That's **5 active Proxmox endpoints** (one of them a 2-host cluster), one Unraid box, and two Docker hosts. **Container placement is confirmed by the config:**
- `docker.yaml` defines `my-docker` (the `dockerproxy` container on pvedocker) and `my-unraid` (jupiter, `:2375`). **Both are socket-proxy containers, not the raw Docker API.**
- Each service names its `server` and `container`.
- Linkding and Karakeep have no container mapping in the config, but **both run on pvedocker**. Discovery should link them by itself, through Traefik routes and published ports. That makes a good correlator test.

**The hub's likely home is pvedocker**, where Homepage runs today (`instanceName: pvedocker`). It's a good spot: it isn't the router host (pluto), and it's already the place that reaches both Docker endpoints.

### Services (grouped the way Homepage groups them)

| Group | Services | What the owner watches |
|---|---|---|
| **Home** | Home Assistant · Frigate | People home (2/2), lights on (1/39), switches on (141/305) · 3 cameras, uptime, version, **recent person detections per camera** |
| **Servers** | OPNsense · 5× PVE · Unraid · Cockpit | CPU, active memory, WAN up/down totals · VMs/LXCs running, CPU, memory · HTTP latency |
| **Services** | Traefik · Uptime Kuma | 61 routers / 56 services / 7 middlewares · 9 sites up, 0 down, 100% uptime |
| **Media** | Plex · Radarr · Sonarr · Bazarr · Tautulli · calendar | Wanted/queued counts · missing subtitles · active streams · upcoming episodes |
| **Apps** | Immich · Mealie | 2 users, ~44k photos, ~2.4k videos, 273 GiB · 87 recipes |
| **Archive** | Linkding · Karakeep | Bookmarks (links only) |
| **Downloads** | qBittorrent · Prowlarr | Leech/seed counts, speeds · grabs, queries, failures |
| **Backup** | Proxmox Backup Server (on jupiter) | **Datastore 92%**, **1 failed task in 24h**, CPU, memory |

---

## What the current page says vs. what it should say

These are real issues from the 2026-10-03 snapshot:

| # | What Homepage shows | The problem | What Cha Dash does |
|---|---|---|---|
| 1 | Plex: a red block with raw `401 Unauthorized` HTML | An error dump, no fix offered | **Repair** in the Inbox: "Plex rejected its token. Reconnect" (Plex PIN sign-in, about 10 seconds). The tile keeps showing last-known state with "as of" |
| 2 | Karakeep: a red block with `404 page not found` on `/api/v1/users/me/stats` | Upstream API changed; the user can't tell why | **Repair**: "Karakeep's API changed (detected vX). Update available for this integration." **A broken integration doesn't mean the service is down**: the service's own HTTP check still shows it as up |
| 3 | PBS: "92%" and "1 failed task" in the same neutral style as "87 recipes" | The most important facts on the page carry no visual weight | Rises to the **Needs attention** section; the status sentence leads with it; the bar shows a threshold and a trend ("full in ~3 weeks") |
| 4 | jupiter-nas memory 95%, pluto memory 80% | Bare percentages with no context, and pluto runs the **router** | Bars with normal-range bands; pluto is tagged *network core* so pressure there ranks higher |
| 5 | Green "RUNNING / HEALTHY" badges on every container | Healthy noise everywhere | Healthy is quiet: no badge. Only stopped-but-should-be-running or unhealthy containers get a mark |
| 6 | Bazarr: "795 missing episodes" | Big number, no context, not urgent | Secondary detail with a trend ("down 40 this week"); never alarming |
| 7 | Five separate PVE links by hostname | No combined view, no idea what runs where, no control | **Fleet card**: 6 PVE hosts across 5 clusters, guests listed per host, power and snapshots (v0.2), exact deep links |
| 8 | No relationships | The page doesn't know OPNsense runs on pluto, PBS runs on jupiter, or that the *arrs depend on qBittorrent and Prowlarr | The Home Graph holds these, which drives blast radius ("Reboot pluto takes down the network") and cause hints |
| 9 | Icons hot-linked from jsDelivr and Iconify | Privacy, CSP, breaks offline | Icons are resolved by name, fetched once and served locally |
| 10 | Frigate detections as plain text rows | No thumbnails, can't act on them | Camera Hero widget: live snapshots, detections with thumbnails, tap for the clip |

### The same moment, in Cha Dash

**Status sentence:**
> ▲ **2 things need you** · Backups: 1 task failed, PBS datastore 92% · jupiter-nas memory 95%   ○ 30 services healthy · 3 cameras · 2 people home

**Inbox badge:** `2 repairs` (Plex token, Karakeep integration). These are integration problems, not outages, so they don't shout in the sentence.

```
┌ Briefing ─────────────────────────────────────────────────────────────────────────────┐
│ ▲ 2 things need you · Backups: 1 failed, PBS 92% · jupiter-nas memory 95%   Inbox (2) │
│ ○ 30 services healthy · 3 cameras · 2 people home · Next up: Black Clover 04:00        │
└───────────────────────────────────────────────────────────────────────────────────────┘
┌ Needs attention (only exists when non-empty) ─────────────────────────────────────────┐
│ PBS · datastore 92% ▲ full in ~3w · 1 failed task  │ jupiter-nas · mem 95% ▲ ▁▂▃▅▇ │
└───────────────────────────────────────────────────────────────────────────────────────┘
┌ Home ───────────────────────────┐ ┌ Infrastructure ────────────────────────────────────┐
│ Frigate · 3 cameras  [Hero 8×4] │ │ Fleet · 6 PVE hosts · Unraid · 2 Docker   [Large]   │
│ side deck · person · 19:14      │ │ OPNsense · WAN ↓852 GB ↑418 GB · CPU 20%  [Wide]    │
│ Home Assistant · 2 home · 1 lit │ │ Traefik · 61 routes   Uptime Kuma · 9/9   [Small×2] │
└─────────────────────────────────┘ └────────────────────────────────────────────────────┘
┌ Media ─────────────────────────────────────────┐ ┌ Apps ──────────────────────────────┐
│ Now playing · no streams   Calendar · 2 today   │ │ Immich · 44.1k photos · 273 GiB    │
│ Sonarr · 13 queued   Radarr · 10 wanted         │ │ Mealie · 87   Linkding   Karakeep  │
│ qBittorrent · ↓0 ↑0 · 20/78   Prowlarr · 1,116  │ └────────────────────────────────────┘
└─────────────────────────────────────────────────┘
```

---

## What this means for the plan

### v0.1 integration set (derived from this home)

| Tier 0 (Go) | Tier 1 (internal specs) | Launch breadth (not in this home, but popular) |
|---|---|---|
| Proxmox VE (**multi-instance + cluster**), PBS, Unraid, Docker (socket-proxy, local + remote TCP), **Traefik** (routes → discovery), **OPNsense**, Home Assistant, **Frigate**, Plex (+ PIN auth), Sonarr, Radarr, Prowlarr, qBittorrent, Immich, Uptime Kuma, built-in HTTP/TCP/ICMP checks | Tautulli, Bazarr, Mealie, Linkding, Karakeep | Jellyfin, Pi-hole, AdGuard, SABnzbd (as specs where possible) |

Cockpit and the native PVE and Unraid UIs are **deep links**, not integrations.

### Specific requirements this home adds
- **Multiple PVE instances are the normal case**, not an edge case. Fleet aggregation is part of the v0.1 Proxmox widget.
- **Network-core awareness.** The router is a VM, so the graph needs `provides_network_for`. Blast radius has to warn that actions on pluto take down the network, *including your connection to Cha Dash*.
- **PBS runs as a container on Unraid.** The graph must link backup target → container → array, so an Unraid outage shows up as a backup risk.
- **Docker hosts include a Proxmox LXC** (pvedocker). Nodes must run happily inside LXCs.
- **Unraid stays Unraid.** Its containers stay managed by Unraid and are controlled through the Unraid API. Owned stacks target compose hosts like pvedocker. An optional **thin node on jupiter** (v0.3+) could add host metrics, logs, SMART and update checks alongside the API, without taking over the containers.
- **Traefik with subpath routing** (e.g. `/sonarr` on the Unraid host's domain). The correlator must handle path-based routers, not just host-based ones.
- **Frigate is a home essential**, so it gets a first-class camera widget in v0.1.

### What the config itself reveals
- **18 credentials sit in plain YAML**: PVE token secrets, the OPNsense key and secret, a Home Assistant token, *arr keys, Plex, Uptime Kuma, Karakeep and qBittorrent. That's the "dashboard as a vault of every API key" problem from the research, in one file. The importer moves all of them into the encrypted vault.
- **The owner wrote a 6-step PVE least-privilege recipe as YAML comments** (group → PVEAuditor → user → privilege-separated token → token permission). That's direct evidence for the **token recipe generator**: Cha Dash should produce exactly this, per instance, with a copy button. It should also offer the step up to control privileges later.
- **All five PVE tokens are read-only (PVEAuditor).** Moving to control in v0.2 means one deliberate upgrade per instance, shown as an Inbox offer and never assumed.
- **Container names don't match app names** (`Plex-Current`, `binhex-qbittorrentvpn`, `binhex-prowlarr`). Name matching alone would fail. Explicit `server` + `container` links, port/URL correlation and Traefik upstreams all matter.
- **Some services exist only as container labels** (Mealie, PBS, probably Immich), and the "Backup" group isn't even in `settings.layout`. The importer has to read live labels, not just YAML.
- **The calendar widget references its sources by `service_group` / `service_name` strings.** That coupling breaks silently when you rename something. In Cha Dash the calendar binds to the `media.calendar` capability of whichever *arr instances exist.
- **The Home Assistant `custom` block** (commented out) shows intent: **total power, energy today, switches on (a template), wind speed**. So the v0.1 HA widget should support picked entities *and* template-rendered stats via HA's template API. The power and energy interest also supports the "power & cost" moonshot.
- **Credential hygiene the hints should catch, without ever showing values:**
  - one password reused for the Traefik dashboard and qBittorrent;
  - credentials sent over plain `http://` (the OPNsense API and the Traefik dashboard URL);
  - **jupiter's socket proxy is too open.** [Spike 4.4](../../spikes/docker-proxy/README.md) found a coarse filtering proxy on `:2375`, published to the whole LAN: it allows every read under `/containers/`, so anyone on the network can read each container's environment (58 secret-looking keys across 29 containers), plus logs and files inside containers. The probe should raise the **warning** tier. pvedocker's proxy still needs probing from the hub's own host.
- **Weather provider keys are placeholders** and there's no `widgets.yaml`, so clock and weather start fresh.

### Importer acceptance test
This is now a real fixture: [`testdata/importers/homepage/dogfood/`](../../testdata/importers/homepage/dogfood/README.md). It's a sanitized copy of this home's config with fake secrets in the same formats, plus the full expected import result and pass criteria. In short:
- zero unmapped entries;
- 18 secrets moved into the vault, and none of them appear in logs or exports;
- explicit container links preserved;
- label-only services found through discovery;
- the hygiene hints above raised;
- re-importing produces no changes.

### What the Proxmox spike found (4.1, read-only)
[Spike 4.1](../../spikes/pve/README.md) read all five PVE endpoints with the read-only tokens. The fleet is **mixed 8.4 and 9.x**, so the integration needs version adapters from day one. The data shows problems the current page hides:
- **PBS outages hit two nodes on the same nights.** pluto's and mars's backup jobs both failed on 12 of the last 21 nights with "no route to host" to PBS on jupiter. minig5's weekly run failed the same way.
- **io's backup has failed every recorded night.** Two VMs pass through the same GPU. The job also targets the node's own disk, and one VM is in no job at all.
- **jupiter-nas's VM is in `internal-error`, not stopped,** and holds 21 GB of the host's 33.5 GB: that's the 95% memory. jupiter-nas has no backup job.
- **minig5 backs up weekly only,** the Frigate LXC isn't covered, and `apt-get update` fails.
- **mars runs HA on Ceph with two nodes and no QDevice,** so it can't fail over if a node dies.

These are exactly the Inbox's job, and they belong in the Demo Home's script next.

### Demo Home simulator
Seed it from **the shape of this home**: 5 PVE endpoints, one of them a cluster; Unraid with PBS in a container; a Docker LXC; a router VM; HA and Frigate. Include the incidents above as scripted scenarios:
- Plex token revoked
- Karakeep API change
- PBS filling up
- a failed backup task
- memory pressure on a host
