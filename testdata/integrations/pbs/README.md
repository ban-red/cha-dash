# Proxmox Backup Server fixture (synthetic)

> **Synthetic.** No PBS was captured: the spikes didn't reach it. These responses are written by hand from the PBS API's source and documentation, not recorded. Field names and shapes follow the API; values are made up to agree with the Proxmox VE capture. Replace them with a capture when one exists.

They seed the `pbs` integration's contract tests ([`integrations/pbs`](../../../integrations/pbs/README.md)).

## The scenario

The dogfood home's PBS, as the [Proxmox VE capture](../proxmox/README.md) sees it from the other side: one PBS 4.0.14 in a container on jupiter (`10.0.11.120:8007`), read on 2026-10-04 at 01:30 UTC with a token holding the `Audit` role on `/`.

- **Five datastores, one filesystem.** `mars`, `mini-g5`, `pluto.lab.example`, `pve.lab.example` and `store-2` are the datastores the PVE instances' storages point at. Each reports the same 42.26 of 45.98 TB (91.9%), the numbers PVE showed for every PBS storage. PBS estimates the filesystem full on 2026-10-24.
- **Prune.** `prune-mars` exists but is disabled (it last ran Sep 14); `pluto.lab.example` and `store-2` have daily prune jobs; `mini-g5` and `pve.lab.example` have none.
- **Garbage collection.** Daily on four datastores; `mars` has no schedule and last ran Sep 14. Last night's GC on `pve.lab.example` ended with 2 warnings.
- **Verification.** The weekly verify job on `store-2` failed on Oct 3.
- **No backups in the last day.** PVE's scheduled runs to this PBS failed to connect, so PBS never saw them; there are no backup tasks in the task list.

## Files (`jupiter/`)

| File | API | Notes |
|---|---|---|
| `_index.json` | | Every request: `path` (with query), `file`, `status`, and `"synthetic": true`. Same layout as the Proxmox fixtures |
| `version.json` | `GET /version` | `{version: "4.0", release: "14"}`: PBS puts major.minor in `version` and the patch level in `release` (PVE's are `release: "9.0"`, `version: "9.0.9"`) |
| `access/permissions.json` | `GET /access/permissions` | `Audit` on `/`: `Sys.Audit` and `Datastore.Audit` on `/`, `/access`, `/datastore`, `/remote`, `/system` (PBS's default paths). Values are the propagate flag |
| `status/datastore-usage.json` | `GET /status/datastore-usage` | `DataStoreStatusListItem`: `store`, `total`, `used`, `avail`, `mount-status`, `backend-type`, `history` (30 daily ratios), `history-start`, `history-delta`, `estimated-full-date`, `gc-status` |
| `admin/datastore.json` | `GET /admin/datastore` | `DataStoreListItem`: no maintenance mode set |
| `admin/gc.json` | `GET /admin/gc` | `GarbageCollectionJobStatus`: the GC status flattened, plus `schedule`, `next-run`, `last-run-endtime`, `last-run-state`, `duration` |
| `admin/prune.json` | `GET /admin/prune` | `PruneJobStatus`: config (`id`, `store`, `disable`, `schedule`, `keep-*`) and `JobScheduleStatus` flattened |
| `admin/verify.json` | `GET /admin/verify` | `VerificationJobStatus`, the same pattern |
| `nodes/localhost/status.json` | `GET /nodes/localhost/status` | `memory`, `swap`, `root`, `uptime`, `loadavg`, `kversion`, `current-kernel`, `cpuinfo`, `cpu`, `wait`, `info.fingerprint`, `boot-info` |
| `nodes/localhost/tasks.json` | `GET /nodes/localhost/tasks?errors=1&limit=1000&since=1790991000` | `TaskListItem`s that didn't end OK in the 24 h before the read: the failed verification, and the GC with warnings. `node` is `localhost`, as PBS answers. UPIDs escape `-` and `:` in worker IDs as `\x2d` and `\x3a` |

## Sources

The shapes come from the PBS source at `git.proxmox.com` (read 2026-10-03):
- `proxmox.git`, `pbs-api-types/src/datastore.rs` (`DataStoreStatusListItem`, `GarbageCollectionStatus`, `GarbageCollectionJobStatus`, `DataStoreListItem`, `DataStoreMountStatus`), `jobs.rs` (`JobScheduleStatus`, `KeepOptions`, `PruneJobConfig`/`Status`, `VerificationJobConfig`/`Status`) and `acl.rs` (privileges and roles);
- `proxmox-backup.git`: `src/api2/status/mod.rs` (datastore-usage and its permissions), `src/api2/node/tasks.rs` (task list parameters and who sees which task), `src/api2/node/status.rs`, `src/api2/access/mod.rs` (permissions), `src/api2/admin/gc.rs`, `src/api2/version.rs` and `pbs-buildcfg` (how the version is split).

## Sanitizing

Nothing real is in here. The values follow the Proxmox fixtures' scheme so the leak check (`TestFixturesHaveNoGenericLeaks` in `integrations/pbs`) passes the same way: `10.0.11.x` addresses (none appear in the bodies), `*.lab.example` names, zeroed fingerprints (`00:…:05`), no e-mail addresses, no secrets.

## Regenerating

There's no capture tool: edit the JSON by hand, keep `_index.json` in step, and run `go test ./integrations/pbs`. When a PBS becomes reachable, capture it read-only the way [spike 4.1](../../../spikes/pve/README.md) captured PVE and replace this fixture.
