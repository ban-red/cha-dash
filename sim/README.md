# Demo Home simulator (`sim/`)

A deterministic stand-in for a real home: a busy, fictional family home whose resources, metrics and incidents are shaped like what the hub's integrations really publish. It isn't any real lab. It powers:
- the M0 demo: a live front page with no lab attached;
- the public demo, later;
- the JSON snapshots in [`snapshots/`](snapshots/) that design prototypes read.

It is `package sim`, standard library only. Its types follow the Home Graph vocabulary in [03 · Architecture](../docs/plan/03-architecture.md#the-home-graph-core-data-model): resources, relations, health, blast radius, cause hints and backup coverage. A later wave can map its event stream onto the hub's `internal/graph` with a thin adapter. It doesn't depend on the hub.

## Quick start

```go
s, err := sim.New(1)                // seed 1, default script, clock at 2026-10-03 00:00 UTC
st := s.Snapshot()                  // the full state, sorted, with 24 h of sparkline history
ev := s.Advance(30 * time.Second)   // every change in (now, now+30s], in order
ev, err = s.Fix("plex-token-revoked") // press an open incident's suggested action
```

| API | What it does |
|---|---|
| `New(seed, opts...) (*Sim, error)` | Builds the home. Options: `WithStart(t)`, `WithScript(s)`, `WithSampleInterval(d)` (default 30s), `WithHistory(points, step)` (default 48 × 30m) |
| `Snapshot() State` | Resources, relations, metric series (value, band, thresholds, history) and every incident opened so far |
| `Advance(d) []Event` / `AdvanceTo(t)` | Moves the clock and returns the events. The result doesn't depend on step size |
| `Skip(d)` | Moves the clock without producing events. The state matches `Advance`; take a new `Snapshot` afterwards |
| `Fix(id) ([]Event, error)` | Applies an open incident's suggested action now. It replaces any later scripted fix |
| `CanFix(id) error` | Whether `Fix` would accept the incident now: nil, or the error `Fix` would return. Changes nothing |
| `BlastRadius(id) []Impact` | What an action on a resource would affect. Use it for the hold-to-confirm list. A package-level `BlastRadius(rels, id)` works on any relation list |
| `Detections(camera, from, to)` | Frigate events for the camera hero widget (`front_door`, `driveway`, `garden`, `garage`) |
| `Record(span, step) Timeline` | Advances and returns a compact columnar recording |
| `EncodeState`, `EncodeTimeline` | Stable, diff-friendly JSON: sorted keys, sorted items, one item per line |
| `DefaultScript()`, `Script.Validate()` | The default incident script, and validation for custom ones (JSON-friendly: offsets are duration strings) |

A `Sim` is safe for concurrent use. Snapshots and events are deep copies, so changing them never changes the simulation.

## The home

Four people live here: two adults and two kids. The default day is a Saturday: the kids are at football from 09:30 to 12:00, and one adult is out for groceries from 10:30 to 12:45, so `people_home` goes 4 → 2 → 1 → 3 → 4. Home Assistant publishes two calendars, Family and School (`sim/calendar.go`), with two weeks of invented events that start with that Saturday. The house has 52 lights, 360 switches and four cameras.

The hosts are named after birds. Everything is invented: names, VMIDs, sizes, versions, addresses and the stories.

## Topology

165 resources and 246 relations. Addresses are `10.20.30.x` and `*.home.example` only, plus `open-meteo.com`, the weather source the weather integration credits. A test enforces this.

```
internet ── net:opnsense/wan ── net:opnsense (router · provides_network_for every host and camera)
                                  ├ runs_on pve:wren/qemu/100
                                  └ depends_on app:adguard (the home's DNS)

pve:wren            PVE 9.1.4 · network core · 16 GB    qemu/100 opnsense · lxc/101 adguard ─ app:adguard · lxc/102 tailscale
pve:aviary          cluster of 3, quorate
 ├ pve:heron        PVE 9.0.11 · home · 32 GB           qemu/200 homeassistant ─ app:homeassistant ──depends_on──▶ app:frigate
 │                                                        app:homeassistant ──exposes──▶ calendar family · school
 │                                                      lxc/201 frigate ─ app:frigate ──exposes──▶ camera front_door · driveway · garden · garage
 ├ pve:egret        PVE 9.1.4 · 32 GB                   lxc/210 rook ─ docker:rook (filtering socket proxy)
 │                                                        chadash (this hub, self: true) · homepage · dockerproxy · traefik · uptimekuma · linkding
 │                                                        karakeep-web-1 ─depends_on─▶ karakeep-chrome-1, karakeep-meilisearch-1
 │                                                        paperless-ngx ─depends_on─▶ paperless-redis · nextcloud ─depends_on─▶ nextcloud-db
 │                                                        seerr · speedtest-tracker
 └ pve:stork        PVE 9.1.4 · files · 8 GB, no swap   lxc/220 files (Samba) ─ app:cockpit · qemu/221 sandbox (off, not backed up)
pve:owl             PVE 9.2.3 · workstation · 64 GB     qemu/301 win-games (off) · qemu/302 studio (started by hand) · qemu/303 homework (off)
                                                        win-games and studio pass through the same GPU (hostpci 0000:03:00)
unraid:magpie       Unraid 7.2.4 · 64 GB
 ├ array            84 TB: parity + disk1–6 ─ shares media, photos, books (parity, disk4–6 spin down when idle)
 ├ pool cache       2 TB NVMe ─ share appdata (container appdata), docker.img
 ├ pool backups     2 TB ─ pbs:main (the PBS datastore)
 └ docker:magpie    socket proxy :2375, a broad one · depends_on the array
                    plex · jellyfin · tautulli · sonarr · radarr · bazarr · prowlarr · qbittorrent · sabnzbd
                    mealie · immich · audiobookshelf · pbs ─ app:pbs ◀─part_of─ pbs:main · socket-proxy
                    mealie, immich and pbs carry homepage.* labels (label discovery)

pve:<instance>/backup/daily   one nightly job per PVE endpoint (wren 02:00, aviary 02:15, owl 02:30; done by 03:01) ──stores_on──▶ pbs:main (PVE storage pbs-magpie)
10 guests ──backed_up_by──▶ their instance's job (all but sandbox)
traefik:route/*               23 routes (host- and path-based) ──routes_to──▶ apps, part_of app:traefik
check:checks/*                2 built-in checks: Magpie Unraid (its web UI) and Linkding ──monitors──▶ app:linkding
weather:demo/here             Greenwich, from the weather instance "demo" (Open-Meteo)
integration:*                 33 integration instances ──monitors──▶ what they read
```

| Kind | Count | | Kind | Count |
|---|---|---|---|---|
| integration | 33 | | storage (PVE) | 5 |
| container | 29 | | lxc | 5 |
| app | 26 | | camera, share | 4 each |
| route | 23 | | backup_job | 3 |
| disk | 7 | | engine, pool, check | 2 each |
| host (5 PVE + Unraid) | 6 | | cluster, array, router, wan, backup_target, weather | 1 each |
| vm | 6 | | | |

**Integration instances:** `proxmox-wren`, `proxmox-aviary`, `proxmox-owl`, `unraid-magpie`, `docker-rook`, `docker-magpie`, `opnsense`, `adguard`, `pbs`, `homeassistant`, `frigate`, `traefik`, `uptimekuma`, `plex`, `jellyfin`, `tautulli`, `sonarr`, `radarr`, `bazarr`, `seerr`, `audiobookshelf`, `prowlarr`, `qbittorrent`, `sabnzbd`, `karakeep`, `linkding`, `paperlessngx`, `immich`, `mealie`, `nextcloud`, `speedtesttracker`, `checks` and `demo` (the weather).

**IDs** are namespaced and stable: `pve:heron/lxc/201`, `docker:magpie/sonarr`, `unraid:magpie/disk/disk1`, `pbs:main`, `app:plex`, `integration:plex`, `traefik:route/sonarr`, `frigate:camera/driveway`, `net:opnsense`, `check:checks/linkding`, `weather:demo/here`. Containers are keyed by engine and name. The Docker container ID is the `container_id` attr, so a recreate keeps the resource ID and changes the attr. They predate the hub's `<integration>:<instance id>/<path>` convention and stay, so stored layouts keep working; widgets find a service by `attrs.type` either way.

**Attrs follow what the v0.1 integrations publish:**
- every `app` has `attrs.type`, the ID of the integration that reads it (`app:sonarr` is a `sonarr`); Cockpit and Cha Dash itself, which no integration reads, carry their own name;
- PVE hosts have `node` and `pve_version`, aviary's nodes `cluster`, the cluster `platform`, `cluster`, `nodes` and `quorate`; integration instances have the version they read in `upstream_version`;
- guests have `hostpci` for passed-through devices, and `backup.last_error` beside `backup.last_*` when their last backup failed; jobs have `last_run.error` and `last_ok`;
- engines have the probe's `access` (`broad-proxy` on magpie, `filtered-proxy` on rook), and `host`/`ip`;
- a container with Homepage labels has the ones the Docker integration keeps (`homepage.group`, `.name`, `.href`, `.icon`, `.description`, `.widget.type`, `.widget.url`; never a secret), and its `homepage.href` as a link;
- AdGuard Home has `running`, `protection_enabled`, `filtering_enabled`, `filters` and `stats_hours`; Jellyfin and Plex `movies` and `shows`; Nextcloud `php_version`, `database` and `users`; Paperless-ngx `tags`, `correspondents` and `document_types`; Linkding `archived` and `tags`;
- the weather resource has the weather integration's exact shape (`integrations/weather/README.md`): `location`, `observed_at`, `code`/`condition`/`summary`, `is_day`, `high_c`/`low_c`/`precip_pct`, `sunrise`/`sunset`, wind, `hourly` (6) and `daily` (5), and the Open-Meteo attribution. Its times carry Greenwich's offset (BST or GMT, worked out without a time zone database);
- checks have `check`, `target` and `status_code`, a link and `caps: ["checks.uptime"]`.

**Relations** read `From <type> To` and use the architecture doc's names. Here is how the brief's names map onto them:

| Brief | Relation | Example |
|---|---|---|
| runs-on | `runs_on` | `pve:heron/qemu/200 runs_on pve:heron`; `app:sonarr runs_on docker:magpie/sonarr` |
| stores-on | `stores_on` | `pbs:main stores_on unraid:magpie/pool/backups`; `app:audiobookshelf stores_on …/share/books` |
| backs-up | `backed_up_by` (inverse) | `pve:heron/qemu/200 backed_up_by pve:aviary/backup/daily` |
| routes-through | `provides_network_for` (inverse) | `net:opnsense provides_network_for pve:wren` |
| depends-on | `depends_on` | `app:sonarr depends_on app:qbittorrent`; `app:seerr depends_on app:plex`; `net:opnsense depends_on app:adguard` |
| exposed-by | `routes_to` (inverse) | `traefik:route/sonarr routes_to app:sonarr` |
| — | `part_of` | `pve:heron part_of pve:aviary`; `unraid:magpie/disk/disk1 part_of …/array` |
| — | `exposes` | `app:frigate exposes frigate:camera/driveway` |
| — | `monitors` | `integration:plex monitors app:plex`; `check:checks/linkding monitors app:linkding` |

**Blast radius** (`BlastRadius`) walks:
- dependents transitively over `runs_on`, `part_of`, `depends_on` and `stores_on`;
- dependents one step over `backed_up_by` and `routes_to`, so a guest loses its backups and a route breaks, but nothing further goes down;
- forwards over `provides_network_for` and `exposes`;
- one step forwards over `monitors`, so a broken integration makes what it reads stale.

Rebooting wren reaches the whole home, including Cha Dash itself; so does stopping AdGuard Home, since nothing resolves without it. Stopping magpie's array reaches Docker, PBS and the 10 backed-up guests' coverage, but not the guests themselves. qBittorrent going down reaches Sonarr, Radarr, Bazarr and Seerr, not Plex.

## Metrics

159 series. Every series carries `unit`, `min`/`max` (the scale), `band` (the normal range, drawn as a gray band), optional `warn`/`crit` thresholds, `value`, `as_of`, `stale` and `history`. Every history in a snapshot shares one axis (`State.History`). By default that's 48 points 30 minutes apart, ending at the last sample tick.

| Resource | Metrics |
|---|---|
| hosts | `cpu.pct`, `mem.pct` (warn 90, crit 98). No `temp.c`: neither the PVE nor the Unraid API reports one |
| running guests | `cpu.pct`, `mem.pct` |
| containers | `cpu.pct`, `mem.mib`, on `qbittorrent` and `pbs` only |
| storage, array, disks, pools, datastore | `used.pct`; disks and pools also `temp.c` (a disk's is unknown while it's spun down) |
| router · WAN | `cpu.pct`, `mem.mib` · `rx.mbps`, `tx.mbps`, `rx.gb_30d`, `tx.gb_30d` |
| apps | `http.ms` (the hub's own check), plus app stats: HA `power.kw`, `energy.kwh`, `people_home`, `lights_on`, `switches_on`; AdGuard `queries.24h`, `blocked.24h`, `blocked.pct`, `blocklist`; Plex `streams`; Jellyfin `streams`, `streams.transcode`; Audiobookshelf `streams`, `books`, `podcasts`; Seerr `requests.pending`, `requests.processing`, `requests.available`; Sonarr `queue`, `wanted`; Radarr `wanted`, `queue`; Bazarr `wanted`, `missing.episodes`, `missing.movies`; Prowlarr `grabs`, `queries.24h`, `failures.24h`; qBittorrent `dl.mbps`, `ul.mbps`, `leeching`, `seeding`, `queued`; SABnzbd `dl.mbps`, `queue`, `left.gib`; Immich `photos`, `videos`, `storage.gib`; Karakeep and Linkding `bookmarks`; Paperless-ngx `documents`, `inbox`; Nextcloud `users`, `users.active`, `files`, `free.gib`; Speedtest Tracker `dl.mbps`, `ul.mbps`, `ping.ms`; Uptime Kuma `monitors.up`, `monitors.down`; PBS `tasks.failed_24h`; Frigate `inference.ms` |
| checks | `http.ms` |
| cameras | `detections.24h` |
| weather | `temp.c`, `feels.c`, `humidity.pct`, `wind.kmh`, `precip.mm` (the hour's reading; `temp.c` and `feels.c` are banded by today's low and high) |

The keys are the integrations' own (their READMEs and specs): Radarr's queue is `queue` like Sonarr's, Bazarr publishes `wanted` (the sum of what's missing) for the *arr tile, qBittorrent adds `queued`, AdGuard Home uses Pi-hole's keys. Every app gives the Service tile (`web/src/widgets/serviceStats.ts`) the stats it reads.

Container stats aren't collected by the Docker integration yet (`integrations/docker`). The Demo Home keeps them only on `qbittorrent`, whose OOM story reads them, and `pbs`, whose load follows the nightly backups and the prune.

The curves are a base, a diurnal swing (evening media, the kids' shows on Jellyfin in the late afternoon, daytime Frigate load, morning and evening house power), slow value noise and a little jitter. Routine windows add to them: nightly backups raise magpie's CPU and PBS's load between 02:00 and 03:05, SABnzbd works through its queue at night, the mover warms the disks it writes to. Speedtest Tracker runs a test every 30 minutes, at :10 and :40; its series hold the last result. Incidents reshape specific series on top of that.

**Honest data:**
- A metric that **can't be read** (its resource isn't up, or its integration isn't connected) isn't sampled. It keeps its last-known `value` with `stale: true` and an `as_of`. Stopped guests have no metrics; while Cha Dash can't read Unraid's API, everything magpie's Unraid integration reports goes stale; while Nextcloud answers 503, its stats do.
- A metric the source **reads as unknown** is sampled as `null`, never 0: a spun-down disk's `temp.c` (Unraid reports none, and reading one would wake the disk). It isn't stale: `null` is what was read, as of then.
- During a repair, the app's stats go stale, while the app's own `http.ms` check stays live, as does the Unraid web UI's check. A broken integration is not an outage.

## Incidents

### The default script (day one, a Saturday, times UTC)

Every incident is open at **14:05** (`DefaultAllOpen`), the moment the prototype depicts. From the afternoon they're fixed, or clear, one by one. By 20:02 only the standing hints and the two backup failures that clear with the next night's runs are left.

The findings use the IDs, scenarios, facts and wording the v0.1 integrations use (`<instance id>.<local id>`; [`findings.go`](findings.go)). The simulator's own stories use the script scenario as their ID.

| Incident `id` (script scenario) | Opens | What happens | Class · severity | Suggested action (tier) | Resolves |
|---|---|---|---|---|---|
| `traefik.hint.api_no_auth` (`traefik-dashboard-no-auth`) | standing | Traefik's dashboard is routed without an auth middleware (scenario `credential-hygiene`) | problem · **info** | Open the integration's settings (T0) | Not in the day |
| `docker-magpie.hint.docker_endpoint` (`docker-proxy-broad`) | standing | magpie's socket proxy lets every GET under `/containers/` through (`credential-hygiene`, `access: broad-proxy`) | problem · **info** | Open the integration's settings (T0) | Not in the day |
| `frigate.unauthenticated-admin` (`frigate-anonymous-admin`) | standing | Frigate answers port 55000 on 10.20.30.41, its internal port published as is, as an anonymous admin (`frigate-unauthenticated-admin`) | problem · **info** | Use the authenticated port (T0) | Not in the day |
| `adguard.hint.plain_http` (`adguard-plain-http`) | standing | Cha Dash reads AdGuard Home's admin API over plain http://, so the password crosses the LAN in clear (`credential-hygiene`, `transport: http`) | problem · **info** | Open the integration's settings (T0) | Not in the day |
| `proxmox-wren.backup.daily` (`network-backup-unreachable`) | 02:00 | wren's run can't reach PBS (`could not activate storage 'pbs-magpie' … No route to host`) and stops before any guest (`backup-target-unreachable`, `facts.endpoint`, `facts.reason`) | problem · attention | Open the backup jobs in Proxmox (T0) | 02:05 the next night: its next run succeeds (`auto`) |
| `backup-failed` | 02:19 | homeassistant's backup, inside aviary's 02:15 run, fails: the QEMU guest agent doesn't answer fs-freeze | problem · attention | Back up homeassistant now (**T1**) | 15:34: the retry started at 15:30 succeeds. Unfixed, the next nightly run resolves it (`auto`) |
| `pbs-datastore-filling` | 02:44 | studio's backup (02:30–02:52) writes 108 GB and the datastore crosses 90%, then reaches 92%: "full in about 3 weeks". The prune job has been disabled since 14 Sep | problem · attention | Prune per policy (**T2**; alternatives: re-enable prune job T1, open PBS T0) | 16:22: prune at 16:00, garbage collection until 16:22, back to 86% (below 88%) |
| `proxmox-owl.backup.daily` (`workstation-backup-passthrough`) | 02:55, the run's end (opened 02:30) | win-games's backup has to start it, but studio (started by hand yesterday 21:15) holds the GPU both pass through (`backup-passthrough-conflict`, `facts.pci_device`, `facts.in_use_by`) | problem · attention | Open the backup jobs in Proxmox (T0) | studio is shut down at 23:05; 03:01 the next night (`auto`) |
| `nextcloud-maintenance` | 07:10 | An update recreates nextcloud with 32.0.1; the upgrade finishes at 07:12, but maintenance mode stays on and its check answers 503. Nextcloud's stats go stale; Uptime Kuma counts it down | problem · **down** | Turn off maintenance mode (**T1**; undo turns it back on) | 15:10:30: turned off at 15:10, answering normally 30 s later. Unfixed, it stays open |
| `karakeep-api-changed` | 09:05 | karakeep-web-1 is recreated with 0.31.0 at 09:02; the stats endpoint 404s. Karakeep itself stays up (`api-changed`, `facts.code: api_changed`) | **repair** · attention | Update the Karakeep integration (**T1**; alternative: roll back to 0.30.2 T2) | 19:00:30 |
| `speedtest-slow` | 09:40 | Speedtest Tracker's runs measure about 180 of the usual 930 Mb/s down; OPNsense's WAN tops out at the same speed. Speedtest Tracker's health is attention | problem · attention | Open Speedtest Tracker (T0) | 16:10: the provider fixes it, and that run measures the usual speed (`auto`) |
| `proxmox-aviary.guest.221` (`sandbox-internal-error`) | 11:20 | sandbox on stork, started at 11:19, is in internal-error (`guest-error`) | problem · **down** | Open sandbox in Proxmox (T0) | 18:05: stopped by its owner (`auto`) |
| `unraid-magpie.repair` (`unraid-api-unsupported`) | 12:05 | Unraid's API is 4.37.2, older than the 4.37.4 the integration requires (`api-changed`, `facts.code: unsupported_version`). magpie's Unraid data stays as of 12:05, stale | **repair** · attention | Open the integration (T0) | 16:45: the API is updated at 16:40, and the next retry (every 5 minutes) reads it (`auto`) |
| `plex-token-revoked` | 13:27 | Plex answers 401. Plex itself stays up; its stats go stale (`auth-rejected`, `facts.code: auth`, `facts.field: token`) | **repair** · attention | Reconnect Plex with a PIN (**T0**) | 18:00:10 |
| `host-memory-pressure` | 13:30 | files grows from 0.5 to 3.1 GB from 11:40. stork (8 GB, no swap) crosses 90% at 13:30 and plateaus at about 95% from 14:00 | problem · attention | Restart files (**T2**) | 17:10: restart at 17:00 (20 s down), memory settles, below 85% for 10 min |
| `qbittorrent-oom` | 14:03 | Memory climbs from 1.1 to 4 GiB from 13:38. OOM-kill (exit 137), Docker restarts it at 14:03:20, VPN up at 14:07. Sonarr and Radarr can't reach it meanwhile; Uptime Kuma shows 12/14 | problem · **down** → attention → info | Raise the memory limit to 6 GiB (**T2**; Unraid removes and recreates the container) | 20:02: fix at 20:00, recreated at 20:00:15, healthy at 20:02. Unfixed, it auto-resolves 24 h after the kill |

**The PBS outage, seen as one problem.** One instance can't see another's incidents, so on a real hub the correlator links them ([`internal/correlate`](../internal/correlate/README.md), "Incidents across instances"). The simulator adds the same causes: every open PVE backup failure (wren's, owl's and homeassistant's) gets, in front of its own causes, "It writes to PBS datastore main on Proxmox Backup Server, which has a problem of its own: PBS datastore is filling up", then one cause per other failing host, up to three. They change as the failures open and resolve, and the PBS cause goes when PBS is pruned. Resolved incidents keep only their own causes.

**Hints** are quiet problems (severity `info`), as on a real hub: no causes, no blast radius, the framework's "Open the integration's settings" where the integration names no action. They stand: the default script opens them nine days before the day (`HintsOpen`), when the instances first read the home, so they're in `healthy.json` too. `Fix` resolves one, as if the setup had changed.

**Repairs** carry the hub's facts: `code`, `instance`, and `field` and `status` where they apply, so the Inbox's Reconnect opens the right settings.

**Backups the hub can only report** suggest opening Proxmox, as v0.1's read-only Proxmox integration does; they clear with the job's next run. `Fix` on one is a run started by hand (on owl, after shutting studio down).

The simulator's own stories keep their own actions, which show the safety ladder (T1, T2): the backup retry, the prune, the restart, the new memory limit and turning Nextcloud's maintenance mode off. `host-memory-pressure` and `qbittorrent-oom` have no v0.1 counterpart: the Proxmox integration shows host memory as the host's health, and Docker an OOM kill as the container's.

### What an incident carries

| Field | Meaning |
|---|---|
| `class` | `problem` (Needs attention, status sentence, unless it's `info`) or `repair` (Inbox only; an integration issue, not an outage) |
| `scenario` | What kind of incident it is, as the hub names it (`backup-target-unreachable`, `auth-rejected`, `credential-hygiene`, …). For the simulator's own stories it's the script scenario |
| `severity`, `phase`, `status` | `info` / `attention` / `down`; the current phase (`open`, `rising`, `high`, `pruning`, `restarting`, `turning-off`, `recovered`, …, and `resolved` once resolved); `open` / `resolved` |
| `category` | `security` on the standing hints (left out otherwise). The severity here is the declared one; the hub publishes a security finding at the severity its security posture gives it (`internal/security`) |
| `title`, `summary` | Copy in the 02-experience voice: symptom first, specific times, calm |
| `target` | The primary resource |
| `facts` | Numbers and keys the copy quotes (`used_pct: 92`, `full_in_days: 21`, `status: 401`, `endpoint: 10.20.30.20:8007`, …) |
| `causes` | Root-cause hints: a text, the resource it points at and when it changed. Always a list, possibly empty (hints) |
| `blast_radius` | What it affects: `BlastRadius(target)`, or what the integration reports (a job's guests, a repair's resources). Always a list, possibly empty |
| `suggested`, `alternatives` | Actions with `id`, `label`, safety `tier`, `target` and `detail`. T2 and up carry their own `impact` list for hold-to-confirm |
| `resolves_when`; `resolved_by`, `resolution` | How it will resolve; once resolved, `action` or `auto` plus what happened |

### Custom scripts

A `Script` is a list of `{scenario, id?, open, fix?}`, with offsets from the start as Go duration strings in JSON (negative for something already standing). Leave `fix` out and the incident stays open until `Sim.Fix`, or until it auto-resolves (backup failures, OOM). For `speedtest-slow` and `sandbox-internal-error` the fix is what happens outside Cha Dash (the provider repairs the line, the owner stops the VM), so they resolve `auto`. Scenarios can repeat with distinct IDs as long as they don't overlap. `Script.Validate` checks all of this. `WithScript(nil)` gives a quiet home, without hints.

## Event model

`Advance` returns events in time order. Every event has a `seq` (incrementing by one per event, per `Sim`), an `at` and a `type`:

| Type | Payload | When |
|---|---|---|
| `resource.upsert` | `resource`: the full resource | A resource appears or any field changes (state, health, attrs) |
| `resource.remove` | `id` | A resource disappears (qBittorrent's container while Unraid recreates it) |
| `relation.upsert` / `relation.remove` | `relation` | An edge appears or disappears (with the resource it touches) |
| `incident.open` / `incident.update` / `incident.resolve` | `incident`: the full incident | It opens, its phase, copy or causes change, it resolves |
| `metrics` | `samples`: `[{resource, metric, value}]` | Every sample interval (default 30 s, aligned to the Unix epoch). Readable series only; `value` is `null` for one read as unknown |

**Ordering at one instant:** relation removals, resource removals, resource upserts, relation upserts, incident events, then the metrics batch. A consumer applying events one by one never holds a relation to a missing resource. `TestEventReplay` replays a whole day onto the starting snapshot, checks this after every event, and lands exactly on the final snapshot.

**Contract with the hub's realtime protocol:** `State.Seq` is the last event before the snapshot. That maps directly onto `snap` (with `seq`) followed by `diff`s. Resources carry only facts that change at discrete moments. Anything continuous is a metric, so upserts stay rare: 110 in the default day, against 2,880 metrics batches.

**Determinism:** the world is a pure function of (seed, script, fixes, time):
- Randomness is counter-based hashing (SplitMix64 over seed, stream key and counter), not a stateful generator.
- Non-metric state changes only at declared change points: script phases, backup runs, disks spinning, weather refreshes and camera detections. `Advance` evaluates exactly those plus the sample ticks.

So the same seed gives byte-identical snapshots, and `Advance(24h)` equals any split into smaller steps (`TestStepSizeDoesNotMatter`). A different seed changes noise, uptimes, container IDs, camera detections, speed test results and the weather, but not the topology or the script.

## Snapshots

Generated by a golden test from seed 1:

| File | Moment | Size |
|---|---|---|
| [`healthy.json`](snapshots/healthy.json) | t0, 2026-10-03 00:00: nothing needs you (the standing hints are open), with the previous 24 h as history | 142 KB |
| [`incidents.json`](snapshots/incidents.json) | 14:05: every incident open | 166 KB |
| [`timeline.json`](snapshots/timeline.json) | 00:00 → 24:00: 159 series × 49 frames (30 min, the history's grid), every incident, and all 156 non-metric events. Replay the events on `healthy.json` | 224 KB |

`Record` skips metrics events, so in `timeline.json` the `seq` values count only the non-metric events. The total budget is 1 MiB, and the test enforces it; the three come to about 524 KB. The budgets users feel are separate: the first-paint page (`internal/server`, 200 kB) and the realtime diffs (`internal/demo`, 55 kB per simulated minute). Regenerate after changing the simulator:

```sh
go test ./sim -run TestSnapshots -update
```

`go test ./sim` fails if a snapshot is out of date. Snapshots use `EncodeState` / `EncodeTimeline`: sorted keys, sorted items, one item per line, no HTML escaping.

## Decisions

- **The world is a pure function of time**, not a stepped state machine. This gives step-size independence, cheap `Skip`, history pre-rolled before t0, and `Fix` that only rewrites the future.
- **Incident timing is scripted.** An incident opens and resolves at its scripted (or `Fix`) time, and its metrics are clamped to agree: for example, PBS stays at or below 89.9% before opening and at or above 90% while open, and the WAN stays under the slow line's speed while `speedtest-slow` is open. Tests can therefore assert exact times. Threshold detection is the hub's job, not the simulator's.
- **Scenarios are fixed to the home's stories**: the memory pressure is always stork, the failed backup is always homeassistant, the unreachable target is always PBS. A script picks which scenarios happen and when. A script scenario is the simulator's behaviour; the incident's `scenario` is the hub's name for the kind of incident.
- **The Demo Home is a fictional home shaped like the integrations' real output, not any real lab.** The demo exercises the same widget and Inbox paths as a real hub: findings shaped like the integrations' (same IDs, scenarios, facts, wording), the correlator's causes across instances, the real metric keys and attrs. Everything else is invented: the household, the hosts and their names, VMIDs, memory and disk sizes, which disks spin down, the addresses, versions, show titles and the weather. Unraid's unsupported API is an afternoon episode, so the rest of the day's Unraid data is live.
- **Backups are runs**: each job's nightly run lays out its guests one after another, then the scenarios shape it (a guest that fails, a run that stops before any guest, a slow guest) or add runs started by hand. Guests' and jobs' attrs, and the PBS incident's ramp, follow the runs.
- **Weather refreshes every three hours**, the readings hourly. The integration polls every 15 minutes while watched, but each refresh is a whole-resource event (about 2 KB), and the snapshots have a budget. The place is the weather integration's own example, Greenwich; no coordinates are published.
- **No host temperatures.** The PVE and Unraid APIs don't report them (spike 4.1, `integrations/unraid`); they wait for the node agent.
- **Hint severity is `info`**, as 07 · v0.1 decided for every hint. The Docker integration raises its endpoint hint at `attention` (and Traefik its open-API and plain-http ones), so one side still has to change.
- **Every app has `attrs.type`**, so a demo app and a real `<integration>:<instance>/app` resolve the same way; the IDs stay (`app:sonarr`), so stored layouts keep working.
- **Each PVE endpoint has its own nightly job** (wren 02:00, aviary 02:15, owl 02:30), all writing to the PVE storage `pbs-magpie`. aviary is one endpoint, so one job covers its three nodes. 10 of the 11 guests are covered; `stork/qemu/221 sandbox` is deliberately left out as a backup-coverage gap.
- **The PBS datastore lives on a dedicated 2 TB Unraid pool** (`backups`). The array is 71% of 84 TB, so a datastore at 92% can't sit on it. Stopping the array still reaches PBS, because the Docker engine `depends_on` the array.
- **AdGuard Home is the home's DNS.** OPNsense hands it out and forwards to it, modelled as `net:opnsense depends_on app:adguard`, so its blast radius is the whole home.
- **One flat LAN**, 10.20.30.0/24, with the router at .1, the network core's guests at .2–.4, the servers at .20–.30, the house at .40–.70 (Home Assistant, Frigate, files, the cameras, studio).
- **Containers are the known set only:**
  - on magpie: 14 containers;
  - on rook: 15 containers.
- **Container stats only where a story reads them** (qBittorrent's OOM, PBS's backup load): the Docker integration doesn't publish them yet. Containers carry no `restart_policy` attr (`desired` already says it), and the engine, not each container, `stores_on` the appdata share.
- **Every app with a URL of its own has a Traefik route**, except Frigate, whose standing hint is about the internal port it's read on.
- **The hub and the dashboard it replaces run side by side on rook.** `chadash` is the hub (`app:chadash` has `self: true`, so hub self-awareness shows up in blast radii). `homepage` is the dashboard the **Homepage importer** reads during first run. Import sources are named after what they are; other importers (Homarr, plain YAML) work the same way.
- **Tiers:**
  - Reconnect Plex is T0: a sign-in, nothing to undo.
  - Update the Karakeep integration is T1: it can be undone.
  - Back up now is T1: non-disruptive.
  - Turn off maintenance mode is T1: undo turns it back on.
  - Prune per policy is T2, as in the prototype, with a blast-radius list.
  - Restarting an LXC and recreating a container with a new limit are T2.
  - Opening Proxmox, Speedtest Tracker or an integration's settings is T0.
- **Health adds `repair`** to the Home Graph's five statuses. It's used only on integration resources, so the UI can tell "can't read it" from "it's broken".
- **Integration instances are resources** (`integration:*`, 33 of them), so repairs have a target, `monitors` edges, and a blast radius ("these tiles go stale").
- **Attrs use flat dotted keys** (`backup.last_ok`, `last_event.label`), so cloning and diffing stay shallow.

## Not yet

- A correlation "merge?" suggestion. The demo graph has nothing honestly weak to suggest, and answering one goes to `/api/correlate/suggestions/{id}/…`, which in demo mode serves the correlator of the hub's private topics, so Merge and Keep separate would both 404.
- Other findings the integrations can raise: a job writing to `local`, guests no job covers (beyond sandbox), a failing `apt-get update`, secret-looking container labels, Traefik's plain-http credentials hint.
- Read-only PVE token upgrade offers in the Inbox.
- Arbitrary actions beyond each incident's suggested one (for example, actually rebooting wren in the demo). `BlastRadius` covers the confirm dialog.
- A weekday schedule for the household, a repeat/loop mode, and a per-home time zone.
