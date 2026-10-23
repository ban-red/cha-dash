# 01 · Vision & Product

> **Cha Dash** (`dash.cha.sh`) · binaries `chadash` (hub) and `chadash-node` · AGPL-3.0.
> Decisions so far are recorded in [04 · Roadmap → Decisions](04-roadmap.md#decisions). The real homelab we test against first is described in [05 · Dogfood home](05-dogfood-home.md).
> This is a brainstorm-stage plan. Anything marked **(?)** is still open for discussion.

---

## The one-liner

**The front page of your home.**
One calm, beautiful page that tells you everything is fine, and lets you fix things safely when they aren't.

## The thesis: three bets

Each bet targets a gap that today's homelab tools leave open.

### 1. Model the home as one connected system: the Home Graph

Today a dashboard is a grid of unrelated widgets, each with its own URL and API key. Cha Dash builds a single graph of the home:

- **Layers:** hosts → VMs/LXCs → container engines → stacks → containers → services (URLs) → disks, pools and shares → backup jobs → network devices.
- **Sources:** Proxmox, Unraid, Docker, reverse proxies, backup tools and the apps themselves.

Integrations *contribute facts* to the graph. Widgets are *views* of it. That unlocks things no tile grid can do:

- A Sonarr tile can also show its container's health, the host it runs on, whether an update is pending, and whether its appdata is backed up. You configure none of that.
- "Reboot this host" can show exactly what will go down before you confirm.
- "Sonarr is failing" can say *why*: "qBittorrent's container stopped 4 minutes ago."
- "Is everything backed up?" has an answer.

### 2. Act from the page, safely

Read-only dashboards get abandoned ("why not just bookmarks?"). Cha Dash lets you act, with friction proportional to blast radius:

- **Reversible** actions offer undo.
- **Disruptive** actions need a hold-to-confirm that shows what will be affected.
- **Destructive** actions need you to type the name and re-authenticate.

Every action goes through **one registry**, so buttons, ⌘K, the API, mobile and (later) AI agents all behave the same way.

Every action is scoped, audited and reversible where possible.

### 3. Calm design wins

The scarcest resource in a homelab is the owner's attention. Cha Dash is **quiet when healthy**: green walls of status lights fade back, and problems rise in color, position and prominence. The page answers "is everything OK?" in one second and "what's wrong and how do I fix it?" in two taps.

### What makes the bets possible

- **`docker compose up -d` and you're running.** Docker Compose is the main way to self-host Cha Dash: one small image, no required environment variables. It's light enough for a Raspberry Pi.
- **Cha Dash owns your stacks.** Compose files live on disk on the host they run on, with version history. Existing projects can be imported. A thin **node** on each Docker server gives the hub control without opening any ports.
- **Config as both GUI and file**: edit in the GUI or in YAML, and they stay in sync.
- **Phone and wall display as first-class targets**, not afterthoughts.
- **Security as a visible feature**, not hidden plumbing.

---

## Who it's for

| Persona | Setup | What they want | What fails them today |
|---|---|---|---|
| **Sam, the Tinkerer** (our [dogfood home](05-dogfood-home.md) fits this persona) | Several standalone Proxmox hosts plus a small cluster, Unraid, PBS, a Docker LXC, OPNsense on a VM, Home Assistant and Frigate | One place for everything; multiple hosts without exposing sockets; real control | Their dashboard is read-only and every widget looks equally important; there's a different tool for each layer |
| **Riley, the Unraid owner** | One Unraid box, 35 containers, 2 VMs | Pretty, simple, works great on the phone, hard to break things by accident | The Unraid UI is dated; dashboards are YAML chores or RAM hogs |
| **The Household** | Partners, kids, parents, roommates | "Is Plex down?", request a show, guest Wi-Fi, "is it the internet or us?" | Admin dashboards are scary; nothing is made for them |
| **Couch on-call** (any of the above) | A phone, away from the desk | A useful push, a clear diagnosis, and a fix in two taps | Mobile UIs are an afterthought; alerts are noisy |

### Non-goals

These are deliberate. Saying no keeps the product sharp.

- **Not an enterprise or Kubernetes fleet manager.** Homelab scale means 1–20 hosts and up to a few hundred containers.
- **Not a Grafana or Prometheus replacement.** We keep lightweight history and integrate with Prometheus when it's already there.
- **Not a full replacement for the PVE, Unraid or TrueNAS UIs.** We cover the 80% of daily operations and deep-link precisely into native UIs for the rest. See the control line below.
- **Not a backup engine, log warehouse or reverse proxy.** We observe and orchestrate the tools you already run.
- **No cloud dependency.** It works fully offline on a LAN, with no account, and no telemetry unless you opt in.
- **Not a native app, for now.** The installable PWA, served by your compose-hosted hub, is the mobile client: Home Screen install, Web Push and passkeys.

---

## Jobs to be done

| Job | What "great" looks like in Cha Dash |
|---|---|
| **Glance**: "Is everything OK?" | The status sentence at the top of the page answers in under a second. A healthy page is visually quiet. |
| **Launch**: "Take me to X" | ⌘K, type 3 letters, Enter. Or tap the tile. Links to services are discovered, not typed. |
| **Triage**: "Something's wrong, what and why?" | The problem rises to the top with a symptom-first sentence, a likely cause from the graph, and a timeline of what changed. Logs and metrics open in place. |
| **Fix**: "Make it work again" | The right action sits next to the problem (restart, roll back, re-auth), with safety proportional to risk and undo where possible. |
| **Maintain**: "Keep things updated and safe" | The Update Center stages updates in dependency order with release notes, takes a snapshot first, and rolls back automatically if health checks fail. Backup coverage shows the gaps. |
| **Change**: "Deploy or adjust a stack" | Edit compose files with a diff preview, deploy with streamed progress, and keep a git-friendly history. |
| **Household**: "Is it working? Can I…?" | A friendly family page with media, requests, guest Wi-Fi, "report a problem", and a few safe buttons. |
| **Ambient**: "Show me without my asking" | A wall display that is calm art when healthy and speaks up only when needed. Night tint and burn-in care included. |
| **Away**: "Tell me, let me act from my phone" | Symptom-only push notifications. One-tap diagnosis and an approval flow. |

---

## Product principles

These are tie-breakers when we disagree.

1. **Status first, then control, then links.** Bookmarks are the least valuable thing on the page.
2. **Healthy is quiet.** Color and motion are reserved for change and for problems.
3. **Desired state beats current state.** A container you *meant* to stop is not a problem. A problem is the gap between what should be and what is.
4. **Every number has context:** unit, normal range, threshold, trend and freshness. A bare "73%" is never acceptable.
5. **Never fake freshness.** Show the last-known value plus "as of", and make staleness visible. Never hide a value behind a spinner.
6. **Value before configuration.** Discovery fills the page within minutes of `docker run`. An empty first-run page is a failure.
7. **Front page, not cockpit.** Do the daily 80% beautifully and deep-link the rest precisely.
8. **Friction scales with blast radius.** Use undo for reversible actions, hold-to-confirm for disruptive ones, and typed confirmation plus re-auth for destructive ones.
9. **One action registry.** Every surface (tile, ⌘K, API, MCP, mobile) invokes the same typed, scoped and audited actions.
10. **One layout, derived everywhere.** You design once; phone, tablet and wall layouts follow automatically, with optional overrides.
11. **Your config is a file you own.** Export, diff, version and import, with no lock-in.
12. **Secure and read-only by default.** Control is opt-in per integration and per scope. Secrets never reach the browser.
13. **Respect the room.** Desk, phone, wall, TV and e-ink are first-class contexts.
14. **Light enough for a Pi.** Footprint is a feature.

## The Pact: public promises

These go in the README and are treated as product requirements.

- **Open source (AGPL-3.0), built in the open.** Contributions come in under an [OSI-limited CLA](../../CLA.md). Contributors keep their copyright, and their code can only ever be released under open-source licences.
- **Your secrets never leave the server.** Credentials are encrypted at rest and redacted in exports and diagnostics.
- **Read-only until you say otherwise.** Every control capability is an explicit, scoped opt-in.
- **No cloud and no phoning home.** Telemetry is off unless you opt in. Fonts, icons and assets are bundled.
- **Your config is yours.** You can export it as YAML at any time.
- **Small and simple to host.** One `docker compose up -d`. One container, under 100 MB of RAM at typical homelab scale, and no required configuration.
- **Accessible.** WCAG 2.2 AA as a release gate.

---

## The control line: what we do natively vs. deep-link

| Domain | Cha Dash does natively | Cha Dash deep-links to the native UI |
|---|---|---|
| **Docker** | Lifecycle, logs, exec, stats, image updates, compose edit and deploy, prune with preview | Building images, registry management, advanced network surgery |
| **Proxmox** | Power, snapshots, vzdump now, tasks, console (proxied), migrate, guest/node/storage status, updates | Creating VMs, hardware edits, SDN, Ceph admin, HA configuration, permissions |
| **Unraid** | Array start/stop, parity, disks and temperatures, shares usage, Docker, VMs, notifications, flash backup status, OS and plugin updates | Disk assignment, share configuration, plugin settings |
| **TrueNAS** | Pools, scrubs, alerts, apps, VMs, snapshots, replication run | Dataset and ACL configuration, sharing services |
| **Backups** | Coverage map, last success and last verify, run now, prune and verify jobs | Restore workflows (v1 deep-links), repository configuration |
| **Network** | WAN status, devices and clients summary, DNS blocking toggle, device restart | Firewall rules, VLANs, Wi-Fi configuration |

Deep links must land on the **exact** page: the specific VM, container or disk. A link to the native UI's home page doesn't count.

---

## Feature map (brainstorm)

Tags indicate the target milestone. See [04 · Roadmap](04-roadmap.md).

### Front page and layout
- Boards: one page per context. Desk, phone and tablet layouts are *derived* from one canonical layout. Wall and Family are optional extra boards. `v0.1`
- Sections that keep their column structure when the layout reflows. `v0.1`
- Edit in place, a lifted edit mode, size snapping, keyboard editing, undo and layout history. `v0.1`
- **Lenses:** click a phrase in the status sentence, a host or a tag, and the page filters in place to that context without navigating away. `v0.2`
- Per-device overrides and "pin to top on mobile". `v0.2`

### The Briefing
- **Status sentence**, for example: "All 31 services healthy · Array 71% · 2 updates · Backups OK". Every phrase is a lens. `v0.1`
- **Pulse strip**: a 24-hour whole-home health band. Scrubbing it time-travels the entire page. `v0.3`
- Morning digest: weather, calendar, overnight events, what auto-healed, what needs you. `v0.4`
- **Release radar**: new releases *of the apps you actually run*, derived from your container images, with changelogs. `v0.4`
- Feeds (RSS, HN, GitHub releases, Reddit) for a page you open every day. Kept secondary. `v1.0`

### Discovery and Inbox
- Docker label discovery: Homepage `homepage.*` labels read **as-is**, plus our `chadash.*` namespace. `v0.1`
- Discovery from Proxmox guests, Unraid containers and VMs, and the **Traefik API** (routers → service URLs) `v0.1`. Caddy admin API and Nginx Proxy Manager follow in `v0.3`.
- **The Inbox**, one triaged queue for:
  - Discoveries: "7 new services found: add, ignore, or map to an existing tile". `v0.1`
  - Repairs: "Pi-hole credentials expired: reconnect". `v0.1`
  - Update proposals and unprotected resources. `v0.4`
  - New nodes awaiting approval. `v0.3`
- **Homepage importer**: services.yaml, widgets and labels mapped to Cha Dash in under a minute. `v0.1`
- **More importers**, each named after its source: Homarr and plain YAML. `v0.2`

### Home Graph and Atlas
- A unified resource graph with identity resolution that merges the same container seen by Unraid *and* Docker *and* Traefik. `v0.1` (basic), `v0.3` (full)
- Relations contributed by integrations, for example Sonarr → qBittorrent from Sonarr's own download-client config. `v0.3`
- **Root-cause hints**: "Sonarr unhealthy, probably because qBittorrent stopped at 03:12". `v0.3`
- **Atlas**: a canvas view with toggleable layers (compute, network, storage, backups, dependencies), available as a Hero widget or a full-screen lens. `v0.3`
- **Hub self-awareness**: the graph knows which host runs Cha Dash, and warns "this action will also take Cha Dash offline". `v0.2`

### Actions and control
- Action engine: a typed registry, risk tiers, scopes, undo, hold-to-confirm, typed confirmation, step-up auth, and an append-only audit log. `v0.2`
- Docker: start, stop, restart, pause, recreate, remove (with confirmation), logs, exec, image pull. `v0.2`
- Proxmox: power, snapshots (create, rollback, delete), vzdump now, live task progress, migrate, proxied console. `v0.2–v0.3`
- Unraid: Docker and VM power, container updates, parity check start/pause/cancel, array start/stop (T3), notification archive. `v0.2`
- App actions: pause Pi-hole or AdGuard, pause or resume downloads, trigger *arr searches, approve or decline media requests, run a speed test, trigger Home Assistant scripts. `v0.2`
- **Staged operations** (Railway-style): queue several changes, review them, apply them together, and watch progress. `v0.4`
- Scheduled actions and simple rules ("every Sunday 4am: prune images on all hosts"). `post-1.0`

### Stacks and deploy: Cha Dash owns them
- **Managed stacks:**
  - Compose files live **on disk, on the host they run on**, at `<stacks root>/<name>/compose.yaml`, never hidden in a database.
  - Each stacks root is a **git repo that Cha Dash commits to on every deploy**. That gives you history, diffs and one-click rollback for free.
  - Rollout: the hub's own Docker host `v0.2`; every other Docker server via `chadash-node` `v0.3`.
- **Compose editor:**
  - Validates against compose-spec.
  - Shows a diff of the rendered config against what's currently running.
  - Keeps secrets as `${secret:…}` references.
  - Deploys with streamed pull and up progress, waits for health checks, and rolls back to the previous commit if they fail. `v0.2`
- **Import and adopt.** Bringing existing work under management is a first-class flow, not an afterthought:
  - Running compose projects are detected from labels (`com.docker.compose.project.working_dir` / `config_files`). You can **adopt in place** (keep the path) or **move into the managed root**. `v0.2–v0.3`
  - Dockge stacks directories (same layout as ours) and Portainer stacks. `v0.3`
  - **Unraid stays Unraid.** Containers that Unraid's own Docker UI manages stay that way and are controlled through the Unraid API. An optional thin node on Unraid (`v0.3+`) adds host metrics, logs, exec, SMART and image-update checks alongside the API. It never takes ownership of Unraid's containers.
- **Self-update.** On the hub's own host, the node updates the Cha Dash stack itself, so the hub can restart safely partway through. `v0.3`
- Optional git remote for stacks: push to your own git, or pull and redeploy from a signed webhook, through the same health-gated deploy. `v0.3`
- No template catalog: stacks arrive by adopting what you run or writing compose ([04 · Roadmap → Not planned](04-roadmap.md#not-planned)).

### Self-hosting Cha Dash
- **Docker Compose is the main install path.** Every install doc starts with a `compose.yaml`. `v0.1`
  - The hub ships with an optional, pre-tuned `socket-proxy` sidecar (read-only, plus start/stop/restart).
  - It needs zero required environment variables.
- Adding a node is also a compose snippet. The hub generates it with a single-use join token already filled in. `v0.3`
- Secondary channels:
  - Unraid Community Apps template (`v0.1`, since many users, including us, run Unraid)
  - binaries + systemd (`v0.1`)
  - TrueNAS community app (`v0.3`)
  - Helm and Umbrel (`v1.0`)

### Home (the "home" in front page of your home)
- **Home Assistant:** people home, lights and switches on, picked entities as tiles, **template-rendered stats** via HA's template API (e.g. "switches on"), and **power and energy** (total power, energy today). Read-only in `v0.1`; triggering scripts comes in `v0.2`.
- **Frigate:** cameras, recent detections with snapshots ("side deck · person 79% · 19:14"), uptime and version. `v0.1`
- **Media calendar:** upcoming episodes and movies from Sonarr and Radarr. `v0.1`
- Clock and weather. `v0.1`
- Family board `v0.2`; feeds `v1.0` (see Briefing).

### Update Center
- One list covering container images (registry digest checks), Unraid containers, OS and plugins, PVE apt, TrueNAS apps, and node hosts' OS packages. `v0.4`
- Risk classification:
  - semver major, minor or patch;
  - pinned versus floating tags;
  - changelogs from `org.opencontainers.image.source` → GitHub releases.
- **Safe updates:**
  1. Pull first.
  2. Snapshot (the PVE guest, a ZFS snapshot, or a volume copy).
  3. Restart in dependency order.
  4. Wait for health to pass.
  5. Roll back automatically to the previous digest if it doesn't. `v0.4`

### Backups and assurance
- Sources: PBS, PVE `/cluster/backup-info/not-backed-up`, Backrest, Kopia, Duplicati, borg-ui, Zerobyte, Unraid flash backup, TrueNAS replication and snapshots. `v0.4`
- **A coverage map**: every VM, LXC, volume and appdata path, marked protected, stale or unprotected, with last success and last verify. `v0.4`
- A freshness objective per resource ("daily"); missing it triggers a symptom alert. `v0.4`
- Run now, verify now. `v0.4`

### Health, alerts and notifications
- Automatic HTTP/TCP/ICMP checks for every discovered service URL, with uptime history. `v0.1`
- Symptom-first alert defaults:
  - a service unreachable for more than 2 minutes;
  - array degraded;
  - SMART failing;
  - backup objective missed;
  - update failed;
  - certificate expiring within 7 days;
  - disk more than 90% full and trending up.
  
  With flap damping. `v0.2`
- Channels: Web Push (PWA), ntfy, Apprise, email, webhook, Home Assistant notify. `v0.2`
- Maintenance windows (silence plus a banner, also shown on the Family board). `v0.4`
- An **incident timeline** built automatically from correlated events. `v0.4`
- A PVE notification webhook receiver, which gives near-realtime backup events. `v0.4`

### Command bar (⌘K)
- Search the whole graph: services, containers, VMs, hosts, settings and docs. `v0.1`
- Actions on the selected item: Enter for the primary action, ⌘Enter for the secondary, Tab to list all actions, `>` for commands, and `!bang` for web search. `v0.2`
- Every action shows its keyboard shortcut, Raycast-style.

### People and access
- Owner, Admin, Operator, Viewer and Household roles. Per-action scopes, optionally per resource. `v0.2`
- Passkeys and TOTP `v0.1`. OIDC (Authelia, Authentik, Pocket ID, Keycloak, Kanidm) `v0.3`. Forward-auth behind trusted proxies `v0.3`.
- **Family board**: friendly status ("Movies are working ✓"), media, requests, guest Wi-Fi QR code, "report a problem", and a few whitelisted safe buttons. `v0.2`
- **Kiosk tokens**: revocable, board-bound device tokens for wall tablets, with no full login on shared devices. `v0.2`

### Contexts
- PWA: installable, offline last-known state, Web Push, thumb-zone action bar. `v0.1–v0.2`
- Wall/kiosk mode: wake lock, auto-dim (scheduled, by sun position, or from a Home Assistant lux sensor), night red tint, burn-in shift, page rotation. `v1.0`
- TV / 10-foot: D-pad focus and the simplified level of detail. `v1.0`
- E-ink: an "ink" render mode and TRMNL BYOS compatibility (?). `post-1.0`

### Configuration
- The database is authoritative. YAML export and import with a diff preview and secret references (`${secret:pihole}`). `v0.1`
- Git sync: commit on change and apply from the repo with a diff. `v1.0`
- Schema-driven forms for every integration, with "Test connection" and a least-privilege recipe generator, for example "Create a PVE token with exactly these privileges". `v0.1`
- **Unraid one-click connect** via its `ApiKeyAuthorize` consent flow. `v0.1`

### Extensibility
- Tier 0: built-in Go integrations with fixtures and quality tiers. `v0.1`
- Tier 1: **declarative integration specs** (YAML).
  - In `v0.1` the engine is used **internally**, for simple read-only apps like Mealie, Linkding, Karakeep, Bazarr and Tautulli. That proves the format on real services.
  - The public spec format, community repository and "Workbench" gallery come in `v1.0`.
- Tier 2: WASM plugins with allowlisted host functions. `post-1.0`
- A public, versioned API (OpenAPI) with scoped API tokens. `v0.2`
- An **MCP server** where agents read the graph and *propose* actions; humans approve them from their phone. `post-1.0`

---

## Signature experiences

These are the moments that make people screenshot it and tell a friend.

1. **The sentence.** Opening Cha Dash shows a single line of calm prose: *"All 31 services healthy · Array 71% · 2 updates · Backups OK."* When something breaks, the sentence changes first: *"Immich is down since 14:02 · likely: its database container restarted 3× ·* **[Fix]***"*. Each phrase is a lens.

2. **Quiet when healthy.** A healthy page settles to hairlines, monochrome logos and tabular numerals. A problem lifts its tile toward the top, gives it the one signal color, and colors its logo in. On a wall it's ambient art that only speaks when it needs to.

3. **Expand in place.** Tapping any tile grows it from where it sits into a detail sheet (overview, logs, metrics, actions, related, history). It can be interrupted mid-gesture, dismissed with a flick, and has its own URL.

4. **⌘K that acts.** Typing `rest jel` and pressing Enter restarts Jellyfin, with an Undo toast for 10 seconds. ⌘Enter opens the logs. Tab shows everything else it can do.

5. **Blast-radius confirm.** Holding "Reboot pluto" fills a ring while a list builds beneath it: *"Takes down **OPNsense, your router**. The whole network goes offline, including your connection to Cha Dash. The reboot will continue on its own."* Holding "Reboot jupiter" shows instead: *"Stops 24 containers · Plex, Immich and Mealie are used by the household · PBS backups will pause."* Let go and nothing happens. The graph is what makes this possible.

6. **The Inbox.** New things, broken things and suggestions arrive here, never as silent changes to your page: *"New container `paperless` found → Add tile (preview shown) · Ignore."*

7. **"Why?" answers.** Hovering a failing service shows the graph's best guess at the cause, with evidence: *"qBittorrent stopped at 03:12 (exit 137, out of memory) · Sonarr began failing at 03:13."*

8. **Safe Sunday updates.** *"6 updates ready. 1 is a major version (Immich 3→4, breaking changes ▸). Snapshot first · update in dependency order · roll back if unhealthy."* One button, one live progress strip with a clear end.

9. **"Everything is backed up."** Or rather, *"41 of 44 protected · 3 unprotected: `paperless` volume, `vm-108`, `/mnt/user/photos`."* One tap opens a fix wizard.

10. **Linked scrubbing.** Pressing and dragging on any sparkline makes every chart on the page show its value at that moment. *What else happened at 03:12?*

11. **The Family board.** Your partner sees "Movies are working ✓", what's playing, a request button, the guest Wi-Fi QR code, and "Something's wrong" (which pings you). It's a home page in the original sense.

12. **Phone approvals.** Push notification: *"Immich update failed health check → rolled back automatically. Tap for details."* Or, later: *"An agent proposes restarting Sonarr. Approve?"* Approved with a passkey (Face ID or Touch ID) right in the PWA.

---

## Moonshots (post-1.0, to keep the north star in view)

- **Proxmox deploy:** PVE 9.1+ can create LXCs from OCI images, so "deploy this image as an LXC" from Cha Dash.
- **Power and cost:** UPS, smart plugs and BMC readings turned into watts and €/month per host and per service.
- **Multi-site:** your parents' house as a second "home" behind its own node.
- **Explain this** (optional, bring your own LLM, local first): plain-language incident summaries and log explanations.
- **Config time machine:** browse and restore any past state of your layout, config or stacks.
- **TRMNL / e-ink** BYOS server built in.
