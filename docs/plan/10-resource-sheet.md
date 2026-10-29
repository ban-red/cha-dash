# 10 · The resource sheet, containers first, and a Nodes pane

> **Status: agreed** (2026-10-29). **Waves 1–4 are built** (see the status sections at the end); streaming logs wait for v0.3. The sheet from [02 · Experience → Surfaces](02-experience.md#surfaces-and-navigation), built for containers first, plus the Nodes pane Settings has been missing since v0.2. Target: v0.2.x, before v0.3 builds remote nodes and the Update Center on top of it.

---

## Why

Cha Dash can act on a container, but the page gives it nowhere to stand:
- **The containers tile only displays.** Its rows aren't buttons or links.
- **Actions are reachable only through ⌘K**, and only if you know the container's name: the tile's resource is the engine.
- **There's no single place for one container:** its state, why it's down, what depends on it, its logs, what was done to it.
- **Nodes have no screen.** Listing them, revoking one and regenerating the pairing token are API-only, and nodes on other hosts are now in use ([deploy/README.md → Nodes on other hosts](../../deploy/README.md#nodes-on-other-hosts)).

Most of what the sheet needs already exists on the hub: the graph and metrics topics, `GET /api/actions?target=`, the action ladder (`ui.runAction`), the `tasks` topic, `GET /api/audit?target=`, and `GET /api/nodes`. Waves 1–3 are web work only.

## Scope

| In | Out (and where it goes) |
|---|---|
| A sheet shell any resource kind can use, with tabs | Sheets for every other kind. Each kind is added when its Overview is designed. Engines and stacks come in wave 3 |
| **Container:** Overview, Actions, Logs (the last 50 lines), Related, History | Streaming logs and exec (v0.3, need hub streaming routes) |
| Opening it from the containers tile, ⌘K, the Inbox and Related | Opening every tile's sheet by tapping the tile: needs the rule for tiles whose link is the point. See Open questions |
| **Settings → Nodes:** list, state, policy verbs, revoke, regenerate the pairing token | Join tokens and Inbox approval (v0.3, [04 · Roadmap](04-roadmap.md#v03-fleet-remote-nodes-everywhere)) |
| A Metrics tab when the source publishes `cpu.pct` and `mem.mib` | Publishing container stats (wave 4: per-resource interest on the hub) |

## The sheet

**One component, `components/ResourceSheet.vue`, lazy, its own chunk.**
- **Where it shows:** over the front page, like the Inbox: a glass panel on the right on desktop, and a full-height card a flick dismisses on a phone. The board, the socket and the Inbox stay live underneath.
- **What it shows:** the resource named by `ui.state.sheet`. It follows the graph live: a recreated container keeps its ID (keyed by name), so the sheet stays on it.
- **Deep link:** `?r=<resource id>` on the current front-page path (`/`, `/boards/{id}`). A query, not a path, so the board under it stays the one you were on, and a reload reopens both. `lib/router.ts` reads it; `ui.openSheet(id)` and `ui.closeSheet()` write it with `replace`, so Back closes the sheet.
- **Layering:** it closes ⌘K, and the Inbox closes it. Esc closes only the topmost layer (`layerAbove`, as the Inbox and the step-up dialog do). Focus goes to the sheet's heading, and back to whatever opened it on close.
- **Gone or hidden resources:**
  - A resource that leaves the graph shows "This container isn't in the graph any more" with its last-known name, and keeps its History tab.
  - One the viewer's role can't see opens to the same sentence: the household graph leaves out nodes.

**Tabs** (WAI-ARIA tabs: arrows move, Home and End jump). The kind decides which tabs exist, and a tab with nothing to show isn't drawn:

| Tab | Container | Who sees it |
|---|---|---|
| **Overview** | Always | Everyone who can see the resource |
| **Actions** | When `GET /api/actions` lists any | Every role, filtered by the hub |
| **Logs** | When the Logs action is listed (Operator and up, node-backed engines) | Operator and up |
| **Metrics** | When `cpu.pct` or `mem.mib` exists for it | Everyone |
| **Related** | When it has relations | Everyone |
| **History** | Always | Everyone sees their own tasks. Admins and the owner also see the audit log |

The header holds the identity line (monogram or icon, name, kind, engine), the status as glyph, word and colour, and the open incident as one line that opens the Inbox on it. **The sheet never duplicates the Inbox:** a problem's facts, causes and fix stay there.

### Container · Overview

Facts, each "—" when unknown:
- **State and why**, the words the tile uses today: "exited (137) at 03:12 · OOM-killed". Move `rowOf` from `ContainersWidget.vue` into a shared module (built as `widgets/containers.ts`, beside the status helpers it uses) so the tile and the sheet say the same thing.
- **Image and tag**, and the update flag when a source sets `update_available`.
- **Started**, as uptime, or **finished** with the exit code.
- **Restart policy and count**, the memory limit, and **health check** with its failing streak.
- **Ports and bindings** (`published`), **networks and addresses** (`ips`), and the network mode.
- **Compose:** project and service, linking to the stack. That's Settings → Stacks → that stack when it's owned (`/settings/stacks/{node}/{name}`, admin and up), else the stack's sheet once stacks have one.
- **Where:** engine, node and host, each opening its own sheet or tile.
- **Seen by:** each source when Docker and Unraid both report it (`sourcesOf`).
- **Links:** `attrs.links`.
- **Labels:** the allowlisted ones the integration keeps, folded.

### Container · Actions

- **Grouped by tier**, from `GET /api/actions?target=<id>`: T0 → T3, each a button with its tier tag. Unavailable ones stay listed and disabled, with the hub's sentence and its recipe link (as ⌘K does).
- **Running one goes through `ui.runAction`**, the same ladder: Undo for T1, the hold ring with blast radius for T2, a typed name and step-up for T3.
- **The task working on this container** shows at the top of the tab: its step, progress and Cancel, from the `tasks` topic.
- **Remove asks about anonymous volumes** before planning: a checkbox, "Also delete its anonymous volumes", sent as `{"volumes": true}`. Named volumes always stay, as the plan says. ⌘K can't pass params today, so this is the first place the choice exists.
- **The list is asked again** when the container's state changes, because preconditions depend on it ("already stopped").

### Container · Logs

- **A "Show the last 50 lines" button** runs `docker.container.logs` (T0). The lines render **in the tab**, not in the log dialog: an `ActionRequest` gains `onLog?: (log: TaskLog) => void`, and `finished()` hands `reads_data` lines to it instead of opening the dialog.
- **Same copy rule as the dialog:** the lines are kept while the sheet is open, never stored, gone on close. Copy, and "Show again" runs it again.
- **The tab says what it is:** "The last 50 lines when you asked. Live logs come with v0.3."

### Container · Related

The graph's neighbours, grouped in plain words, each opening its own sheet when its kind has one, else ringing its tile (`ui.reveal`):
- **Depends on** (`depends_on` out) and **Used by** (`depends_on` and `runs_on` in, apps first).
- **Routed by** (`routes_to` in, Traefik).
- **Part of** (stack), **Runs on** (engine, host).
- **The same as** (`same_as`, other sources' copies).

It's the walk the action plans use for blast radius, so Related and the hold ring agree.

### Container · History

- **Tasks** on this target from the `tasks` topic: running, plus the last 24 hours. Who, what, when and how it ended, with Show log for those this tab still holds.
- **For admins and the owner,** the audit log: `GET /api/audit?target=<id>`, 20 at a time with "Older", each opening Settings → Audit filtered to it. A container is keyed by name, so its history survives a recreate.
- **Otherwise,** "Older history is in the audit log, for admins."

## Opening it

| From | What happens |
|---|---|
| **The containers tile** | Each row becomes a button (tall and large sizes), named "<name>, <status words>". The wide size's three recent changes too. Rows keep their look; focus and hover show they're buttons |
| **⌘K** | Opening a container opens its sheet, instead of ringing a tile it rarely has. Other kinds keep today's behaviour until they have a sheet. The Run group stays as it is |
| **The Inbox** | An incident whose target has a sheet gets "Open <name>" beside the facts |
| **Related, Overview** | Links between sheets replace the sheet. Back returns to the previous one, since each open is a history entry within the sheet |
| **A deep link** | `?r=docker:i_x/container/sonarr` |

## Settings → Nodes

A pane `settings/NodesPane.vue` (admin and up, its own chunk), at `/settings/nodes`. The route row exists already (`GET /api/nodes`).

| | |
|---|---|
| **List** | One row per node: name, connected or last seen, version, address, the key's fingerprint, and how it was enrolled and approved. A node whose hub key isn't current says so |
| **One node** | Its policy as plain words: each verb the node allows or refuses, grouped (Docker read, logs, lifecycle, update, remove, exec; stacks read, deploy, adopt). "Refused by this node's policy" is the node's decision, and the page says the hub can't change it |
| **Revoke** | Type the node's name to confirm (and step up if the hub asks). The sentence says what follows: its session ends now, its key never works again, a fresh pairing token is written, and its Docker instance stops updating |
| **Pairing** | Whether pairing is on (`pairing` in the response) and **Regenerate** (hold to confirm). Which addresses may pair (`CHADASH_NODE_PAIRING_FROM`) would need one more field in the response; until then the pane names the setting. The token itself is never shown: the hub never returns it. The pane shows the command that reads it on the hub's host, and links to "Nodes on other hosts" |
| **Empty** | "No nodes yet." The compose snippet for this host, and the link for others |

Engines link to their node, and the node to its Docker instance in Settings → Integrations.

## Roles

| | Household | Viewer | Operator | Admin, owner |
|---|---|---|---|---|
| Open a container's sheet | Only from a board item, and the household graph's view of it | ✓ | ✓ | ✓ |
| Actions | Only those marked household | T0 that doesn't read data | Up to T2 | Up to T3 (T4 when on) |
| Logs | — | — | ✓ | ✓ |
| History: own tasks · audit | Own tasks · — | Own tasks · — | Own tasks · — | All tasks · audit |
| Settings → Nodes | — | — | — | ✓ |

The page hides what a role can't use. The hub enforces it either way.

## Waves

| Wave | What | Hub changes | Depends on |
|---|---|---|---|
| **1 · Shell and Overview** | `ResourceSheet.vue` with tabs, `?r=`, layering and focus, the phone card. Container Overview, with `widgets/containers.ts` shared with the tile. Opening from the tile's rows and from ⌘K | None | — |
| **2 · Act** | Actions (tiers, unavailable sentences, the working task, Remove's volumes choice), Logs in the tab (`onLog`), Related, History (tasks; audit for admins). Opening from the Inbox | None | 1 |
| **3 · Nodes, engines, stacks** | Settings → Nodes (list, verbs, revoke, regenerate, empty state). Sheets for an **engine** (its node, its containers as rows that open their sheets, the probe's grants) and a **compose stack** from the graph (its containers, and a link to the owned stack's editor) | None | 2 |
| **4 · Live** | Metrics: the Docker integration publishes `cpu.pct` and `mem.mib` for a container while a sheet watches it. Needs per-resource interest from the realtime hub to the runtime, the gap [integrations/docker](../../integrations/docker/README.md#what-it-publishes) names. Then streaming logs, which belong to v0.3 | Yes: interest per resource, and a stats publisher | 2; v0.3 for streaming |

Each wave ends with its tests green, axe passing in all six themes, and the Workbench cells for the sheet's states ([docs/design/workbench.md](../design/workbench.md#page-level-components)). The web README's Structure, Settings and "Not yet" sections, and the changelog, move with each wave.

## States to design

Every one in each of the six themes, desktop and phone:
- loading (rare: the graph is in the snapshot);
- live;
- **stale** (hub offline: last-known values, as of, actions disabled);
- the container down, with an incident, and with a task running;
- permission-limited (a viewer: no Logs, fewer actions);
- **not in the graph any more;**
- the Demo Home: read-only Docker (see Open questions).

## Tests

- **Component tests** on the sim's fixtures:
  - the tabs a kind and role get;
  - each Overview fact and its "—";
  - Actions through the fake engine (a tier each, an unavailable one, Remove's params, a running task);
  - Logs in the tab, without the dialog;
  - Related groups;
  - History for each role;
  - the Nodes pane against a fake `GET /api/nodes`, revoke and regenerate included.
- **Keyboard and focus:** tabs, Esc layering, focus return, and Back closing the sheet.
- **Playwright** on the release binary's Demo Home: open a container's sheet from the tile, move through the tabs, reload on the deep link, and run axe there in all six themes.
- **Budgets:** the sheet and the Nodes pane are lazy chunks. Initial JS grows only by the router's `?r=` and the store's sheet state.

## Decisions

Settled on 2026-10-29:
1. **Tapping a tile.** Rows and tiles of kinds with a sheet open it. Service tiles keep their link; their sheet comes on long-press or right-click once the context menu exists.
2. **The Demo Home.** For now the sheet says "In the Demo Home, Docker is read-only" where Actions and Logs would be. Simulated container actions in the simulator are the better demo, and follow once the simulator rework lands.
3. **`?r=`, not a path.** Sheets are layers over a board, so the board's route stays as it is.
4. **No per-target history route yet.** Wave 2 uses the tasks topic and, for admins, the audit log. Revisit after using it.

## Wave 1 status

Built 2026-10-29. Details are in [web/README.md → The resource sheet](../../web/README.md#the-resource-sheet).
- `ResourceSheet.vue` (lazy), `?r=` in `lib/router.ts`, `ui.sheet`, `openSheet`, `closeSheet` and `hasSheet`, and `model/sheets.ts` listing the kinds (containers).
- Container Overview, plus **Related**, pulled forward from wave 2 because it's read-only and gives the tabs a second tab to test.
- Opening from the containers tile's rows (tall, large and the wide size's recent changes) and from ⌘K.
- `Tile.vue` now ignores clicks and keys that start on a control inside a clickable tile. Before, a row's click would also have opened the tile's incident, closing the sheet it had just opened.
- Tests: `web/test/resource-sheet.test.ts`, on a small home of its own, axe included.
- **Not yet:** the Playwright pass on the Demo Home (part of wave 2's end), and the Workbench cells for the sheet's states.

## Wave 2 status

Built 2026-10-29, on top of wave 1. Details are in [web/README.md → The resource sheet](../../web/README.md#the-resource-sheet).
- **Actions:** by tier, filtered by role, asked again when the state changes, when a task on it ends and when the hub is back. Unavailable ones keep their sentence and recipe link. The working task shows with Cancel and holds the rest. Remove's anonymous-volumes choice.
- **Logs:** `ActionRequest.onLog` sends a `reads_data` task's lines to the tab instead of the log dialog. A new permission, `can.readData` (Operator and up), hides the tab from roles the hub's policy refuses anyway.
- **History:** the tasks topic for everyone. For admins, the audit log with Older and a link into Settings → Audit, which now reads `?target=`.
- **The Inbox:** "Open <name>" on an incident whose target has a sheet.
- **The Demo Home** shows Overview, Related and History only, as decided: its Docker has no actions.
- Tests: `web/test/resource-sheet.test.ts` (22), axe on the Actions tab included.
- **Still open:** the Playwright pass on the Demo Home and the Workbench cells, which move to wave 3's end.

## Wave 3 status

Built 2026-10-29. Details are in [web/README.md](../../web/README.md#the-resource-sheet) (the sheet, and Settings → Nodes).
- **Settings → Nodes** at `/settings/nodes`: each node's state, version, address, key and enrolment; its policy as grants in words, allowed or refused; its Docker integration; Revoke with the name typed out; Pairing with the command to read the token, Regenerate on a hold, and the hub's key. Revoke has no step-up: the route is plain admin, so the typed name is the confirmation.
- **Engine and stack sheets:** their Overview facts, their containers (problems first, each opening its sheet, left out of Related), and links into Settings → Nodes and Settings → Stacks for admins. An engine's container count falls back to the graph when the source doesn't report one (the Demo Home's engines).
- **The containers tile's engines** open their sheet.
- Tests: `web/test/resource-sheet.test.ts` and `web/test/nodes-pane.test.ts` (30 together), axe on the engine sheet and the Nodes pane included.
- **Still open from waves 1–3:** the Playwright pass on the Demo Home's release binary and the Workbench cells for the sheet's states. Both need a build, so they wait for the maintainer.

## Wave 4 status

Built 2026-10-29: metrics. Streaming logs stay in v0.3.
- **Close interest.** A sheet subscribes to `metrics:resource/<id>` while it's open: the metrics document with only that resource's series (`topics.ResourceMetrics`). `runtime.Watch` turns each one into an entry in `integration.Interest.Resources`, so per-resource interest needed no lens topics. `RealtimeClient.watch`/`unwatch` add and drop the topic, across reconnects, without holding up "live"; `live.watchResource(id)` counts holders.
- **A faster interval while watched.** The new optional `integration.InterestIntervaler` picks the sync interval from the interest, and the coordinator asks again when the interest changes, so opening a sheet speeds the source up at once.
- **Docker stats.** For each watched running container (at most 16), one one-shot stats request every 10 s: `cpu.pct` as the share of the host's CPU time between readings (the first is unknown), `mem.mib` without reclaimable page cache, with the limit as its max. The list and inspects keep their own pace between. The series live in the `stats` scope and go when the watch ends. Through chadash-node the same request is the `container.stats` verb, which `docker.read` already allowed.
- **The Metrics tab** appears once either series exists: the value, memory against its limit, and a sparkline of the readings seen while open.
- Tests: `TestStatsWhileWatched` (Docker), `TestResourceInterest` and `TestIntervalFor` (runtime), `TestResourceMetrics` (topics), the client's watched topics, and the sheet's subscription and Metrics tab with axe.
- **Not checked against a real engine yet.** The fake answers stats; it needs a live run on a node-backed engine and on a socket proxy.
