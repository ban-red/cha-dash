# Proxmox VE fixtures: dogfood lab

Recorded responses from the owner's five PVE endpoints (six nodes, one of them a 2-node cluster), captured read-only on 2026-10-03 by [spike 4.1](../../../spikes/pve/README.md). They seed the v0.1 Proxmox integration's contract tests. Every request was a `GET` with a privilege-separated `PVEAuditor` token.

The fixtures are sanitized (scheme below). A leak check scanned them for every real value seen during capture and found none.

## Layout

```
<instance>/
  _index.json          every request, in capture order
  _tls.json            what the TLS handshake taught us (no fingerprints, no dates)
  version.json         …one file per response, at its API path under /api2/json
  cluster/resources.json
  nodes/<node>/status.json
  nodes/<node>/qemu/<vmid>/config.json
  …
```

- **Bodies are PVE's own envelope**: `{"data": …}`. Errors carry `"data": null`, plus `"message"` on some.
- **`_index.json`** lists each request with:
  - `path`: relative to `/api2/json`, with its query;
  - `file`;
  - `status` and `reason`: PVE puts the error text in the HTTP status line, e.g. `403 Permission check failed (/nodes/io, Sys.Modify)`;
  - `ms`: latency from the capturing laptop over the LAN;
  - `bytes` (decoded) and `wire_bytes` (as sent; pveproxy gzips bodies over about 1 kB);
  - `trimmed`: set when a body was cut down (see below);
  - `dropped_keys`: per instance, which keys were removed and how many times.
- **Replay:** serve `GET /api2/json` + `path` from `file` with `status`. Paths with a query (`tasks?limit=50&source=all`, `rrddata?timeframe=hour&cf=AVERAGE`) are listed in full in the index, so match on the full path.

## Instances

| Instance | PVE | Shape | Guests | Worth testing |
|---|---|---|---|---|
| `pluto` | 9.0.9 | standalone | OPNsense VM (qemu/103, the router) · 1 LXC | The router VM's guest agent shows the LAN gateway (`10.0.11.1`), VLANs and a tailscale interface. Scheduled backups to PBS fail with "No route to host"; a manual backup to `usb` succeeded |
| `minig5` | 8.4.19 | standalone | `haos` VM · 1 LXC (Docker inside) | **PVE 8 shapes**: no `memhost`, no `memory.available`, a 70-point day RRD, `sdn` resources. The guest agent returns **403** (`VM.Monitor`). Weekly backup only, the LXC isn't covered. `apt-get update` fails. The URL host isn't in the certificate's SAN |
| `mars` | 9.1.5 | **cluster** `mars`: `mars-1`, `mars-2` | `pvedocker` LXC (**HA-managed**, rootfs on Ceph RBD) · `chadash-sandbox` LXC | One endpoint lists both nodes. Shared Ceph storage (`rbd`, `cephfs`) appears once per node. `/cluster/config/join` has both nodes' cert fingerprints. HA status (quorum, master, LRMs, `ct:101`). `network` resources (9.1+) instead of `sdn` |
| `io` | 9.2.20 | standalone | Windows VM (GPU) · Linux VM (GPU, stopped) · `iolinux` VM (running) | **The nightly job fails every night**: VM 100 and VM 200 share GPU `0000:01:00`. The job writes to `local`, the node's own disk. VM 101 isn't in any job. `mem` > `maxmem` for `iolinux`. Whole NVMe disks passed through by `/dev/disk/by-id` |
| `jupiter-nas` | 8.4.21 | standalone | Debian LXC · VM 101 in **`internal-error`** | VM 101 holds 21 GB of the host's 33.5 GB, which explains the host's 95% memory. `qmreboot` timed out, then `qmreset`. **No backup job at all**: both guests are in `not-backed-up` |

## What each fixture shows

