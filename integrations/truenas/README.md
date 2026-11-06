# TrueNAS (`truenas`)

Reads one TrueNAS (Community Edition or Enterprise, 25.04 and later), read only: the system as a `host`, its pools and their health, datasets and their usage, disks with SMART and temperature, apps, periodic snapshot tasks, and replication tasks as backup targets that feed the coverage map. A degraded pool, a failed replication or snapshot task, and TrueNAS's own alerts at ERROR or worse are incidents.

**Tier:** silver (repairs, version check, fixture tests). **Capabilities:** `storage.pools`, `storage.disks`, `storage.shares`, `backups.targets`. **Fixture:** [`testdata/integrations/truenas/`](../../testdata/integrations/truenas/README.md), **written from TrueNAS 25.10's API schema, not recorded**: there's no TrueNAS in the maintainer's lab, so `live:verify` can't check the fixtures until an instance is added.

## Config

```json
{
  "url": "https://nas.lab.example",
  "tls": {"mode": "tofu"},
  "api_key": "${secret:instance/i_x/api_key}"
}
```

`url` must be **https**: TrueNAS revokes an API key that arrives over plain HTTP, so an `http://` URL is refused before anything is sent. `api_key` (vault) is a key of a user with the **Read-only Administrator** privilege ([recipe](recipe.md)).

## The API

TrueNAS's REST API is deprecated (W3); this speaks the **WebSocket JSON-RPC 2.0 API** at `wss://<host>/api/current` through `github.com/coder/websocket` (already a dependency) on the instance's `d.HTTP`, so the TLS policy and pins apply. One session: `auth.login_with_api_key` with the key in the message (never the URL), then one call at a time. The session stays open between syncs, so polling isn't a login a minute in TrueNAS's audit log; a session TrueNAS closed is dialled again once within the sync (reconnecting, not retrying: a call that fails on a fresh session fails the sync). `auth.login_with_api_key` is deprecated for removal in TrueNAS 28 in favour of `auth.login_ex`; it's the call that works on 25.04 and 25.10 without the username.

| Every sync (60 s watched, 5 min idle) | For |
|---|---|
| `system.info` | Version check, host facts |
| `pool.query` | Pools, status, usage, scrub, topology (which disk is FAULTED) |
| `pool.dataset.query [[], {extra: {flat, retrieve_children: false, user_properties: false}}]` | Datasets and their usage |
| `disk.query [[], {extra: {pools: true}}]` | Disks |
| `app.query` | Apps |
| `pool.snapshottask.query`, `replication.query` | Tasks and their zettarepl state |
| `alert.list` | SMART (the `SMART*` alert classes; 25.10 dropped the SMART test API) and other alerts |
| `disk.temperatures [[], false]`, only while watched | `temp.c` per disk |

**Test connection** reads `system.info` and `auth.me` (the key's user's roles as grants; anything but `READONLY_ADMIN` and `*_READ` roles is a write grant, which warns).

**Errors.** `login_with_api_key` answering `false` (wrong, expired or revoked key) is an auth Repair on `api_key`, as is `EACCES` on `system.info` or `pool.query`. A 404 on `/api/current` is `unsupported_version` (TrueNAS before 25.04), as is a version below 25; above 25.x it's read the same way with an info warning. `EACCES` on the other calls hides that part with a warning; a method this release lacks is skipped; other JSON-RPC errors keep what was read before with a `partial_read` warning. The key never reaches a log, an error (JSON-RPC errors are redacted), a warning or `d.State`.

## What it publishes

