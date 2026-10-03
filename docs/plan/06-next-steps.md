# 06 · Next Steps: finishing M0

> Where we are (2026-10-03):
> - The plan is written.
> - The repo is public under AGPL-3.0 with an OSI-limited CLA.
> - The importer fixture exists.
> - Design prototype v1 shows both directions. **Direction A was chosen, built to be very themeable.**
> - **Wave 1 done:**
>   - ADRs 0001–0012 and 0018–0020, plus issue templates;
>   - tokens v0 and prototype v2;
>   - the layout engine package;
>   - the hub skeleton;
>   - the Demo Home simulator;
>   - the node spike (4.6).
> - **Wave 2 next:**
>   - the `web/` scaffold with the Vue renderer (2.3, 3.8);
>   - the Dockerfile and compose file (3.9);
>   - CI (3.10);
>   - wiring the simulator into the Home Graph.
>
> M0 is done when a live front page runs on Demo Home data in the chosen direction, the layout engine feels right, and decisions 12–17 have ADRs ([04 · Roadmap](04-roadmap.md#m0-foundations)).

**House rule:** builds and dev servers only run when you say so. Every step below that needs one marks it with **▶ you run**, or with "tell me to run it". Tests and type-checks are fine to run.

---

## The critical path

```
Pick direction (12) ──▶ Prototype v2 + tokens v0 ──┐
                                                    ├──▶ M0 demo: live front page on Demo Home ──▶ M0 review → v0.1
Layout engine package ──▶ Vue renderer ────────────┤
                                                    │
Hub skeleton + Demo Home sim ───────────────────────┘
Plumbing spikes (your lab, read-only) ──▶ fixtures + ADRs 14–15 (feed v0.1, not the M0 demo)
```

Four of these tracks can run **in parallel**: design, layout engine, hub and simulator, and spikes. Parallel agents in separate git worktrees work well for this.

---

## Track 0: Housekeeping (small, do first)

| # | Task | Who | Output |
|---|---|---|---|
| 0.1 | ✅ Private vulnerability reporting enabled | you | done |
| 0.2 | ✅ `cla-signatures` branch created; repo renamed to lowercase `ban-red/cha-dash` | me / you | done |
| 0.3 | ✅ Write **ADRs** for the decisions already made: 0001–0011 (name, license + CLA, Vue, dogfood, v0.1 scope, owned stacks + node, compose-first, repo/images, port, Unraid stays Unraid, PWA-only), 0012 (Direction A + theming) and 0018 (module path), plus 0019 (Bun) and 0020 (node deploys) | me | [`docs/adr/`](../adr/README.md) |
| 0.4 | ✅ Issue templates (bug, integration request, design feedback), labels, `CODEOWNERS`. **Still to do:** create the labels on GitHub from `.github/labels.yml`; issue forms silently skip labels that don't exist | me / **you** (label sync) | `.github/` |
| 0.5 | ✅ **Go module path: `dash.cha.sh`** (decision 18). Still to do: serve the `go-import` tag on `dash.cha.sh`. [docs/infra/vanity-import.md](../infra/vanity-import.md) has a Cloudflare Worker and a Caddy option | **you** (hosting) | `curl …?go-get=1` shows the tag |

## Track 1: Design (decides how everything looks)

| # | Task | Output | Done when |
|---|---|---|---|
| 1.1 | ✅ **Direction A, very themeable** (decision 12). B becomes the built-in Paper theme | — | done |
| 1.2 | ✅ **Prototype v2** in Direction A, **with the theme engine**: <ul><li>theme switcher: Instrument Dark/Light, OLED, Night, High Contrast, Paper</li><li>live base/accent/contrast editor that shows status-pair validation as you change it</li><li>surface (card/ruled), density and Briefing (sentence/headline) toggles</li><li>first-run and empty states</li><li>Inbox repairs flow</li><li>motion study (expand in place, reflow springs, hold ring, lens fade)</li><li>night and phone polish</li><li>the "simplified" wall detail level for 3 widgets</li></ul> | `design/prototypes/m0-v2.html` | You'd ship the look |
| 1.3 | ✅ **Tokens v0 + theme schema**: the token document format (YAML/JSON) and a generator from base/accent/contrast. OKLCH 12-step scales per theme, type scale, spacing, radii and motion tokens, exported as CSS variables. Status pairs re-validated with the palette validator, and APCA checks on text | `design/tokens/` → `web/src/design/tokens.css` | Validator and contrast checks pass |
| 1.4 | ✅ Seed the **Workbench** list (19 widgets, 80 size cells; **waiting on your agreement**): every v0.1 widget × size × state it must support | `docs/design/workbench.md` | Agreed list |

## Track 2: Layout engine spike (independent; can start now)

| # | Task | Output |
|---|---|---|
| 2.1 | ✅ **`packages/layout`** (pure TypeScript, framework-agnostic): <ul><li>model: sections; items with `supportedSizes`</li><li>operations: place, move, resize-to-size, remove, smart insert</li><li>dense Z-reflow; derived breakpoints (12 → 8 → 4 columns)</li><li>immutable history (undo/redo); serialization</li></ul> | package + API doc |
| 2.2 | ✅ **Property tests** (fast-check, 9,400 runs per `bun test`): <ul><li>no overlaps; all items in bounds</li><li>derivation is deterministic</li><li>undo restores exactly</li><li>resizing only ever lands on supported sizes</li></ul> | green test suite |
| 2.3 | **Vue 3.6 renderer** (note: the workspace resolved TypeScript 7, the native compiler; check `vue-tsc` works with it or pin 5.x/6.x in `web/`): pointer drag with momentum projection, Motion for Vue springs, a corner resize handle that snaps, keyboard move and resize, edit mode | `web/` demo route |
| 2.4 | **One-day gridstack 14 comparison** on the same scenarios | ADR-0013 |
| 2.5 | Feel check on a real phone: 60 fps drag | ▶ you run the dev server |

## Track 3: Hub skeleton and the Demo Home simulator

| # | Task | Output |
|---|---|---|
| 3.1 | ✅ Toolchain: Go **1.27.1** via `go.mod` (the local 1.25.1 switches toolchains automatically). JavaScript tooling is **Bun** (package manager, workspaces, test runner) instead of pnpm; Bun 1.4 is already installed | — |
| 3.2 | ✅ **`go.mod`: `module dash.cha.sh`**. **`cmd/chadash`**: `serve`, `healthcheck`, `version`; config from env and flags; structured logs | binary |
| 3.3 | ✅ **Storage**: SQLite plus goose migrations, schema v0 (settings, users, sessions, integration instances, vault, boards, layouts, audit stub) | migrations |
| 3.4 | ✅ **First run**: setup token printed to logs → owner account → session cookie. Passkeys follow in v0.1 | auth flow |
| 3.5 | ✅ **Vault**: XChaCha20-Poly1305 envelope encryption; key auto-generated on first boot or loaded from `CHADASH_SECRET_KEY_FILE` | `internal/vault` |
| 3.6 | ✅ **Home Graph v0 + realtime**: an in-memory graph, and the WebSocket protocol (`hello` / `sub` / `snap` / `diff` / `ping`) from the architecture doc | `internal/graph`, `internal/realtime` |
| 3.7 | ✅ **Demo Home simulator v0** ([`sim/`](../../sim/README.md)): the dogfood topology (5 PVE endpoints, Unraid with PBS, pvedocker, OPNsense, HA, Frigate) plus scripted incidents (PBS filling up, failed backup, memory pressure, Plex token revoked, Karakeep API change, OOM-killed qBittorrent). It also exports JSON snapshots so later prototypes use the same data | `sim/` |
| 3.8 | **`web/` scaffold**: Vue 3.6 + Vite SPA embedded with `go:embed`; one live page subscribed to the simulator over the WebSocket | SPA |
| 3.9 | **Ship shape**: multi-stage Dockerfile (scratch, non-root, `/data`), `deploy/compose.yaml` with the socket-proxy preset | ▶ you run `docker compose up -d` |
| 3.10 | **CI**: go vet/test/govulncheck; bun lint/typecheck/test; image build (no push); bundle-size budget. Then turn on branch protection for `main` | `.github/workflows/ci.yml` |

## Track 4: Plumbing spikes against your lab

All **read-only**, time-boxed, throwaway code. Each produces notes plus **sanitized recorded fixtures** that seed the v0.1 integrations.

| # | Spike | Against | Also settles |
|---|---|---|---|
| 4.1 | PVE multi-instance + cluster: `/cluster/resources`, tasks, check that privilege separation is in effect | your 5 PVE endpoints | — |
| 4.2 | Unraid: GraphQL introspection, subscriptions, `ApiKeyAuthorize` consent flow | jupiter | — |
| 4.3 | Traefik routers, including path-based ones (`/sonarr`) → service URL correlation | pvedocker Traefik | — |
| 4.4 | Docker through the socket proxy: list, inspect (with env redaction), events, and a proxy-vs-raw probe | both proxies | — |
| 4.5 | OPNsense SSE traffic stream; Frigate events and snapshots | router, minig5 | — |
| 4.6 | ✅ **Node prototype**: Compose SDK + go-git deploy → diff → health gate → rollback. All 8 checks pass; verdict: build the node on the SDK. [Findings](../../spikes/node-compose/README.md#findings) | sandbox LXC on mars-2 via [`spikes/node-sandbox/sandbox.sh`](../../spikes/node-sandbox/README.md) | node size + memory → ADR-0016 |
| 4.7 | Node transport: mTLS vs Noise (ergonomics, rotation, code size) | local | ADR-0014 |
| 4.8 | SQLite driver: modernc vs ncruces (encryption cost, performance) | local | ADR-0015 |

Credentials for the spikes:
- ✅ **Proxmox:** the existing read-only Homepage tokens (PVEAuditor, privilege-separated) are in the git-ignored `.env.local`. The format is in `.env.example`.
- **Unraid:** the key comes from the consent flow during spike 4.2.
- Nothing secret goes in commits or fixtures.

---

## Suggested order

1. **Now:** Track 0. You review the prototype and pick a direction (1.1).
2. **Next, in parallel:**
   - Layout engine (2.1–2.2).
   - Hub skeleton + simulator (3.1–3.7).
   - Prototype v2 + tokens (1.2–1.3), once the direction is chosen.
3. **Then:**
   - Renderer (2.3–2.5).
   - SPA + compose + CI (3.8–3.10).
   - Spikes 4.1–4.5 alongside.
4. **M0 demo:** the live front page on simulator data, in the chosen direction with tokens v0, editable with the layout engine, served by `docker compose up -d`.
5. **M0 review:** write ADRs 12–17 (direction, grid engine, node transport, SQLite driver, hardware floor, AI/MCP stance) plus 18 (module path), run the node spike (4.6), then start **v0.1**.

---

## What I need from you

| When | What |
|---|---|
| ~~Now~~ | ✅ Direction chosen · ✅ vulnerability reporting · ✅ module path · ✅ Proxmox tokens |
| Whenever convenient | Serve the `go-import` tag on `dash.cha.sh` ([how](../infra/vanity-import.md)) |
| ~~Before the node spike~~ | ✅ Sandbox created on mars-2 |
| ~~To run the node spike~~ | ✅ Key added, spike run |
| Before the Unraid spike | Be ready to click "Authorize" on jupiter's consent screen |
| During Tracks 2–3 | Say "run it" when you want me to start dev servers or builds |