| Fixture | What it's for |
|---|---|
| `version.json` | Version detection: `release` `8.4` / `9.0` / `9.1` / `9.2` picks the adapter |
| `cluster/status.json` | Cluster or standalone. A cluster adds a `type: cluster` entry (name, nodes, quorate). Each node entry has `online`, `local` (the node we're talking to), `nodeid`, `ip` |
| `cluster/resources.json` | **The one call for the fleet:** nodes, guests, storage and SDN with live `cpu`/`mem`/`disk`/`net*`/`uptime`/`status`. Shared storage is repeated per node |
| `cluster/tasks.json` · `nodes/*/tasks.json` | Recent tasks (vzdump, aptupdate, qmstart …). `status` is `OK` or the error text. Node lists have `total` (~1000) and honor `limit` |
| `nodes/*/tasks/vzdump-*.{status,log}.json` | The newest and newest-failed vzdump task with its log. **Per-guest backup results only exist in the log**: `Finished Backup of VM 103 (00:03:24)`, `ERROR: Backup of VM 100 failed - PCI device …` |
| `cluster/backup.json` | Backup jobs: schedule, storage, `vmid` list, `next-run`, prune settings |
| `cluster/backup-info/not-backed-up.json` | Guests in no job: backup coverage without any parsing |
| `cluster/ha/status/current.json` | Quorum, plus HA manager and resources on `mars` |
| `cluster/config/join.json` | `mars`: nodes, addresses, `pve_fp` per node. Standalone: **424** "node is not in a cluster" (not an error to surface) |
| `cluster/metrics/export.json` · `export.history-10m.json` | The pull-metrics endpoint (works on 8.4 and 9.x). Flat `{id, metric, timestamp, type, value}` rows at 10 s resolution. The history file is trimmed to 3 timestamps |
| `nodes.json` | Node list with `ssl_fingerprint` (matches the served certificate) |
| `nodes/*/status.json` | Host tile: memory (9.x adds `available`), swap, rootfs, loadavg, uptime, kernel, `pveversion`, CPU model, boot mode. **`cpu` reads 0**: use `cluster/resources` |
| `nodes/*/storage.json` | Per-node storage status, `active`/`enabled`, `used_fraction`. Slow (200–700 ms): it probes every storage, PBS included |
| `nodes/*/storage/*/content.json` | **What an auditor can list**: ISOs and templates only. Backup volumes are filtered out (they need `VM.Backup` + `Datastore.AllocateSpace`) |
| `storage.json` | Storage config: types, PBS server/datastore/fingerprint, NFS export (`/mnt/user/games` on jupiter = an Unraid share) |
| `nodes/*/rrddata.hour.json` · `rrddata.day.json` | Sparkline backfill. PVE 9: 60 points/hour and **1440 points/day** (670 kB, downsampled to 72 here); adds `memavailable`, `arcsize` and PSI `pressure*`. PVE 8.4: 70 points each, 13 fields |
| `nodes/*/<type>/<vmid>/rrddata.hour.json` | One guest's history shape (PVE 9 adds `memhost` and pressure) |
| `nodes/*/disks/list.json` | Disks: model, health (`PASSED`/`UNKNOWN`), wearout, type, Ceph OSD ids on `mars` |
| `nodes/*/certificates/info.json` | The served cert (`pve-ssl.pem`) and the cluster CA (`pve-root-ca.pem`). Auditors can read both, so the CA can be pinned. PEM bodies dropped |
| `nodes/*/apt/update.json` | **403** (`Sys.Modify`): an auditor can't see pending updates |
| `nodes/*/<type>/<vmid>/config.json` | `onboot` (desired state), `startup` order, `protection`, `hostpci*`, disk → storage mapping, `tags`, `ostype`. `description`, `args` and raw `lxc` lines dropped |
| `nodes/*/lxc/<vmid>/interfaces.json` | LXC addresses (works on 8.4 and 9.x): the correlator's link from a guest to a Docker endpoint or Traefik upstream |
| `nodes/*/qemu/<vmid>/agent/network-get-interfaces.json` | VM addresses via the guest agent: 200 on 9.x (`VM.GuestAgent.Audit` is in PVEAuditor), **403** on 8.4 |
| `access/permissions.json` · `permissions.owner.json` | The token's effective privileges, and its owner user's, per path |
| `access/user.json` | The owner user, with `tokens.<name>.privsep: 1`. **This is how privilege separation is verified read-only** |
| `access/token.json` | **403**: reading a token needs `User.Modify` |
| `access/acl.json` · `access/roles.json` | All ACL entries (visible with `Sys.Audit`) and every role's privileges (PVEAuditor's set differs between 8.4 and 9.x) |
| `pools.json` | Empty everywhere |

## How it was sanitized

The repo is public. Field names, shapes, numbers, timestamps, statuses and error texts are kept as captured. Only these were replaced, consistently across all files and instances:

| Original | Fixture |
|---|---|
| Node names (hostnames) | Host nicknames: `pluto`, `minig5`, `mars-1`, `mars-2`, `io`, `jupiter-nas`. The cluster is `mars` |
| LAN IPs | `10.0.11.x`, last octet kept. Other subnets (VLANs, WAN, tailnet) are `10.0.20.x`, `10.0.24.x`, `10.0.28.x` …, spaced so a prefix up to /22 never overlaps another |
| Docker's default bridges (`172.17–31.x`) | **Kept**: they're the same on every Docker host |
| IPv6 | `fd00:11::n` (global/ULA) and `fe80::be24:11ff:fe00:n` (link-local). Hand-set link-locals such as `fe80::1` kept |
| Domains | `*.lab.example` (names ending in `.lan` and friends too, including one storage ID) |
| MACs | `BC:24:11:00:00:nn` (Proxmox's OUI, zeroed) |
| Certificate fingerprints, PBS fingerprint and encryption-key fingerprint | `00:00:…:00:nn` |
| UUIDs (`vmgenid`, `smbios1`, CA `OU`, PBS task ids) | `00000000-0000-4000-8000-0000000000nn` |
| Disk serials, WWNs | `FAKESERIALnnnn`, `0x5000000000nnnnnn`. Models kept |
| E-mail | `admin@lab.example` |
| Token ID | `api@pam!chadash`. The secret never appears in any response |
| Guest, storage, tag, user and path names that could identify someone | Neutral names (`vm-101`, `ct-200`, `store-3`, `tag-1`, `user-1`). Names made only of generic words (`OPNsense`, `haos`, `pvedocker`, `iolinux`, `local-lvm`, `jupiter-pbs` …) kept |
| `description`, `comment`, `args`, raw `lxc` lines, certificate PEMs, user e-mail/names/keys | Dropped (counted in `dropped_keys`) |
| `cipassword` | `**********`, as PVE already masks it |

**Trimmed** (marked in `_index.json`):
- PVE 9 `rrddata?timeframe=day` is downsampled from 1440 to 72 points;
- `metrics/export?history=1` keeps its first 3 timestamps.

Sizes in the index are the originals.

## Regenerating

With the owner's `.env.local` and lab access:

```sh
cd spikes/pve
go run . -env ../../.env.local -tofu capture   # raw captures → spikes/pve/raw/ (git-ignored)
go run . -env ../../.env.local sanitize         # → testdata/integrations/proxmox/
go run . -env ../../.env.local leakcheck        # must print "no real values found"
go test ./...                                   # generic leak patterns; no lab needed
```

CI only tests the root module. The generic leak test (`TestFixturesHaveNoGenericLeaks`) should move into the v0.1 integration's tests, so that every fixture change is checked.

The re-sanitized output is deterministic for the same captures.
