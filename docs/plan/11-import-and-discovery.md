# 11 · Import and discovery: one way to bring a home in

> **Status: built** (2026-10-30; agreed 2026-10-29). All four waves are in; see [Status](#status) for what differs from this plan and what still needs a live run. A full UI/UX for everything that brings things into Cha Dash: another dashboard's config, a Cha Dash export, the hub's own probes, and what connected integrations notice later. Target: v0.2.x, alongside the Homarr and plain-YAML importers the [2026 roadmap](09-roadmap-2026.md#v02x--before-v03-no-longer-optional) already plans. Nothing here changes a trust boundary: probes keep their limits, secrets keep their rules.

---

## Why

Cha Dash can already bring a lot in, but each way in is its own screen with its own words:

| Today | Where | What it does | What's missing |
|---|---|---|---|
| Homepage import | Settings → Import | Three text boxes, a dry run, apply all | No way to leave things out; no picture of the board; Homarr and plain YAML are greyed-out buttons |
| Cha Dash restore | Settings → Backup | Field-level diff, passphrase for secrets | A second, different review screen for the same idea |
| Wizard → Discover | First run only | Sidecar, ticked Docker endpoints, up to 8 named hosts | Gone once the wizard ends; every non-Docker hit opens a form, one at a time |
| Wizard → Import | First run | Sends you to Settings and back | Leaves the wizard's layer to do its main job |
| Label discovery | Inbox, per board | `homepage.*` labels become tiles | Only labels; only tiles, never a connection |
| Adoptable stacks | Settings → Stacks | Running compose projects to adopt | Not offered anywhere you'd notice |
| Merge suggestions | Inbox | The correlator's "same thing?" | Fine as is; out of scope |

The pieces underneath are sound: a pure parser per source, `importer.Service` with `Diff` and a transactional, idempotent `Apply`, secrets that redact themselves, and probes that can never become a network scan. What's missing is one front end over them.

## The idea

**Everything that brings things in produces candidates, and every candidate is reviewed in the same place before anything changes.**

```
  Sources                      Review (one sheet)                  Result
  ─────────                    ──────────────────                  ──────
  A file you drop     ─┐
  A probe you run     ─┼──▶  candidates, grouped by what      ──▶  one transaction:
  Something connected ─┘     happens; tick, fill a key,            board, connections,
  integrations noticed       pick where tiles land; preview        vault; an import record
                                                                   you can look back at
```

Three rules hold everywhere:
1. **Preview first.** Nothing is written until you press Apply, and Apply writes exactly what the preview showed (or refuses).
2. **Nothing is deleted.** Imports and discovery only add and update. Stale things are reported, never removed. Revert is a separate, explicit step that only removes what an import created and nobody has touched since.
3. **The hub reads, the browser shows.** Files are parsed on the hub; probes run from the hub; secrets go to the hub once and come back as field names.

---

## Where you meet it

| Entry point | Opens | Notes |
|---|---|---|
| **Settings → Bring in** (renamed from Import) | The pane, on Import or Discover | Tabs: **Import · Discover · History**. `/settings/import` redirects |
| **Wizard → Start** | Same three paths, but Import and Discover run **inside the wizard's layer** | No more bouncing to Settings and back |
| **Drop a file anywhere** (admin, signed in) | The Review sheet, source detected | A full-page drop target appears only while dragging a file |
| **⌘K** | "Import a config…", "Discover…", "Restore a backup…" | Admin only, like every other route that would 403 |
| **Inbox → Discoveries** | The Review sheet with that discovery's candidates | Replaces label discovery's own Add/Dismiss card (it becomes one kind of discovery) |
| **Empty board** | "Bring in what you run" with the same three paths | An empty board is a first run in miniature |

Restore stays in **Settings → Backup**: restoring your own hub is a different promise from importing someone else's config. A Cha Dash export dropped anywhere is detected and opens Backup's restore with the file already loaded.

---

## Import

### 1. Choose or drop

```
┌ Bring in ─────────────────────────────────────────────────────────┐
│  Import   Discover   History                                      │
│                                                                   │
│  ┌──────────────────────────────────────────────────────────────┐ │
│  │      Drop your config files or folder here, or  [Choose…]    │ │
│  │   Homepage · Homarr · plain YAML · a Cha Dash export         │ │
│  └──────────────────────────────────────────────────────────────┘ │
│  Or paste:  [ Homepage ▾ ]  ( text box )                          │
│                                                                   │
│  Where to find them                                               │
│  Homepage   …/config/services.yaml, docker.yaml, settings.yaml    │
│  Homarr     Management → Boards → Export  (a .json or .zip)       │
└───────────────────────────────────────────────────────────────────┘
```

- **Drop several files, a folder or a zip.** The browser reads names and sizes and sends the text; the hub's `POST /api/import/detect` names the source and which file is which. Unknown files are listed as "not used", not as errors.
- **Detection is shown, and can be overruled.** "These look like a Homepage config: services.yaml, docker.yaml. settings.yaml wasn't included; title and layout will use defaults." A source picker corrects a wrong guess.
- **Paste stays**, for people who copy from an SSH session. One box per expected file, as today, behind the picker.
- **Limits are said before they bite.** Homepage stays at 1 MiB. Homarr exports can carry icons, so its limit is 6 MiB, like a Cha Dash export (icons are dropped; the hub's icon cache fetches its own).

### 2. Review

The Review sheet is the centre of this plan. Every source fills the same shape: `importer.Result` with `changes`, which Homepage already returns and the Cha Dash restore nearly does.

```
┌ Review: Homepage → new board "Home" ───────────────── [Cancel] [Apply 31] ┐
│ 31 of 34 selected · 18 secrets move into the vault · 3 need a key           │
│                                                                             │
│ Where tiles go  (●) A new board "Home"   ( ) Into board [Main ▾]            │
│                 ( ) Connections only, no tiles                              │
│                                                                             │
│ ┌ Board preview ──────────────────┐  ┌ What happens ──────────────────────┐ │
│ │ Media        ▢▢▢▢               │  │ ☑ Connections        12 new  2 upd │ │
│ │ Network      ▢▢                 │  │   ☑ Plex             new           │ │
│ │ Infra        ▢▢▢ ▣              │  │   ☑ Proxmox · pve    update  ▸ 2   │ │
│ │                                 │  │   ☐ Sonarr  key needed  [Add key]  │ │
│ │ ▣ highlighted = selected row    │  │ ☑ Tiles              19            │ │
│ └─────────────────────────────────┘  │ ☑ Checks              6 targets    │ │
│                                      │ ☑ Links (runs on)     4            │ │
│                                      │ ⚠ Hints               2            │ │
│                                      │ ⓘ Won't come across   5   ▸        │ │
│                                      └────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Grouped by what happens,** in this order: Connections, Tiles, Checks, Links, Board settings, then two read-only groups, **Hints** (reused secrets, plain http) and **Won't come across** (unmapped and ignored, each with its reason and the entries it came from).

**Each row** shows its name and icon, a change badge, and what it needs:

| Badge | Meaning | Default |
|---|---|---|
| **new** | Created | Ticked |
| **update** | Exists, changes; `▸ n` opens the field-level diff (old → new; secrets as "changes", never values) | Ticked |
| **unchanged** | Already the same; shown collapsed as a count | Not selectable |
| **clash** | You made one by hand with this name; the imported one becomes "Name (imported)" | Ticked, with a "Use my existing one instead" option that binds the tiles to it |
| **stale** | An earlier import made it; the source no longer has it | Informational; nothing is deleted |
| **key needed** | A `{{HOMEPAGE_VAR_…}}` or a missing secret | Unticked, with **Add key** |
| **already connected** | The hub reads this URL through another instance | Unticked, tiles bind to the existing one |

- **Ticking is tied together.** Unticking a connection unticks the tiles and links that depend on it, and says so ("Also leaves out 2 tiles"). Ticking a tile ticks its connection. Group checkboxes are tri-state.
- **Add key** opens the integration's form inline in a side panel: only the missing fields, with the recipe beside them (`RecipePanel`). The value goes into the apply request, never into the preview. Leaving it empty is fine: the connection is created and shows a Repair in the Inbox, as today.
- **The board preview** is the planned layout rendered read-only (`StaticBoard` with placeholder tiles: icon, name, size). Hovering or focusing a row highlights its tile; clicking a tile scrolls to its row. Into an existing board, the preview shows that board with the new tiles marked, placed by the engine's reflow at the end of each matching section, or in new sections. This is the "visual diff before it's applied" from [02 · Experience](02-experience.md).
- **The summary line is live**: counts, secrets moved, keys still missing. It is the `aria-live` region, polite.
- **Phones** get one column: the summary, "Where tiles go", the groups, and the preview behind a "Show the board" disclosure.

### 3. Apply

- **Apply n** sends the files again with the reviewed plan's hash, the choices (`skip`, `target`, `use_existing`) and any keys typed. The hub rebuilds the plan, applies the choices, and refuses with `409 plan_changed` if the result differs from what was reviewed (the board changed in the meantime, or another import ran). The sheet then re-previews and says what moved.
- **One transaction**, as today. Progress is a single busy state; imports are fast.
- **Landing.** You land on the board, its new tiles marked "new" for 10 seconds (a ring, not motion, under reduced motion). The toast says what came in and links to the import's History entry: "Imported from Homepage: 19 tiles, 12 connections. 18 secrets are in the vault. 3 connections need a key."
- **Wizard.** Applying inside the wizard moves it to Done, as now, without leaving its layer.

### 4. Importers

| Source | Detect by | Reads | Status |
|---|---|---|---|
| **Homepage** | `services.yaml` shape: a list of groups of entries | `services.yaml`, `docker.yaml`, `settings.yaml` | Built; gains `skip`, `target`, `use_existing` |
| **Homarr** 2.x | Its backup zip (Management → Tools → Backup): `db.sqlite` and `metadata.json` | Boards, sections and items, apps, integrations with their URLs | New. Secrets: Homarr encrypts them with its own key, so every integration is **key needed** and the recipe does the rest |
| **Homarr** 0.x (legacy) | `configs/*.json` with `schemaVersion` | Apps, categories, widgets | Not now ([Decisions](#decisions) 3). Detected, and answered with how to go through Homarr 2's own migration |
| **Plain YAML** | `kind: Board` | Boards, sections, tiles with sizes and order, and the services they connect to | New. For people with no dashboard, or who'd rather write a file than fill ten forms. Format [below](#plain-yaml) |
| **Cha Dash export** | `kind: Home` | Everything | Built; opens Backup's restore |

Every importer is the same two halves as Homepage: a pure parser in `internal/importer/<source>`, and the shared `importer.Service`. The parser's job ends at `importer.Result`; the Review sheet never knows which source it's showing beyond the title and the "Where to find them" text.

### Plain YAML

Plain YAML describes a full layout ([Decisions](#decisions) 6), so it sits close to the Home file's `boards` and `instances`. The risk is two formats that slowly disagree. The rule that prevents it: **plain YAML is a hand-writable front end to the Home file's board types, not a format of its own.** `internal/importer/yaml` parses into `internal/export`'s board, tile and instance structs, and from there into the shared plan. A change to those types breaks the plain-YAML tests, not production.

What it leaves out is what a person shouldn't have to write: IDs, coordinates, `supportedSizes`, `format_version`, TLS pins, origins.

```yaml
apiVersion: chadash/v1alpha1
kind: Board
name: Home
sections:
  - title: Media
    span: 8                      # of 12; default 4
    tiles:
      - name: Sonarr
        href: https://sonarr.home.example
        connect: sonarr          # an integration ID; the tile binds to it
        url: https://sonarr.home.example
        api_key: 0123abcd…       # moved into the vault, then a hint says to remove it from the file
        size: wide               # optional; the widget's default otherwise
        widget: queue            # optional; the integration's catalog widget otherwise
      - name: Plex
        connect: plex
        url: https://plex.home.example
        token: ${PLEX_TOKEN}     # not resolved: "key needed", like a Homepage variable
  - title: Links
    tiles:
      - {name: Wiki, href: https://wiki.home.example}   # a plain link tile, with an http check
```

- **Order is position.** Tiles flow in file order through the engine's reflow, like the Homepage importer. Explicit `x`/`y` aren't accepted; a layout that needs them is edited in the app.
- **IDs are derived** from the board name, section title and tile name, so re-importing the same file changes nothing, and renaming a tile is an update plus a stale one, as with Homepage.
- **Connections are shared.** Two tiles with the same `connect` and `url` use one instance. `name:` on a tile names the instance only when it's the first to use it.
- **Several boards** go in one file as a `kind: Boards` list of the same shape.
- **Secrets** are allowed in the file (people will put them there anyway): they move into the vault, and the Review's Hints say which fields to remove from the file afterwards. `${VAR}` is never resolved.
- **The schema** is generated from the same structs and published with the OpenAPI document, so an editor can validate the file while it's written.

---

## Discover

Discovery outlives the wizard. It becomes the **Discover** tab, and its results go through the same Review sheet.

### Three kinds of looking

| Kind | What the hub does | When | Asks first? |
|---|---|---|---|
| **This host** | The local node (`node://local`) or the compose sidecar | When the tab opens | No: it's the hub's own sidecar |
| **Places you name** | Docker endpoints you tick, and machines you type (at most 8, one machine each, no ranges, GET only, 20 s) | When you press Look | Yes, by naming them. Limits unchanged from `internal/onboarding` |
| **Leads from what's connected** | Reads the graph, never the network: containers whose image or `homepage.widget.type` matches the catalog (`catalogMatch`), `homepage.*` labels, reverse-proxy routes (Traefik routers, their hosts), Proxmox and Unraid guests with addresses, compose projects that can be adopted | Continuously; new leads go to the Inbox | No network access to find them. **Checking** a lead (probing its URL to confirm the version) is one click per batch, and only probes the addresses the leads name |

Leads are the new part. The hub already knows `sonarr` runs in a container on `pvedocker`, published on port 8989, and that Traefik routes `sonarr.home.example` to it. It shouldn't take a typed hostname to suggest connecting Sonarr. Nothing is probed until the owner presses Check; the probe set is exactly the URLs on screen.

### The tab

```
┌ Bring in ─────────────────────────────────────────────────────────┐
│  Import   Discover   History                                      │
│                                                                   │
│  This host        Docker via chadash-node · 23 containers  ✓ Added│
│                                                                   │
│  Leads  (9)       From what's connected · [Check all 9]           │
│   ▣ Sonarr        container sonarr on pvedocker · :8989            │
│                   also routed as sonarr.home.example   key needed │
│   ▣ Jellyfin      label homepage.widget.type=jellyfin   ready     │
│   ▣ Stack "media" running, not managed               [Adopt…]    │
│   …                                                               │
│                                                                   │
│  Look somewhere   Machines  ( nas, 10.20.0.5 )                     │
│                   Docker    ☐ tcp://dockerproxy:2375 …            │
│                   [Look]                                          │
│                                                                   │
│  [Review 7 selected]                                              │
└───────────────────────────────────────────────────────────────────┘
```

- **Each candidate** says how it was found (container, label, route, guest, probe), where it is, and whether it's **ready** (no credential: Docker, Uptime Kuma's public status page), **key needed**, **already connected**, or **didn't answer** (with the reason in words: refused, timed out, answered but not as Sonarr).
- **When one service is found several ways** (a container, a label, a route), it's one candidate listing every way, and the suggested URL prefers the route over the published port, the published port over the container address.
- **Review n selected** opens the Review sheet with the candidates as connections, and, when they have tiles, as tiles into the board you pick. Ready ones apply in one click; key-needed ones get the inline key panel.
- **Adopting a stack** is an action (`stack.adopt`, T1), so it runs through the action ladder, not the Review sheet. The lead links to it.
- **Dismiss** is per candidate and remembered on the hub (generalizing the per-board label dismissals to hub-wide discovery dismissals), with Undo in the toast and a "Dismissed · n" list to offer them again.

### In the Inbox

New leads arrive as **one** discovery incident per batch, never one per container: "4 new services found on pvedocker. Review?" It opens the Review sheet. The existing label-discovery card becomes this, so there's one discovery item, not two. The hub batches leads for a minute after a sync so a `docker compose up` of ten containers is one item.

---

## History

Every apply, from any source, leaves a record in **History**: when, who, the source, the files' names (never their contents), counts, and the IDs it created.

| Action | What it does |
|---|---|
| **Open** | The board, or the connections it made |
| **Import again** | Opens Import with that source picked: drop the newer files, and the Review shows only what changed, plus what's now stale |
| **Revert…** | A dry run lists what the import created and what has changed since. Revert removes only what's unchanged since the import, keeps everything else, and says which. Admin, step-up when it would remove connections, audited |

The hub doesn't keep the files. Revert is driven by the record's IDs, not by re-parsing.

---

## States to design

| Surface | Loading | Live | Error | Empty |
|---|---|---|---|---|
| Drop zone | Reading n files… | Detected: Homepage (2 of 3 files) | Not a config we know; which source is it? | Where-to-find text |
| Review | Building the preview… | The grouped list and board | Line-numbered parse error (the hub names file and line, never text) | "Nothing to import: everything is already here" |
| Review after 409 | Re-previewing | "3 things changed since you looked" banner | — | — |
| Discover · this host | Asking the node… | Found / added | Why it didn't answer, with the fix | No node and no sidecar: how to add one |
| Leads | — | List with sources | — | "Nothing new. Connected services are all here." |
| Probe | Checking 4 machines… (with a cancel) | Per-target results | Per target, in words | — |
| History | — | Records | — | "Nothing imported yet" |

---

## Contracts

### HTTP

| Route | Role | Body → answer |
|---|---|---|
| `POST /api/import/detect` | admin | `{"files":[{"name","text"}]}` (≤ 6 MiB) → `{"source","files":{name: role},"unused":[name],"missing":[role]}` |
| `POST /api/import/{source}?dry_run=1` | admin | Files as each source takes them → `importer.Result` plus `plan_hash`. `source` is `homepage`, `homarr`, `yaml` |
| `POST /api/import/{source}` | admin | Files, `plan_hash`, `target` (`new`, `board:<id>`, `none`), `skip` (object IDs), `use_existing` (`{plan instance id: instance id}`), `secrets` (`{plan instance id: {field: value}}`) → `{"import_id","board_id","instance_ids","changes"}`. `409 plan_changed` with the new `Result` |
| `GET /api/imports` | admin | Records, newest first |
| `POST /api/imports/{id}/revert?dry_run=1`, `POST …/revert` | admin, step-up when it removes connections | `{"remove":[…],"keep":[{id, why}]}` |
| `POST /api/discover` | admin | `/api/onboarding/discover`'s body and limits, plus `"leads":[lead id]` to check named leads. The old route stays as an alias for a release |
| `GET /api/discover/leads` | admin | `{"leads":[…],"dismissed":[…]}` |
| `POST /api/discover/dismiss`, `…/restore` | admin | `{"leads":[…]}` |

Each needs its row in `internal/authz/routes.go` and its entry in `authz/audited.go`.

### Plan shape

- `Change` gains a stable `object` ID (`instance:<id>`, `tile:<id>`, `link:<from>→<to>`, `check:<target>`) and `depends_on`, so the sheet can tie ticks together without knowing the source.
- `plan_hash` is a SHA-256 over the canonical plan JSON (no secret values, which aren't in it anyway).
- `Apply` takes `Choices{Skip, Target, UseExisting, Secrets}`. Skipping is applied after parsing and before `Diff`, so idempotency holds: re-importing with the same choices writes nothing.
- Secrets typed in the sheet use the same `importer.Secrets` type, so they redact everywhere.

### Storage

- **Import records** move from `settings["import/<source>"]` to an `imports` table (migration `00016`, or the next free number): `id` (`imp_…`), source, actor, created_at, file names, counts, created IDs, the record's metadata. One record per apply. Homepage's existing record is migrated into it.
- **Discovery dismissals** become hub-wide (`discovery_dismissals`), keyed by a lead's stable key (integration + normalized URL, or resource ID). Per-board label dismissals migrate in.

### Web

- `web/src/import/`: `DropZone.vue`, `ReviewSheet.vue` (with `ReviewGroup`, `ReviewRow`, `FieldDiff`, `BoardPreview`, `KeyPanel`), `HistoryList.vue`, `model.ts` (the generic plan model, replacing `model/importPlan.ts` and folding in `model/backupPlan.ts`'s diff rendering).
- `web/src/discover/`: `DiscoverPane.vue`, `LeadRow.vue`; the wizard's `DiscoverStep.vue` becomes a thin wrapper.
- A page-level drop target in the signed-in shell, admin only, lazy: the import chunk loads on the first drag.
- Budget: the Review sheet, preview and drop zone load on demand, never in the initial bundle.

---

## Accessibility

- The Review list is a tree of checkboxes with real `<input type=checkbox>` and `indeterminate` on groups; the dependent-tick message is announced.
- Board preview tiles are buttons that move focus to their row, and are hidden from the accessibility tree's reading order (the list is the source of truth; the preview is a picture of it).
- Drop zones are also buttons and accept files from the keyboard; a page-wide drop has the Choose button as its equivalent.
- Busy states use `aria-busy` and a polite live region; errors use `role="alert"` once.
- axe runs on every state in the table above.

## Security

- Probes keep `internal/onboarding`'s limits exactly. Leads add **no** new network reach: they come from the graph, and checking them probes only their stated URLs, under the same caps.
- File contents never reach logs, audit or responses; errors name the file and line.
- Secrets typed in the Review sheet follow the importer's rules; the leak tests that scan responses, rows and logs extend to them.
- Every route is admin. The drop target, the ⌘K items and the Inbox discovery are hidden from lower roles, so the page never calls a route that would 403.
- Revert that removes connections needs a step-up, like other destructive settings changes.

## Tests

- Go: `detect` against every fixture and against near-misses (a Docker Compose file isn't a Homepage config); `Choices` keep idempotency; `409 plan_changed`; revert keeps changed objects; leads from a Demo Home graph; dismissal migration.
- Fixtures: a Homarr 2 export under `testdata/importers/homarr/` (`*.example` domains, fake secrets) and a plain-YAML set.
- Web (Vitest): dependent ticks, group tri-state, clash and use-existing, the 409 flow, the key panel, every state above with axe.

## Waves

| Wave | Tracks | Depends on |
|---|---|---|
| **A · Review** | Plan `object` IDs, `Choices`, `plan_hash`, the `imports` table · the Review sheet and board preview over Homepage · Settings → Bring in, the wizard embedding it | — |
| **B · Sources** | `detect` and the drop zone (page-wide too) · Homarr 2 parser · plain YAML (`kind: Board`) on `internal/export`'s types, and its JSON Schema · Cha Dash export detection opening Backup; Homarr 1.x detection with migration advice | A's plan shape |
| **C · Discover** | `/api/discover`, leads from the graph, hub-wide dismissals · the Discover tab · the Inbox batch replacing label discovery | A's Review sheet |
| **D · History** | History list, Import again, Revert with step-up | A's `imports` table |

A is the most value for the least: it fixes the current importer's all-or-nothing apply and gives Homarr and plain YAML a home.

## Decisions

All six decided 2026-10-29.

1. **The pane is "Bring in"**, with Import, Discover and History tabs. `/settings/import` redirects.
2. **Restore stays in Backup.** Restoring your own hub is a different promise from importing someone else's config. A dropped Cha Dash export opens Backup's restore with the file loaded.
3. **No Homarr 0.x parser for now** (the JSON configs from before Homarr 1.0; "1.x" in the first draft meant these). Homarr 2 ships first. A 0.x config is detected and answered with how to migrate through Homarr 2. Revisit if people ask.
4. **Leads go to the Inbox, batched**, admin only. One item per batch, replacing the label-discovery card.
5. **Revert is in, in wave D.** A dry run first. It removes only what the import created and nobody has touched since, and needs a step-up when it removes connections.
6. **Plain YAML describes a full layout:** boards, sections, tile sizes and order, and connections, but no coordinates or IDs. To keep it from drifting from the Home file, it parses into `internal/export`'s board types ([Plain YAML](#plain-yaml)).

## Status

### What was built

| Wave | Built |
|---|---|
| **A · Review** | `importer.Choices` (skip, target, use existing, typed keys), `Hash` and `ErrPlanChanged`, `DiffWith`/`ApplyWith`, `Change.existing`/`match`, adding to an existing board with its tiles and section overrides kept · migration `00016` (`imports`, `discovery_dismissals`) · `importhttp`, the API every source shares · web: `import/` (BringInPane, ImportFlow, BoardPreview, model.ts), Settings → Bring in, the wizard embedding the flow, new-tile marks |
| **B · Sources** | `POST /api/import/detect` · the drop zone and the page-wide drop target · `importer/homarr` (the backup zip's SQLite, read-only) · `importer/yamlboard` · `importer.Kit` and `TLSFor`/`Slug`/`WidgetSizes` shared with Homepage · a Home file routes to Backup; a Homarr 0.x config gets migration advice |
| **C · Discover** | `POST /api/discover` with `urls` (leads' addresses, at most 16), hub-wide dismissals · `importer/leads` (the `discovery` source) · web: `model/leads.ts`, `stores/leads.ts`, the Discover tab, the Inbox's leads item, ⌘K entries |
| **D · History** | `GET /api/imports`, `GET /api/imports/{id}`, `POST /api/imports/{id}/revert` (dry run, step-up when it removes connections) · web: `HistoryList` with Open, Import again and Revert |

Tests: Go (`importer` choices, hash, history and revert; Homarr and plain-YAML fixtures; `leads`; onboarding's lead checks and dismissals; server: detect, revert's step-up, every new route audited) and web (the Review model, ImportFlow, History, leads, the Discover tab, the wizard's import, axe on the preview, the Discover tab and History).

### Where it differs from the plan

1. **The URL stays `/settings/import`**, with `/discover` and `/history` under it. The pane is named Bring in; no redirect is needed, and old links keep working.
2. **No `object` and `depends_on` on `Change`.** The web derives both from the plan itself (a tile's `instance` and `sources`), which every source already fills. `Change` gained `existing` and `match` for "use my existing one".
3. **Choices re-run the dry run.** The sheet shows rows from the dry run without choices, so a row it left out can be ticked again, and counts, the board and the hash from a second dry run with them (debounced). Apply sends that hash. Typed keys ride with both dry runs; they're needed for the hash and never come back.
4. **The import record stays where it was.** `settings["import/<source>"]` still holds a source's last state, for stale reports; History is a new table with one row per apply that changed something. Nothing was migrated.
5. **Homarr reads the backup zip.** Homarr 2's only export is the SQLite backup, so the parser opens a temporary copy read-only and immutable (`store.OpenForeign`). The schema is Homarr's own `sqlite.ts`; the fixture is synthetic, built from SQL in the test.
6. **Plain YAML compiles to the importer's plan, not `internal/export`'s types.** Those keep a board's layout and tiles as opaque JSON, so there was nothing typed to share; the plan's layout and tiles documents are what a Home file carries. One board per file; `kind: Boards` and a published JSON Schema are follow-ups.
7. **Leads are found in the web, not by `GET /api/discover/leads`.** The graph, `catalogMatch` and label discovery already live there. The hub adds what the page can't do: checking addresses, remembering dismissals for everyone, and turning picked leads into an import (`/api/import/discovery`) so they get the Review sheet, History and Revert.
8. **Label discovery keeps its card and its per-board dismissals.** It adds tiles to the board on screen, which a hub-wide dismissal can't express. Leads (connections) sit beside it in the Inbox's Discovered group as one item, however many there are, so there's no minute-long batching delay.
9. **Leads come from containers and routes only.** Proxmox and Unraid guests have an address but no service identity, and adopting a stack stays in Settings → Stacks; both are follow-ups.
10. **Revert removes tiles an import added to an existing board while they're still there**, edited or not: tile edits aren't tracked per tile. Connections and a board the import made are kept when they changed since.

### Not verified yet

- **Nothing ran in a browser** (no dev server or build on this track): the drop target, the sheet's layout at phone width, focus moves and the live region are covered by Vitest and axe only.
- **The bundle** wasn't measured. The new pane, the Discover tab and the leads item are lazy; `import/fresh.ts` and `PageDrop.vue` ride with the front page and are small.
- **A real Homarr backup.** The parser follows Homarr's schema and backup docs; the first real backup may show columns or item kinds this didn't expect.
- **Leads against the lab.** Route-to-integration matching uses the host's first label (`sonarr.home.example`); homes that name hosts otherwise get container leads only.
