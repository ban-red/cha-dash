# @chadash/web

The Cha Dash web app: first-run setup and sign-in, the front page (Briefing, Needs attention, the board), the Inbox, and the realtime client. Vue 3.6 and Vite, built into `internal/webui/dist`, which the hub embeds with `go build -tags webui`.

Design: Direction A ("Instrument") from [02 · Experience](../docs/plan/02-experience.md), following [prototype v2](../design/prototypes/m0-v2.html). Every colour, size and spring comes from [`@chadash/tokens`](../design/tokens/README.md).

## Scripts

Run these in `web/` (or from the root, where `bun run build`, `bun run test` and `bun run typecheck` fan out):

| Script | What it does |
|---|---|
| `bun run dev` | Vite dev server, proxying `/api` and `/ws` to a hub on `localhost:3696` |
| `bun run build` | `vite build` into `../internal/webui/dist` (emptied first) |
| `bun run test` | Vitest with happy-dom |
| `bun run typecheck` | `vue-tsc --noEmit`, strict, templates included |

## Running against a local hub

```sh
chadash serve --demo     # the hub with the Demo Home, on :3696
bun run dev              # in web/, then open the URL Vite prints
```

The dev server proxies `/api` and `/ws` (`ws: true`) to `localhost:3696`. `changeOrigin` stays off: the hub's cross-origin checks compare `Origin` with `Host`, and through the proxy both are the dev server's. The first visit asks for the setup token from the hub's log.

Demo mode shows absolute times in UTC, because the Demo Home keeps UTC as its clock and its incident copy quotes UTC times. Outside demo mode times are local.

## Structure

```
index.html            no inline script or style; loads public/boot.js, then src/main.ts
public/boot.js        applies the remembered theme before first paint
src/
  main.ts             tokens.css, fonts, global styles, mount
  App.vue             the doors: setup · sign-in · front page (lazy) · hub unreachable
  views/              SetupView, LoginView, FrontPage (creates the live, board and UI stores)
  board/
    BoardSurface.vue  the only file that picks the board renderer (see "Integration seam")
    StaticBoard.vue   read-only renderer: deriveLayout() at 12/8/4 columns from its own width
    SectionHeader.vue label · meta · rule, meta live from the graph
  components/         Briefing, NeedsAttention, HubBanner, InboxSheet, IncidentItem,
                      ConfirmDialog, HoldToConfirm, ThemeMenu, Toasts, Tile, marks/
  widgets/            one component per widget type, plus registry.ts and WidgetHost.vue
  model/              pure views over the topics: graph, inbox, briefing copy, widget catalog,
                      default layout, words
  realtime/           protocol v1 client (client.ts), frames, backoff
  stores/             session, live (realtime + clock + Fix), board (layout load/create/save), ui
  lib/                RFC 6902 applier, fetch wrapper, formatting, themes, motion tokens
  styles/             fonts, base, board, tiles, chrome (global, token-only CSS)
test/                 Vitest: patches, protocol, stores, components; fixtures from sim/snapshots
```

**State.** Plain Vue reactivity with `provide`/`inject`; no store library. The realtime client keeps one `reactive()` document with a member per topic (`graph`, `metrics`, `inbox`) and applies diffs in place, so a metrics diff only re-renders the widgets reading the series it touched.

**Realtime.** One WebSocket per tab, one subscription per topic, so a hub that doesn't serve a topic still serves the others. A patch that doesn't apply re-subscribes its topic (a fresh snapshot); `snap` with `resync: true` replaces state. A watchdog drops the socket after 2.5 silent ping intervals; `ping()` runs when the tab becomes visible or the network returns. Reconnects back off exponentially with equal jitter (0.5–1 s, 1–2 s, … up to 15–30 s). After two failed attempts the session is re-checked, because a 401 on the upgrade looks like a network error to the browser.

**Layouts.** A board with `layout: null` gets the default from `model/defaultLayout.ts`, built with `@chadash/layout` from what the graph contains, and is PUT back with the revision the GET returned. On 409 the other writer's layout wins and is loaded. Layout items have no config field, so a widget bound to one resource carries it in its id: `host@unraid:jupiter`, `arr@app:sonarr`.

**Safety ladder.** Fix runs an incident's suggested action through `POST /api/demo/incidents/{id}/fix`: T0 and T1 straight away, T2 behind the hold-to-confirm ring (about 650 ms, with the blast radius lighting up as you hold). T3 and T4 aren't offered yet. Progress and the resolution arrive on the `inbox` topic.

## Edit mode: `@chadash/layout-vue`

`board/BoardSurface.vue` is the only file that picks the renderer. A front page you only read uses `StaticBoard`. **Edit layout** swaps in `board/EditableBoard.vue`, which wraps `<LayoutBoard>` (drag with momentum, snapping resize, keyboard moves) and loads on demand, so the gesture and motion code never ride with a read-only page.

- Each committed gesture goes through `useBoard().save(next)`: it shows the board, PUTs it with the current revision, and on a 409 loads the other writer's layout. You get a toast either way something goes wrong.
- **Undo / Redo** (buttons, or ⌘Z / ⇧⌘Z) use `useLayoutHistory` and save the board they step to.
- Item labels in announcements come from the widget name plus its resource ("Host: jupiter").

## The CSP

The hub serves the app with `default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'`. So:

- **No inline scripts or styles.** The theme bootstrap is `public/boot.js`, a file. Templates never use static `style="…"` attributes; dynamic values go through `:style` bindings, which use the CSSOM.
- **No `data:` URIs.** `build.assetsInlineLimit` is 0, so every asset is a file. Paper's grain ships from `@chadash/tokens` as an SVG file next to `tokens.css`, and Vite bundles it like any other asset.
- **Self-hosted fonts.** Instrument Sans, Geist Mono, Doto, Schibsted Grotesk and Newsreader come from the `@fontsource-variable` packages (OFL-1.1), declared in `styles/fonts.css` under the family names the tokens use. A theme only downloads its own faces; Newsreader keeps its optical-size axis for Paper's headlines.
- **Same-origin only.** `fetch` uses relative URLs; the WebSocket URL is built from `location`.

## Bundle

The initial JS is the entry chunk only (no modulepreloads): the Vue runtime, the doors and the session. The front page, the Inbox and the confirm dialog load on demand; motion-v rides with the Inbox and the confirm dialog. CI enforces a 150 kB gzip budget on the initial JS.

## Versions

| Package | Version | Note |
|---|---|---|
| vue | 3.6.0-rc.10 | Newest 3.6 prerelease, exact. Bun warns that motion-v's and vueuse's peer ranges exclude prereleases; that's expected |
| vite | 8.3.2 | with @vitejs/plugin-vue 6.0.9; `resolve.dedupe: ['vue']` |
| typescript | 6.0.3 | Pinned here. vue-tsc 3.3.12 loads `typescript/lib/tsc`, which the native TypeScript 7 package doesn't ship (`ERR_PACKAGE_PATH_NOT_EXPORTED`); the other workspaces stay on 7 |
| vitest | 5.0.3 | with @vue/test-utils 2.5.1 and happy-dom 20.14.5 |
| motion-v | 2.5.2 | with its peer @vueuse/core 15 |

## Not yet

- Detail sheets for tiles; tiles with an open incident open the Inbox on it instead.
- Rolling numerals, the ⌘K command bar, lenses, wall and ink levels of detail.
- Real camera frames (placeholders drawn from tokens), service logos (monograms).
- Undo toasts for T1 and type-to-confirm for T3, which need the action engine.
