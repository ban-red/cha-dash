# Workbench list: v0.1 widgets × sizes × states

> Status: **seed list for review** (M0 task 1.4). Agree it, then it becomes the definition of done for every v0.1 widget.
> The Workbench itself is the dev route `/dev/workbench` ([03 · Architecture → Testing](../plan/03-architecture.md#testing-and-developer-experience)): every widget × size × state × theme × level of detail, rendered from Demo Home fixtures. Prototype v2 has a static preview of it (`design/prototypes/m0-v2.html`, view **Workbench**).

Sources for this list:
- **Widgets:** [04 · Roadmap → v0.1 Widgets](../plan/04-roadmap.md#v01-front-page-read-only-beautiful-live).
- **Integrations:** the [v0.1 integration set](../plan/05-dogfood-home.md#v01-integration-set-derived-from-this-home).
- **Sizes, states and levels of detail:** [02 · Experience](../plan/02-experience.md#widget-sizes).

---

## How to read this

Every widget lists:
1. **Sources:** the integrations that feed it.
2. **Sizes:** the sizes it supports, and what each one shows. Each size is its own layout, never a scaled copy. The resize handle snaps only to these sizes.
3. **States:** the treatment of each state *for this widget*. The shared rules are in [The eight states](#the-eight-states). A dash (—) means the state can't happen for this widget, with the reason.

Rendering one cell means rendering it from a fixture, not hand-written HTML, and it must pass the [acceptance checks](#acceptance-checks).

## The dimensions

### Sizes

| Size | U | Notes |
|---|---|---|
| Glyph | 1×1 | One value or one toggle. Name plus value only; the state word goes to the accessible label |
| Strip | 4×1 · 8×1 | One line: identity, a bar or sparkline, a value |
| Small | 2×2 | One metric plus context, or one control |
| Wide | 4×2 | Metric with chart, or a list of 3–4 items |
| Tall | 2×4 | A list |
| Large | 4×4 | A table or a rich summary |
| Hero | 8×4 | Cameras, Atlas, a big chart. **Falls back to Large on phone** (4 columns) |

### The eight states

These are the required states (the brief's list). Every widget designs all of them, or says why one can't happen.

| State | When | Treatment | Glyph | Never |
|---|---|---|---|---|
| **Loading** | First load with no snapshot ever | Skeleton matching the final layout, no spinner. Rare in practice: the hub inlines the last-known snapshot into first paint | — | A spinner over a last-known value |
| **Empty** | The source works and has nothing to show | A helpful sentence ("Queue clear", "Nothing playing"); secondary facts stay | — | A blank tile, or a fake 0 |
| **Healthy** | Normal | Quiet: no badge, monochrome logo, neutral marks | ○ in lists only | Green "OK" badges |
| **Attention** | A threshold or condition needs the user | Ember (amber) inset bar and tinted hairline, logo colours in, ▲ + word. Mirrored into **Needs attention** | ▲ | Colour alone |
| **Down** | The service, host or check is down | Crimson inset bar, ■ + "Down since 14:02", likely cause from the graph. Mirrored to the **top** of Needs attention | ■ | Raw error text |
| **Stale / unreachable** | The hub can't reach the source, or the page can't reach the hub | Last-known values, hatch, dashed hairline, "as of 14:02" chip | dashed outline | Zeros, or hiding the value |
| **Needs repair** | The integration is broken (auth, API change) but the service may be fine | Unknown values are "—"; last-known values keep "as of"; a note with the fix ("Reconnect"); a Repair item in the **Inbox**. Not mirrored to Needs attention: a broken integration is not an outage | ◌ dashed circle | Calling it "down" |
| **Permission-limited** | The viewer's role can't see a field or run an action | Visible but locked: lock glyph, "Ask an admin". Values the role can't read are "—" because **the hub never sends them**. Actions are hidden, or disabled with the reason | lock | Blurring real data in the browser |

Three more states from the status vocabulary apply wherever they make sense, and are listed per widget:
- **Maintenance:** ‖ muted blue, alerts silenced, "until 22:00".
- **Off, expected:** ○ hollow, dimmed, "Stopped (as intended)". **Not a problem.**
- **In progress:** ◔ determinate arc, accent, "Updating · 40%".

The **hub offline** state is page-level, not per widget: a banner ("Cha Dash hub unreachable since 10:02"), every tile in its stale treatment, and actions disabled. It's listed once, under [Page-level components](#page-level-components).

### Levels of detail

| Level | Where | Applies to |
|---|---|---|
| **Full** | Desk, large sizes | Every widget |
| **Compact** | Small sizes, phone | Every widget |
| **Simplified** | Wall and TV: bigger type, no controls, the one number that matters | Widgets marked **wall** below |
| **Ink** | E-ink and print (post-1.0) | Widgets marked **ink** below: one colour, no motion, absolute times |

Plus a **night tint** for every widget (the Night theme), where state is carried by glyph shape and word only.

---

## Coverage at a glance

● supported · ○ supported, secondary · blank: not offered

| Widget | Glyph | Strip | Small | Wide | Tall | Large | Hero | Wall | Ink |
|---|---|---|---|---|---|---|---|---|---|
| [Service tile](#service-tile) | ● | | ● | ● | | | | | ● |
| [Metric stat and sparkline](#metric-stat-and-sparkline) | ● | ● | ● | ● | | | | ● | ● |
| [Uptime check](#uptime-check) | ● | ● | ● | ● | | | | ● | ● |
| [Fleet card](#fleet-card) | | | | ● | ○ | ● | ● | ● | ● |
| [Host card](#host-card) | ● | ● | ● | ● | | ● | | ● | ● |
| [Guest list](#guest-list) | | | | ● | ● | ● | | | |
| [Container list](#container-list) | | | | ● | ● | ● | | | |
| [Unraid array](#unraid-array) | ● | ● | ● | ● | ● | ● | ● | ● | ● |
| [Storage bar](#storage-bar) | ● | ● | ● | ● | | | | ● | ● |
| [Backups (PBS)](#backups-pbs) | | ● | ● | ● | | ● | | ● | ● |
| [Router (OPNsense)](#router-opnsense) | ● | ● | ● | ● | | ● | | ● | |
| [Camera Hero (Frigate)](#camera-hero-frigate) | | | ● | ● | | ● | ● | ● | |
| [Home Assistant summary](#home-assistant-summary) | ● | ● | ● | ● | | ● | | ● | |
| [Media calendar](#media-calendar) | | ● | | ● | ● | ● | | ● | ● |
| [Now playing](#now-playing) | ● | ● | ● | ● | | ● | | ● | |
| [Download queue](#download-queue) | ● | | ● | ● | ● | | | | |
| [Bookmarks](#bookmarks) | ● | | ● | ● | ● | | | | ● |
| [Clock](#clock) | ● | ● | ● | ● | | | | ● | ● |
| [Weather](#weather) | ● | ● | ● | ● | | | | ● | ● |

That's 19 widgets and 80 size cells. Each cell is rendered in every applicable state.

---

## Widgets

### Service tile

The default tile for any service: a live stat from its integration, plus context from the Home Graph (host, container, update pending) that needs no configuration.

**Sources:** any integration, or a plain URL with a built-in check. v0.1 stats per integration:

| Integration | Primary stat | Context line |
|---|---|---|
| Sonarr · Radarr | queued · wanted | next search, failed imports |
| Prowlarr | grabs (24h) | queries, failures |
| Bazarr | missing subtitles | trend ("down 40 this week") |
| Traefik | routers | services, middlewares, errors (1h) |
| Uptime Kuma | monitors up / total | uptime 24h |
| Immich | photos | videos, storage |
| Mealie | recipes | categories |
| Linkding · Karakeep | bookmarks | — |
| Tautulli | streams | (feeds Now playing) |
| Pi-hole · AdGuard *(breadth)* | queries blocked % | blocklist size |
| Jellyfin · Plex | (see Now playing) | — |
| Plain URL | HTTP status, latency | uptime 24h |

| Size | Shows |
|---|---|
| Glyph | Name, primary value (or a link glyph for plain URLs) |
| Small | Name and context (when it fits), primary value with unit, one secondary fact, graph context in the footer ("on jupiter · update available") |
| Wide | Primary value, a sparkline of the primary stat with its normal band, two secondary facts |

| State | Treatment |
|---|---|
| Loading | Skeleton: name bar, value block, footer bar |
| Empty | Integration-specific sentence: "Queue clear", "No bookmarks yet" |
| Healthy | Value plus context, no badge |
| Attention | Integration-defined ("Queue stalled 2h", "3 failed imports") |
| Down | "Down since 14:02 · exit 137 · likely: out of memory" (cause from the graph) |
| Stale | Last value, hatch, "as of" |
| Needs repair | "—" plus "API key rejected (401). Reconnect"; footer keeps "service up · 120 ms" from the HTTP check |
| Permission-limited | Viewer sees up/down only; stat is "—" with "Ask an admin" |
| Maintenance | "Paused · maintenance until 22:00" |
| Off, expected | "Stopped (as intended)" dimmed |

### Metric stat and sparkline

One number with its context: unit, normal band, trend and freshness. Never a bare "73%".

**Sources:** any numeric stat (host CPU and memory, WAN throughput, HA template sensors, disk temperature, house power).

| Size | Shows |
|---|---|
| Glyph | Value plus unit |
| Strip | Label · sparkline · value |
| Small | Value, word-sized sparkline with normal band and last-point dot |
| Wide | Value, larger sparkline, min/max over the window, threshold line if one is set |

**Wall:** the value at wall size and a thick bar or sparkline. **Ink:** value plus "as of 14:05".

| State | Treatment |
|---|---|
| Loading | Skeleton |
| Empty | "No data yet · history starts now" (new sensor) |
| Healthy | Neutral line and dot |
| Attention | Line and dot in Ember past the threshold; ▲ + word ("Memory 95% and climbing") |
| Down | The sensor's source is down: last value plus ■ "source down since 14:02" |
| Stale | Line stops at the last sample; the gap is hatched; "as of" |
| Needs repair | "—" plus a repair note on the source |
| Permission-limited | "—" plus lock |
| Maintenance · off | The source's state, inherited |

Linked scrubbing applies at every size: scrubbing any sparkline moves the time cursor on all of them.

### Uptime check

**Sources:** built-in HTTP/TCP/ICMP checks; Uptime Kuma monitors.

| Size | Shows |
|---|---|
| Glyph | Up/total, or one check's status |
| Strip | Name · 24h status bars · uptime % |
| Small | Up/total, 24 hourly bars, uptime % |
| Wide | Per-check rows (3–4) with latency sparklines |

| State | Treatment |
|---|---|
| Loading | Skeleton bars |
| Empty | "No checks yet. Every discovered URL gets one automatically" |
| Healthy | Neutral bars |
| Attention | Degraded: latency over threshold or flapping (damped), ▲ "slow · 1.2 s" |
| Down | ■ "Karakeep down since 14:02 (HTTP 502)"; the hour bars carry the shape, not only colour |
| Stale | Hatch plus "as of" (the checker itself can't run) |
| Needs repair | Uptime Kuma only: key rejected; built-in checks keep running and say so |
| Permission-limited | Check names visible, targets hidden |
| Maintenance | ‖ bars, "silenced until 22:00" |
| Off, expected | A paused check: ○ "paused" |

### Fleet card

All the hosts in the home: multiple Proxmox VE instances (the normal case), clusters, Unraid and Docker hosts.

**Sources:** Proxmox VE (multi-instance, clusters), Unraid, Docker hosts, the graph's `provides_network_for` and "runs Cha Dash" relations.

| Size | Shows |
|---|---|
| Wide | Host count; the worst three hosts as rows; "+4 healthy" |
| Tall | Host names with memory bars (no role or CPU) |
| Large | Table: host · role · CPU · memory bar with threshold tick · guests up/total |
| Hero | The table grouped by cluster, with a 24h memory sparkline per host |

**Wall:** the one line that matters ("jupiter-nas needs you") plus thick memory bars. **Ink:** table, absolute time.

| State | Treatment |
|---|---|
| Loading | 7 skeleton rows |
| Empty | "No hosts yet" with the token-recipe CTA (first run) |
| Healthy | ○ "all healthy" tag; rows neutral |
| Attention | Row ▲ (memory ≥ 90%, a failing task); tile tag "1 needs you"; mirrored |
| Down | Row ■ "pluto down since 14:02". **Network-core hosts rank first** and say what they take with them ("router VM unreachable") |
| Stale | Per row when one instance is unreachable; whole tile when all are |
| Needs repair | One instance's token is rejected: that row shows "—" and ◌; Inbox Repair "Reconnect io" |
| Permission-limited | Power actions hidden; "read-only (PVEAuditor)" in the sheet; the upgrade offer lives in the Inbox |
| Maintenance | Row ‖ "maintenance until 22:00" |
| Off, expected | A host that's meant to be off (a workstation): ○ dimmed |

### Host card

One host in detail: a PVE node, the Unraid box, or a Docker host.

**Sources:** Proxmox VE, Unraid, Docker (and later the node for host metrics).

| Size | Shows |
|---|---|
| Glyph | Name, memory % |
| Strip | Name · memory bar · CPU |
| Small | Memory % with bar and threshold, CPU, guests up/total |
| Wide | CPU and memory sparklines with normal bands, guests, uptime |
| Large | Wide plus storage bars and the guest list (top 6) |

| State | Treatment |
|---|---|
| Loading | Skeleton |
| Empty | — (a host always has metrics; a host with no guests says "No guests") |
| Healthy | Neutral |
| Attention | ▲ memory or storage over threshold; pressure on a network-core host ranks higher |
| Down | ■ "unreachable since 14:02"; blast radius from the graph ("3 guests, the router") |
| Stale | Hatch, "as of" |
| Needs repair | Token rejected or privilege missing: "—" and the specific missing privilege |
| Permission-limited | Metrics visible, power actions hidden |
| Maintenance | ‖ |
| Off, expected | ○ "powered off (expected)" |

### Guest list

VMs and LXCs of a host or a cluster.

**Sources:** Proxmox VE (`/cluster/resources`), Unraid VMs.

| Size | Shows |
|---|---|
| Wide | 3–4 guests: name, status, CPU, memory |
| Tall | Up to 10 guests: name and status glyph |
| Large | Table with CPU, memory bar, uptime, last backup |

| State | Treatment |
|---|---|
| Loading | Skeleton rows |
| Empty | "No guests on mars-2" |
| Healthy | Running guests listed quietly; stopped-but-expected ones dimmed |
| Attention | Guest row ▲: no backup in 7 days, memory pressure |
| Down | Guest that should run but doesn't: ■ "stopped since 14:02, autostart on" |
| Stale | Hatch, "as of" |
| Needs repair | Per-instance token problem, as in the Fleet card |
| Permission-limited | Guests the role can't see are counted, not named ("+3 hidden") |
| Maintenance | Row ‖ |
| Off, expected | ○ "stopped (expected)": **not a problem** |
| In progress | ◔ "backing up · 40%", "migrating" |

### Container list

**Sources:** Docker through the socket proxy (local and remote), Unraid's Docker.

| Size | Shows |
|---|---|
| Wide | Count, unhealthy count, the 3 most recent changes |
| Tall | Up to 10 containers: name and status glyph; healthy ones get no badge |
| Large | Table: container, image tag, uptime, CPU, memory, update available |

| State | Treatment |
|---|---|
| Loading | Skeleton rows |
| Empty | "No containers on this engine" |
| Healthy | Quiet list, **no green badges** |
| Attention | ▲ unhealthy healthcheck, restart loop (damped), update available (secondary) |
| Down | ■ "exited (137) at 03:12 · OOM-killed"; dependants named from the graph ("Sonarr began failing at 03:13") |
| Stale | Hatch, "as of" |
| Needs repair | The endpoint rejects the hub, or a proxy blocks a read: says which |
| Permission-limited | Names visible, env and logs hidden |
| Maintenance | ‖ |
| Off, expected | ○ "stopped (expected)" |
| In progress | ◔ "pulling · 60%" (v0.2 actions; designed now) |

### Unraid array

The Workbench's "re-lay out, never scale" reference widget. Prototype v2 shows all seven sizes.

**Sources:** Unraid API.

| Size | Shows |
|---|---|
| Glyph | Array % |
| Strip | Name · array bar · % · "parity ✓ · 41°" |
| Small | Array % with threshold bar, parity state |
| Wide | "71% of 62 TB" with bar; parity age, hottest disk, containers |
| Tall | Array % plus every disk: usage bar, %, temperature |
| Large | Wide plus the disk list and cache |
| Hero | A disk map (a bar per disk, parity hatched), hottest-disk sparkline, containers, VMs, flash backup |

| State | Treatment |
|---|---|
| Loading | Skeleton per layout |
| Empty | — (an array always has disks; a stopped array is its own state below) |
| Healthy | Neutral bars |
| Attention | ▲ a disk ≥ 90% or hot, parity check overdue, flash backup stale |
| Down | ■ array degraded or a disk disabled: "disk3 disabled since 14:02 · parity protects data" |
| Stale | Hatch, "as of" |
| Needs repair | API key revoked: Inbox Repair via the consent flow ("Reconnect jupiter") |
| Permission-limited | Read-only view; array start/stop (T3) hidden |
| Maintenance | ‖ planned downtime; a running parity check is **in progress** instead |
| Off, expected | ○ "array stopped (by you, 14:02)" |
| In progress | ◔ parity check with ETA |

### Storage bar

A single pool, datastore or share: PVE storage, Unraid share, PBS datastore.

| Size | Shows |
|---|---|
| Glyph | % used |
| Strip | Name · bar with threshold tick · % · free |
| Small | % used, bar, "full in ~3 weeks" when trending up |
| Wide | Bar plus a 30-day usage sparkline and the growth trend |

| State | Treatment |
|---|---|
| Loading | Skeleton bar |
| Empty | — |
| Healthy | Neutral bar |
| Attention | ▲ over threshold **and** trending up (cause alerts are avoided; "90% and climbing" is the symptom) |
| Down | ■ storage offline or read-only |
| Stale | Hatch, "as of" |
| Needs repair | Source token problem |
| Permission-limited | % visible, contents hidden |
| Maintenance · off | Inherited from the pool |

### Backups (PBS)

The dogfood home's top incident ("Datastore 92%", "1 failed task"). A dedicated widget so it can rise with the right sentence.

**Sources:** Proxmox Backup Server; graph links datastore → container → Unraid array.

| Size | Shows |
|---|---|
| Strip | "Backups · 92% · 1 failed" |
| Small | Datastore %, threshold bar, last result ("verified 02:00" or "1 task failed") |
| Wide | Small plus the last 7 nights as a row of results and "full in ~3 weeks" |
| Large | Wide plus failed tasks with times, guests covered, prune policy |

**Wall:** the percentage at wall size, a thick bar, "Full in about 3 weeks". **Ink:** "▲ Backups 92% · as of 14:05".

| State | Treatment |
|---|---|
| Loading | Skeleton |
| Empty | "No backup jobs yet" with a link to PBS |
| Healthy | "verified 02:00 · 9 guests" |
| Attention | ▲ datastore ≥ 85% and trending up, or a failed task in 24h; mirrored as a strip with **Prune per policy (T2)** |
| Down | ■ PBS unreachable while backups are due; or the array it lives on is down ("backups at risk") |
| Stale | Hatch, "as of" |
| Needs repair | Token rejected |
| Permission-limited | Results visible; prune and verify hidden |
| Maintenance | ‖ planned downtime; garbage collection is **in progress** instead |
| Off, expected | — |
| In progress | ◔ "verify · 40%", "prune · GC" |

### Router (OPNsense)

The network core. Its states outrank everything else because everything depends on it.

**Sources:** OPNsense API (and its SSE traffic stream); graph `provides_network_for`.

| Size | Shows |
|---|---|
| Glyph | WAN ↓ now |
| Strip | WAN · throughput sparkline · ↓ now |
| Small | ↓ now, 30-day totals |
| Wide | ↓ now, throughput sparkline, 30-day ↓/↑ totals, CPU |
| Large | Wide plus gateways, interfaces and top clients (v0.3) |

OPNsense only reports since-boot counters, so the hub accumulates the 30-day totals itself. Live throughput comes from its SSE stream, opened only while the tile is on screen (each stream holds a worker on the router). CPU needs *Lobby: Dashboard*, and gateway-based Attention/Down needs *System: Gateways*; without them the tile shows Permission-limited for that part ([spike 4.5](../../spikes/opnsense/README.md)).

| State | Treatment |
|---|---|
| Loading | Skeleton |
| Empty | — |
| Healthy | ○ "WAN up" |
| Attention | ▲ WAN packet loss or latency over threshold, CPU pressure |
| Down | ■ "WAN down since 14:02"; the sentence leads with it ("Internet is down") |
| Stale | Hatch, "as of" (often because the hub's own network path is down) |
| Needs repair | Key and secret rejected |
| Permission-limited | Totals visible; reboot (T2) hidden |
| Maintenance | ‖ firmware update window |
| Off, expected | — |

### Camera Hero (Frigate)

**Sources:** Frigate events and snapshots.

| Size | Shows |
|---|---|
| Small | Latest detection snapshot with caption |
| Wide | Two cameras side by side with captions |
| Large | One big camera plus two small; recent detections |
| Hero | Three cameras plus a detection list with thumbnails (falls back to Large on phone) |

**Wall:** one big frame plus two small, the last detection in a large caption, no controls.

| State | Treatment |
|---|---|
| Loading | Grey frames with the camera names, no spinner |
| Empty | "No detections today" under live frames |
| Healthy | Frames with timestamps; "3 recording" |
| Attention | ▲ a camera offline while others record; storage nearly full |
| Down | ■ Frigate down: last frames, hatched, "last frame 14:02" |
| Stale | Frames freeze and hatch with "last frame 14:02" |
| Needs repair | API change or auth: frames still load from the last snapshot, events show "—" |
| Permission-limited | Household role: snapshots only, no clips |
| Maintenance | ‖ per camera |
| Off, expected | ○ a camera disabled on purpose (privacy mode) |

### Home Assistant summary

**Sources:** Home Assistant: picked entities plus template-rendered stats (people home, lights on, switches on, power and energy).

| Size | Shows |
|---|---|
| Glyph | People home (2/2) |
| Strip | Home · lights · power |
| Small | One stat plus a sparkline (power) |
| Wide | Three stats plus house-power sparkline and energy today |
| Large | Wide plus picked entities as rows |

| State | Treatment |
|---|---|
| Loading | Skeleton |
| Empty | "Pick a few entities to show here" |
| Healthy | Stats, quiet |
| Attention | Template-defined (a door open for 30 min) |
| Down | ■ Home Assistant down |
| Stale | Hatch, "as of" |
| Needs repair | Long-lived token revoked: Inbox Repair |
| Permission-limited | Household sees stats, not entity controls (controls arrive in v0.2) |
| Maintenance · off | Inherited |

### Media calendar

**Sources:** the `media.calendar` capability of every Sonarr and Radarr instance (not names: renames don't break it).

| Size | Shows |
|---|---|
| Strip | "Next: Black Clover 04:00" |
| Wide | Today and tomorrow, 3–4 rows |
| Tall | Tonight plus the week (6–8 rows) |
| Large | A week grid |

| State | Treatment |
|---|---|
| Loading | Skeleton rows |
| Empty | "Nothing airing this week" |
| Healthy | Rows |
| Attention | ▲ an aired episode missing for 24h (secondary) |
| Down | Rows from the instances still up, plus "Radarr down: movies missing from this list" |
| Stale | Hatch, "as of" |
| Needs repair | One instance's key rejected: its rows show "—" and a note |
| Permission-limited | — (calendar data is household-safe) |
| Maintenance · off | — |

### Now playing

**Sources:** Plex (with PIN sign-in) and Tautulli; Jellyfin for breadth.

| Size | Shows |
|---|---|
| Glyph | Active streams |
| Strip | "2 streams · Severance · Living room" |
| Small | Streams count or "Nothing playing", library counts |
| Wide | Current streams with user, device and progress; library counts |
| Large | Wide plus recently added |

| State | Treatment |
|---|---|
| Loading | Skeleton |
| Empty | "Nothing playing · last watched yesterday 22:41" |
| Healthy | Streams with progress bars |
| Attention | ▲ transcoding load high, a stream buffering |
| Down | ■ Plex down: "Movies aren't working since 14:02" on the Family board |
| Stale | Hatch, "as of" |
| Needs repair | **The dogfood case:** "Plex rejected its token. Counts are last-known. Reconnect" (PIN flow, about 10 s) |
| Permission-limited | Household sees titles, not users or IPs |
| Maintenance | ‖ planned downtime; a library scan is **in progress** instead |
| Off, expected | — |

### Download queue

**Sources:** qBittorrent, the *arr queues; SABnzbd for breadth.

| Size | Shows |
|---|---|
| Glyph | Active downloads |
| Small | Leeching / seeding, speeds |
| Wide | Top 3 items with progress and ETA |
| Tall | Up to 8 items |

| State | Treatment |
|---|---|
| Loading | Skeleton rows |
| Empty | "Queue clear · nothing downloading" |
| Healthy | Items with determinate progress |
| Attention | ▲ stalled items, VPN tunnel down with the client still up |
| Down | ■ client down (the dogfood's OOM-killed qBittorrent), with the *arr that depend on it named |
| Stale | Hatch, "as of" |
| Needs repair | Login rejected |
| Permission-limited | Counts only; pause (T1) hidden |
| Maintenance | ‖ planned downtime; a scheduled pause is **off, expected** instead |
| Off, expected | ○ "paused (by you)" |
| In progress | Every item is progress: determinate arcs, never spinners |

### Bookmarks

**Sources:** manual links, Linkding and Karakeep, and discovered service URLs.

| Size | Shows |
|---|---|
| Glyph | One link with its icon |
| Small | 4 links |
| Wide | 6–8 links in two columns |
| Tall | A list of up to 10 |

| State | Treatment |
|---|---|
| Loading | Skeleton rows |
| Empty | "No bookmarks yet. Pin a service or import from Linkding" |
| Healthy | Links, monochrome icons |
| Attention | — (links don't need you; their checks do) |
| Down | A link whose check is down gets ■ on that row |
| Stale | — (links are local config) |
| Needs repair | Linkding/Karakeep sync broken: list keeps last-known, note |
| Permission-limited | Admin-only links hidden |
| Maintenance · off | — |

### Clock

| Size | Shows |
|---|---|
| Glyph | hh:mm |
| Strip | Time · date |
| Small | Time (dot-matrix face) and date |
| Wide | Time, date and a second time zone |

**Wall:** the hero clock. **Ink:** time printed at refresh.

States: only **Loading** (never visible: it's local) and **Healthy** apply. The rest can't happen: the clock has no source.

### Weather

**Sources:** a configurable provider (the dogfood's keys are placeholders, so it starts fresh).

| Size | Shows |
|---|---|
| Glyph | Temperature |
| Strip | Temperature · condition · high/low |
| Small | Temperature, condition, high/low |
| Wide | Now plus 6 hours |

| State | Treatment |
|---|---|
| Loading | Skeleton |
| Empty | "Set a location to see the weather here" (first run) |
| Healthy | Values |
| Attention | ▲ provider warning (storm, frost) |
| Down | Provider down: last forecast, hatched |
| Stale | Hatch, "as of" |
| Needs repair | API key rejected |
| Permission-limited | — |
| Maintenance · off | — |

---

## Page-level components

These aren't resizable widgets, but they're in the Workbench because they have states too.

| Component | States to render |
|---|---|
| **Briefing** (sentence and headline styles) | All quiet · needs you (attention) · down leads · first run ("found 11 services") · hub offline ("Last known at 10:02") · loading (rare: snapshot inlined). Every phrase is a lens |
| **Needs attention** | Hidden when empty · one attention strip · attention plus down (down first) · stale (hatched) · collapsing away on the `move` spring when cleared |
| **Inbox** | Empty ("Nothing needs you") · repair: idle → in progress (PIN, spec diff) → done → collapses · repair failed · discovery with a real-data tile preview · suggestion · hub offline (read-only copy) |
| **Sheet** (expand in place) | Overview · placeholder tabs · actions by tier (T0–T4) · permission-limited actions · phone bottom card with momentum dismiss |
| **Hub offline** | Banner, every tile stale, actions disabled, Retry now |
| **First run** | Setup cards (import, PVE recipe, Unraid consent, Traefik) · discovered tiles with checks · locked tile needing a key · empty weather |
| **Command bar** | Results grouped · no matches |

## Acceptance checks

A cell is done when:
1. It renders from a Demo Home fixture (simulator snapshot), not hand-written markup.
2. It passes in **all six themes** (Instrument Dark and Light, OLED, Night, High Contrast, Paper) and both **card and ruled** surfaces. The token checks guarantee contrast; the cell must not hard-code colours.
3. **Status is glyph + word + colour.** In Night it must still read with colour removed.
4. **Never a fake zero:** unknown is "—"; stale shows the last-known value with "as of".
5. **No spinners, no infinite animation,** and nothing pops in one at a time.
6. Text fits at both densities; numbers use tabular figures; units are smaller and secondary.
7. axe passes, and the widget has a semantic structure (heading, value, context) plus a text summary for any chart.
8. A visual-regression snapshot exists for: every state at the widget's default size in all six themes; every size in the healthy state in Instrument Dark and Paper; and the simplified level for wall widgets in Night.

## Open questions for review

1. **Backups (PBS)** as its own widget vs. a variant of the storage bar. It's separate here because it carries task results and its own sentence.
2. **Router** as its own widget vs. a service tile. Separate here because WAN down outranks everything.
3. **Fleet Tall** (○) may be cut if phone layouts never derive it.
4. The **Strip 8×1** variant is only listed for the Briefing-like widgets; is it worth a second layout everywhere?
