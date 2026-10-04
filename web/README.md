# @chadash/web

The Cha Dash web app: first-run setup (with an optional passkey) and sign-in (password or passkey), the front page (Briefing, Needs attention, the board), the Inbox, the ⌘K command bar, Settings (integrations, import from Homepage, passkeys), and the realtime client. An installable PWA that opens on the last-known page when the hub can't be reached. Vue 3.6 and Vite, built into `internal/webui/dist`, which the hub embeds with `go build -tags webui`.

Design: Direction A ("Instrument") from [02 · Experience](../docs/plan/02-experience.md), following [prototype v2](../design/prototypes/m0-v2.html). Every colour, size and spring comes from [`@chadash/tokens`](../design/tokens/README.md).

## Scripts

Run these in `web/` (or from the root, where `bun run build`, `bun run test` and `bun run typecheck` fan out):

| Script | What it does |
|---|---|
| `bun run dev` | Vite dev server, proxying `/api` and `/ws` to a hub on `127.0.0.1:3696` (the root `bun run dev` starts both) |
| `bun run build` | `vite build` into `../internal/webui/dist` (emptied first) |
| `bun run test` | Vitest with happy-dom |
| `bun run typecheck` | `vue-tsc --noEmit`, strict, templates included |

## Running against a local hub

From the repo root, `bun run dev` starts both: the hub on the Demo Home (127.0.0.1:3696) and this dev server. The setup and sign-in screens then offer **Login as Local Dev**, which signs in as the owner, creating a passwordless `dev` owner on first run. To run them separately:

```sh
CHADASH_DATA_DIR=./data go run ./cmd/chadash serve --demo   # repo root: the hub on :3696
bun run dev                                                 # in web/, then open the URL Vite prints
```

The dev server proxies `/api` and `/ws` (`ws: true`) to `127.0.0.1:3696`. `changeOrigin` stays off: the hub's cross-origin checks compare `Origin` with `Host`, and through the proxy both are the dev server's. Run this way, the hub has no dev login (add `-tags devlogin` to `go run` for it), so the first visit asks for the setup token from the hub's log.

Demo mode shows absolute times in UTC, because the Demo Home keeps UTC as its clock and its incident copy quotes UTC times. Outside demo mode times are local.

