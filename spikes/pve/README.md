# Spike 4.1 · Proxmox VE: multi-instance, cluster, read-only

Throwaway code that answers four questions against the owner's real lab, so the v0.1 Proxmox integration starts from facts:
1. What do five PVE endpoints, one of them a 2-node cluster, actually return?
2. Is privilege separation really in effect on the tokens, and what can they do?
3. How does that map onto the Home Graph?
4. What does polling it cost?

It also produced the **sanitized fixtures** in [`testdata/integrations/proxmox/`](../../testdata/integrations/proxmox/README.md).

Not product code. The integration is written fresh.

## How it works

| Command | What it does |
|---|---|
| `instances` | Lists the instance nicknames from the env file. Never URLs or tokens |
| `capture` | Walks every instance with **GETs only** and writes raw bodies to `-raw` (git-ignored): sequential requests, 250 ms apart, 15 s timeout, one connection per instance |
| `tlscheck` | One `GET /version` per instance against the pinned fingerprints, without `-tofu` |
| `sanitize` | Turns the raw captures into the public fixtures (scheme in the [fixtures README](../../testdata/integrations/proxmox/README.md#how-it-was-sanitized)). Deterministic |
| `leakcheck` | Rebuilds every real value from the env file and the raw captures, **in memory only**. Plants a canary in a scratch file and must find it. Then scans the fixtures and this directory, printing file:line and the value's class, never the value |
| `report` | Prints the tables below from the **sanitized** fixtures only |

**Safety:**
- The client has no method but `Get`, and a transport guard refuses anything else. Redirects are refused.
- The `Authorization` header is never printed (`-debug` shows `PVEAPIToken=<redacted>`).
- Transport errors are scrubbed of the URL and host.
- Paths in the progress log are templated (`/nodes/{node}/status`).

```sh
cd spikes/pve
go run . -env ../../.env.local -tofu capture     # first run pins each instance's certificate
go run . -env ../../.env.local tlscheck          # later runs compare against the pin
go run . -env ../../.env.local sanitize
go run . -env ../../.env.local leakcheck         # → "no real values found"
go run . report
go test ./...                                    # canary, mapper and generic-pattern tests; the full leak test runs with PVE_ENV/PVE_RAW
```

## Findings

Captured 2026-10-03 (evening, lab time): 202 requests across 5 instances, nothing written. Server time was 1–10 s per instance, most of it in a few slow storage and SMART calls.

**Verdict:**
- **The integration needs one call per PVE "domain" (a cluster or a standalone node) for live state: `/cluster/resources`.** Everything else is slow-path.
- **Privilege separation is in effect on all five tokens, and you can verify it read-only.**
- **The fleet mixes PVE 8.4 and 9.x**, so adapters by version are needed from day one.
- **An auditor can't list backup volumes.** Backup state comes from tasks and their logs, or from a PBS integration.

### The instances

| Instance | PVE | Shape | Nodes | Guests (running/total) | Storage | TLS (first run) |
|---|---|---|---|---|---|---|
| pluto | 9.0.9 | standalone | pluto | 2/2 | 4 | pinned on first use; SAN has the URL host |
| minig5 | 8.4.19 | standalone | minig5 | 2/2 | 3 | pinned on first use; **URL host not in the SAN** |
| mars | 9.1.5 | cluster `mars`, 2 nodes, quorate | mars-1, mars-2 | 2/2 | 6 | pinned on first use; SAN has the URL host |
| io | 9.2.20 | standalone | io | 1/3 | 5 | pin matched (pinned by an earlier run) |
| jupiter-nas | 8.4.21 | standalone | jupiter-nas | 1/2 | 5 | pinned on first use; SAN has the URL host |

### Privilege separation (per instance, sanitized)

| Instance | privsep | ACLs on `/` | Token's effective privileges | Beyond PVEAuditor | Owner user's privileges |
|---|---|---|---|---|---|
| pluto | **1** | group → PVEAuditor; token → PVEAuditor | Datastore.Audit Mapping.Audit Pool.Audit SDN.Audit Sys.Audit VM.Audit VM.GuestAgent.Audit | **none** | same as the token |
| minig5 | **1** | same | Datastore.Audit Mapping.Audit Pool.Audit SDN.Audit Sys.Audit VM.Audit | **none** | same |
| mars | **1** | same | as pluto (9.x set) | **none** | same |
| io | **1** | same | as pluto (9.x set) | **none** | same |
| jupiter-nas | **1** | same | as minig5 (8.4 set) | **none** | same |

**How it was verified (all GETs):**
- **`privsep`:** `GET /access/users/{user}` returns `tokens.<name>.privsep`. Reading it needs `Sys.Audit` on `/access`, which PVEAuditor on `/` grants. `GET /access/users/{user}/token/{name}` is **403** (it needs `User.Modify`).
- **Effective privileges:** `GET /access/permissions` with no parameters gives the token's own privileges. They're compared against PVEAuditor's set read from `GET /access/roles` on the same server, because the set differs by version: 9.x adds `VM.GuestAgent.Audit`.
- **Owner user:** `GET /access/permissions?userid={user}` gives the owner's privileges. They're identical, so even with privsep off the token couldn't do more. That's defense in depth: the owner user is an auditor too.
- **ACLs:** `GET /access/acl` lists every ACL entry on the system (with `Sys.Audit`): the group and the token each hold PVEAuditor on `/`, propagated.

**What PVEAuditor can't do (403 or filtered):**

| Want | Blocked by | v0.1 consequence |
|---|---|---|
| Pending updates (`/nodes/{n}/apt/update`) | `Sys.Modify` | No update counts from PVE read-only. Use the node agent later |
| VM IPs on **PVE 8.4** (`agent/network-get-interfaces`) | `VM.Monitor` (too powerful to ask for) | No VM addresses on 8.4. 9.x works. LXC addresses work on both |
| **Backup volumes** in storage content | `VM.Backup` + `Datastore.AllocateSpace` | Content lists show ISOs and templates only. Backups come from tasks and logs, or from PBS |
| Token details | `User.Modify` | Use `/access/users/{user}` instead |

**What PVEAuditor can see that users may not expect:**
- full task logs;
- guest configs (PVE masks `cipassword`);
- every ACL entry;
- cluster join info: node addresses and certificate fingerprints;
- disk serials;
- certificate PEMs, including the cluster CA.

The integration should **drop these at ingestion** (descriptions, serials) unless a widget needs them.

### Home Graph mapping

**Domain** = the cluster name when clustered, the node name when standalone (`/cluster/status`). VMIDs are unique per domain.

| PVE source | Kind | ID | Key attrs | Relations |
|---|---|---|---|---|
| instance | `integration` | `integration:proxmox-<instance>` | version, TLS mode, token tier, privsep | `monitors` the cluster/hosts |
| `/cluster/status` `type: cluster` | `cluster` | `pve:<cluster>` | quorate, node count, HA quorum | hosts `part_of` it |
| `/cluster/status` node + `/cluster/resources` node + `/nodes/{n}/status` | `host` | `pve:<node>` | ip, cpus, mem, pveversion, kernel, boot mode, `boot_at` (from uptime), cert expiry | `part_of` cluster; `net:<router> provides_network_for` it (inferred, below) |
| `/cluster/resources` qemu/lxc + `config` | `vm` / `lxc` | **`pve:<domain>/qemu/<vmid>`**, `pve:<domain>/lxc/<vmid>` | vmid, name, status, `onboot`, startup order, protection, `hastate`, `lock`, tags, ostype, IPs, MACs, `hostpci` | `runs_on` its current node; `stores_on` storage (from disk keys) or a passed-through `disk`; `backed_up_by` job |
| `/cluster/resources` storage + `/storage` | `storage` | shared: `pve:<domain>/storage/<id>`; local: `pve:<node>/storage/<id>` | plugintype, content, shared, used/total, active/enabled; PBS server/datastore/fingerprint; NFS server/export | local `part_of` host, shared `part_of` cluster; PBS storage `stores_on` `pbs:<datastore>`; NFS `stores_on` the exporting share (`/mnt/user/games` on jupiter → `unraid:jupiter/share/games`) |
| `/cluster/backup` | `backup_job` | `pve:<domain>/backup/<job-id>` | schedule, next run, selection (`vmid` list, `all`, pool), storage, last status, last OK | `stores_on` storage; guests `backed_up_by` it |
| `/nodes/{n}/disks/list` | `disk` | `pve:<node>/disk/<serial or wwn>` | model, type, size, health, wearout, use (LVM/ZFS/OSD) | `part_of` host |
| `/cluster/ha/status/current` | attrs | — | HA state on guests, master/LRM states on the cluster | — |

**Why the guest ID uses the domain, not the node** (the simulator uses `pve:mars-1/lxc/110` today):
- `pvedocker` is **HA-managed** on `mars`, with its rootfs on shared Ceph, so it can move to `mars-2`.
- With the node in the ID, a migration would change its identity.
- With the domain, only the `runs_on` edge changes.
- For standalone hosts the two forms are identical (`pve:pluto/qemu/103`).
- PVE normally refuses to join a node that has guests to a cluster, so a guest doesn't cross domains.

**Inferring the router:**
- OPNsense (`pve:pluto/qemu/103`) reports `10.0.11.1` through its guest agent, and every LXC with a static address uses `gw=10.0.11.1`.
- So "the guest whose address is everyone's gateway" gives `provides_network_for` without asking.
- The agent's `pfsync0`/`pflog0`/`enc0` interfaces also identify a pf firewall.

**Dedupe:**
- **One PVE domain seen through two instances** (e.g. both cluster nodes added as instances, which is a good thing for failover): same cluster CA, merge.
  - `GET /nodes/{n}/certificates/info` returns `pve-root-ca.pem`. Its fingerprint (and its random `OU` UUID) is shared by every node of a cluster.
  - It survives leaf renewals and is unique per cluster or standalone install.
  - Guests then dedupe on (domain CA, vmid), nodes on (domain CA, node name).
- **A node seen via its cluster and directly:**
  - `/cluster/config/join` lists every member's `pve_fp`;
  - `/nodes` gives `ssl_fingerprint` per node.
  - Both matched the served leaf on every instance. A direct instance with the same leaf (or the same CA plus node name) is the same node.
- **Name collisions:** two unrelated domains can share a name (`pve` is the installer default). Disambiguate the ID with a short CA-fingerprint suffix only when that happens.
- **The PBS datastore seen five times:**
  - The same PBS (one fingerprint, `10.0.11.120:8007`) appears as five storage IDs with five datastores (`pbs-unraid-pluto`, `jupiter-pbs`, `jupiter-pbs-mars`, `jupiter-pbs-pve.lab.example`, `pbs-unraid-io`).
  - All report **identical** capacity (42.3 of 46.0 TB, **91.9%**) because they share one filesystem on jupiter.
  - Dedupe capacity on (PBS fingerprint, identical totals) and let the PBS integration own the target. This is the "PBS datastore 92%" from [05 · Dogfood Home](../../docs/plan/05-dogfood-home.md).
- **Across integrations:**
  - LXC `interfaces` give `10.0.11.201` for `pvedocker`, which is the Docker endpoint's host, so `docker:pvedocker runs_on pve:mars/lxc/101` with no input.
  - Guest config MACs (`net0: virtio=…`) are exact keys that OPNsense DHCP/ARP also sees. That covers PVE 8.4 VMs with no readable IP.

### Health signals

| Resource | Signal | Source | Suggested rule |
|---|---|---|---|
| host | offline | `cluster/status` `online: 0` | down |
| host | memory | 9.x `memory.available`; 8.4 `used/total` | attention ≥ 90%, critical ≥ 98% |
| host | rootfs, swap | `nodes/{n}/status` | attention ≥ 90% |
| host | disk health | `disks/list` `health` not `PASSED`/`UNKNOWN`; `wearout` ≤ 10 | attention |
| host | certificate expiry | `certificates/info` `notafter` | attention < 30 days (leaves are 2-year certs) |
| host | failing `aptupdate` | `cluster/tasks` | quiet hint (minig5: `apt-get update` exit 100) |
| cluster | quorum lost | `cluster/status` `quorate: 0` | down, with blast radius |
| cluster | 2 nodes, no QDevice | `config/join` | quiet hint: HA can't fail over when a node dies (mars runs HA and Ceph) |
| guest | stopped but should run | `status` ≠ running and `onboot: 1` | attention. Running with `onboot: 0` is fine (jupiter-nas's LXC, the sandbox) |
| guest | `internal-error` | `status` | **down** (jupiter-nas VM 101, holding 21 GB) |
| guest | locked | `lock` (backup, snapshot, migrate) | info |
| guest | HA error | `hastate` error/fence | down |
| guest | no backup | `backup-info/not-backed-up` | backup coverage gap |
| backup job | last run failed | vzdump task `status`; per-guest from the log | attention, with the log line as the reason |
| backup job | target unreachable | task text `could not activate storage … No route to host` | root cause = the target, across instances |
| backup job | stays on the host | job storage is `local` (io) | quiet hint |
| storage | usage | `used_fraction` | attention ≥ 85%, critical ≥ 95% |
| storage | inactive | `active: 0` and `enabled: 1` | attention. Ignore disabled storage (pluto has one) |

### Metric keys (aligned with the simulator's names)

| Resource | Key | Source |
|---|---|---|
| host | `cpu.pct` | `/cluster/resources` `cpu` × 100. **`nodes/{n}/status` `cpu` reads 0** |
| host | `mem.pct` | `(total − available) / total` on 9.x, `used / total` on 8.4 |
| host | `swap.pct`, `rootfs.pct`, `load.1` | `nodes/{n}/status` |
| host | `net.rx_bps`, `net.tx_bps`, `iowait.pct` | `/cluster/metrics/export` (`net_in`/`net_out` counters, `cpu_iowait`), or RRD. Node entries in `/cluster/resources` have no net counters |
| host | `psi.cpu`, `psi.io`, `psi.mem` | 9.x RRD `pressure*` only |
| host | ~~`temp.c`~~ | **Not in the PVE API.** The simulator shows it for PVE hosts; it needs the node agent |
| guest | `cpu.pct` | `cpu` × 100 (fraction of the guest's own vCPUs) |
| guest | `mem.pct` | `mem / maxmem`, **clamped**: on 9.x `mem` is host-side and can exceed `maxmem` (iolinux: 53.6 GB of 52.4 GB) |
| guest | `disk.read_bps`, `disk.write_bps`, `net.rx_bps`, `net.tx_bps` | Δ of the cumulative `diskread`/`diskwrite`/`netin`/`netout` |
| guest | `disk.pct` | `disk / maxdisk`. **LXC only**: VMs report `disk: 0` |
| storage | `used.pct` | `used_fraction` × 100 |
| disk | `wearout.pct` | `disks/list` |
| backup job | `tasks.failed_24h`, `last_ok.age_h` | tasks |

### Fields the v0.1 tiles need

| Tile | Fields | Calls |
|---|---|---|
| **Fleet table** | per guest: `id`, `name`, `type`, `vmid`, `node`, `status`, `cpu`/`maxcpu`, `mem`/`maxmem`, `uptime`, `tags`, `template`, `hastate`, `lock`. Slow path: `onboot`, `ostype`, `hostpci`, primary IP. Per host: status, cpu, mem, guests running/total | `/cluster/resources` (fast); `config`, `interfaces` (slow) |
| **Host tile** | cpu, `memory` (`available` on 9.x), swap, rootfs, `loadavg`, uptime, `pveversion`, kernel, CPU model; storage list (used/total, type, shared); disk health; cert days left; sparkline backfill | `/cluster/resources`, `nodes/{n}/status`, `nodes/{n}/storage`, `rrddata?timeframe=hour`, `disks/list?skipsmart=1`, `certificates/info` |
| **Backups** | jobs (schedule, `next-run`, storage, selection, enabled); not-backed-up guests; recent vzdump tasks (`starttime`, `endtime`, `status`, `id` = vmid for single-guest runs); per-guest result from the log; target capacity (PBS 91.9%) | `/cluster/backup`, `backup-info/not-backed-up`, `/cluster/tasks`, `tasks/{upid}/log` (once per finished run), `/cluster/resources` storage |

### Polling cost (measured, one full capture)

| Instance | Requests | Non-200 | Server time (sum) | Wall | Decoded | On the wire |
|---|---|---|---|---|---|---|
| pluto | 40 | 3 | 10.2 s (one storage content call took 9 s) | 20.0 s | 988 kB | 145 kB |
| minig5 | 37 | 4 | 3.1 s | 12.2 s | 344 kB | 55 kB |
| mars | 50 | 3 | 4.7 s | 17.1 s | 1.9 MB | 314 kB |
| io | 39 | 3 | 1.2 s | 10.8 s | 999 kB | 145 kB |
| jupiter-nas | 36 | 3 | 5.5 s (`disks/list` 3.7 s) | 14.4 s | 375 kB | 49 kB |

Most of the bytes are RRD: PVE 9 `rrddata?timeframe=day` is **1440 points, ~670 kB decoded, ~120 kB on the wire, per node**. 8.4 returns 70 points (21 kB).

| Endpoint | Median latency | Max decoded / wire |
|---|---|---|
| `/cluster/resources` | 14 ms | 4.0 kB / 1.0 kB |
| `/cluster/tasks` | 6 ms | 11.2 kB / 2.1 kB |
| `/cluster/status` | 5–431 ms (median 164 ms); the slow ones paid for a new TLS handshake | 0.3 kB |
| `/cluster/metrics/export` | 61 ms | 7.7 kB / 1.2 kB |
| `/cluster/metrics/export?history=1` (10 min) | 97 ms | 460 kB / 38 kB |
| `/nodes/{n}/status` | 37 ms | 1.8 kB / 1.1 kB |
| `/nodes/{n}/storage` | **412 ms**: probes every storage, PBS and Ceph included | 1.2 kB |
| `/nodes/{n}/disks/list` | 262 ms (3.7 s with HDDs: it runs SMART) | 1.6 kB |
| `/nodes/{n}/rrddata?timeframe=hour` | 29 ms | 28 kB / 5.6 kB |
| guest `config` / `interfaces` | 4–83 ms | ≤ 9.6 kB |

**Polling plan:**

| Tier | Interval | Calls | Notes |
|---|---|---|---|
| Live | 15 s while watched (never faster than 10 s: pvestatd refreshes every 10 s) | `/cluster/resources` per domain | All live state for nodes, guests and storage |
| Recent | 60 s | `/cluster/tasks`, `/cluster/status` per domain; `/cluster/ha/status/current` if HA is configured | New failed tasks, finished backups, quorum |
| Slow | 5 min | `nodes/{n}/status` per node | `memory.available`, kernel, load |
| | 10 min, or when a config task appears in `/cluster/tasks` | `nodes/{n}/storage` per node; guest `config`, LXC `interfaces`, 9.x agent interfaces | Desired state, IPs, disk mapping |
| | 15 min, or when a vzdump task finishes | `/cluster/backup`, `backup-info/not-backed-up`; the finished task's log, **once** | Backup state |
| Rare | 1 h / on connect | `/version`, `/access/permissions`, `/access/users/{user}`, `certificates/info`, `disks/list?skipsmart=1` (untested here; SMART once a day, since it may wake sleeping HDDs) | Version adapter, privilege drift, cert expiry |
| Startup | once | `rrddata?timeframe=hour` per node (`day` on 8.4 only) | Sparkline backfill; the hub keeps its own history after that |

For the dogfood home (5 domains, 6 nodes, 11 guests) that's about **35 requests a minute, roughly 0.1 req/s per endpoint and ~50 kB/min on the wire**. Gentle enough for the router host.

`/cluster/metrics/export?history=1&start-time=<last poll>` (8.4 and 9.x) is an alternative live source: incremental 10 s samples of every metric in one call, at 4–8 kB decoded (~1 kB on the wire) per domain per poll. It's worth an ADR if sparklines need sub-poll resolution.

### TLS: trust on first use, per instance

**What the lab looks like:**
- All five serve `pve-ssl.pem` over TLS 1.3, signed by that domain's own **PVE Cluster Manager CA**.
- The CA is RSA-4096 and valid 10 years. The leaf is RSA-2048, valid **730 days**, with 343–562 days left.
- The server sends **the leaf only** (no chain).
- There's no custom `pveproxy-ssl.pem` and no ACME.

**What the spike does:**
- `VerifyConnection` runs on every handshake. It first tries the system roots with the URL host; a publicly trusted cert needs no pin.
- Otherwise it checks the leaf's SHA-256 against the instance's pin. With no pin and `-tofu`, it records one; otherwise it refuses.
- `InsecureSkipVerify` only hands chain checking over to that callback; it's never set without it.
- The first capture pinned io (`tofu-new`). The full capture matched io (`tofu-match`) and pinned the other four. `tlscheck` then matched all five, and with an empty pin store refused all five (`no-pin`).
- The pins live in `raw/tofu.json`, git-ignored.

**Observed:**
- **pveproxy reconnects often:** 18–26 handshakes for 36–50 requests. Expect a handshake on most polls. The pin check costs nothing measurable.
- `/nodes` `ssl_fingerprint` matched the served leaf on all five. `/cluster/config/join` `pve_fp` matched on mars, and lists mars-2's too.
- **`certificates/info` hands auditors the CA PEM.** On 4 of 5 the served leaf verifies against it with the URL host.
- **On minig5 it fails hostname verification**: the URL host isn't in the SAN (the SAN has the node's own name and IP). Users reach PVE by aliases and IPs all the time.

**Recommendations for the integration's TLS UX:**
1. **First connect:** show the leaf's SHA-256 in colon hex, the way PVE's GUI shows it (Node → System → Certificates), and ask the user to confirm it. Also offer "paste fingerprint" and "paste CA (`/etc/pve/pve-root-ca.pem`)".
2. **After confirming, pin the CA**, fetched over the pinned connection from `certificates/info`.
   - A CA pin survives leaf renewals (2-year leaves, renewed by PVE; the CA stays for 10 years).
   - It covers every node of a cluster, so failover endpoints and the `join` list need no second prompt.
   - Verify the chain against the pinned CA, but check the SAN against the **node name from `/cluster/status`**, not the URL host.
3. **On mismatch:** fail closed and raise a Repair, "pluto presented a different certificate", showing old and new fingerprints, with a re-pin after the user confirms. A leaf that changed but still chains to the pinned CA is a renewal: accept it quietly.
4. **Publicly trusted certs** (ACME on `pveproxy-ssl.pem`) skip pinning entirely. "Skip verification" exists only as a labeled, per-instance escape hatch, never as the default.

### Surprises

1. **Mixed versions: 8.4.19, 8.4.21, 9.0.9, 9.1.5, 9.2.20.** The shapes differ:
   - `/cluster/resources`: `network` entries replace `sdn` on 9.1+ (9.0 still has `sdn`); `memhost` is 9.x only;
   - node status: `memory.available` is 9.x only;
   - RRD: 9.x has 60 points/hour and 1440/day, plus `memavailable`, `arcsize` and PSI; 8.4 has 70 points with 13 fields;
   - PVEAuditor: 6 privileges on 8.4, 7 on 9.x;
   - the guest agent: readable on 9.x, 403 on 8.4.
2. **Backups fail on the same nights across hosts.**
   - pluto's 03:00 job and mars's 04:30 job failed on the **same 12 of the last 21 nights**, both with `could not activate storage … Can't connect to 10.0.11.120:8007 (No route to host)`. minig5's last weekly run failed too.
   - That's one cause, PBS on jupiter unreachable overnight, behind many failures: a textbook cross-instance root-cause hint.
3. **io's job has failed every night for 10 nights.**
   - VM 100 (Windows) and VM 200 (`iolinux`) both pass through GPU `0000:01:00`. Backing up the stopped VM 100 has to start it, and that fails ("PCI device already in use by VMID 200").
   - The job also writes to `local`, the node's own disk.
   - VM 101 isn't in any job.
4. **jupiter-nas VM 101 is in `internal-error`**, not "stopped". It holds 21 GB of the host's 33.5 GB, which is the 95% memory from the dogfood snapshot. Recent tasks show a `qmreboot` timeout, then `qmreset`. jupiter-nas has **no backup job at all**.
5. **An auditor can't see backups in storage content.** Read-only backup state needs vzdump task logs (per-guest results only exist there) or the PBS API.
6. **`nodes/{n}/status` reports `cpu: 0`**: use `/cluster/resources`. **VM `disk` is 0**: only LXCs report usage. **`mem` can exceed `maxmem`** on 9.x.
7. **`/nodes/{n}/storage` takes 200–700 ms**, and a single storage content call took 9 s on pluto: they probe live. `/cluster/resources` has the same capacity numbers, cached by pvestatd.
8. **Shared storage repeats per node** in `/cluster/resources` (mars: 6 storages, 12 rows).
9. **mars is a 2-node cluster with HA and Ceph** (`pvedocker` is an HA resource on RBD) and no QDevice. Losing either node loses quorum, so HA can't actually fail over.
10. **The router VM tells you the network:** gateway address, VLAN subnets, a tailscale interface. That's free `provides_network_for` and network-core tagging.
11. **Errors live in the status line**: `403 Permission check failed (/nodes/io, Sys.Modify)`. The body is `{"data": null}`, sometimes with `message`. `/cluster/config/join` returns **424** on standalone nodes; that means "standalone", not an error.
12. **`disks/list` runs SMART** (3.7 s on jupiter-nas's HDDs). Use `skipsmart=1` for inventory (untested here).
13. **Real VMIDs and placement differ from the simulator.**
    - pluto's OPNsense is qemu/103 (the simulator has 100);
    - `pvedocker` is lxc/101 (110);
    - minig5's LXC is 200;
    - io's VMs are 100/101/200 (201/202/203);
    - mars nodes have 16 GB (32 GB), io has 94 GiB (64 GB), jupiter-nas 32 GB (8 GB).

### Recommendations for v0.1

**Integration:**
- **Version adapters from the start** (8.4 and 9.x): pick by `/version` `release`, and test against both fixture sets.
- **Guest IDs on the domain**: `pve:<domain>/<type>/<vmid>`, with `runs_on` to the current node.
- **The cluster CA is the domain identity.** Instances that share it merge, and an instance with several endpoints fails over between them.
- **Live state from `/cluster/resources` only**, every 15 s while watched. Everything else follows the polling plan above.
- **Backups:**
  - jobs from `/cluster/backup`;
  - coverage from `not-backed-up`;
  - results from vzdump tasks, with per-guest lines parsed from the log once per finished task;
  - target capacity from storage.
  - Correlate "could not activate storage" across instances into one root cause.
- **Privilege check on connect and hourly:**
  - `/access/permissions` ⊆ the expected tier, where read = PVEAuditor's set from `/access/roles` on that server;
  - `privsep` from `/access/users/{user}`.
  - Flag anything else, especially `Sys.Modify`, `Sys.PowerMgmt`, `Sys.Console`, `Permissions.Modify`, `User.Modify`, `Realm.*`, `Datastore.Allocate*`, `VM.Allocate`, `VM.Config.*`, `VM.Console`, `VM.Migrate`, `VM.Monitor` and `VM.GuestAgent.Unrestricted`/`FileRead`/`FileWrite`.
  - `privsep: 0` → "this token has all of `<user>`'s rights". A `root@pam` token → labeled admin-equivalent.
- **Drop at ingestion** what no widget needs: descriptions, disk serials, certificate PEMs (after pinning), task logs beyond the parsed result.

**Token recipe** (the generator should emit this per instance; `@pve` avoids a Linux user):

```sh
pveum user add chadash@pve --comment "Cha Dash (read-only)"
pveum acl modify / --users chadash@pve --roles PVEAuditor
pveum user token add chadash@pve dash --privsep 1 --comment "Cha Dash"
pveum acl modify / --tokens 'chadash@pve!dash' --roles PVEAuditor
```

**With `--privsep 1`, the token's rights are the intersection of the token's ACL and the user's.** Both ACL lines are needed, which is why the owner's own recipe has a group ACL and a token ACL.

The **control upgrade** (v0.2) is a custom role, e.g. `ChaDashControl` = `VM.PowerMgmt VM.Snapshot`, granted to both the user and the token, offered per instance from the Inbox.
- Listing backups would also take `VM.Backup` + `Datastore.AllocateSpace` on the backup storage. Prefer a PBS token with `DatastoreAudit` instead.
- Don't recommend `VM.Monitor` (8.4) or `Sys.Modify` for read features.

## Files

| File | What |
|---|---|
| `main.go` | Commands and flags |
| `env.go` | Env-file parsing (values never printed) |
| `client.go` | GET-only client, TOFU `VerifyConnection`, scrubbed errors |
| `capture.go` | The endpoint walk |
| `mapper.go` | Real → fake mapping, built in memory from env and raw captures |
| `sanitize.go` | Key rules, trimming, `_index.json`, `_tls.json` |
| `leak.go` | The scanner, the canary self-test, the `leakcheck` command |
| `report.go` | Tables from the sanitized fixtures |
| `leak_test.go` | Canary, mapper stability and generic-pattern tests; the full real-value test runs with `PVE_ENV` and `PVE_RAW` |
