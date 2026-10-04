# 04 · Roadmap, Decisions & Risks

> Milestones are defined by what they achieve and how we know they're done, not by dates. Each one ends with a **demo moment**: the thing we post on r/selfhosted.
> **Dogfood rule:** every milestone has to work on the [owner's real homelab](05-dogfood-home.md) before it ships.

---

## Decisions

### Decided ✅

| # | Decision | Outcome | Notes |
|---|---|---|---|
| 1 | **Name** | **Cha Dash** · domain `dash.cha.sh` | Slugs: `chadash` (hub), `chadash-node` (node). Docker labels: `chadash.*`. Env prefix: `CHADASH_*` |
| 2 | **License** | **AGPL-3.0-or-later** for hub and node; Apache-2.0 for the spec schema, design tokens and icon pipeline. **Contributions: an [OSI-limited CLA](../../CLA.md)** signed via the CLA Assistant bot | Contributors keep their copyright. We may relicense, but **only under OSI-approved licences**, which keeps the option of a friendlier licence for institutions or dual licensing. Contributions can never be distributed under proprietary terms |
| 3 | **Frontend** | **Vue 3.6** (RC now, stable when released) + **latest Vite**, as a plain SPA embedded in the Go binary | Vapor mode opt-in per component, adopted only where measurements show a gain. See [03 · Architecture → Tech stack](03-architecture.md#tech-stack) |
| 4 | **Dogfood** | The owner's homelab | See [05 · Dogfood home](05-dogfood-home.md). It sets the v0.1 integration list |
| 5 | **How much "home" in v0.1** | Infrastructure first, plus the home integrations the owner actually uses: **Home Assistant, Frigate, media calendar, clock/weather**. The Family board ships in v0.2; feeds in v1.0 | — |
| 6 | **Stacks** | **Cha Dash owns them.** Files live on disk on their host, in a git-versioned stacks root. Adopt and import are first-class. A **thin node** runs on each Docker server | The node is the only component that ever writes to Docker. The hub stays unprivileged |
| 7 | **Self-hosting** | **Docker Compose is the main install method.** Plain `docker compose up -d` with zero required env vars; nodes are added with a compose snippet too | Unraid CA, binaries, TrueNAS and Helm are secondary channels |
| 8 | **Repo and images** | **[github.com/ban-red/cha-dash](https://github.com/ban-red/cha-dash)** (public; renamed to lowercase on 2026-10-03). Images: `ghcr.io/ban-red/chadash` and `ghcr.io/ban-red/chadash-node` | The rename happened **before** any Unraid CA submission, so there's no blacklist risk, and GitHub redirects the old URL. **No more renames or transfers once it's submitted to CA** |
| 9 | **Default port** | **3696** (hub HTTP and the node WebSocket endpoint `/node`) | Avoids the ports other self-hosted dashboards commonly default to (3000, 7575, 8080) |
| 10 | **Unraid** | **Unraid stays Unraid.** Containers stay Unraid-managed and are controlled through the Unraid API; nothing is converted to compose. An **optional thin node on Unraid** (v0.3+) adds metrics, logs, SMART and update checks alongside the API | Owned stacks target compose hosts such as pvedocker |
| 11 | **Mobile** | **The PWA is the mobile client.** It's served by the compose-hosted hub; no native app for now | Home Screen install, Web Push and passkey approvals cover the phone |
| 12 | **Visual direction** | **A · Instrument, built to be very themeable.** A's structure is fixed; every visual is a theme token. B lives on as the built-in **Paper** theme plus an optional `headline` Briefing style | See [02 · Experience → Theming](02-experience.md#theming). Prototype v2 demos the theme engine |
| 13 | **Grid engine** | **Build our own:** `@chadash/layout` + `@chadash/layout-vue` | The spike 2.4 comparison with gridstack 14 and dnd-kit/dom. [ADR-0013](../adr/0013-grid-engine.md) |
| 14 | **Node transport and identity** | **Mutual TLS 1.3 inside the WebSocket**, Ed25519 keys pinned at enrollment, no CA | Works behind TLS-terminating proxies; post-quantum key exchange from the standard library. [ADR-0014](../adr/0014-node-transport.md) |
| 15 | **SQLite driver** | **`ncruces/go-sqlite3`**; Adiantum encryption at rest as an opt-in | Level or faster than modernc on the hub's queries, fewer modules; encryption costs ~4× on writes and makes a lost key fatal. [ADR-0015](../adr/0015-sqlite-driver.md) |
| 16 | **Hardware floor** | **amd64 and arm64 fully supported, armv7 best-effort, 512 MB minimum** | Hub ≤ 55 MB with 20 tabs, node ≤ 32 MB, image ~6 MB. [ADR-0016](../adr/0016-hardware-floor.md) |
| 17 | **AI/MCP stance** | **Agents read and propose; humans approve every write.** Off by default; MCP after 1.0 | The propose API is designed now. [ADR-0017](../adr/0017-ai-and-mcp.md) |
| 18 | **Go module path** | **Custom import path `dash.cha.sh`** (`module dash.cha.sh`) | `dash.cha.sh` must serve a `go-import` meta tag. See [docs/infra/vanity-import.md](../infra/vanity-import.md). Local builds work before that's live |
| 19 | **JavaScript tooling** | **Bun** for packages, workspaces and scripts; `bun test` for plain TypeScript packages | Vite stays the dev server and bundler; Vue component tests use Vitest launched through Bun. [ADR-0019](../adr/0019-bun-for-javascript-tooling.md) |
| 20 | **Node deploys** | **Compose SDK + go-git in the node:** hash plan, dry-run preflight, `up --wait` plus a restart-aware soak, rollback as a new commit | Validated by spike 4.6. [ADR-0020](../adr/0020-node-deploys-with-compose-sdk-and-go-git.md) |

### Still open

None right now. New open questions get a row here, and an ADR when they're settled.

---

## Milestones at a glance

| Milestone | Theme | Outcome |
|---|---|---|
| **M0** | Foundations | Open decisions settled, design direction picked, the layout engine feels right, Demo Home running |
| **v0.1** | Front Page | A beautiful, live, read-only front page that **replaces the owner's Homepage** |
| **v0.2** | Control | Safe actions on Docker, Proxmox and Unraid; **a local node and owned stacks on the hub's host**; audit, roles, notifications |
| **v0.3** | Fleet | **Remote nodes on every Docker server**; stacks fleet-wide; history, the Atlas, cause hints |
| **v0.4** | Care | Update Center with safe rollouts, backup assurance, incident timeline |
| **v1.0** | Home | Public declarative integrations, TrueNAS, wall and TV modes, i18n, security review, stable config and API |
| **Post-1.0** | Beyond | WASM plugins, MCP with approvals, multi-site, GitOps |

---

## M0: Foundations

**Decisions:** ✅ 13–17 settled and accepted on 2026-10-03, each written up as an ADR in [`docs/adr/`](../adr/README.md).

**Design spike**
- Build static prototypes of **Direction A (Instrument)** and **Direction B (Paper)**, both rendering the **dogfood home's real shape** with sample values.
- Each prototype shows:
  - the front page, healthy and with today's real incidents (PBS, Plex, Karakeep)
  - one widget in all 7 sizes, in every state
  - the phone layout
  - the wall and night modes
- Pick one direction and produce token v0: color scales, type, spacing, motion.

**Layout engine spike:** `packages/layout` (pure TS) plus a Vue 3.6 renderer using Motion for Vue. It must prove:
- drag with momentum projection
- snapping only to supported sizes
- sections that reflow in Z-order
- a phone layout derived automatically
- keyboard editing
- undo
- 60 fps on a mid-range phone

Compare it for one day against a gridstack 14 prototype.

**Plumbing spikes**
- Multiple PVE instances plus a cluster: privilege-separated token reads of `/cluster/resources`.
- Unraid GraphQL subscriptions and the `ApiKeyAuthorize` consent flow.
- Docker reads through a socket proxy (local and remote TCP).
- Traefik API router discovery, including path-based routers.
- OPNsense SSE traffic stream.
- Frigate events and snapshots.
- **Compose SDK + go-git** in a node prototype: deploy, diff, roll back.
- One multiplexed WebSocket carrying a snapshot followed by diffs.

**Repo and process**
- Skeleton, `deploy/compose.yaml`.
- CI: lint, test, govulncheck, bundle budget.
- ~~CLA bot~~ ✅ (CLA Assistant, pinned; signatures on the `cla-signatures` branch), CODEOWNERS, contributor guide v1.

**Demo Home simulator v0**, seeded from the dogfood home's shape, with its real incidents scripted.

**Done when:** a clickable prototype on Demo Home feels right on desktop and phone, and all ADRs are written.

**Demo moment:** a 20-second recording of editing the layout while the PBS incident rises to the top.

---

## v0.1: Front Page (read-only, beautiful, live)

**Platform**
- **Install with `docker compose up -d`**: hub plus an optional read-only socket-proxy sidecar.
- First run: setup token → owner account → passkey.
- SQLite and vault; WebSocket updates with the snapshot inlined into the first paint.
- Unraid CA template and binaries as secondary channels.

**Experience**
- Boards and sections; edit in place plus edit mode; layout history and undo.
- **Status sentence**; a **Needs attention** section that appears automatically.
- **Inbox**: discoveries and **repairs**.
- ⌘K search and open.
- Light, dark and OLED themes.
- Installable PWA that shows last-known state when offline.

**Widgets**
- Service tile, with a live stat and context from the graph
- **Fleet card** (multiple PVE instances + Unraid + Docker hosts), host card, guest list
- Container list, Unraid array, storage bar
- **Camera Hero** (Frigate) and HA summary
- Media calendar, now playing, download queue
- Uptime check, metric stat and sparkline
- Bookmarks, clock and weather

**Integrations (read):** the [dogfood set](05-dogfood-home.md#v01-integration-set-derived-from-this-home).
- **Tier 0:**
  - Infrastructure: Proxmox VE (multiple instances, clusters), PBS, Unraid, Docker via socket-proxy, Traefik, OPNsense, Uptime Kuma
  - Home: Home Assistant, Frigate
  - Media and downloads: Plex (PIN auth), Sonarr, Radarr, Prowlarr, qBittorrent
  - Apps: Immich
  - Built-in checks
- **Internal specs:** Tautulli, Bazarr, Mealie, Linkding, Karakeep.
- **Breadth:** Jellyfin, Pi-hole, AdGuard, SABnzbd.

**Config**
- YAML export and import with a diff preview.
- **Homepage importer**, tested on the owner's real config. It's the first of several importers, each named after its source.
- Generator for least-privilege tokens (one per PVE instance).

**Done when**
- The **owner switches off their Homepage**.
- A fresh install goes from `docker compose up -d` to a populated, live page in **under 3 minutes**, with no YAML written.
- Performance budgets are met and WCAG AA checks pass.
- 10 external beta users run it every day.

**Demo moment:** "I pointed it at my 5 Proxmox boxes, my Unraid and Traefik, and it built this page by itself."

---

## v0.2: Control (plus owned stacks on the hub's host)

**Engine and security**
- Action engine: registry, tiers, scopes, undo, plan/approve/execute, per-resource locks.
- **Audit log.**
- Roles: Owner, Admin, Operator, Viewer, Household.
- Step-up auth.
- Scoped API tokens and a public OpenAPI.

**Local node:** `chadash-node` runs in the **same compose file** as the hub and replaces the socket-proxy for that host. From here on, all Docker writes go through a node.

**Owned stacks (on the hub's host)**
- Git-versioned stacks root.
- Compose editor with validation and a diff against what's running.
- Deploy pipeline: pull → up → health gate → commit, or roll back.
- **Adopt existing compose projects in place.**

**Actions**
- **Docker:** start, stop, restart, recreate, pull, remove (T3), logs, opt-in exec.
- **Proxmox:** guest power, snapshots, run vzdump now, live task progress. Works across all instances.
- **Unraid:** Docker and VM power, container updates, parity, array (T3).
- **Apps:** pause and resume qBittorrent, *arr searches, HA scripts, Pi-hole/AdGuard pause, Seerr approve.

**Experience**
- Safety ladder UI: undo, hold to confirm with a blast-radius preview (**"Reboot pluto takes down the router"**), typed confirmation.
- ⌘K actions, **lenses**, and hub self-awareness.
- **Homarr and plain-YAML importers**, on the same per-source pipeline as the Homepage importer.

**Notifications:** symptom-first defaults; Web Push, ntfy, Apprise, webhook, email, HA.

**People:** OIDC, forward-auth behind trusted proxies, **Family board**, kiosk tokens.

**Done when**
- Every action is audited, authorized centrally (tested in CI), and gets the correct tier UX.
- The owner manages the hub host's stacks from Cha Dash, including one deliberately bad deploy that rolls back on its own.

**Demo moment:** a hold-to-reboot ring warning that the whole network is about to go down.

---

## v0.3: Fleet (remote nodes everywhere)

**Remote nodes**
- Enrollment: "Add node" generates a compose snippet with a single-use token; keys are pinned; an admin approves in the Inbox.
- A local policy file on each node.
- Runs inside **Proxmox LXCs** such as pvedocker. It can also run **on Unraid** (CA template) as an optional companion to the Unraid API: metrics, logs, SMART and update checks. Unraid keeps managing its own containers.

**Fleet stacks**
- Owned stacks on every node.
- **Move-into-root adoption** that keeps the project name.
- Importers for **Dockge and Portainer**.
- **Self-update** of Cha Dash via the hub host's node.

**Graph v2**
- Correlation across sources: Unraid, Docker, Traefik, apps.
- Caddy and NPM discovery.
- `depends_on` relations contributed by apps.
- **Cause hints.**
- The **Atlas** view.

**History**
- Host metrics from nodes, metric rollups, the **Pulse strip** with time travel, **linked scrubbing**.

**Extras**
- Logs streaming and exec through nodes.
- Proxied Proxmox console.
- TrueNAS community app listing.

**Done when**
- The dogfood home's Docker servers are all managed through nodes with **zero inbound ports**.
- Cause hints are correct across the Demo Home incident suite.

**Demo moment:** the Atlas view of the dogfood home, plus "Sonarr is failing *because* qBittorrent was OOM-killed."

---

## v0.4: Care

**Update Center**
- One list covering image digests (via nodes), Unraid, PVE apt, TrueNAS apps and node host packages.
- Risk classification plus changelogs.
- **Staged, dependency-ordered updates.** Each one takes a snapshot first (a PVE snapshot of pvedocker, or a git commit for stacks), runs a health gate, and rolls back automatically.
- **Release radar.**

**Backups**
- Sources: PBS (including PBS running in a container), PVE not-backed-up, Backrest, Kopia, Duplicati, borg-ui, Zerobyte, Unraid flash.
- **Coverage map**, freshness objectives, run now and verify now.

**Operations**
- Maintenance windows, **incident timeline**, the PVE notification webhook receiver, and a morning digest.

**Done when**
- "Update all" on the dogfood home survives a deliberately broken image.
- Backup coverage is correct for every guest across all 5 PVE endpoints.

**Demo moment:** "Sunday updates: 6 updated, 1 rolled back automatically, all while I made coffee."

---

## v1.0: Home

- **Public declarative integration specs:** a community repo, the Workbench, a scaffolding CLI.
- **More integrations:**
  - TrueNAS
  - Home Assistant both ways (Cha Dash entities and actions exposed to HA)
  - UniFi, pfSense, Tailscale, Cloudflare Tunnel
  - Gatus, Prometheus, NUT, Technitium, Synology (read)
  - Feeds, iCal
- **Contexts:** wall and kiosk mode (dimming, night tint, burn-in care, rotation), TV mode, and a simplified level of detail for every widget.
- **Git:** sync for config, and a git remote for stacks.
- **Quality:**
  - i18n through Weblate
  - docs site and a public demo running on the simulator
  - **external security review** and a published threat model
  - stability guarantees for the config schema, API and spec format
- **Distribution:** Helm and Umbrel.

**Done when**
- There's a stable upgrade path from 0.x.
- No P0/P1 security findings are open.
- Every widget meets the 4-level detail spec.

**Demo moment:** a wall tablet that has been calm for a week wakes up with one amber line, and the problem gets fixed from a phone.

---

## Post-1.0 candidates

- WASM plugins
- MCP server with approval flows
- Multi-site support
- Proxmox deploy (OCI image → LXC)
- Power and cost tracking
- Automation rules and scheduled actions
- TRMNL / e-ink
- Bring-your-own-LLM "Explain this"
- Home Assistant add-on

---

## Integration priority (by milestone)

| Milestone | Integrations |
|---|---|
| **v0.1** (dogfood, Tier 0) | Proxmox VE (multiple instances and clusters) · PBS · Unraid · Docker (socket-proxy) · Traefik · OPNsense · Home Assistant · Frigate · Plex · Sonarr · Radarr · Prowlarr · qBittorrent · Immich · Uptime Kuma · built-in checks |
| **v0.1** (internal specs) | Tautulli · Bazarr · Mealie · Linkding · Karakeep |
| **v0.1** (launch breadth) | Jellyfin · Pi-hole · AdGuard · SABnzbd |
| **v0.2** | Seerr/Overseerr/Jellyseerr · Nextcloud · Transmission · NZBGet · Lidarr · Audiobookshelf · Paperless-ngx · Speedtest Tracker |
| **v0.3** | Node host metrics · SMART · Caddy/NPM discovery · Glances/Beszel (as sources) · Dozzle deep links |
| **v0.4** | WUD/Cup/Diun · Backrest · Kopia · Duplicati · borg-ui · Zerobyte · Scrutiny |
| **v1.0** | TrueNAS · UniFi · pfSense · Tailscale · Cloudflare · Gatus · Prometheus · NUT · Technitium · Synology · feeds · iCal |

---

## Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| **"Why another dashboard?"** | High | High | Be clearly better where it matters: the unified graph, control depth across Proxmox and Unraid, owned stacks with git history and rollback, a ~30 MB single binary, design quality, safety UX. Don't chase widget-count parity. |
| **Owning stacks means we can break production** | Medium | Critical | Plan and diff before every deploy, health gates, automatic rollback to the previous commit, preserved project names, protect-lists, audit. Dogfood every deploy path on the owner's lab first. |
| **Integration upkeep overwhelms maintainers** | High | High | Capability contracts, versioned adapters, recorded fixtures for each version, CODEOWNERS, quality tiers, specs for the long tail. The Karakeep 404 on the dogfood page is exactly the failure to design for. |
| **Security incident** (we hold the keys to the house) | Medium | Critical | Hub has no Docker write access; node local policy; no third-party JS; strict CSP; central authorization tests on hub and node; step-up auth; external review before 1.0. |
| **Scope creep into rebuilding every platform's native UI** | High | High | Stick to the control line: handle the daily 80% natively and deep-link precisely for the rest. |
| **Design quality slips as contributors add widgets** | Medium | High | Primitives and tokens, Workbench visual regression, a design review checklist, design CODEOWNERS. |
| **Vue 3.6 RC churn / Vapor maturity** | Medium | Low | Pin versions; use standard (non-Vapor) components by default; keep the layout engine framework-agnostic; upgrade to stable once released. |
| **Bus factor / burnout** | Medium | High | Keep docs and ADRs current; make contributing easy (specs, scaffolding, Workbench, fixtures); keep scope narrow. |
| **Distrust of "vibe-coded" software** | Medium | Medium | Show the engineering rigor openly: tests, ADRs, threat model, signed releases, careful changelogs. |
| **Upstream API churn** (TrueNAS 26, Jellyfin 12, Sonarr v5, Karakeep…) | Certain | Medium | Version detection, adapters, fixture CI, and Repairs that explain what broke. |
| **Distribution gates** (Unraid CA rename blacklist; community-scripts needs 1k stars) | Medium | Medium | Lock the org/repo slug before submitting (decision 8). Lead with compose. |
| **The hub's host goes down exactly when it's needed** (e.g. pluto, which hosts the router) | Medium | Medium | The PWA shows last-known state when offline; the hub warns before actions that affect itself; docs recommend running the hub on a host that doesn't also run the router. |

---

## Open questions

None right now. The M0 decisions (13–17) are settled.

*Answered:*
- **Homepage config** is now the [importer fixture](../../testdata/importers/homepage/dogfood/README.md).
- **Container placement:** see [05 · Dogfood home](05-dogfood-home.md#hosts-and-platforms). Both Docker endpoints are socket-proxy containers, and Linkding and Karakeep run on pvedocker.
- **Repo and port:** decisions 8 and 9.
- **Unraid:** decision 10.
- **Mobile:** decision 11.

---

## Immediate next steps

> The detailed, sequenced plan for finishing M0 is in [06 · Next steps](06-next-steps.md).

1. ~~**Repo bootstrap**~~ ✅ done 2026-10-03, private vulnerability reporting enabled: `LICENSE` (AGPL-3.0), `SECURITY.md`, `CONTRIBUTING.md`, docs and fixture pushed to [ban-red/cha-dash](https://github.com/ban-red/cha-dash). OSI-limited CLA added.
2. **Design exploration:** prototype v1 is built at [`design/prototypes/m0-directions.html`](../../design/prototypes/README.md), with both directions on the dogfood home. **Direction A chosen (decision 12); next is prototype v2 with the theme engine.**
3. **Layout engine spike** (TS + Vue 3.6 + Motion for Vue), plus a one-day gridstack comparison.
4. **Hub skeleton:**
   - `chadash serve`, setup token, SQLite, WebSocket snapshot/diff
   - `deploy/compose.yaml` with the socket-proxy preset
   - Demo Home simulator v0
5. **Plumbing spikes:**
   - multiple PVE instances
   - Unraid consent flow and subscriptions
   - Traefik routers
   - Docker reads through the socket proxy
   - ~~Compose SDK + go-git deploy and rollback~~ ✅ spike 4.6, [ADR-0020](../adr/0020-node-deploys-with-compose-sdk-and-go-git.md)
6. ~~**ADRs**~~ ✅ 0001–0020 are written and accepted ([index](../adr/README.md)).
