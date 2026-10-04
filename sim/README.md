# Demo Home simulator (`sim/`)

A deterministic stand-in for a real homelab, shaped like the [dogfood home](../docs/plan/05-dogfood-home.md). It powers:
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
| `BlastRadius(id) []Impact` | What an action on a resource would affect. Use it for the hold-to-confirm list. A package-level `BlastRadius(rels, id)` works on any relation list |
| `Detections(camera, from, to)` | Frigate events for the camera hero widget (`driveway`, `side_deck`, `backyard`) |
| `Record(span, step) Timeline` | Advances and returns a compact columnar recording |
| `EncodeState`, `EncodeTimeline` | Stable, diff-friendly JSON: sorted keys, sorted items, one item per line |
| `DefaultScript()`, `Script.Validate()` | The default incident script, and validation for custom ones (JSON-friendly: offsets are duration strings) |

A `Sim` is safe for concurrent use. Snapshots and events are deep copies, so changing them never changes the simulation.

## Topology

129 resources and 201 relations. Addresses are sanitized: `10.0.11.x`, `*.lab.example` and `*.family.example` only. A test enforces this.

```
internet ── net:opnsense/wan ── net:opnsense (router · provides_network_for every host and camera)
                                  └ runs_on pve:pluto/qemu/100

pve:pluto           network core · 16 GB    qemu/100 opnsense · lxc/101 tailscale
pve:minig5          home · 16 GB            qemu/100 homeassistant ─ app:homeassistant ──depends_on──▶ app:frigate
                                            lxc/101 frigate ─ app:frigate ──exposes──▶ camera driveway · side_deck · backyard
pve:mars            cluster of 2
 ├ pve:mars-1       32 GB                   lxc/110 pvedocker ─ docker:pvedocker (socket proxy)
 │                                            chadash (this hub, self: true) · homepage · dockerproxy · traefik · uptimekuma · linkding
 │                                            karakeep-web-1 ─depends_on─▶ karakeep-chrome-1, karakeep-meilisearch-1
 └ pve:mars-2       32 GB                   (no guests)
pve:io              workstation · 64 GB     qemu/201 win-gaming (off) · qemu/202 iolinux · qemu/203 devbox (off)
pve:jupiter-nas     NAS · 8 GB, no swap     lxc/100 debian-nas ─ app:cockpit · qemu/101 scratch (off, not backed up)
unraid:jupiter      Unraid 7.3 · 64 GB
 ├ array            62 TB: parity + disk1–6 ─ shares media, photos
 ├ pool cache       2 TB NVMe ─ share appdata (container appdata), docker.img
 ├ pool backups     2 TB ─ pbs:main (the PBS datastore)
 └ docker:jupiter   socket proxy :2375 · depends_on the array
                    Plex-Current · radarr · sonarr · bazarr · tautulli · binhex-qbittorrentvpn
                    binhex-prowlarr · mealie · immich · pbs ─ app:pbs ◀─part_of─ pbs:main · socket-proxy

pve:<instance>/backup/daily   one nightly job per PVE endpoint (02:00–03:01, staggered) ──stores_on──▶ pbs:main
9 guests ──backed_up_by──▶ their instance's job
traefik:route/*               17 routes (host- and path-based) ──routes_to──▶ apps, part_of app:traefik
integration:*                 24 integration instances ──monitors──▶ what they read
```

| Kind | Count | | Kind | Count |
|---|---|---|---|---|
| integration | 24 | | storage (PVE) | 6 |
| container | 19 | | backup_job | 5 |
| app | 18 | | lxc | 4 |
| route | 17 | | camera, share | 3 each |
| host (6 PVE + Unraid) | 7 | | engine, pool | 2 each |
| disk | 7 | | cluster, array, router, wan, backup_target | 1 each |
| vm | 6 | | | |

**IDs** are namespaced and stable: `pve:mars-1/lxc/110`, `docker:jupiter/sonarr`, `unraid:jupiter/disk/disk1`, `pbs:main`, `app:plex`, `integration:plex`, `traefik:route/sonarr`, `frigate:camera/driveway`, `net:opnsense`. Containers are keyed by engine and name. The Docker container ID is the `container_id` attr, so a recreate keeps the resource ID and changes the attr.

**Relations** read `From <type> To` and use the architecture doc's names. Here is how the brief's names map onto them:

| Brief | Relation | Example |
|---|---|---|
| runs-on | `runs_on` | `pve:minig5/qemu/100 runs_on pve:minig5`; `app:sonarr runs_on docker:jupiter/sonarr` |
| stores-on | `stores_on` | `pbs:main stores_on unraid:jupiter/pool/backups`; `app:plex stores_on …/share/media` |
| backs-up | `backed_up_by` (inverse) | `pve:minig5/qemu/100 backed_up_by pve:minig5/backup/daily` |
| routes-through | `provides_network_for` (inverse) | `net:opnsense provides_network_for pve:pluto` |
| depends-on | `depends_on` | `app:sonarr depends_on app:qbittorrent` |
| exposed-by | `routes_to` (inverse) | `traefik:route/sonarr routes_to app:sonarr` |
| — | `part_of` | `pve:mars-1 part_of pve:mars`; `unraid:jupiter/disk/disk1 part_of …/array` |
| — | `exposes` | `app:frigate exposes frigate:camera/driveway` |
| — | `monitors` | `integration:plex monitors app:plex` |

**Blast radius** (`BlastRadius`) walks:
- dependents transitively over `runs_on`, `part_of`, `depends_on` and `stores_on`;
- dependents one step over `backed_up_by` and `routes_to`, so a guest loses its backups and a route breaks, but nothing further goes down;
- forwards over `provides_network_for` and `exposes`;
- one step forwards over `monitors`, so a broken integration makes what it reads stale.

Rebooting pluto reaches the whole home, including Cha Dash itself. Stopping jupiter's array reaches Docker, PBS and the 9 backed-up guests' coverage, but not the guests themselves.

## Metrics

152 series. Every series carries `unit`, `min`/`max` (the scale), `band` (the normal range, drawn as a gray band), optional `warn`/`crit` thresholds, `value`, `as_of`, `stale` and `history`. Every history in a snapshot shares one axis (`State.History`). By default that's 48 points 30 minutes apart, ending at the last sample tick.

| Resource | Metrics |
|---|---|
| hosts | `cpu.pct`, `mem.pct` (warn 90, crit 98), `temp.c` (follows CPU load) |
| running guests | `cpu.pct`, `mem.pct` |
| containers | `cpu.pct`, `mem.mib` |
| storage, array, disks, pools, datastore | `used.pct`; disks and pools also `temp.c` |
| router · WAN | `cpu.pct`, `mem.mib` · `rx.mbps`, `tx.mbps`, `rx.gb_30d`, `tx.gb_30d` |
| apps | `http.ms` (built-in check), plus app stats: HA `power.kw`, `energy.kwh`, `people_home`, `lights_on`, `switches_on`; Plex `streams`; Sonarr `queue`, `wanted`; Radarr `wanted`, `queued`; Bazarr `missing.*`; Prowlarr `grabs`, `queries.24h`, `failures.24h`; qBittorrent `dl.mbps`, `ul.mbps`, `leeching`, `seeding`; Immich `photos`, `videos`, `storage.gib`; Karakeep `bookmarks`; Uptime Kuma `monitors.up`, `monitors.down`; PBS `tasks.failed_24h`; Frigate `inference.ms` |
| cameras | `detections.24h` |

The curves are a base, a diurnal swing (evening media, daytime Frigate load, morning and evening house power), slow value noise and a little jitter. Routine windows add to them: nightly backups raise jupiter's CPU and PBS's load between 02:00 and 03:05. Incidents reshape specific series on top of that.

**Honest data:**
- A metric is *unknown* (JSON `null`, never 0) when its resource isn't up or its integration isn't connected. Stopped guests have no metrics.
- During a repair, the app's stats go stale. The series keeps its last-known `value` with `stale: true` and an `as_of`, while the app's own `http.ms` check stays live. A broken integration is not an outage.

## Incidents

### The default script (day one, times UTC)

All six are open at **14:05** (`DefaultAllOpen`), the moment the prototype depicts. They are fixed one by one in the afternoon. By 20:02 the home is quiet again.