Passkeys work at `http://localhost:5173` (the dev server's URL) with no configuration; browsers refuse them on `127.0.0.1` and on any plain-http LAN address, and the page says so where the passkey button would be. The service worker only registers in a production build.

## Structure

```
index.html            no inline script or style; loads public/boot.js, then src/main.ts
public/               boot.js (theme before first paint), manifest.webmanifest, icons/, favicon
sw/service-worker.js  the service worker's source; `vite build` emits it as /sw.js
src/
  main.ts             tokens.css, fonts, global styles, mount, service worker registration
  App.vue             the doors: setup · passkey offer (lazy) · sign-in · front page (lazy) · unreachable
  views/              SetupView, PasskeyOffer, LoginView, FrontPage (the signed-in shell: creates
                      the live, board and UI stores; routes; ⌘K; the snapshot)
  settings/           SettingsSheet (lazy) and its panes: IntegrationsPane, IntegrationEditor,
                      SchemaField (forms from JSON Schema), TestResultView, ImportPane, SecurityPane
  dev/                dev-server-only pieces (Login as Local Dev); each export is undefined in
                      `vite build`, so production bundles carry none of it
  board/
    BoardSurface.vue  the only file that picks the board renderer (see "Edit mode")
    StaticBoard.vue   read-only renderer: deriveLayout() at 12/8/4 columns from its own width
    SectionHeader.vue label · meta · rule, meta live from the graph
  components/         Briefing, NeedsAttention, HubBanner, InboxSheet, IncidentItem, CommandPalette
                      (lazy), ConfirmDialog, HoldToConfirm, ThemeMenu, Toasts, Tile, marks/
  widgets/            one component per widget type (ServiceWidget for imported services),
                      plus registry.ts and WidgetHost.vue
  model/              pure views: graph, inbox (grouping), briefing copy, widget catalog, default
                      layout, words, commands (⌘K), integrations, import plan, service status
  realtime/           protocol v1 client (client.ts), frames, backoff
  stores/             session (doors, offline start), live (realtime, clock, demo Fix, restore),
                      board, ui (layers, safety ladder), integrations, tiles (import record), persist
  lib/                router, fetch wrapper, RFC 6902 applier, formatting, themes, motion tokens,
                      fuzzy, schema (JSON Schema subset), webauthn, passkeys, snapshot (IndexedDB), pwa
  styles/             fonts, base, board, tiles, chrome (global, token-only CSS)
test/                 Vitest: patches, protocol, stores, components, ⌘K, Inbox, settings forms,
                      import, passkeys, offline; fixtures from sim/snapshots and the importer's golden plan
```

**State.** Plain Vue reactivity with `provide`/`inject`; no store library. The realtime client keeps one `reactive()` document with a member per topic (`graph`, `metrics`, `inbox`) and applies diffs in place, so a metrics diff only re-renders the widgets reading the series it touched.

**Realtime.** One WebSocket per tab, one subscription per topic, so a hub that doesn't serve a topic still serves the others. A patch that doesn't apply re-subscribes its topic (a fresh snapshot); `snap` with `resync: true` replaces state. A watchdog drops the socket after 2.5 silent ping intervals; `ping()` runs when the tab becomes visible or the network returns. Reconnects back off exponentially with equal jitter (0.5–1 s, 1–2 s, … up to 15–30 s). After two failed attempts the session is re-checked, because a 401 on the upgrade looks like a network error to the browser.

**Layouts.** A board with `layout: null` gets the default from `model/defaultLayout.ts`, built with `@chadash/layout` from what the graph contains, and is PUT back with the revision the GET returned. On 409 the other writer's layout wins and is loaded. Layout items have no config field, so a widget bound to one resource carries it in its id: `host@unraid:jupiter`, `arr@app:sonarr`.

**Routes.** A hand-rolled history router (`lib/router.ts`, about 1 kB) instead of vue-router (about 10 kB gzip): there are five routes, all owned by the signed-in shell, and the hub already answers any non-file path with `index.html`.

| Route | What it shows |
|---|---|
| `/` | the front page on the first board |
| `/boards/{id}` | the front page on that board (where an import lands) |
| `/settings/integrations` | instances and their status; `/new` the catalog, `/new/{type}` the add form, `/{id}` the edit form (`?field=` focuses a field, `?reason=auth` the first secret) |
| `/settings/import` | import from Homepage |
| `/settings/security` | passkeys |

Settings is a sheet over the front page, not a separate app: the board, the socket and the Inbox stay live underneath, and Esc goes back.

**The Inbox** reads the `inbox` topic the same way in demo and real mode, grouped by status: **open** (problems, down first, then repairs), **being fixed** (a fix was accepted, the phase says it's in hand, or it's info), and **resolved**. Groups hold still while the Inbox is open and re-sort the next time it opens. A **repair** (class `repair`) offers **Reconnect**: the integration's settings with the failing field focused (`facts.field`, else the first secret for a 401/403); its target is `integration:<instance id>`, resolved by id, then name, then the only instance of that integration.

**Safety ladder.** In demo mode only (`GET /api/demo` says so), an incident's suggested action runs through `POST /api/demo/incidents/{id}/fix`: T0 and T1 straight away, T2 behind the hold-to-confirm ring (about 650 ms, with the blast radius lighting up as you hold). Elsewhere the flow shows what Cha Dash would do and the button says actions arrive with the action engine. `link.open` (T0) opens the target's link in any mode. T3 and T4 aren't offered yet. Progress and the resolution arrive on the `inbox` topic.

## The command bar (⌘K)

⌘K or Ctrl K anywhere, or `/` outside a text field. It searches boards, the Home Graph's resources (fuzzy over name, kind and ID; `lib/fuzzy.ts`), integrations (from `GET /api/integrations/instances`), open incidents and Settings, and runs Edit layout, the six themes and Sign out. Opening a resource shows its open incident in the Inbox, else scrolls to and rings its tile, else opens its link.

It's an ARIA combobox: focus stays in the input, the active option is `aria-activedescendant`, groups are `role="group"`; arrows move (and wrap), Page Up/Down jump, Enter runs, Esc closes. `CommandPalette.vue` is its own chunk, mounted only while open.

## Settings

**Integrations.** The catalog (`GET /api/integrations/catalog`) and the instances (`GET /api/integrations/instances`) with status and warnings. The add and edit forms are generated from each manifest's `config_schema` (`lib/schema.ts`): string, number, integer, boolean, object, array, `enum` or `oneOf` choices, `required`, `default`, `format` uri and hostname, `pattern`, ranges, nested objects as fieldsets, lists of objects, and `if`/`then`/`else` branches; errors are plain sentences. **Secrets (`writeOnly`) are write-only**: inputs start empty whatever the hub sends, a "set" chip says one is stored, blank keeps it, typed values live apart from the config, go out only when typed and are cleared after saving. **Test connection** posts unsaved settings to `/api/integrations/test` (a saved, unchanged instance to `/instances/{id}/test`) and shows version, latency, grants (write grants flagged on a read-only connection), warnings and errors. Delete asks in place first. A secret field marked `x-chadash-authorize` (Plex's token) gets **Sign in with …** (`settings/AuthorizeButton.vue`): it opens the provider's page in a new tab with its opener cut, polls the hub, and fills the field as if typed; nothing is saved until Save.

