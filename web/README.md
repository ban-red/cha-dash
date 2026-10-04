# @chadash/web

The Cha Dash web app: first-run setup (with an optional passkey, then "Connect your home") and sign-in (password or passkey), the front page (Briefing, Needs attention, the board), the Inbox, the ⌘K command bar, Settings (integrations, import from Homepage, backup, passkeys), and the realtime client. An installable PWA that opens on the last-known page when the hub can't be reached. Vue 3.6 and Vite, built into `internal/webui/dist`, which the hub embeds with `go build -tags webui`.

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
  onboarding/         the first-run wizard, "Connect your home" (lazy): OnboardingWizard and its
                      Discover and Add steps; state.ts (the hub-kept step), discover.ts, draft.ts
  settings/           SettingsSheet (lazy) and its panes: IntegrationsPane, IntegrationEditor,
                      SchemaField (forms from JSON Schema), TestResultView, ImportPane,
                      BackupPane (its own chunk), SecurityPane
  dev/                dev-server-only pieces (Login as Local Dev); each export is undefined in
                      `vite build`, so production bundles carry none of it
  board/
    BoardSurface.vue  the only file that picks the board renderer (see "Edit mode")
    StaticBoard.vue   read-only renderer: deriveLayout() at 12/8/4 columns from its own width
    EditableBoard.vue edit mode (lazy): LayoutBoard, add / edit / remove, undo over board and tiles
    WidgetPicker.vue  "Add widget" (lazy, its own chunk); TileEditor.vue, per-tile Edit (lazy)
    SectionHeader.vue label · meta · rule, meta live from the graph
  components/         Briefing, NeedsAttention, HubBanner, InboxSheet, IncidentItem, DiscoveryItem,
                      SuggestionItem, CommandPalette (lazy), ConfirmDialog, HoldToConfirm, ThemeMenu, Toasts, Tile, marks/
  widgets/            one component per widget type (see "Widgets"), registry.ts, WidgetHost.vue;
                      subject.ts (the eight states), read.ts, checks.ts, guests.ts, serviceStats.ts
                      (the service tile's stat per integration), use.ts, parts/ (Frame, StateNote,
                      StatusBars, WxIcon, WidgetPending), widgets.css
  model/              pure views: graph, inbox (grouping), briefing copy, widget catalog, default
                      layout, words, commands (⌘K), integrations, import plan, backup plan, service status,
                      feeds (what the graph can feed each widget), edit (add / edit / remove),
                      discovery (label discovery)
  realtime/           protocol v1 client (client.ts), frames, backoff
  stores/             session (doors, offline start), live (realtime, clock, demo Fix, restore),
                      board (layout and tiles), tiles (tile config adapter), discovery, ui (layers,
                      safety ladder), integrations, persist
  lib/                router, fetch wrapper, RFC 6902 applier, formatting, themes, motion tokens,
                      fuzzy, schema (JSON Schema subset), webauthn, passkeys, snapshot (IndexedDB),
                      inline (the hub's first-paint snapshot), pwa
  styles/             fonts, base, board, tiles, chrome (global, token-only CSS)
test/                 Vitest: patches, protocol, stores, components, ⌘K, Inbox, settings forms,
                      import, passkeys, offline, widgets (Demo Home and real-instance graphs), first
                      paint, the service worker's shell, accessibility (axe-core, token pairs);
                      fixtures from sim/snapshots and the importer's golden plan
```

**State.** Plain Vue reactivity with `provide`/`inject`; no store library. The realtime client keeps one `reactive()` document with a member per topic (`graph`, `metrics`, `inbox`) and applies diffs in place, so a metrics diff only re-renders the widgets reading the series it touched.

**One of each thing.** The hub keeps one resource per source and links the ones that are one thing with `<alias> same_as <primary>` ([`internal/correlate`](../internal/correlate/README.md)). `byKind`, `distinct` (⌘K) and the containers tile skip aliases; `members`, `mergedHealth` (the worst) and `sourcesOf` merge what each source says, so a container Docker and Unraid both report is one row naming both.

**Realtime.** One WebSocket per tab, one subscription per topic, so a hub that doesn't serve a topic still serves the others. A patch that doesn't apply re-subscribes its topic (a fresh snapshot); `snap` with `resync: true` replaces state. A watchdog drops the socket after 2.5 silent ping intervals; `ping()` runs when the tab becomes visible or the network returns. Reconnects back off exponentially with equal jitter (0.5–1 s, 1–2 s, … up to 15–30 s). After two failed attempts the session is re-checked, because a 401 on the upgrade looks like a network error to the browser.

**Layouts.** A board with `layout: null` gets the default from `model/defaultLayout.ts`, built with `@chadash/layout` from what the graph contains, and is PUT back with the revision the GET returned. On 409 the other writer's layout wins and is loaded, and saves queued behind the conflict are dropped rather than overwriting it. Saves run one at a time, each on the revision the previous one returned. Layout items have no config field, so a widget bound to one resource carries it in its id: `host@unraid:jupiter`, `arr@app:sonarr`; everything else about an item is its tile config (below).

**Tile configuration.** Each layout revision carries the board's tiles next to the layout: `GET /api/boards/{id}/layout` → `{revision, format_version, layout, tiles}`, and a PUT sends `tiles` too when they changed (without it, the hub keeps the revision's tiles, so a drag never touches them). The document is keyed by item id:

```jsonc
{ "arr@app:sonarr": { "name": "TV", "icon": "sonarr.png", "href": "https://sonarr.lab.example",
                      "bind": { "instance": "i_x", "check": "Sonarr", "resource": "app:radarr",
                                "sources": [ { "instance", "capability", "options" } ] },
                      "options": { "queue": true },
                      "origin": { "source": "homepage" | "labels", "file", "group", "entry" } } }
```

`stores/tiles.ts` is the one adapter over it: it reads it forgivingly (a malformed entry is dropped; links must be `http(s)://`, the hub's own rule), edits it immutably, and `WidgetHost` provides each item's entry to the widget it renders. **Tile.vue applies `name`, `href` and `chromeless` to every widget's identity line**, so renaming or relinking works for any widget type without the widget knowing; widgets read the rest with `useTile()`. `bind.resource` overrides the target in the item id (a queue pointed at the other Sonarr keeps its id). The generic `service` tile takes its status from `bind.check`, else `bind.instance`, else `bind.resource`. The Homepage importer writes this document; the last-known snapshot keeps it.

**Routes.** A hand-rolled history router (`lib/router.ts`, about 1 kB) instead of vue-router (about 10 kB gzip): there are six routes, all owned by the signed-in shell, and the hub already answers any non-file path with `index.html`.

| Route | What it shows |
|---|---|
| `/` | the front page on the first board |
| `/boards/{id}` | the front page on that board (where an import lands) |
| `/settings/integrations` | instances and their status; `/new` the catalog, `/new/{type}` the add form, `/{id}` the edit form (`?field=` focuses a field, `?reason=auth` the first secret) |
| `/settings/import` | import from Homepage |
| `/settings/backup` | export this hub as a Home file; import one with the diff preview |
| `/settings/security` | passkeys |

Settings is a sheet over the front page, not a separate app: the board, the socket and the Inbox stay live underneath, and Esc goes back.

**The Inbox** reads the `inbox` topic the same way in demo and real mode, grouped by status: **open** (problems, down first, then repairs), **discovered** (services found by their labels, below), **suggestions** (class `suggestion`: the hub's correlator asking whether two things are one; **Merge** or **Link them**, and **Keep separate**, answer through `POST /api/correlate/suggestions/{id}/confirm` or `…/reject`, and count in the Inbox badge until answered), **being fixed** (a fix was accepted, the phase says it's in hand, or it's info), and **resolved**. Groups hold still while the Inbox is open and re-sort the next time it opens. A **repair** (class `repair`) offers **Reconnect**: the integration's settings with the failing field focused (`facts.field`, else the first secret for a 401/403); its target is `integration:<instance id>`, resolved by id, then name, then the only instance of that integration.

**Safety ladder.** In demo mode only (`GET /api/demo` says so), an incident's suggested action runs through `POST /api/demo/incidents/{id}/fix`: T0 and T1 straight away, T2 behind the hold-to-confirm ring (about 650 ms, with the blast radius lighting up as you hold). Elsewhere the flow shows what Cha Dash would do and the button says actions arrive with the action engine. `link.open` (T0) opens the target's link in any mode. T3 and T4 aren't offered yet. Progress and the resolution arrive on the `inbox` topic.

## The command bar (⌘K)

⌘K or Ctrl K anywhere, or `/` outside a text field. It searches boards, the Home Graph's resources (fuzzy over name, kind and ID; `lib/fuzzy.ts`), integrations (from `GET /api/integrations/instances`), open incidents and Settings, and runs Edit layout, the six themes and Sign out. Opening a resource shows its open incident in the Inbox, else scrolls to and rings its tile, else opens its link.

It's an ARIA combobox: focus stays in the input, the active option is `aria-activedescendant`, groups are `role="group"`; arrows move (and wrap), Page Up/Down jump, Enter runs, Esc closes. `CommandPalette.vue` is its own chunk, mounted only while open.

## Settings

**Integrations.** The catalog (`GET /api/integrations/catalog`) and the instances (`GET /api/integrations/instances`) with status and warnings. The add and edit forms are generated from each manifest's `config_schema` (`lib/schema.ts`): string, number, integer, boolean, object, array, `enum` or `oneOf` choices, `required`, `default`, `format` uri and hostname, `pattern`, ranges, nested objects as fieldsets, lists of objects, and `if`/`then`/`else` branches; errors are plain sentences. **Secrets (`writeOnly`) are write-only**: inputs start empty whatever the hub sends, a "set" chip says one is stored, blank keeps it, typed values live apart from the config, go out only when typed and are cleared after saving. **Test connection** posts unsaved settings to `/api/integrations/test` (a saved, unchanged instance to `/instances/{id}/test`) and shows version, latency, grants (write grants flagged on a read-only connection), warnings and errors. Delete asks in place first. **The recipe** (`settings/RecipePanel.vue`, `settings/recipe.ts`) sits beside the form, open while adding and folded while editing: how to make a credential with exactly the access the integration needs, as numbered steps with commands to copy, labelled honestly (read-only, limited, or full access only where the service has nothing narrower). Its generator fills the commands with the names you choose and, where a command needs them, the form's URL and the instance's name; a value its parameter's pattern refuses never reaches a command, a command with a missing value shows the gap and can't be copied, and copying falls back to the copy command on plain-http hubs, where the Clipboard API is off. **Use in the form** carries what the recipe determines (Proxmox's token ID) into the form, and after Test connection the panel says whether the credential holds more grants than the recipe gives it. Recipe text allows only `code` and **bold**, rendered as elements; nothing is ever inserted as HTML. On wide screens the sheet widens so the recipe fits beside the form. A secret field marked `x-chadash-authorize` (Plex's token) gets **Sign in with …** (`settings/AuthorizeButton.vue`): it opens the provider's page in a new tab with its opener cut, polls the hub, and fills the field as if typed; nothing is saved until Save.

**Import from Homepage.** The source picker (Homepage; Homarr and Plain YAML marked v0.2), `services.yaml`, `docker.yaml` and `settings.yaml` pasted, uploaded or dropped, then the dry run as the preview: sections and tiles, connections and their secret fields (and missing ones), secrets moved as a count, hints, what discovery fills in later, unmapped entries, ignored settings, and how a re-import compares. Apply lands on the new board.

**Imported tiles.** Every imported Homepage entry without a catalog widget is a `service` tile (`service@homepage:<section>/<entry>`, small or wide). Its name, link and description come from the board's tile config, which the import writes; its status from the check or instance that watches it. The import record (`GET /api/import/homepage`) no longer holds tiles.

**Backup.** `settings/BackupPane.vue`, loaded with the pane. **Export** downloads the hub as a Home file (`GET /api/export`: boards, layouts and tiles, connections, carried settings; secrets as `!secret` placeholders). "Include secrets, encrypted with a passphrase" asks for it twice (12 characters at least) and posts it to `POST /api/export`; the file arrives as a blob URL and an `<a download>` (no `data:` URIs under the CSP), and the passphrase fields are cleared once it's used. **Restore or import** takes a Home file chosen, dropped or pasted. When it carries a secrets bundle a passphrase field appears; a wrong one is said at that field. The dry run (`POST /api/import/chadash?dry_run=1`, read by `model/backupPlan.ts`) shows the totals, the bundle's state, warnings, the secrets still needed, then a section per kind (boards, layouts, tile settings, connections, settings): each created, updated or stale object with its field changes (`+` added, `~` changed, `−` removed, `⇅` reordered, paths in words) and its secrets by field (restored, kept, needed), and the unchanged ones folded into a count. Apply posts the same body, toasts the totals, reloads the instances and the board on screen, and points at Integrations for any secret still needed. Secret values never reach the page; the hub only names them. Cha Dash YAML lives here rather than in Import's source picker: importers move another dashboard's config in, while a Home file is this hub's own state going out and back (with its encrypted bundle, stale objects and field-level diff), so it sits next to Export. Import's source section links here.

**Security.** Passkeys: list (synced, other RP ID, clone warning), rename, delete after a confirmation, add one on this device.

## First run: Connect your home

After setup and the passkey offer, the front page opens with the wizard over it (`onboarding/`, its own chunk). The hub keeps where the owner is (`GET`/`PUT /api/onboarding`, one settings row), so it shows once per hub: on a new hub with nothing connected (never in demo mode), and again on each visit until it's skipped or finished. Closing it (✕ or Esc) keeps the step for next time; **Skip setup** and **Open the front page** end it; **Settings → Integrations → Setup guide** opens it again.

| Path | What happens |
|---|---|
| **Import from Homepage** | Opens the importer in Settings, above the wizard. Applying lands on the new board, and the wizard moves to its last step |
| **Discover** | Asks the hub to look for the socket proxy `compose.yaml` runs beside it (`tcp://socket-proxy:2375`, the only address probed without asking). Docker found there is added in one click. **Look further** checks only what you list: machines by name or address (no ranges, subnets, ports or schemes, at most 8), and well-known Docker addresses you tick. The hub sends GET requests only. A find that needs a credential opens its add form with the address filled in (`draft.ts`) and the recipe beside it |
| **Add one by one** | The catalog grouped the way a home is, each with its recipe's label; each opens its add form in Settings, and closing Settings comes back |

"Connected so far" follows the instances' statuses while the wizard is open. It ends on the front page: if the board is still the one laid out when the hub was empty (everything on it is also in the default for what's connected now), it's laid out again from the graph, so the new tiles appear; a board someone arranged, or an imported one, is left alone.

## Passkeys

`lib/webauthn.ts` does the browser side without a library: base64url to and from buffers, creation and request options from the hub's JSON, and the credential back as `toJSON()` produces it (the browser's own `parse…FromJSON` and `toJSON` when it has them). `lib/passkeys.ts` talks to `/api/auth/passkeys/*` and turns the hub's codes into sentences (`passkeys_unavailable` is shown as is; `unknown_passkey` is signalled to the password manager). Both load on demand.

