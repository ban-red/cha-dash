# Proxmox VE (`proxmox`)

Reads one Proxmox VE "domain" per instance: a cluster, or a standalone node. It publishes the cluster, its hosts, guests, storage and backup jobs with the Demo Home's kinds and metric keys, and raises backup failures, broken guests, lost quorum and a few quiet hints in the inbox. Read-only: nothing it does changes PVE.

**Tier:** silver (repairs, version adapters, multi-version fixture tests). **Capabilities:** `host.metrics`, `compute.guests`, `backups.jobs`. **Built from** [spike 4.1](../../spikes/pve/README.md) and its captures in [`testdata/integrations/proxmox/`](../../testdata/integrations/proxmox/README.md): PVE 8.4.19, 8.4.21, 9.0.9, 9.1.5 and 9.2.20, one 2-node cluster.

## Config

```json
{
  "url": "https://pve.lab.example:8006",
  "tls": {"mode": "tofu"},
  "token_id": "chadash@pve!dash",
  "token_secret": "${secret:instance/i_x/token_secret}"
}
```

| Key | Meaning |
|---|---|
| `url` | Any node's API, `https://host:8006` (or a proxy path). For a cluster, one node is enough: it lists them all |
| `tls` | `verify`, `pin`, `tofu` or `insecure`. PVE serves a self-signed leaf, often without the URL host in its SAN, so `tofu` (the importer's choice for LAN hosts) or `pin` |
| `token_id` | `user@realm!name`, plain config |
| `token_secret` | The token's secret, in the vault. Sent only as `Authorization: PVEAPIToken=<token_id>=<token_secret>` |

These are the keys in [07 · v0.1](../../docs/plan/07-v0.1.md#instance-config-keys); the importer's shape validates as is.

**Token recipe** (privilege-separated, both ACLs because a separated token gets the intersection):

```sh
pveum user add chadash@pve --comment "Cha Dash (read-only)"
pveum acl modify / --users chadash@pve --roles PVEAuditor
pveum user token add chadash@pve dash --privsep 1 --comment "Cha Dash"
pveum acl modify / --tokens 'chadash@pve!dash' --roles PVEAuditor
```

## What it reads, and how often

The live tier is every sync; the coordinator polls every **15 s** while someone watches (pvestatd refreshes every 10 s, so never faster) and every **5 min** otherwise. The other tiers run when due.

| Tier | Every | Calls | For |
|---|---|---|---|
| Live | each sync | `GET /cluster/resources` | Nodes, guests and storage with live CPU, memory, disk, uptime, status |
| Recent | 1 min | `/cluster/status`, `/cluster/tasks` | Quorum and membership; finished backups, config changes, failing apt runs |
| Slow | 5 min | `/nodes/{n}/status` per online node | `memory.available` (9.x), swap, root disk, kernel, PVE version, CPU model |
| Config | 10 min watched, 30 min idle, at once for a new guest or after a config task | `/storage`; `/nodes/{n}/{type}/{vmid}/config` per guest; while watched, `lxc/{vmid}/interfaces` and (where readable) `qemu/{vmid}/agent/network-get-interfaces` for running guests | Start at boot, disks → storage, passthrough, MACs, addresses; PBS and NFS servers |
| Backups | 15 min, or when a vzdump task finishes | `/cluster/backup`, `/cluster/backup-info/not-backed-up` | Jobs and coverage |
| Logs | once per failed run | `/nodes/{n}/tasks/{upid}/log?limit=1000` for each job's newest run, only when it failed | Per-guest results (they exist only in the log) |
| Rare | 1 h, and on start | `/version`, `/access/permissions`; with `Sys.Modify` only, `/nodes/{n}/apt/update` | The version adapter, privilege drift, pending updates |

For the dogfood home that's about 35 requests a minute while watched, as the spike measured. Nothing is asked of a node that's offline. Not used: RRD backfill (the hub keeps its own 30-minute history), `/cluster/metrics/export`, disks, certificates, HA status (`hastate` comes with the resources).

**Test connection** reads `/version`, `/access/permissions` (the grants), and `/access/users/{user}` for `privsep`.

## Version adapters

Picked from `/version`'s `release` and refined by the token's privileges:

| | 8.4 | 9.x |
|---|---|---|
| Host memory | `mem / maxmem` | `(total − available) / total` from the node status, while it's fresh |
| Guest memory | `mem / maxmem` | the same, **clamped to 100**: `mem` is host-side and can exceed `maxmem` |
| VM addresses | only with `VM.Monitor` (never asked for); not requested otherwise | the guest agent, with `VM.GuestAgent.Audit` (in 9.x's PVEAuditor) |
| PVEAuditor | 6 privileges | 7 (adds `VM.GuestAgent.Audit`) |

PVE before 8 is an `unsupported_version` Repair. A major above 9 is read like 9.x, with an info warning.

## What it publishes

IDs are `proxmox:<instance id>/<local path>`.

| Resource | ID | Kind | Attrs |
|---|---|---|---|
| The cluster (only when clustered) | `…/cluster` | `cluster` | `cluster`, `nodes`, `quorate`, `platform: pve` |
| Each node | `…/node/<node>` | `host` | `platform: pve`, `node`, `ip`, `cores`, `mem_gb`, `boot_at`, `pve_version`, `kernel`, `cpu_model`, `swap_gb`, `cluster`, `updates` (only when the token can read them) |
| Each guest | `…/qemu/<vmid>`, `…/lxc/<vmid>` (the VMID is cluster-wide, so a migration keeps the ID) | `vm`, `lxc` | `vmid`, `node`, `type`, `cores`, `mem_gb`, `onboot`, `os`, `ip`, `mac`, `tags`, `hastate`, `lock`, `hostpci`, `started_at`, `cluster`, `backup.last_at`, `backup.last_ok`, `backup.last_status`, `backup.last_error`, `backup.covered` |
| Shared storage | `…/storage/<id>` (once, though `/cluster/resources` lists it per node) | `storage` | `storage`, `type`, `shared`, `content`, `size_tb`, `nodes`, `pbs.server`, `pbs.datastore`, `pbs.fingerprint`, `nfs.server`, `nfs.export` |
| Local storage | `…/node/<node>/storage/<id>` | `storage` | as above, with `node` |
| Each backup job | `…/backup/<job id>` | `backup_job` | `job`, `schedule` ("daily 03:00", "weekly sun 05:00"), `mode`, `storage`, `node`, `selection`, `enabled`, `next_run`, `last_ok`, `last_run.start`, `last_run.end`, `last_run.status` (`ok`/`failed`), `last_run.ok`, `last_run.failed`, `last_run.error` |

Templates are skipped. Desired state is `running` for a guest with `onboot`; a guest without it has none (running it by hand is fine).

**Relations:** host `part_of` cluster; guest `runs_on` its current node; storage `part_of` its node (local, or shared on a standalone node) or the cluster (shared); guest `stores_on` the storages its disks live on; guest `backed_up_by` job; job `stores_on` its storage; `integration:<instance id>` `monitors` the cluster and every host.

**Metrics** (the simulator's keys first):

| Resource | Key | Unit | Source |
|---|---|---|---|
| host | `cpu.pct` | % | `/cluster/resources` `cpu` × 100 (node status reads 0) |
| host | `mem.pct` | % | see the adapters; warn 90, crit 98 |
| host | `swap.pct` | % | node status, when the node has swap |
| host | `rootfs.pct` | % | node status; warn 90, crit 95 |
| guest (running) | `cpu.pct`, `mem.pct` | % | `/cluster/resources`; `mem.pct` clamped |
| lxc (running) | `disk.pct` | % | `disk / maxdisk` (VMs report 0) |
| storage | `used.pct` | % | `disk / maxdisk`; warn 85, crit 95 |
| backup job | `tasks.failed_24h` | count | failed runs of the job in the last 24 h |
| backup job | `last_ok.age_h` | h | hours since the last good run, unknown when none is in the task list |

`swap.pct`, `rootfs.pct`, `disk.pct`, `tasks.failed_24h` on jobs and `last_ok.age_h` are keys the simulator doesn't have. **There's no `temp.c`:** PVE's API has no temperatures (the spike checked); the Demo Home's host temperatures need the node agent. Stopped guests have no series, as in the Demo Home. A value Cha Dash can't know is unknown, never 0.

**Health:**
- host: `down` when offline; `attention` at memory ≥ 90% (" · no swap" when it has none) or root disk ≥ 90%.
- cluster: `down` without quorum; `attention` while a node is offline.
- guest: `down` in `internal-error` or with HA state `error`/`fence`; `attention` when stopped but set to start at boot, or paused; otherwise `ok` ("stopped (expected)" for a stopped guest without `onboot`).
- storage: `attention` at ≥ 85% used, or when it isn't active; `ok` and state `disabled` when disabled.
- backup job: `attention` when its newest run failed, with what failed; `unknown` when no run is in the task list.

## Backups, from what a read-only token sees

An auditor can't list backup volumes. Results come from the vzdump tasks in `/cluster/tasks` and, for a failed run, its log, read once per run.

- **Which task is which job's run.** PVE doesn't record the job in the task. A task is a scheduled run of a job when it ran on the job's node (if the job names one), its ID is the guest for a one-guest job and empty otherwise, and it started within five minutes after a scheduled time: `next-run` gives one scheduled instant, compared at a day's period (an hour's for sub-daily schedules, with an hour of leeway for daylight-saving changes). A manual backup or "Run now" isn't a run (pluto's manual backup to `usb` isn't counted). Two jobs on one node at the same time with multi-guest selections can't be told apart.
- **Outcome.** `OK`: every selected guest finished. A task-level error (`could not activate storage …`): no guest did. `job errors`: some failed, and the log says which (`Finished Backup of VM 200`, `ERROR: Backup of VM 100 failed - …`).
- **Selection.** A `vmid` list; `all` on a node minus `exclude`; a pool can't be resolved read-only, so per-guest results are unknown for pool jobs.

## Inbox

| Incident | Class, severity | When | Resolves |
|---|---|---|---|
| `backup.<job id>` | problem, attention | The job's newest run failed. Scenario `backup-target-unreachable` (with `facts.endpoint`, `facts.reason`, `facts.failed_at`), `backup-passthrough-conflict` (`facts.pci_device`, `facts.in_use_by`), or `backup-failed`. Targets the guest when exactly one failed, else the job; the guests are the blast radius | When a later run succeeds |
| `guest.<vmid>` | problem, down | `internal-error`, or HA `error`/`fence`; the newest task on the guest is a cause | When it leaves that state |
| `node.<node>` | problem, down | A cluster member is offline | When it's back |
| `cluster.quorum` | problem, down | The cluster isn't quorate | When it is |
| `hint.backup-local.<job id>` | problem, info | The job writes to storage inside the host (io's job to `local`) | When it doesn't |
| `hint.not-backed-up` | problem, info | Guests no job covers (from `not-backed-up`), or no job at all | When every guest is covered |
| `hint.aptupdate.<node>` | problem, info | The newest `apt-get update` task failed (minig5: exit code 100, four days running) | When one succeeds |

**Correlated outages.** Backups on several hosts that fail the same nights for one reason (spike 4.1: pluto's and mars's jobs, 12 of 21 nights, PBS on jupiter unreachable) get scenario `backup-target-unreachable`, the cause pointed at the storage resource, the endpoint (`10.0.11.120:8007`) normalized in `facts.endpoint`, and the failed nights in `facts.failed_at`. One instance can't see another's incidents, so merging them into one root cause across instances is the correlator's job (wave 6); these facts are its keys, with `pbs.server`/`pbs.datastore` on the storage.

**Passthrough conflicts.** When a backup fails with `PCI device … already in use by VMID …` (io: VM 100 and VM 200 share GPU `0000:01:00`, and backing up the stopped VM 100 has to start it), the incident names both guests and the device.

Auth failures (401) are Repairs through the framework. A sync fails only when `/version` or `/cluster/resources` can't be read.

## Grants and warnings

**Test** turns `/access/permissions` into one grant per privilege, naming the paths it's held on. Everything but `*.Audit`, `Sys.Syslog` and `VM.GuestAgent.FileRead` is `Write: true` (`Permissions.Modify`, `Sys.Console`, `User.Modify` and `Realm.*` as `admin`), so the framework warns about anything beyond auditing. A `root@pam` token is listed as admin-equivalent. Test warnings:
- `privsep_off`: the token isn't privilege-separated;
- `guests_hidden`, `nodes_hidden`, `storage_hidden`: a privilege PVEAuditor would give is missing;
- `updates_hidden` (info): pending updates need `Sys.Modify`, which a read-only token shouldn't have;
- `backups_from_tasks` (info): a read-only token can't list backup volumes; results come from tasks and logs.

**Sync warnings** (they make the instance `degraded`): a part PVE refused with 403 (`cluster_status_hidden`, `tasks_hidden`, `backups_hidden`, `node_status_hidden`, `config_hidden`, `permissions_hidden`), named with PVE's reason; `partial_read` for a call that failed this time (the last data stays); `write_grant` when the hourly check finds write privileges; `version_untested`. The two read-only limits a PVEAuditor token always has (pending updates, backup volumes) are Test warnings only, so a correctly scoped instance stays `ok`. Everything else keeps working when a part is hidden.

**Dropped at ingestion:** descriptions, comments, cloud-init, SMBIOS and vmgenid UUIDs, raw `lxc` lines, disk serials, certificate PEMs, task logs beyond the parsed result.

## Not yet

- Merging instances that see the same domain (the cluster CA as identity), and failover between a cluster's nodes.
- Temperatures, disk health, certificate expiry, HA manager state, QDevice detection ("2 nodes, no QDevice").
- Backup state for pool-selected jobs; mapping a task to a job by anything but schedule and selection.
- The router hint (`provides_network_for` from the guest that is everyone's gateway), MAC and IP correlation with OPNsense and Docker: the facts are published (`ip`, `mac`, `pbs.*`, `nfs.*`) for the correlator.
- Actions (v0.2).