**Import from Homepage.** The source picker (Homepage; Homarr and Plain YAML marked v0.2), `services.yaml`, `docker.yaml` and `settings.yaml` pasted, uploaded or dropped, then the dry run as the preview: sections and tiles, connections and their secret fields (and missing ones), secrets moved as a count, hints, what discovery fills in later, unmapped entries, ignored settings, and how a re-import compares. Apply lands on the new board.

**Imported tiles.** Every imported Homepage entry without a catalog widget is a `service` tile (`service@homepage:<section>/<entry>`, small or wide). The layout only has ids, types and sizes, so its name, link and description come from the import record (`GET /api/import/homepage`), read through one adapter, `stores/tiles.ts`, until tiles get a real config home. Its status comes from the check or instance that watches it.

**Security.** Passkeys: list (synced, other RP ID, clone warning), rename, delete after a confirmation, add one on this device.

## Passkeys

`lib/webauthn.ts` does the browser side without a library: base64url to and from buffers, creation and request options from the hub's JSON, and the credential back as `toJSON()` produces it (the browser's own `parse…FromJSON` and `toJSON` when it has them). `lib/passkeys.ts` talks to `/api/auth/passkeys/*` and turns the hub's codes into sentences (`passkeys_unavailable` is shown as is; `unknown_passkey` is signalled to the password manager). Both load on demand.

- **First run, step 2 of 2:** "Add a passkey", skippable.
- **Sign in:** "Sign in with a passkey" (usernameless), and passkeys in the username field's autofill where the browser supports conditional mediation.
- **Errors in words:** cancelled (quiet), not allowed or timed out, already registered, the address doesn't match `CHADASH_PUBLIC_URL`, ceremony expired, the last way to sign in.
- **WebAuthn needs HTTPS or localhost.** On a plain-http address the page explains that inline instead of offering a button that can't work.

## Offline and the PWA

- **Installable:** `public/manifest.webmanifest`, PNG, maskable and SVG icons, an apple-touch-icon.
- **Service worker:** `sw/service-worker.js`, emitted as `/sw.js` by the `chadash-sw` plugin in `vite.config.ts` with this build's shell filled in (index.html, the public files, every JS, CSS and SVG asset) and a version hashed from that list. Navigations are network first with the cached shell as the fallback; hashed `/assets/` are cache first (fonts arrive this way); **`/api`, `/ws` and `/healthz` pass straight through, so nothing that carries the session is ever cached.** It registers from the bundle (`lib/pwa.ts`), never an inline script, and only where service workers exist (a secure context).
- **Last-known snapshot:** `lib/snapshot.ts` keeps the graph, metrics and inbox, the board's layout, who was signed in, the display time zone and the hub's clock offset in IndexedDB, behind try/catch and a timeout. `stores/persist.ts` refreshes it at most every 15 s while live and when the tab hides. Signing out deletes it.
- **Hub unreachable at start-up** (a network error, or a proxy's 502/503/504) with a snapshot on this device: the front page opens on it, every tile hatched, under the offline banner with its "as of" time. When the socket is back, the session is confirmed with the hub (or you're asked to sign in) and live data replaces the snapshot.

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

The initial JS is the entry chunk only (no modulepreloads): the Vue runtime, the doors, the session and the router. The front page, the Inbox, the confirm dialog, the command bar, Settings, the passkey offer and the WebAuthn code load on demand; motion-v rides with the Inbox and the confirm dialog. CI enforces a 150 kB gzip budget on the initial JS: it was 37.3 kB before wave 4 and is 39.4 kB after it (`boot.js` 0.5 kB plus the entry chunk).

## Versions

| Package | Version | Note |
|---|---|---|
| vue | 3.6.0-rc.10 | Newest 3.6 prerelease, exact. Bun warns that motion-v's and vueuse's peer ranges exclude prereleases; that's expected |
| vite | 8.3.2 | with @vitejs/plugin-vue 6.0.9; `resolve.dedupe: ['vue']` |
| typescript | 6.0.3 | Pinned here. vue-tsc 3.3.12 loads `typescript/lib/tsc`, which the native TypeScript 7 package doesn't ship (`ERR_PACKAGE_PATH_NOT_EXPORTED`); the other workspaces stay on 7 |
| vitest | 5.0.3 | with @vue/test-utils 2.5.1 and happy-dom 20.14.5 |
| motion-v | 2.5.2 | with its peer @vueuse/core 15 |
| fake-indexeddb | 6.2.5 | dev only: IndexedDB for the snapshot tests (happy-dom has none) |

## Not yet

- Detail sheets for tiles; tiles with an open incident open the Inbox on it instead.
- Rolling numerals, lenses, wall and ink levels of detail.
- Real camera frames (placeholders drawn from tokens), service logos (monograms: the CSP forbids hot-linking, and the hub doesn't cache icons yet).
- Undo toasts for T1 and type-to-confirm for T3, which need the action engine.
- Inline snapshot in the first paint (wave 6); today a snapshot is only used when the hub can't be reached.