- **First run, step 2 of 2:** "Add a passkey", skippable. "Connect your home" follows on the front page.
- **Sign in:** "Sign in with a passkey" (usernameless), and passkeys in the username field's autofill where the browser supports conditional mediation.
- **Errors in words:** cancelled (quiet), not allowed or timed out, already registered, the address doesn't match `CHADASH_PUBLIC_URL`, ceremony expired, the last way to sign in.
- **WebAuthn needs HTTPS or localhost.** On a plain-http address the page explains that inline instead of offering a button that can't work.

## First paint: the inline snapshot

For a signed-in owner the hub embeds the first board's snapshot in `index.html` (`internal/firstpaint`, `internal/webui/inline.go`), so the front page renders with real values on its first paint, then goes live.

- **Format:** `<script type="application/json" id="chadash-snapshot">` holding `{v: 1, user, boards, board: {id, name, revision, format_version, layout, tiles}, demo?, graph, metrics, inbox}`: the documents the socket would send, trimmed to what the front page shows (every open incident and only the newest resolution; series of resources in the graph). It's at most 512 KiB; a bigger one isn't inlined and the page starts the usual way.
- **CSP:** a JSON data block never executes, so `default-src 'self'` holds and nothing inline runs. The JSON escapes `<`, `>` and `&`, so no resource name can end the element; `lib/inline.ts` reads it as text, parses it, and removes it.
- **Who gets it:** only a valid owner session (checked by `auth.Identify`); everyone else, and the service worker, get the generic shell.
- **Caching:** the personalised page is `Cache-Control: no-store` with no ETag (`Vary: Cookie`); the generic shell keeps its validator. The service worker precaches the shell without cookies and never keeps a `no-store` navigation.
- **The page:** `stores/session.ts` starts signed in with no request (the hub checked the session to inline it); `FrontPage` hydrates the live document (`live.hydrate`: current values, so no skeletons and no stale treatment) and seeds the board without `GET /api/boards`. Demo times are UTC from the first paint. The socket's first snapshots replace the documents in place: same values, no change on screen. It's preferred over the IndexedDB snapshot, which is only for an unreachable hub.

