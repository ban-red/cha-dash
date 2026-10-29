# 12 · Unraid control beyond start, stop and update

> **Status: step 1 (from the published schema) and steps 2, 4, 5 and 6 are built; step 3 was dropped. The live run (7) and the array start and stop are not done.** Step 3 changed: there is no `full` mode (see Design). It builds on [08 · v0.2 → Unraid](08-v0.2.md) and [ADR-0010](../adr/0010-unraid-stays-unraid.md), and uses the "can slip to v0.2.x" row for parity and array in 08's action table.

## What exists

A `control` instance can start, stop and update containers and start and shut down VMs, through the Unraid API (`integrations/unraid/actions.go`). It needs `docker:update_any` and `vms:update_any`. The instance's `mode` (`read_only` or `control`) decides whether it has actions at all and which scopes the consent link asks for. Before an action runs, the integration reads the key's grants back with `me` and refuses, naming the missing scope, if the key lacks one.

The recipe's consent step now also offers two fixed links: read and update on everything, and read plus full control of Docker and VMs. A key made from either has more than `control` uses. Test connection reads the key's grants back and marks any write grant (`grants()` in `unraid.go`); how it words the extra ones for a `control` instance hasn't been checked.

## Goal

When someone gives Cha Dash a key with more write access, Cha Dash offers the extra actions that key allows, and only those. It never asks for more than the person chose, and never uses a grant they didn't pick.

## Design

**The mode caps, the grants decide.** An action is offered when the instance's mode allows it *and* the key's grants cover it. Today the mode is the only gate in the UI and the grants are checked at run time. Move the grants check into the action list, so an action the key can't do shows as unavailable up front, with the scope it needs and how to add it.

**No third mode.** The runtime's `Control.ModeOn` is one value, and the schema shows only one operation needs more than update: `removeContainer` needs `docker:delete_any`. So `control` stays the only "Cha Dash may act" mode, and the key decides how far: Remove appears as unavailable, naming the missing scope, until the key has Docker delete access, which only the "full control" consent link grants. (This answers the second open question below.)

Parity and array actions don't get a mode either. They need `array:update_any`, which can also add and remove disks, so they stay behind an explicit second choice (see Array and parity).

**Test connection says what the key has beyond the mode.** "This key can also delete containers. Cha Dash won't use that in Control mode." It's the read-back it already does; first check what it says today for a key with more than the mode needs, and make that a note, not a warning.

**Reads don't change.** Never query `disks` (it wakes spun-down drives). A write key doesn't change what the integration reads.

## Actions

**Confirmed from the published 4.37.2 schema and resolvers** (`api/generated-schema.graphql` and `graph/resolvers/{docker,vms,array}` at the tag): `docker { restart, pause, unpause, removeContainer(id, withImage) }`, `vm { pause, resume, forceStop, reboot, reset }` and `parityCheck { start(correct), pause, resume, cancel }` exist. Every Docker and VM mutation needs `UPDATE_ANY`, except `removeContainer`, which needs `DELETE_ANY`. No Docker or VM mutation needs `CREATE_ANY`, so the recipe's full-control link doesn't ask for it. Every parity and array mutation needs `ARRAY: UPDATE_ANY`. What the live server does is still untested.

| Action | Scope | Tier | Undo | Notes |
|---|---|---|---|---|
| Container restart | `docker:update_any` | T1 | none | Stop then start; one step in the plan |
| Container pause, unpause | `docker:update_any` | T1 | the other | |
| VM pause, resume | `vms:update_any` | T1 | the other | |
| VM reboot | `vms:update_any` | T1 | none | Graceful, like shut down |
| VM force stop | `vms:update_any` | T3 | none | Typed name; can corrupt the guest. Offered only on a VM whose shut down didn't finish |
| Container remove | `docker:delete_any` | T3 | none | `full` mode. Typed name; says whether the image and appdata stay |
| Parity check pause, resume | `array:update_any` | T1 | the other | See below |
| Parity check start | `array:update_any` | T2 | cancel | |
| Array stop | `array:update_any` | T4 | none | Stops every container and VM; the hub itself may run there (self-aware T4) |
| Array start | `array:update_any` | T3 | none | |

`Disrupts` applies as it does for stop and update today: anything that would take down the hub or the router is raised to T4.

## Array and parity

`array:update_any` is the scope the recipe deliberately never asks for. Keep that default. Offer it only as a separate, labelled choice in the recipe ("Also let Cha Dash run parity checks and start or stop the array") that adds the scope to the link, and never as part of `control` or `full`. Ship parity check pause, resume and start first. Ship array start and stop last, or not at all: the cost of a mistake is the whole server, and Unraid's own UI is one tap away.

## Plan

1. **Spike.** Names, arguments and scopes: done, from the published source (above). Still to do, against the dogfood server with a throwaway key, VM and container: what each mutation does live (does `removeContainer` stop a running container? how long does `reboot` take to leave `running`?), what happens with a missing scope, and what the consent page shows for `docker:delete_any`.
2. **Grants in the action list.** *Already true.* The action list (`internal/action/list.go`) runs each action's preconditions, so a missing scope shows as unavailable with its sentence before anything is planned.
3. ~~**Modes.**~~ Dropped: no `full` mode (see Design). The second consent link already covers it.
4. **Low-risk actions.** *Built.* Container restart (T2), pause and unpause; VM pause, resume and reboot (T2). Fake-server tests per action in `actions_test.go`.
5. **Remove and force stop.** *Built.* T3, typed name; the plan says the image stays.
6. **Parity.** *Built.* A `parity_control` switch on the connection (off by default, `control` mode only), a third consent link with `array:update_any`, and start (with a `correct` param, off), pause, resume and cancel on the `array` resource. Array start and stop stay out.
7. **Live run.** A checklist like [08b](08-v0.2-live-runs.md), on a throwaway VM and container only. Never on jupiter's real guests.

Docs move with each step: the integration README's scope and action tables, the recipe, `internal/README.md` if routes change, and `CHANGELOG.md`.

## Open questions

- Does Unraid refuse a mutation cleanly when the scope is missing, or does it fail later? The spike decides whether the grants check must be strict.
- Answered: no `full` mode; delete is a second consent link (see Design).
- Should an instance whose key has more than its mode offer to switch ("This key can do more. Use Full?") in Test connection, or stay silent?
