# Proxmox Backup Server (`pbs`)

Reads one Proxmox Backup Server: the server as the instance's `app` resource, and each datastore as a `backup_target` with its usage, when PBS expects it to fill up, garbage collection, prune and verify jobs. Failed tasks of the last day go on the app. It reads with an Audit token; an optional control token lets it prune a datastore by its retention policy (see Actions).

**Tier:** silver (repairs, version check, fixture tests). **Capabilities:** `backups.targets`, `backups.jobs`. **Fixture:** [`testdata/integrations/pbs/`](../../testdata/integrations/pbs/README.md), **synthetic**: no PBS was captured, so it's built from the PBS API's source and agrees with the Proxmox VE capture (one PBS at `10.0.11.120:8007`, five datastores on one filesystem, 91.9% used).

## Config

```json
{
  "url": "https://pbs.lab.example:8007",
  "tls": {"mode": "tofu"},
  "token_id": "chadash@pbs!dash",
  "token_secret": "${secret:instance/i_x/token_secret}"
}
```

`url`, `tls`, `token_id` (plain, `user@realm!name`) and `token_secret` (vault), as in [07 · v0.1](../../docs/plan/07-v0.1.md#instance-config-keys) and as the Homepage importer writes a `proxmoxbackupserver` widget. The header is `Authorization: PBSAPIToken=<token_id>:<token_secret>` (a colon, where PVE uses `=`).

**Token recipe.** A token's rights are limited to its user's, so grant both:

```sh
proxmox-backup-manager user create chadash@pbs
proxmox-backup-manager acl update / Audit --auth-id chadash@pbs
proxmox-backup-manager user generate-token chadash@pbs dash
proxmox-backup-manager acl update / Audit --auth-id 'chadash@pbs!dash'
```

`Audit` is `Sys.Audit` + `Datastore.Audit`. `DatastoreAudit` alone shows the datastores but not other users' tasks or the server's load (see Grants).

**Control token** (optional): `control_token_id` and `control_token_secret`, the form's control group (`Manifest.Control`). Both or neither. The recipe's control variant ([`recipe.control.md`](recipe.control.md)) makes a second user with **DatastoreAdmin** on one datastore: pruning every group (not only the token's own) needs `Datastore.Modify`, only DatastoreAdmin holds it, and PBS has no custom roles. That role can also read and delete the datastore's backups, which the recipe says.

## What it reads, and how often

The coordinator polls every **60 s** while someone watches and every **5 min** otherwise.

| Tier | Every | Calls | For |
|---|---|---|---|
| Live | each sync | `GET /status/datastore-usage` | Every datastore's total/used/avail, `estimated-full-date`, mount status, error, last GC status |
| Live | each sync, with `Sys.Audit` on `/system/tasks` | `GET /nodes/localhost/tasks?errors=1&since=<now − 24 h>&limit=1000` | Tasks that didn't end OK in the last day |
| Jobs | 5 min | `/admin/datastore`, `/admin/gc`, `/admin/prune`, `/admin/verify`; while watched and with `Sys.Audit` on `/system/status`, `/nodes/localhost/status` | Maintenance mode; GC schedule and last run; prune jobs (retention, disabled); verify results; CPU and memory |
| Rare | 1 h, and on start | `/version`, `/access/permissions` | The version check; the grants. `/version` answers anybody, so the permissions call is what proves the token: its 401 is the auth Repair |

A PBS without `/admin/gc` or `/admin/prune` (404) falls back quietly: GC from `datastore-usage`'s `gc-status`, no prune or verify state. **Test connection** reads `/version` and `/access/permissions`.

PBS before 3.x is an `unsupported_version` Repair; above 4.x it's read the same way, with an info warning. PBS's `/version` is `{version: "4.0", release: "14"}`: the full version is `4.0.14`.

## What it publishes

| Resource | ID | Kind | Attrs |
|---|---|---|---|
| The server | `pbs:<instance id>/app` | `app`, `attrs.type: "pbs"`, named after the instance | `version`, `datastores`, `cpu_model`, `boot_at`; link to the GUI; caps `backups.targets`, `backups.jobs` |
| Each datastore | `pbs:<instance id>/datastore/<store>` | `backup_target` | `store`, `backend`, `capacity_tb`, `used_tb`, `full_at`, `full_in_days`, `gc_schedule` (`none` without one), `gc_last`, `gc_state`, `prune_job` (`enabled`/`disabled`/`none`), `retention` ("keep-daily 7 · weekly 4 · monthly 6"), `prune_last`, `prune_state`, `verify_last`, `verify_state`, `maintenance`; link to `#DataStore-<store>` |

**Relations:** datastore `part_of` app; `integration:<instance id>` `monitors` the app. The PVE storages that write here carry `pbs.server`/`pbs.datastore`, for the correlator to join.

**Metrics:**

| Resource | Key | Unit | Notes |
|---|---|---|---|
| app | `tasks.failed_24h` | count | Tasks that ended in an error in the last 24 h (warnings don't count). **Unknown**, not 0, when the token can't see other users' tasks |
| app | `cpu.pct`, `mem.pct` | % | From the node status, while it's fresh; only with `Sys.Audit` on `/system/status` |
| datastore | `used.pct` | % | `used / total`; band 0–80, warn 90, crit 96 (the Demo Home's `pbs:main`) |

`cpu.pct` and `mem.pct` on the app are keys the Demo Home's `app:pbs` doesn't have.

**Health:**
- app: `attention` while a task failed in the last 24 h.
- datastore, worst first: `down` with PBS's `error`; `unknown` for a removable datastore that isn't mounted; `maintenance` in maintenance mode; `attention` at ≥ 90% used, when PBS expects it full within 30 days, or when its last GC, prune or verify failed ("92% used · full in about 3 weeks").

## Inbox

| Incident | Class, severity | When | Resolves |
|---|---|---|---|
| `filling.<store>` | problem, attention, scenario `pbs-datastore-filling` | Datastores at ≥ 90% or due to fill up within 30 days. **Datastores that report identical totals share a filesystem and are one incident**, named after the first (the dogfood's five are one). Causes: a disabled prune job and when it last ran, datastores with no prune job, GC that hasn't run for a week | When usage is below 90% and the estimate is past 30 days |
| `datastore.<store>.error` | problem, down | PBS reports an error for the datastore | When it doesn't |
| `tasks.failed` | problem, attention | Tasks failed in the last 24 h; targets the datastore of the newest one | When none has failed for 24 h |

## Actions

| Action | Tier | Target | What it does |
|---|---|---|---|
| `pbs.datastore.prune` ("Prune per policy") | T2 | a datastore (`backup_target`) | `POST /admin/datastore/{store}/prune-datastore` with the retention of the datastore's prune job (an enabled one first, then by ID; `job` param to choose): its `keep-*` options, `ns` and `max-depth`. Follows the PBS task's log into the task; Verify reads the task's status back with the read token |

- The ID and label are the Demo Home's, so the `filling.<store>` incident suggests it (with the link to PBS as the alternative) when the datastore's prune job is disabled, and the hub fills its `plan_ready`.
- **Preconditions:** a datastore in maintenance (`mars is in maintenance (read-only), so it can't be pruned now.`), no prune job (`mini-g5 has no prune job, so there's no retention policy to prune by. …`), a named job that isn't there, a policy that keeps everything.
- Pruning only marks snapshots gone; the space comes back at the next garbage collection, as the plan's steps say.
- **Without a control token** the action answers `409 unavailable` ("Add a control token to … to let Cha Dash prune its datastores. …") with `recipe` naming the instance. A 403 from PBS fails the task with the role it needs.
- **Fixtures:** synthetic, [`testdata/integrations/pbs/actions/`](../../testdata/integrations/pbs/actions/README.md). Not verified without a live run: that `Datastore.Prune` alone prunes only the token's own groups, and PBS's exact 403 text.

## Grants and warnings

**Test** turns `/access/permissions` into one grant per privilege with the paths it's held on. `*.Audit`, `Datastore.Read`, `Remote.Read` and `Tape.Read` are read-only (`Datastore.Read` can restore every backup it covers, and says so); the rest is `Write: true` (`Permissions.Modify`, `Sys.Console`, `Realm.Allocate` as `admin`). Test warnings: `datastores_hidden`, `tasks_hidden`, `node_status_hidden` (info).

**Sync warnings:** `tasks_hidden` while failed tasks can't be counted; a part PBS refused with 403 (`datastores_hidden`, `jobs_hidden`, `node_status_hidden`); `partial_read` for a call that failed this time; `write_grant` when the hourly check finds write privileges; `version_untested`.

## Not yet

- Snapshots and groups (per-guest last backup, `snapshots_beyond_policy`): listing them is a heavy call per datastore. PVE's own backup results come from the Proxmox integration.
- Sync jobs, tape, remotes, S3 statistics, namespaces.
- Joining a PVE storage to its datastore (`stores_on`) across instances: the correlator's job in wave 6.
- A capture of a real PBS: the fixture is synthetic.
