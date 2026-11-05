# 16 · Stacks parity: retiring Dockhand and Dockge

> **Status: agreed** (2026-11-05). The maintainer settled [S1–S6](#open-questions-s1s6) as recommended ([Decisions](#decisions)). Its waves run inside v0.5 ([15](15-v0.5.md#waves)). Builds on owned stacks ([ADR-0006](../adr/0006-owned-stacks-and-thin-node.md)), node deploys ([ADR-0020](../adr/0020-node-deploys-with-compose-sdk-and-go-git.md)) and git sync ([ADR-0021](../adr/0021-git-sync-and-deploy-webhooks.md)).

**House rule (from [17](17-test-automation.md#the-rule)):** nothing is tested by hand. Every check is an automated test the agent writes and runs, on throwaway data directories, containers and registries. The lab is never a test target: it's only read, by `live:verify`, to check fixtures. Dev servers and builds still need the maintainer's say. **Writing to the lab is an operation, not a test, and needs a go-ahead per run against named targets.**

**The goal:** the maintainer turns Dockhand off on pvedocker and doesn't miss it. Every stack there is managed by Cha Dash, and nothing done day to day needs another tool, except live logs, where linking to Dozzle stays a fine answer.

---

## Where we are

Cha Dash already does more than Dockhand or Dockge where it counts most: deploying safely.

| Already there | Since |
|---|---|
| Deploy in seven steps: validate, plan, preflight, commit, pull and up, a health gate, keep or roll back | v0.2 |
| Every change is a git commit; roll back to any commit, pinned to the digests that last ran | v0.2 |
| Adopt a running project in place, or Move it into the root under the same name, without a restart | v0.2, v0.3 |
| Dockhand stacks started from standard input can be moved | v0.3 |
| A stack follows a git branch; deploys on push or asks first; webhooks from Forgejo, Gitea, GitHub, GitLab | v0.3 |
| `${secret:…}` references, resolved in memory, never on disk | v0.2 |
| New stack and an editor with checks and a plan | v0.2 |
| Container updates as rollouts, maintenance windows, no retry of a rolled-back version | v0.4 |
| CPU and memory in a container's sheet | v0.2 |
| The node's local policy and `docker.protect` | v0.2 |

## What's missing

Ordered by how much it blocks turning Dockhand off.

| # | Gap | Today | Blocks the cutover? |
|---|---|---|---|
| 1 | **Stack lifecycle:** stop, start, restart, down and remove a whole stack | Per container only. Removing a stack means un-adopting it and cleaning up by hand. `stackproto` has no such verbs | Yes: daily use |
| 2 | **Update a stack:** pull every service's tag and recreate what moved | Deploying an unchanged file does nothing. Settings → Updates works per container | Yes, for stacks on `:latest` |
| 3 | **Private registries** | The node pulls without credentials | Yes, for any stack with a private image |
| 4 | **Live logs in Cha Dash** | The node already streams (`container.logs` with `follow`, `internal/node/docker/streams.go`). The action asks for 50 lines (`integrations/docker/actions.go:318`) and the sheet links to Dozzle | No, if Dozzle runs |
| 5 | **A shell in a container** | The `docker.exec` grant and `exec: true` switch exist in the policy (`internal/node/policy.go`); no verb uses them. A call streams only node → hub, so input needs a transport change | No, but it's the most-missed Dockge feature |
| 6 | **Images, volumes and networks:** list, see what uses them, prune | Not shown | No |
| 7 | **Two writers:** warn when Dockhand, Dockge, Arcane or Komodo also manages a stack Cha Dash owns | The guide says "one writer at a time"; nothing checks | No, but it prevents the worst mistake during the move |

**Out of scope:** vulnerability scans, templates or an app store, `docker run` to compose conversion, a raw Docker API. Restic backups belong in backup coverage, not here.

---

## Design

### 1 · Stack lifecycle

New node verbs in `internal/node/stacks`, through the Compose SDK, each a hub action on the `stack` target:

| Verb | Policy | Tier (S2) | What it does | Undo |
|---|---|---|---|---|
| `stack.stop` | `stacks.deploy` | T1 | `compose stop`: containers stay | Start |
| `stack.start` | `stacks.deploy` | T1 | `compose start`; a stack that was never up deploys instead | Stop |
| `stack.restart` | `stacks.deploy` | T1 | `compose restart`, then the health gate, reporting only | — |
| `stack.down` | `stacks.deploy` | T2 | `compose down`: containers and the project's networks go, volumes stay | Deploy the same commit |
| `stack.remove` | `stacks.remove` (new, off by default) | T3, typed to confirm | Down, then delete the stack's folder from the root as a commit (`Chadash-Kind: remove`). Named volumes stay unless ticked, and ticking needs the name typed again | Restore from the commit, then deploy |

- Each refuses a protected container, like a deploy does.
- The plan lists the containers, what depends on them outside the stack (from the Home Graph) and, for down and remove, which volumes stay.
- A stopped stack isn't drift: `stack.list` reads stopped as a state, not a difference.
- The stack's page and the stacks list get the buttons; ⌘K finds them.

### 2 · Update a stack

`stack.update`, T2, through the same seven steps as a deploy, with the file unchanged:

1. Pull each service's tag. Services whose digest didn't move are left alone.
2. The plan lists only the services whose image moved, old and new digest.
3. Recreate those, health gate, roll back to the old digests on failure.
4. Commit with no file change (`Chadash-Kind: update`, a `Chadash-Previous-Image` trailer per service), so history shows it and rollback works as for any deploy.

Settings → Updates groups a stack's containers under the stack and updates them with `stack.update`, so a rollout updates a stack as one, never one container from under its compose file. "Update" on the stack's page does the same. A git-synced stack updates the same way: pulling isn't a change to the file.

### 3 · Private registries

Credentials stay on the Docker host (S4). The node reads a Docker config file mounted into its container (`/etc/chadash-node/docker-config.json`, the usual `auths` format, credential helpers not supported at first) and uses it for pulls and for `image.check`. The policy names which registries it may log in to (`images.registries`). The hub only learns that a registry needs a login and whether the node has one: "ghcr.io/acme: needs a login, none on this node". The recipe shows `docker login` on the host and the mount.

### 4 · Live logs

The node streams already; this is UI and authz.
- The container sheet's Logs tab follows live, with pause, a search that highlights, stdout and stderr told apart, and Download for the last 10,000 lines.
- A stack's page shows the logs of all its services, interleaved, each line tagged with its service.
- It stays `docker.container.logs`, Operator and up, and nothing is stored. A followed stream ends when the sheet closes or after 30 minutes.
- Dozzle links stay for people who prefer it.

### 5 · A shell in a container

The riskiest item, so it's last and off by default twice: the node's `exec: true` plus `exec` in `docker.allow`, and a hub setting.

- **Transport (S3):** add input frames to a call on the existing channel (hub → node: data, resize, close), so there's still one connection and one identity. The node runs `docker exec -it` through the Engine API and streams output back.
- **Who:** Admins only, T3, a fresh step-up, per container. Protected containers never.
- **The terminal** is a small in-house component (no third-party JavaScript), enough for a shell: ANSI colours, cursor movement, resize. Full-screen programs work, best effort.
- **The audit** records who, which container, which command (`sh`, `bash`), when it started and ended. Keystrokes and output are never recorded: they're where secrets are.
- A session ends after 15 minutes idle or 2 hours, and on the hub's restart.
- Agents (MCP) can't propose exec.

### 6 · Images, volumes and networks

Read first, then careful writes. New `docker.read` verbs `image.list`, `volume.list`, `network.list`, with what uses each.
- A node's page lists them, with size and "used by". An unused volume says so, and which stack last used it if the labels tell.
- **Prune images:** dangling only by default, "all unused" as an option, T2, with the space it frees in the plan. New grant `docker.prune`, off by default.
- **Volumes are never pruned in bulk.** One unused volume can be removed, T3, typed to confirm, and the plan says whether backup coverage saw a backup of it.
- Networks: list only.

### 7 · Two writers

`stack.detect` already reads labels. When a stack Cha Dash manages also shows the labels of another manager, or a Dockhand, Dockge, Arcane or Komodo source (the v0.4 specs) lists a stack of the same project name on the same host, the stack's page and the Inbox say "Dockhand also manages this stack: deploy from one of them only". A deploy still goes ahead.

---

## Cutover on the lab

Once 1–3 and 7 have shipped:

1. Update the [Dockhand guide](../../deploy/moving-from-dockhand.md): the gap list (CPU and memory are done; exec wasn't in v0.3), private registries, stack update.
2. Add a short "Moving from Dockge" section: Dockge's layout is ours, so it's Adopt in place, or point `stacks.root` at Dockge's folder.
3. On pvedocker, with a go-ahead per wave: adopt or move the 16 Dockhand stacks, least important first, as the guide says.
4. A script checks the result: every project on pvedocker is managed by Cha Dash, none still carries Dockhand's labels as its writer, each stack's plan says "No changes", and every container is healthy. Then a week with Dockhand stopped but not removed; anything the maintainer had to open it for goes back into this plan, as a gap with its test.
5. Remove Dockhand. Its read-only spec stays for people who keep it.

---

## Open questions S1–S6

| | Question | Recommendation |
|---|---|---|
| **S1** | Where does this land? | **In v0.5, as waves 32a–32c**, right after the open v0.4 tests and before Glance. It's the maintainer's own daily blocker (ADR-0004, dogfooding), and 5 and 6 can slip to v0.5.x |
| **S2** | Tiers for stop, start, down, remove | **As in the table:** stop and start T1 with undo, down T2, remove T3 typed, behind a new `stacks.remove` grant off by default |
| **S3** | How does exec get input to the node? | **Input frames on the existing call**, not a second WebSocket: one channel, one identity, and the policy check is the same. Needs a protocol version bump; older nodes refuse exec |
| **S4** | Where do registry credentials live? | **On the node's host**, in a mounted Docker config. The hub never holds them, which keeps a hub compromise from reaching private registries. A vault option can follow if people ask |
| **S5** | Does a stack update need a commit when the file doesn't change? | **Yes, an empty commit with trailers**, so history shows it and rollback has the old digests. The alternative, keeping it only in the hub's task history, splits history in two |
| **S6** | Does the terminal component count as third-party JavaScript? | **Write our own**, small, scoped to a shell. If it grows past ~25 kB gzipped or full-screen programs matter, revisit with an ADR. It loads only when a shell opens, so the initial-JS budget doesn't move |

---

## Decisions

Settled by the maintainer on 2026-11-05: **S1–S6 all as recommended.**

1. **S1:** in v0.5, as waves 32a–32c after the open v0.4 tests and before Glance; images, volumes and networks and exec slip to v0.5.x.
2. **S2:** stop and start T1 with undo, down T2, remove T3 typed to confirm, behind a new `stacks.remove` grant off by default.
3. **S3:** exec input travels as input frames on the existing call, with a protocol version bump; older nodes refuse exec.
4. **S4:** registry credentials live on the node's host in a mounted Docker config; the hub never holds them.
5. **S5:** a stack update is an empty commit with trailers.
6. **S6:** the terminal is our own small component, loaded only when a shell opens.

---

## Waves

Each wave ships its tests with it: every node verb at layer 3 (a real Docker Engine), every flow at layer 4 (hub, node and browser), as [17](17-test-automation.md#layers) sets out.

| Wave | What |
|---|---|
| 32a | Stack lifecycle (1); two writers (7); the guide's gap list corrected |
| 32b | Update a stack (2), and Settings → Updates grouping by stack; private registries (3) |
| 32c | Live logs (4); lab cutover steps 3–4, with a go-ahead (an operation, checked by its script) |
| v0.5.x | Images, volumes and networks (6); exec (5) with its own security review |

---

## Done when

- Each new verb is in `stackproto`, the node's README verb table, `internal/authz/routes.go` for its routes, and has a test of each policy refusal and of `docker.protect`.
- Engine tests (layer 3, [17](17-test-automation.md#layers)) prove stop, start, restart, down, remove, update and a private-registry pull on throwaway stacks in a real Docker Engine, and that a removed stack can be restored from its commit.
- A system test (layer 4) drives each from the stack's page in the browser, with the confirmation each tier asks for.
- A stack on `:latest` updates with one action, and a broken image (written into the test registry) rolls the stack back to the old digests.
- A stack whose image is on a private registry deploys and updates, and the hub never receives the credential (a test proves it isn't in any frame).
- Following a container's logs for 30 minutes of test-clock time keeps the tab under its line cap, and the stream ends.
- All 16 stacks on pvedocker are managed by Cha Dash, and Dockhand has been stopped for a week with no reason to start it.
- The changelog has each user-facing change under `## [Unreleased]`.