| Resource | ID | Kind | Attrs |
|---|---|---|---|
| The system | `truenas:<instance id>/host` | `host`, label `chadash.role: nas` | `platform: truenas`, `version`, `hostname`, `cpu`, `cores`, `threads`, `mem_gb`, `ecc`, `boot_at`, `ip` (when the URL's host is an address), `alerts` |
| Each pool | `…/pool/<name>` | `pool` | `type: zfs`, `status`, `path`, `size_tb`, `used_tb`, `free_tb`, `fragmentation_pct`, `layout` (`RAIDZ2`, `2 × MIRROR`), `disks`, `scrub_state`, `scrub_last`, `scrub_errors`, `resilver_pct`; state `online`, `degraded`, `faulted`, `resilvering`… |
| Each dataset | `…/dataset/<pool/path>` | `share` (a filesystem) or `volume` (a zvol) | `type: dataset`, `dataset`, `pool`, `path`, `used_gb`, `available_gb`, `quota_gb`, `snapshots_gb`, `encrypted`, `locked`, `snapshots` (an enabled snapshot task covers it). Pool roots, `.system`-style and `ix-apps` datasets aren't published |
| Each disk | `…/disk/<serial>` (the name without one) | `disk` | `device`, `serial`, `model`, `size_tb`, `media`, `rotational`, `rpm`, `bus`, `role` (`data`, `log`, `cache`, `spare`, `special`, `dedup`, `boot`, `unassigned`), `pool`, `status`, `errors`, `smart` (`ok` or the alert) |
| Each app | `…/app/<name>` | `app`, `attrs.type: truenas_app` | `app`, `catalog_app`, `train`, `version`, `app_version`, `custom`, `update_available`, `latest_version`; its portals as links |
| Each snapshot task | `…/snapshot_task/<id>` | `snapshot_task` | `dataset`, `recursive`, `enabled`, `schedule` (cron), `retention`, `last_at`, `last_snapshot`, `error` |
| Each replication task | `…/replication/<id>` | `backup_target`, caps `backups.targets` | `direction`, `transport`, `source_datasets`, `target_dataset`, `remote`, `recursive`, `enabled`, `auto`, `retention`, `schedule`, `last_snapshot`, and the coverage attrs below |

**Relations:** pool `part_of` host; dataset `part_of` its pool; disk `part_of` its pool (else the host); app `runs_on` host; snapshot task `part_of` its dataset; replication task `part_of` host; `integration:<instance id>` `monitors` the host.

**Backup coverage.** A replication task is a backup target ([docs/specs.md](../../docs/specs.md#backup-targets)): `backup.schedule` (its cron, or its snapshot task's), `backup.last_at` and `backup.last_status` (`ok`/`failed`) from zettarepl's state, `backup.last_error`, and `backup.last_ok`, the last FINISHED run, kept in `d.State` because zettarepl's state holds only the newest run. A **push** sets `backup.covers`: `{resource}` for each source dataset and, when recursive, each published dataset under it (minus `exclude`), plus `{host, path}` with the source's mountpoint so the correlator also covers containers on this host whose every mount is under it. A **pull** backs up another machine and covers nothing here. The correlator turns these into `backed_up_by` (rule `covers`), and datasets, as `share`s, appear on the coverage map.

**Metrics:**

| Resource | Key | Unit | Notes |
|---|---|---|---|
| host | `alerts.active` | count | Undismissed alerts at WARNING or worse; unknown until `alert.list` is read |
| host | `load.1m` | | Scale 0 to the thread count |
| pool | `used.pct` | % | `allocated / size`; band 0–70, warn 80, crit 90 (ZFS slows past 80%) |
| dataset | `used.pct` | % | `used / (used + available)`, as TrueNAS shows it; warn 90, crit 96 |
| disk | `temp.c` | °C | While watched; the Unraid scales (HDD warn 50, SSD warn 70) |

**Health:**
- host: `attention` while TrueNAS has undismissed alerts at WARNING or worse.
- pool: `down` when FAULTED, UNAVAIL, OFFLINE, REMOVED or SUSPENDED; `attention` when DEGRADED or unhealthy (naming the disks), or at ≥ 80% used.
- dataset: `unknown` while locked; `attention` at ≥ 90% (of its quota, with one).
- disk: `down` when its pool says FAULTED, UNAVAIL or REMOVED, or a SMART alert is CRITICAL; `attention` when DEGRADED, with ZFS errors, or a SMART alert.
- app: `down` when CRASHED.
- snapshot and replication tasks: `attention` when enabled and the last run failed (a replication also when on HOLD).

## Inbox

| Incident | Class, severity | When | Resolves |
|---|---|---|---|
| `pool.<name>.health` | problem; attention when DEGRADED or unhealthy, down when FAULTED and worse; scenario `truenas-pool-degraded` | The pool isn't ONLINE and healthy. Causes: each disk that isn't ONLINE or has errors, with its counts | When TrueNAS reports it ONLINE and healthy |
| `replication.<id>.failed` | problem, attention, `truenas-replication-failed` | An enabled task's last run failed. Facts include `last_ok`; the blast radius lists the datasets it covers (`via: backed_up_by`) | When its next run finishes |
| `snapshot.<id>.failed` | problem, attention, `truenas-snapshot-failed` | An enabled snapshot task's last run failed | When its next run succeeds |
| `alert.<uuid>` | problem, attention, `truenas-alert` | Any other undismissed alert at ERROR or worse. TrueNAS's alerts for pools, replication, snapshots and SMART aren't repeated: the incidents and health above say it | When TrueNAS clears or dismisses it |

## Not yet

- Actions (snapshots, app restarts) are v0.5.x.
- SMART comes from alerts only: TrueNAS 25.10 removed SMART test results from the API, and 25.04's `smart.test.results` isn't read.
- Pool and dataset history, ARC, network and CPU (`reporting.*`) aren't read.
- Push over `collection_update` subscriptions isn't used; the session is polled.