## Offline and the PWA

- **Installable:** `public/manifest.webmanifest`, PNG, maskable and SVG icons, an apple-touch-icon.
- **Service worker:** `sw/service-worker.js`, emitted as `/sw.js` by the `chadash-sw` plugin in `vite.config.ts` with this build's shell filled in (index.html, the public files, every JS, CSS and SVG asset) and a version hashed from that list. Navigations are network first with the cached shell as the fallback; hashed `/assets/` are cache first (fonts arrive this way); **`/api`, `/ws` and `/healthz` pass straight through, so nothing that carries the session is ever cached.** It registers from the bundle (`lib/pwa.ts`), never an inline script, and only where service workers exist (a secure context).
- **Last-known snapshot:** `lib/snapshot.ts` keeps the graph, metrics and inbox, the board's layout, who was signed in, the display time zone and the hub's clock offset in IndexedDB, behind try/catch and a timeout. `stores/persist.ts` refreshes it at most every 15 s while live and when the tab hides. Signing out deletes it.
- **Hub unreachable at start-up** (a network error, or a proxy's 502/503/504) with a snapshot on this device: the front page opens on it, every tile hatched, under the offline banner with its "as of" time. When the socket is back, the session is confirmed with the hub (or you're asked to sign in) and live data replaces the snapshot.

