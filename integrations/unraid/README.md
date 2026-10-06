# Unraid (`unraid`)

Reads an Unraid server through the Unraid API (GraphQL): the host, the array with its disks, the pools, the shares, the VMs, and the Docker containers Unraid manages, with their autostart settings. A controllable instance also starts, stops and updates containers and starts and stops VMs (see [Actions](#actions)).

**Tier:** silver (version gate, repairs, grants read back, fixture tests). **Capabilities:** `host.metrics`, `storage.pools`, `storage.disks`, `storage.shares`, `containers`, `compute.guests`. **Secret:** `api_key`.

Written from [spike 4.2](../../spikes/unraid/README.md) and tested against its capture of the dogfood server, [`testdata/integrations/unraid/`](../../testdata/integrations/unraid/README.md) (Unraid 7.3.2, API 4.37.2).

## Config

```json
{"url": "http://10.0.11.120", "tls": {"mode": "verify"}, "mode": "read_only", "api_key": "${secret:instance/<id>/api_key}"}
```

| Key | Meaning |
|---|---|
| `url` | The server's webGUI address, `http://` or `https://`. The API is `POST <url>/graphql`; a URL that already ends in `/graphql` works too |
| `tls` | `{"mode": "verify" \| "pin" \| "tofu" \| "insecure", "pin": "<sha256>"}` |
| `mode` | `read_only` (the default, and what a config without it means) or `control`: the answer to "Read-only, or let Cha Dash control Unraid?". It decides which scopes the recipe's consent link asks for and whether the instance has actions. It can be switched later; nothing needs deleting |
| `api_key` | Sent as the `x-api-key` header, and nowhere else |

These are the keys in [07 · v0.1](../../docs/plan/07-v0.1.md#instance-config-keys), in the shape the Homepage importer writes. Homepage's `pool1`–`pool4` options are tile options, not instance config.

**The key to create** (decision 6: one key, its scopes set by the mode), through Unraid's consent URL or under *Settings → Management Access → API Keys*:

| Mode | Scopes |
|---|---|
| `read_only` | `info:read_any, array:read_any, docker:read_any, vms:read_any, share:read_any, me:read_any` |
| `control` | the same, plus `docker:update_any, vms:update_any` |

The recipe's consent link follows the form's mode (a recipe param bound to `config.mode`; see `RecipeOption.When` in `internal/integration`). `info` and `array` are required. Without `docker` or `share`, those parts are missing and the instance says so in a warning; without `vms`, a read-only instance just shows no VMs (keys made from the v0.1 recipe lack it), and a controllable one warns. `me` lets Test connection read the grants back. Never `array:update_any`: it also lets a key add and remove disks. The spike's recipe also asks for `disk` and `notifications`; this version reads neither (see Limits).

## Requirements

- **The Unraid API, 4.37.4 or later.** It's built into Unraid 7.2 and later; earlier releases get it from the Unraid Connect plugin.
- **Why 4.37.4:** older APIs let any handler without permission metadata through, so a "read-only" key could change UPS settings, notifications and more ([unraid/api#2074](https://github.com/unraid/api/pull/2074); spike §4).
- **An older API is a Repair.** The integration reads nothing else from it. Code `unsupported_version`, with a message that names the version found, the one needed, and what to update. The version is read again at every retry (every 5 minutes), so updating clears the Repair on its own. Test connection fails with the same code, but still shows the version and the grants.

## What it reads

| Read (GraphQL operation) | Scope | When | Cost on the dogfood server |
|---|---|---|---|
| `info { os { hostname uptime } cpu { … } versions { core { unraid api } } }` | INFO | First sync, then hourly; every attempt while the version is unsupported | ~0.3 s |
| `info { networkInterfaces { … } }` | INFO | Hourly, only when `url` is a host name; for the LAN address | 1.5–2.6 s |
| `array { state capacity parityCheckStatus boot parities disks caches }` | ARRAY | Every sync | ~7 ms |
| `metrics { cpu memory }` | INFO | Every sync | ~14 ms |
| `shares { name include exclude }` | SHARE | Every 5 minutes | ~7 ms |
| `docker { containers { id names image state autoStart autoStartOrder autoStartWait labels mounts webUiUrl } }` | DOCKER | Every sync while someone watches containers; every 15 minutes otherwise; and when an action resolves or verifies a container | ~0.2 s |
| `vms { domains { id name state } }` | VMS | Every sync while someone watches VMs; every 15 minutes otherwise; and when an action resolves or verifies a VM | ~12 ms |
| `me { name roles permissions }` | ME | Test connection, and before an action (at most every 30 s) | ~5 ms |

**Poll plan.** The coordinator syncs every 60 s while someone watches, and every 5 minutes (the default idle interval) while nobody does. One sync is a few sequential POSTs.

**Never `disks`.** The integration never sends the `disks` query, on a timer or otherwise. Whatever fields it asks for, its resolver runs `smartctl -a` on every disk without `-n standby`, which **wakes spun-down drives** (spike §11: a read timed out at 20 s while nine disks spun up). Everything the integration shows about disks comes from `array`, which reads emhttp's state and touches no disk: the slot, status, spin state, temperature (`null` while spun down), errors and file system use. A test checks that no query selects `disks` or asks for `smartStatus`, `serialNum`, `temperature` or `firmwareRevision`.

Container labels are read to learn the manager (`net.unraid.docker.managed`) and then dropped. They can hold secrets (`homepage.widget.key`), so none reach the graph.

## What it publishes

IDs are `unraid:<instance id>/<path>`.

| Resource | Kind | Attrs | Health |
|---|---|---|---|
| `host` | `host`, named after the server's hostname | `platform: unraid`, `version` (Unraid), `api_version`, `hostname`, `ip`, `cores`, `threads`, `cpu`, `mem_gb`, `boot_at`; label `chadash.role: nas`; a link to the webGUI | `ok` |
| `array` | `array` | `size_tb`, `disks`, `parity_disks`, `parity` (`valid`, `invalid`, `checking`, `none`), `parity_check` (the last check's status), `parity_checked_at`, `parity_errors` | `down` when stopped or unable to start; `down` when more disks are missing or disabled than parity covers; `attention` for a disabled, missing, invalid or erroring disk, or parity errors; `maintenance` during a parity check |
| `disk/<name>` (parity, data, pool devices, `flash`) | `disk` | `role` (`parity`, `data`, `pool`, `boot`), `serial`, `model`, `device`, `fs`, `transport`, `size_tb`, `rotational`, `errors`, and `pool` for pool devices. State `online`, or `idle` while spun down | From emhttp's status: `down` for missing or disabled, `attention` for invalid, wrong or new, and for read/write errors |
| `pool/<name>` | `pool` | `fs`, `devices`, `media` (`nvme`, `ssd`, `hdd`), `size_tb` | `down` while the array is stopped, `attention` when unmounted or a device needs attention |
| `share/<name>` | `share` | `include`, `exclude` (the disks it's limited to) | `ok` |
| `vm/<uuid>` | `vm`, named after the VM | `platform: unraid`, `uuid` (libvirt's). State `running` (also libvirt's idle), `paused`, `stopping`, `stopped`, `error` (crashed) or `unknown` | `attention` when crashed |
| `container/<name>` | `container` | `container_id` (Docker's full ID), `image`, `manager` (`dockerman` or `composeman`), `autostart`, `autostart_order`, `autostart_wait_s`; desired `running` when it's on the autostart list; a link to its web UI | `attention` when it's on the autostart list but not running |

**Relations:** the instance `monitors` the host; `array`, every pool and the flash are `part_of` the host; parity and data disks are `part_of` the array; pool devices are `part_of` their pool; a share is `part_of` the array when its include or exclude list names array disks, else `part_of` the host; a VM `runs_on` the host; a container `runs_on` the host and `stores_on` every share it mounts (`/mnt/user/<share>/…`, `/mnt/user0/<share>/…`, or a disk or pool path).

**Metrics**, with the Demo Home's keys and scales:

| Resource | Keys |
|---|---|
| host | `cpu.pct`, `mem.pct` |
| array | `used.pct` (from the capacity in 1000-byte units) |
| data disks | `used.pct`, `temp.c` |
| parity and pool devices | `temp.c` |
| pools | `used.pct` (the first device's file system), `temp.c` (the hottest device) |
| flash | `used.pct` |

`temp.c` is unknown (`null`), never 0, while a disk is spun down; the HDD scale warns at 50 °C and is critical at 55 °C, solid-state devices at 70 and 80. A failed metrics read leaves `cpu.pct` and `mem.pct` unknown, with a warning.

**Not like the Demo Home:** the boot flash is a `disk` resource, which the simulator doesn't have. (The host has no `temp.c`, as in the Demo Home: the API doesn't report a CPU temperature.) Its serial isn't published: it ties to the Unraid license.

### Pools and disks
- Unraid has no pool object. Pool devices come in `array.caches`, named `<pool>`, `<pool>2`, …, and only the first carries the file system. Pool names can't end in a digit, so the grouping is unambiguous.
- A device's serial is the last `_` segment of its array ID (`<server id>:<model>_<serial>`), the correlator's exact key for disks. The model is the rest, spelled with spaces.
- Sizes: `ArrayDisk.size` is KiB; `fsSize`, `fsUsed` and the capacity are 1000-byte units, the capacity as strings.

### Containers and Docker
- **The join is by container ID, not by name.** The local part of a container's PrefixedID is Docker's full container ID, published as `attrs.container_id`. On the fixtures, all 74 join the Docker integration's containers by ID (`TestContainersJoinDockerByID`). The resource path uses the name, as the Docker integration's does, so it survives a recreate; the correlator (wave 6) merges the two by `container_id`.
- **Desired state comes from Unraid's autostart list.** Docker's restart policy can't say whether a template container should run. On the list, desired is `running`. Off it, desired stays unset, because Docker's restart policy (which this API doesn't report) decides; the correlator combines the two (spike §7). Compose Manager containers are never on the list.
- State comes from Unraid (`running`, `exited`, `paused`). The API folds Docker's `created` into `exited`, so prefer the Docker integration's state when both are present.
- Until the correlator exists, a container that both integrations see appears twice, once per source.

## Test connection

The mode is checked against the key's actual grants, never trusted.

1. The version read. A rejected key fails with `auth`; a server without the API fails with `api_changed`, naming the URL.
2. `me`, turned into grants: one per role (`role:admin` is `admin`; `viewer` and `guest` read; any other role counts as a write) and one per resource, named after it (`array`, `docker`, …), with its actions as the detail. **Anything beyond reading is a write grant**: a `create`, `update` or `delete` action, or an unknown one. The framework then warns about write grants (on a controllable instance too, for now; see Limits). On the spike's first key, `disk: create_any` is flagged.
3. Missing reads: no ARRAY read fails the test (`missing_permission`); no DOCKER or SHARE read is a warning, and so is no VMS read on a controllable instance. A key without ME passes with a `grants_unknown` notice.
4. **Controllable instances:** each missing write scope (`DOCKER: Update`, `VMS: Update`) is a `missing_permission` warning naming the actions it stops and how to add it. The test still passes: the instance reads as before.
5. An API older than 4.37.4 fails with `unsupported_version`.

## Actions

Only a controllable instance (`mode: control`) has actions; a read-only one has none, so tiles and ⌘K show none and nothing probes for write access. All go through the Unraid API's mutations (never the node), with the instance's one key.

| Action | Tier | Undo | Needs | Mutation | Verify (next read) |
|---|---|---|---|---|---|
| `unraid.container.start` | T1 | `unraid.container.stop` | `docker:update_any` | `docker { start(id) }` | the container reports `running` |
| `unraid.container.stop` | T1 | `unraid.container.start` | `docker:update_any` | `docker { stop(id) }` | it reports `exited` |
| `unraid.container.update` | T2 | — | `docker:update_any` | `docker { updateContainer(id) }` | it's listed again, and running if it was |
| `unraid.vm.start` | T1 | `unraid.vm.stop` | `vms:update_any` | `vm { start(id) }` | the VM reports `running` |
| `unraid.vm.stop` | T1 | `unraid.vm.start` | `vms:update_any` | `vm { stop(id) }` (a clean shutdown) | it reports `stopped` |

- **Targets** are this instance's `container/<name>` and `vm/<uuid>` resources. Each action resolves its target with a fresh read, so preconditions see now, not the last sync; another instance's target, or another kind, is `not_found`. The blast radius comes from the Home Graph.
- **Missing scopes:** before planning, the key's grants are read with `me` (cached 30 s). Without the write scope an action needs, it refuses with `409 unavailable`, naming the scope and the two ways to add it (re-run the consent link with Control chosen, or edit the key under *Settings → Management Access → API Keys*), with `recipe` naming the instance. A key that can't read ME is let through, and a `FORBIDDEN` answer fails the task with `missing_permission` and the same sentence.
- **Preconditions** (`409 precondition`): already running or stopped; paused (resume it in Unraid); a VM that's shutting down; a container from a Compose Manager stack can't be updated (Unraid's update covers template containers only).
- **Timeouts:** mutations get a 15-minute HTTP client, since Unraid answers a stop or an update when it's done; an update may run 15 minutes, a VM shutdown 5, and verifying waits 1 to 3 minutes.
- **Audit:** the target and empty params; the key never appears in a plan, task log or audit record.
- **Not offered: parity check and array stop** (the plan's T2/T3, allowed to slip to v0.2.x). Both need `array:update_any`, which also lets a key add and remove disks, and the dogfooding rules keep writes off the array. They can come later with a separate, explicit opt-in.

## Repairs and warnings

| Situation | Result |
|---|---|
| `UNAUTHENTICATED`, or HTTP 401/403 | Repair, `auth`, field `api_key` |
| `FORBIDDEN` on the info or array read | Repair, `missing_permission`, field `api_key`, naming the scope to add |
| `FORBIDDEN` or a failure on metrics, shares or containers | Warning (`missing_permission` or `read_failed`); the rest still publishes, and what that part published before stays |
| API older than 4.37.4, or an unreadable version | Repair, `unsupported_version` |
| `GRAPHQL_VALIDATION_FAILED` (or a parse error) | Repair, `api_changed`, naming the API version |
| A 404, or a 2xx that isn't JSON, at `/graphql` | Repair, `api_changed`, field `url`: no Unraid API there |
| Anything else (`INTERNAL_SERVER_ERROR`, timeouts) | Transient; the coordinator backs off |

GraphQL reports most failures inside a 200, so the integration maps `errors[].extensions.code`, not the HTTP status.

## Limits

- **No SMART data, serial-number cross-check, model from `disks`, or firmware.** They need the `disks` query, which wakes disks. A deliberate "refresh SMART" action could read it on demand later (v0.2 has actions).
- **No notifications yet.** The spike mapped them (Unraid's own notifications, `warningsAndAlerts` for the Inbox).
- **The mutations aren't verified live.** They're written from the published schema (`DockerMutations.start`, `stop`, `updateContainer`; `VmMutations.start`, `stop`) and tested against fakes; the house rule keeps writes off the lab until the maintainer's write run. If an API version lacks one, the task fails with `unsupported`, naming the version.
- **No "update available" yet.** The API's update check isn't read, so an update may pull the same image and recreate the container anyway.
- **Write grants on a controllable instance** still get the framework's "Cha Dash only reads" warning: the runtime doesn't know about modes yet (wave 8, 8A).
- **No live CPU and memory.** The API's `systemMetricsCpu` and `systemMetricsMemory` subscriptions (graphql-ws) would give second-by-second values while a host tile is open; this version polls `metrics` instead. `arraySubscription` is broken on 4.37.2.
- **Shares have no pool.** The API reports `cache: null` and `size: 0` for shares (4.37.2), so a share without an include or exclude list is related to the host, not to its pool, and shares have no usage series.
- **The host's `ip`** is the URL's host when that's an address, else br0's address from the network read.
- Reads aren't strictly free of side effects: the containers read can trigger Unraid's template sync on the server (spike §8). That's harmless, and why containers are read every 15 minutes while nobody watches.