| Incident (`id` = scenario) | Opens | What happens | Class · severity | Suggested action (tier) | Resolves |
|---|---|---|---|---|---|
| `backup-failed` | 02:14 | homeassistant's nightly backup fails: the QEMU guest agent doesn't answer fs-freeze. The other 8 guests are fine | problem · attention | Back up homeassistant now (**T1**) | 15:34: the retry started at 15:30 succeeds. Unfixed, the next nightly run resolves it (`auto`) |
| `pbs-datastore-filling` | 02:44 | win-gaming's backup (02:30–02:52) writes 108 GB and the datastore crosses 90%, then reaches 92%: "full in about 3 weeks". The prune job has been disabled since 14 Sep | problem · attention | Prune per policy (**T2**; alternatives: re-enable prune job T1, open PBS T0) | 16:22: prune at 16:00, garbage collection until 16:22, back to 86% (below 88%) |
| `karakeep-api-changed` | 09:05 | karakeep-web-1 is recreated with 0.31.0 at 09:02; the stats endpoint 404s. Karakeep itself stays up | **repair** · attention | Update the Karakeep integration (**T1**; alternative: roll back to 0.30.2 T2) | 19:00:30 |
| `plex-token-revoked` | 13:27 | Plex answers 401. Plex itself stays up; its stats go stale | **repair** · attention | Reconnect Plex with a PIN (**T0**) | 18:00:10 |
| `host-memory-pressure` | 13:30 | debian-nas grows from 0.5 to 3.1 GB from 11:40. jupiter-nas (8 GB, no swap) crosses 90% at 13:30 and plateaus at about 95% from 14:00 | problem · attention | Restart debian-nas (**T2**) | 17:10: restart at 17:00 (20 s down), memory settles, below 85% for 10 min |
| `qbittorrent-oom` | 14:03 | Memory climbs from 1.1 to 4 GiB from 13:38. OOM-kill (exit 137), Docker restarts it at 14:03:20, VPN up at 14:07. Sonarr and Radarr can't reach it meanwhile; Uptime Kuma shows 8/9 | problem · **down** → attention → info | Raise the memory limit to 6 GiB (**T2**; Unraid removes and recreates the container) | 20:02: fix at 20:00, recreated at 20:00:15, healthy at 20:02. Unfixed, it auto-resolves 24 h after the kill |

### What an incident carries

| Field | Meaning |
|---|---|
| `class` | `problem` (Needs attention, status sentence) or `repair` (Inbox only; an integration issue, not an outage) |
| `severity`, `phase`, `status` | `info` / `attention` / `down`; the scenario's current phase (`rising`, `high`, `pruning`, `restarting`, `recovered`, …); `open` / `resolved` |
| `title`, `summary` | Copy in the 02-experience voice: symptom first, specific times, calm |
| `target` | The primary resource |
| `facts` | Numbers the copy quotes (`used_pct: 92`, `full_in_days: 21`, `status: 401`, …) |
| `causes` | Root-cause hints: a text, the resource it points at and when it changed |
| `blast_radius` | `BlastRadius(target)`: resource, the relation that pulled it in, and depth |
| `suggested`, `alternatives` | Actions with `id`, `label`, safety `tier`, `target` and `detail`. T2 and up carry their own `impact` list for hold-to-confirm |
| `resolves_when`; `resolved_by`, `resolution` | How it will resolve; once resolved, `action` or `auto` plus what happened |

### Custom scripts

A `Script` is a list of `{scenario, id?, open, fix?}`, with offsets from the start as Go duration strings in JSON. Leave `fix` out and the incident stays open until `Sim.Fix`, or until it auto-resolves (backup failure, OOM). Scenarios can repeat with distinct IDs as long as they don't overlap. `Script.Validate` checks all of this. `WithScript(nil)` gives a quiet home.

## Event model

`Advance` returns events in time order. Every event has a `seq` (incrementing by one per event, per `Sim`), an `at` and a `type`:

| Type | Payload | When |
|---|---|---|
| `resource.upsert` | `resource`: the full resource | A resource appears or any field changes (state, health, attrs) |
| `resource.remove` | `id` | A resource disappears (qBittorrent's container while Unraid recreates it) |
| `relation.upsert` / `relation.remove` | `relation` | An edge appears or disappears (with the resource it touches) |
| `incident.open` / `incident.update` / `incident.resolve` | `incident`: the full incident | It opens, its phase or copy changes, it resolves |
| `metrics` | `samples`: `[{resource, metric, value}]` | Every sample interval (default 30 s, aligned to the Unix epoch). Readable series only |

**Ordering at one instant:** relation removals, resource removals, resource upserts, relation upserts, incident events, then the metrics batch. A consumer applying events one by one never holds a relation to a missing resource. `TestEventReplay` replays a whole day onto the starting snapshot, checks this after every event, and lands exactly on the final snapshot.

**Contract with the hub's realtime protocol:** `State.Seq` is the last event before the snapshot. That maps directly onto `snap` (with `seq`) followed by `diff`s. Resources carry only facts that change at discrete moments. Anything continuous is a metric, so upserts stay rare: 94 in the default day, against 2,880 metrics batches.

**Determinism:** the world is a pure function of (seed, script, fixes, time):
- Randomness is counter-based hashing (SplitMix64 over seed, stream key and counter), not a stateful generator.
- Non-metric state changes only at declared change points: script phases, backup slots and camera detections. `Advance` evaluates exactly those plus the sample ticks.

So the same seed gives byte-identical snapshots, and `Advance(24h)` equals any split into smaller steps (`TestStepSizeDoesNotMatter`). A different seed changes noise, uptimes, container IDs and camera detections, but not the topology or the script.

## Snapshots

Generated by a golden test from seed 1:

| File | Moment | Size |
|---|---|---|
| [`healthy.json`](snapshots/healthy.json) | t0, 2026-10-03 00:00: all quiet, with the previous 24 h as history | 111 KB |
| [`incidents.json`](snapshots/incidents.json) | 14:05: all six incidents open | 123 KB |
| [`timeline.json`](snapshots/timeline.json) | 00:00 → 24:00: 152 series × 97 frames (15 min), every incident, and all 122 non-metric events. Replay the events on `healthy.json` | 170 KB |

`Record` skips metrics events, so in `timeline.json` the `seq` values count only the non-metric events. The total budget is 500 KB, and the test enforces it. Regenerate after changing the simulator:

```sh
go test ./sim -run TestSnapshots -update
```

`go test ./sim` fails if a snapshot is out of date. Snapshots use `EncodeState` / `EncodeTimeline`: sorted keys, sorted items, one item per line, no HTML escaping.

## Decisions

- **The world is a pure function of time**, not a stepped state machine. This gives step-size independence, cheap `Skip`, history pre-rolled before t0, and `Fix` that only rewrites the future.
- **Incident timing is scripted.** An incident opens and resolves at its scripted (or `Fix`) time, and its metrics are clamped to agree: for example, PBS stays at or below 89.9% before opening and at or above 90% while open. Tests can therefore assert exact times. Threshold detection is the hub's job, not the simulator's.
- **Scenarios are fixed to the dogfood story**: the memory pressure is always jupiter-nas, and the failed backup is always homeassistant. A script picks which scenarios happen and when.
- **The home keeps UTC as its clock.** Routines follow wall-clock UTC: backups at 02:00, evening media, daylight on the cameras. Script offsets count from `WithStart`, default 2026-10-03 00:00 UTC, the dogfood snapshot's date. For a live demo, start at today's midnight and `Skip` to now.
- **No repeat mode in v0.** After the script ends the home stays quiet. A long-running demo can create a new `Sim` each day.
- **"Failed backup task"** is a PBS/vzdump task failing for one guest (guest-agent timeout). The prototype's "verify 03:12" became this, to match the brief's wording.
- **The PBS datastore lives on a dedicated 2 TB Unraid pool** (`backups`). The array is 71%, so a datastore at 92% can't sit on it. Stopping the array still reaches PBS, because the Docker engine `depends_on` the array.
- **Each PVE endpoint has its own nightly job** (pluto 02:00, minig5 02:10, mars 02:20, io 02:30, jupiter-nas 02:40), since they're separate datacenters. 9 of the 10 guests are covered; `jupiter-nas/qemu/101 scratch` is deliberately left out as a backup-coverage gap.
- **Some facts are invented where 05-dogfood-home gives only counts:**
  - guest names: tailscale, devbox and scratch;
  - VMIDs;
  - memory sizes;
  - disk sizes, tuned so the array is 71% of 62 TB usable;
  - IPs inside 10.0.11.x, keeping the fixture's .1 router, .120 jupiter and .202 Frigate;
  - Karakeep versions 0.30.2 → 0.31.0.
- **Containers are the known set only:**
  - on jupiter: 11 containers, the fixture's plus mealie, immich, pbs and the socket proxy;
  - on pvedocker: 9 containers.

  The prototype's "24 containers" was a placeholder.
- **The hub and the dashboard it replaces run side by side on pvedocker.** `chadash` is the hub (`app:chadash` has `self: true`, so hub self-awareness shows up in blast radii). `homepage` is the dashboard the **Homepage importer** reads during first run. Import sources are named after what they are; other importers (Homarr, plain YAML) work the same way.
- **Tiers:**
  - Reconnect Plex is T0: a sign-in, nothing to undo.
  - Update the Karakeep integration is T1: it can be undone.
  - Back up now is T1: non-disruptive.
  - Prune per policy is T2, as in the prototype, with a blast-radius list.
  - Restarting an LXC and recreating a container with a new limit are T2.
- **Health adds `repair`** to the Home Graph's five statuses. It's used only on integration resources, so the UI can tell "can't read it" from "it's broken".
- **Integration instances are resources** (`integration:*`, 24 of them), so repairs have a target, `monitors` edges, and a blast radius ("these tiles go stale").
- **Attrs use flat dotted keys** (`backup.last_ok`, `last_event.label`), so cloning and diffing stay shallow.

## Not yet

- Static Inbox items that aren't incidents: label-discovered services, credential-hygiene hints, read-only PVE token upgrade offers. The topology carries their raw facts (`transport: http`, `read_only: true`), but the simulator doesn't emit them as items.
- Arbitrary actions beyond each incident's suggested one (for example, actually rebooting pluto in the demo). `BlastRadius` covers the confirm dialog.
- A repeat/loop mode, and a per-home time zone.