## Edit mode: `@chadash/layout-vue`

`board/BoardSurface.vue` is the only file that picks the renderer. A front page you only read uses `StaticBoard`. **Edit layout** swaps in `board/EditableBoard.vue`, which wraps `<LayoutBoard>` (drag with momentum, snapping resize, keyboard moves) and loads on demand, so the gesture and motion code never ride with a read-only page.

- Each committed gesture goes through `useBoard().save(next)`: it shows the board, PUTs it with the current revision, and on a 409 loads the other writer's layout. You get a toast either way something goes wrong.
- **Add widget** (the toolbar, or a section header's **Add**) opens `board/WidgetPicker.vue`, its own chunk loaded on first use. It lists the catalog (`model/widgets.ts`) generically, ready entries first: what the graph can feed (`model/feeds.ts`: a few types are fed by another kind, `fleet` by hosts, `downloads` by qBittorrent; any other type, including ones added later, by resources of its own kind or apps of its own type), in the sizes that fit the chosen section. A widget bound to one resource asks which one (`arr` lists each Sonarr and Radarr not already shown) and gets `<type>@<resource>`; a singleton already on the board is listed as such; the generic service tile asks for a name, a link and what gives it a status, and gets `service@tile:<random>` plus its tile config. It lands in the first gap that fits (`smartInsert`), so nothing else moves.
- **Edit / Remove** act on the selected tile (the last one focused or grabbed), from the toolbar. Edit (`board/TileEditor.vue`, lazy) changes the name, the link, and the binding: another resource for a bound widget, a check, an integration or any resource for a service tile; the rest of the config is kept. Remove (or Delete / Backspace on a focused tile that isn't picked up) never asks; the toast says Undo puts it back. Removing a tile that label discovery added also dismisses its container, so it isn't suggested again.
- **Undo / Redo** (buttons, or ⌘Z / ⇧⌘Z) step over the board and its tiles together (`board/useBoardHistory.ts`, on `@chadash/layout`'s history): one step per gesture, add, edit or remove, and they save what they step to. Tiles go along in the PUT only when the step changed them.
- Item labels in announcements come from the widget name plus its tile name or resource ("Host: jupiter", "Now playing: Living room").

## Label discovery

Containers carrying Homepage's `homepage.group` and `homepage.name` labels (the Docker integration publishes them on `kind: container` resources; `integrations/docker/README.md`) are offered as `service` tiles, never added silently (`model/discovery.ts`, `stores/discovery.ts`):

- **Where:** the section whose title matches the group, case-insensitively, or whose id is its slug (the empty Apps section the importer creates); otherwise a new section at the end, 4 wide.
- **What:** `service@<container id>`, small, with the tile config `{name, href, icon, description, bind: {resource: <container id>}, origin: {source: "labels", group, entry}}` from `homepage.name`, `.href` (http(s) only), `.icon` and `.description`, ordered by `homepage.weight`.
- **Not offered:** a container a tile is already bound to; one whose name a tile in that section already has (the import brought it from `services.yaml`); one that was dismissed. Containers with the same group and name (one service seen by two sources) are one suggestion, dismissed together.
- **The Inbox item:** "3 services found by their labels. Add them?", with where each goes. **Add them** puts them all on the board in one save; **Dismiss** remembers the answer on the hub, per board: `GET /api/boards/{id}/discovery` → `{"dismissed": [...]}`, `POST /api/boards/{id}/discovery/dismiss` `{"resources": [...]}`. The question counts in the Briefing's Inbox badge until it's answered.
- The dismissed list is fetched only once something could be suggested, and nothing is suggested before it arrives, while the hub is unreachable, or on an unsaved board.

## Widgets

The catalog (`model/widgets.ts`) covers the Workbench list's v0.1 rows ([docs/design/workbench.md](../docs/design/workbench.md)): each size is its own layout, never a scaled copy, and every widget designs the eight states. Widgets find what they show by kind and relation, or by `resolve()`/`appsOfType()` for apps, never by fixed ID, so the Demo Home's `app:sonarr` and a real `sonarr:<instance id>/app` read the same way. Targets come in the item id (`<type>@<target>`) or the tile config's `bind.resource`. Lists show one of each thing: a resource the correlator marked `same_as` another is swapped for its primary, and a container row names every source that reports it.

| Type | Sizes | Target | Reads |
|---|---|---|---|
| `service` | glyph · small · wide | an app, a check, or a tile config (`bind.instance`, `bind.check`) | the per-integration stat in `widgets/serviceStats.ts`, by `attrs.type`: Sonarr/Radarr `queue`, `wanted`, next release from `attrs.calendar`; Prowlarr `grabs`; Bazarr `wanted` (else `missing.*`) with its trend; Traefik `attrs.routers`; Uptime Kuma `monitors.up` of `attrs.monitors`; Immich `photos`; Mealie `recipes`; Linkding/Karakeep `bookmarks`; Tautulli, Plex, Jellyfin `streams`; qBittorrent, SABnzbd `dl.mbps`; Pi-hole/AdGuard `blocked.pct` with `blocklist`; Home Assistant `people_home`; Frigate `attrs.cameras`; PBS `tasks.failed_24h`; Seerr `requests.pending`; a plain URL (`check`) `attrs.status_code` and its latency |
| `metric` | glyph · strip · small · wide | a series, `<resource id>#<metric key>` | the series: value, `unit`, `band`, `warn` (the threshold line), history |
| `check` | glyph · strip · small · wide | a check, an Uptime Kuma app, a checks instance, or none (all) | kind `check` (built-in and Uptime Kuma monitors): health, `http.ms`/`tcp.ms`/`ping.ms`/`response.ms`, `attrs.uptime_24h`, `target`, `status_code`; the Demo Home's apps' `http.ms` and Uptime Kuma's `monitors.up`/`monitors.down` |
| `fleet` | wide · tall · large · hero | — | hosts' `cpu.pct`, `mem.pct`; guests; clusters |
| `host` | glyph · strip · small · wide · large | a host | `mem.pct`, `cpu.pct`, `temp.c` (when known), `attrs.boot_at`; guests or containers; storage `part_of` it |
| `guests` | wide · tall · large | a host, a cluster, or none | VMs and LXCs: state, `onboot`, `lock` (in progress), `started_at`, `backup.last_ok`/`last_status`, `cpu.pct`, `mem.pct` |
| `containers` | small · wide · tall · large | an engine, a host, or none | containers: state, health, `image`, `started_at`, `finished_at`, `exit_code`/`last_exit`, `oom_killed`; `cpu.pct`, `mem.mib` when published |
| `unraid` | all seven | an array, its host, or none | the array's `used.pct`, `size_tb`, `parity`, `parity_checked_at`; disks' `role`, `used.pct`, `temp.c`; pools; the host's containers |
| `storage` | glyph · strip · small · wide | a storage, pool, array, datastore or disk | `used.pct`, `size_tb`/`capacity_tb`, `full_in_days` |
| `backups` | strip · small · wide · large | a datastore, or the first | the datastore's `used.pct`, `full_in_days`, `prune_job`, `retention`; the app's `tasks.failed_24h`; PVE backup jobs' `last_run.*` |
| `opnsense` | glyph · strip · small · wide · large | a router, or the first | the WAN's `rx.mbps`, `tx.mbps`, `rx.gb_30d`, `tx.gb_30d`; the router's `cpu.pct` (permission-limited without *Lobby: Dashboard*) |
| `cameras` | small · wide · large · hero | a Frigate app or a camera | cameras' state, `record`, `last_event.*`, `detections.24h`; frames are placeholders drawn from tokens |
| `homeassistant` | glyph · strip · small · wide · large | an instance, or the first | `people_home`, `lights_on`, `switches_on`, `power.kw`, `energy.kwh`, the `*_total` attrs |
| `calendar` | strip · wide · tall · large | an instance, else `bind.sources`, else all | `attrs.calendar` of every `media.calendar` app |
| `plex` (Now playing) | glyph · strip · small · wide · large | a Plex, Jellyfin or Tautulli app | `streams`, `streams.transcode`, `bandwidth.mbps`, `attrs.movies`/`shows` |
| `downloads` | glyph · small · wide · tall | a qBittorrent or SABnzbd app | `dl.mbps`, `ul.mbps`, `leeching`, `seeding`, `queued`; SABnzbd `queue`, `left.gib`; the *arr `queue` |
| `bookmarks` | glyph · small · wide · tall | a group (`labels['chadash.group']`), or none | apps' links and their checks; Linkding and Karakeep as sources |
| `clock` | glyph · strip · small · wide | a second IANA time zone | nothing: the hub's clock in the display zone |
| `weather` | glyph · strip · small · wide | a weather resource, or the first | below |

`arr`, `traefik`, `uptimekuma` and `incidents` keep their small and wide layouts; the service tile covers their stats at the other sizes.

**The states** (`widgets/subject.ts`, `parts/StateNote.vue`): down and attention colour the frame and print one sentence with its time and likely cause ("Down since 14:03 · exit 137 · likely: qBittorrent ran out of memory"), the cause from the open problem on the resource or what it runs on, else the graph. **Needs repair** comes from the integration that reads the resource (an open repair on `integration:<instance id>`, or its health `repair`): the frame stays quiet, values are last-known with "as of", and the note offers **Reconnect** to owners and admins (others are told to ask one); it's never called down. **Off, expected** is a healthy resource in a stopped, exited or `paused` state (SABnzbd's "paused (by you)"), dimmed. **Maintenance** is ‖ and dimmed; a running parity check or a guest's backup lock is **in progress** (◔) instead. Hub offline hatches every tile except the clock.

**Permission-limited**, until the hub has field-level permissions: a resource lists what it didn't send in `attrs.withheld` (metric keys or attr names, such as `["photos"]` or `["target"]`), and the widget shows "—" with a lock and "Ask an admin" there; guests the role can't see are counted from the scope's `attrs['hidden.guests']`; bookmarks labelled `chadash.visibility: admin` are hidden from other roles and counted. Integration privileges a token lacks (OPNsense's CPU and gateways) show the same lock with the privilege's name.

**Weather** reads `weather:<instance id>/here` (kind `weather`; [integrations/weather/README.md](../integrations/weather/README.md)): metrics `temp.c`, `feels.c`, `humidity.pct`, `wind.kmh` (always metric; `attrs.units: "imperial"` converts to °F and mph here); attrs `location`, `summary` (else the `condition` slug in words), `is_day`, `high_c`, `low_c`, `precip_pct`, `hourly` (`[{at, temp_c, condition, precip_pct, is_day}]`, nulls shown as "—", hours on the place's own clock), `attribution` and `attribution_url` (shown at small and wide). Health `attention` (a thunderstorm or freezing rain within six hours) is the hub's sentence; an unreachable provider leaves the series stale, so the last forecast stays, hatched. Without a weather resource: "Not connected. Set a location to see the weather here."

**Loading.** The widgets the default board uses ride with the front page, as before. `metric`, `check`, `guests`, `unraid`, `storage`, `bookmarks`, `clock` and `weather` load on demand, each its own chunk (about 1.5–4.5 kB gzip), behind a skeleton tile; `preloadWidgets()` fetches them ahead (the tests do). The picker learns what the graph can feed each of them from `model/feeds.ts`.

## The CSP

The hub serves the app with `default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'`. So:

- **No inline scripts or styles.** The theme bootstrap is `public/boot.js`, a file. Templates never use static `style="…"` attributes; dynamic values go through `:style` bindings, which use the CSSOM.
- **No `data:` URIs.** `build.assetsInlineLimit` is 0, so every asset is a file. Paper's grain ships from `@chadash/tokens` as an SVG file next to `tokens.css`, and Vite bundles it like any other asset.
- **Self-hosted fonts.** Instrument Sans, Geist Mono, Doto, Schibsted Grotesk and Newsreader come from the `@fontsource-variable` packages (OFL-1.1), declared in `styles/fonts.css` under the family names the tokens use. A theme only downloads its own faces; Newsreader keeps its optical-size axis for Paper's headlines.
- **Same-origin only.** `fetch` uses relative URLs; the WebSocket URL is built from `location`.

## Budgets

From [03 · Architecture → Performance budgets](../docs/plan/03-architecture.md#performance-budgets-enforced-in-ci-where-possible), the ones that run headlessly:

| Budget | Number | Today | Where it's enforced |
|---|---|---|---|
| Initial JS | ≤ 150 kB gzip | 39.4 kB | CI, Build job, "Bundle-size budget" (blocking) |
| `GET /` with the inline snapshot, hub side | median ≤ 10 ms, p95 ≤ 50 ms | about 1.1 ms median, 1.3–8 ms p95 (M4 Pro) | `internal/server/budget_test.go`; CI "Performance budgets" (blocking, without `-race`) |
| Inline snapshot size, Demo Home 13:00–20:30 | ≤ 200 kB (hard cap 512 KiB) | 144.6 kB | same test and step |
| Realtime diffs, one tab, Demo Home | ≤ 55 kB per simulated minute | 46.9 kB | `internal/demo/budget_test.go`; same step |
| Hub RSS, empty, no tabs | ≤ 60 MB peak | 41 MB (macOS) | `spikes/footprint/budget_test.go`; CI Footprint job (non-blocking) |
| Hub RSS, Demo Home, 20 tabs | ≤ 120 MB peak | 55 MB (macOS, earlier run) | same |
| Hub image | ≤ 40 MB gzip, per platform | — | CI, Image jobs, "Image-size budget" (blocking) |

**Bundle.** The initial JS is the entry chunk only (no modulepreloads): the Vue runtime, the doors, the session and the router. The front page, the Inbox, the confirm dialog, the command bar, Settings, the first-run wizard, the passkey offer and the WebAuthn code load on demand; motion-v rides with the Inbox and the confirm dialog. It was 37.3 kB before wave 4 and 39.4 kB after it (`boot.js` 0.5 kB plus the entry chunk). The first-paint code (`lib/inline.ts`, a few hundred bytes) rides in the entry chunk.

## Accessibility

WCAG 2.2 AA is a release gate ([02 · Experience](../docs/plan/02-experience.md#accessibility-a-release-gate-not-a-nice-to-have)). In `bun run test`:

- **axe-core** (`test/axe.ts`, pinned dev dependency) over the doors (loading, setup with errors, the passkey offer, sign-in after a failure, unreachable), the front page (busy, quiet, offline, edit mode), the Inbox (a problem, a repair), ⌘K (empty and with results), the confirm dialog, toasts, every Settings pane (Backup included) and an integration form with errors (`test/a11y-views.test.ts`); the Inbox's Discovered and Suggestions groups, the widget picker and the tile editor, the first-run wizard's steps and a Backup preview (`test/a11y-more.test.ts`); and every catalog widget, the on-demand ones preloaded, at every size in six states: loading, empty, healthy, today's incidents, stale, and the hub's graph shape (`test/a11y-widgets.test.ts`). It runs the WCAG 2.0–2.2 A/AA rules plus best practice; a planted violation proves it sees the tiles.
- **Contrast** can't be measured in happy-dom (no layout or paint), so it's checked on the tokens: `design/tokens` holds every text/background token pair to 4.5:1 in all six themes, and `test/token-pairs.test.ts` checks every pair the components' CSS declares (`color` with `background` in one rule) against the generated tokens.
- **Token rule:** `--text-3` is for labels on `--bg` and `--surface`; on a hover row (`--surface-hover`) a component moves it up to `--text-2`.

## Versions

| Package | Version | Note |
|---|---|---|
| vue | 3.6.0-rc.10 | Newest 3.6 prerelease, exact. Bun warns that motion-v's and vueuse's peer ranges exclude prereleases; that's expected |
| vite | 8.3.2 | with @vitejs/plugin-vue 6.0.9; `resolve.dedupe: ['vue']` |
| typescript | 6.0.3 | Pinned here. vue-tsc 3.3.12 loads `typescript/lib/tsc`, which the native TypeScript 7 package doesn't ship (`ERR_PACKAGE_PATH_NOT_EXPORTED`); the other workspaces stay on 7 |
| vitest | 5.0.3 | with @vue/test-utils 2.5.1 and happy-dom 20.14.5 |
| motion-v | 2.5.2 | with its peer @vueuse/core 15 |
| fake-indexeddb | 6.2.5 | dev only: IndexedDB for the snapshot tests (happy-dom has none) |
| axe-core | 4.13.0 | dev only: accessibility checks in Vitest (MPL-2.0; never shipped) |

## Not yet

- Detail sheets for tiles; tiles with an open incident open the Inbox on it instead.
- Rolling numerals, lenses, wall and ink levels of detail.
- Real camera frames (placeholders drawn from tokens), service logos (monograms: the CSP forbids hot-linking, and the hub doesn't cache icons yet).
- Widget detail the sources don't publish yet: per-torrent download items, who's watching and recently added, Home Assistant's picked entities, per-night backup history, failed *arr imports, the 30-day storage history (sparklines show the 24 h the metrics keep), linked scrubbing across sparklines.
- Undo toasts for T1 and type-to-confirm for T3, which need the action engine.
