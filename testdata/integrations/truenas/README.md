# TrueNAS fixture (written from the schema)

> **Not recorded.** The maintainer's lab has no TrueNAS, so these answers are written by hand from TrueNAS 25.10's API schema (the middleware's `api/v25_10_0` models, and the plugins that fill the untyped parts: `pool.transform_topology`, zettarepl's task state, the alert sources), not recorded from a running system. Field names and shapes follow the API; values are made up. `live:verify` can't check them until an instance is added; replace them with a capture when one exists.

They seed the `truenas` integration's contract tests ([`integrations/truenas`](../../../integrations/truenas/README.md)). Each file is one method's JSON-RPC `result`, named `<method>.json`; the fake in `truenas_test.go` serves them over `wss://…/api/current`. Datetimes are the middleware's `{"$date": <Unix ms>}`, sets `{"$set": [...]}`.

## The scenario

`nas01`, TrueNAS 25.10.0, read on 2026-10-03 at 14:05 UTC (the harness clock) with the API key of a user holding `READONLY_ADMIN`.

- **Pools.** `tank`, RAIDZ2 of six 8 TB disks, 71.2% used, **DEGRADED**: `sdd` is FAULTED with 3 read and 41 checksum errors. `fast`, a mirror of two NVMe drives, ONLINE, 41.9% used. Both scrubbed on Sep 27.
- **Datasets.** The pool roots, `tank/.system` and `tank/ix-apps` (hidden by the integration); `tank/media` with `movies` and `tv`; `tank/backups` with a 5 TiB quota at 80%; `tank/vault`, encrypted and locked; `fast/apps`, `fast/vms` and the zvol `fast/vms/homeassistant`.
- **Disks.** `sda`–`sdf` in `tank`, `nvme0n1`/`nvme1n1` in `fast`, `sdg` the boot SSD (`boot-pool`). Serials are `FAKE…`. Temperatures from `disk.temperatures`; `sdd` runs at 44 °C.
- **Apps.** Jellyfin (running, an upgrade waiting), Immich (running), Nextcloud (CRASHED), Syncthing (stopped).
- **Snapshot tasks.** Daily on `tank/media` and hourly on `fast/apps`, both fine; monthly on `tank/backups`, which failed at 03:00 ("out of space").
- **Replication.** 1: `tank/media` pushed over SSH to `backup.lab.example`, finished 02:00. 2: `fast/apps` to `tank/backups/apps` locally, **failed** at 13:30 ("out of space"). 3: a pull from `backup.lab.example` into `tank/backups/offsite`. 4: `fast/vms` pushed weekly, disabled.
- **Alerts.** `VolumeStatus` (tank degraded), `SMARTFailedSelfTest` (sdd), `ReplicationFailed` (task 2), `CertificateExpired` (critical), `NTPHealthCheck` (warning) and a dismissed `ScrubPaused`.

## Files (`nas/`)

| File | Method | Notes |
|---|---|---|
| `system.info.json` | `system.info` | `version`, `hostname`, `physmem`, `model`, `cores`, `physical_cores`, `loadavg`, `uptime_seconds`, `boottime`, `ecc_memory` |
| `auth.me.json` | `auth.me` | The key's user; `privilege.roles` as a `$set` |
| `pool.query.json` | `pool.query` | `PoolEntry`, with `scan` and `topology` (vdevs with `stats`, `children`, `disk`) |
| `pool.dataset.query.json` | `pool.dataset.query` | `PoolDatasetEntry`, flat, without children; properties as `{parsed, rawvalue, value, source}`; only some properties |
| `disk.query.json` | `disk.query` (`extra.pools`) | `DiskEntry` |
| `disk.temperatures.json` | `disk.temperatures` | `{name: °C}` |
| `app.query.json` | `app.query` | `AppEntry` |
| `pool.snapshottask.query.json` | `pool.snapshottask.query` | `PoolSnapshotTaskEntry`, `state` from zettarepl |
| `replication.query.json` | `replication.query` | `ReplicationEntry`, `ssh_credentials` expanded, `state` from zettarepl |
| `alert.list.json` | `alert.list` | `Alert` |

Sanitized as AGENTS.md asks: `*.lab.example` names, private addresses in `10.0.12.0/24`, UUIDs under `00000000-0000-4000-8000-`, fake serials and host key. `leak_test.go` checks it.
