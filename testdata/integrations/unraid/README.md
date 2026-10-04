# Unraid API fixture: dogfood home (jupiter)

Sanitized responses from jupiter's Unraid API, captured read-only on 2026-10-04 by [spike 4.2](../../../spikes/unraid/README.md):
- **Versions:** Unraid **7.3.2**, API **4.37.2**, over plain http.
- **Seeds:** the v0.1 Unraid integration (host, array, disks, pools, shares, containers, VMs, notifications), the correlator's container join with the [Docker fixture](../docker/README.md), the disk join on serial, and the desired-state rule for Unraid template containers.

**Two runs, two keys:**
1. **First run (03:08 UTC)**, with a key that **wasn't** the one the consent URL asked for: `DISK:CREATE_ANY` instead of `READ_ANY`, plus 8 extra read scopes. `disks` failed, and `logFiles` and `server` succeeded. Everything not listed below comes from this run, which was captured alongside the Docker fixture.
2. **Second run (03:45 UTC)**, with the key issued **through the consent URL**: exactly the 8 requested `read_any` grants. It supplies the key-dependent reads: `disks.json`, `disks-spin.json`, `me.json`, `scope/*.json` and `capture-consent-key.json`.

## Files (`jupiter/`)

| File | GraphQL operation | Notes |
|---|---|---|
| `capture.json` | | First run's summary: status, error codes, bytes and ms per operation; granted vs requested permissions (the first key); root-field counts; the subscription window |
| `capture-consent-key.json` | | Second run's summary, in the same shape, plus `key_label_as_requested` and `reruns` (the disks rerun). Its first `disks` read timed out at 20 s |
| `info.json` | `info { os cpu system versions machineId time }` | `hostname` is the nickname. `machineId` and `system.uuid` are fakes |
| `network.json` | `info { networkInterfaces }` | 49 interfaces: `br0` (LAN), `docker0`, compose bridges `br-<id>`, `tailscale1`, ~40 `veth*`, `vnet0` (VM). Took **2.6 s**, so poll it rarely |
| `metrics.json` | `metrics { cpu memory }` | |
| `array.json` | `array { state capacity parityCheckStatus boot parities disks caches }` | 1 parity, 7 data disks (xfs), 7 pool devices in 5 pools, USB boot flash |
| `parity-history.json` | `parityHistory` | 111 checks |
| `disks.json` | `disks { … smartStatus isSpinning temperature partitions }` | Second run. 16 disks: the same devices as `array.json`, **keyed by the same fake serials** (`id` is `<server id>:<serial>`, the flash included). SMART OK on all but the flash (UNKNOWN). `isSpinning` is false on every disk although all were spinning (API bug, see the spike README) |
| `disks-spin.json` | `array { … name isSpinning temp }`, before and after each `disks` read | Second run. `timed_out_read`: 9 devices spun down before, the parity awake after. `rerun`: all 16 spinning. Reading `disks` wakes disks |
| `docker.json` | `docker { containers { … autoStart autoStartOrder autoStartWait … } }` | 74 containers, the same 74 as the Docker fixture, **with the same (fake) ids** |
| `docker-networks.json` | `docker { networks }` | 8 networks |
| `vms.json` | `vms { domains }` | 2 VMs. Ids are fakes |
| `shares.json` | `shares` | 19 shares |
| `notifications.json` | `notifications { overview list(UNREAD, 20) warningsAndAlerts }` | 572 unread (548 info, 23 warning, 1 alert) |
| `me.json` | `me { name description roles permissions }` | Second run: the consent key. Name and description as requested, no roles, exactly the 8 requested `READ_ANY` grants |
| `scope/*.json` | Reads outside the requested scopes | Second run. `ApiKeys`, `Flash`, `Vars`, `Logs`, `Servers`, `Cloud`, `Network`, `Online`, `Services`: FORBIDDEN. `Ups`: INTERNAL_SERVER_ERROR (the resolver runs without any permission check, with either key; see the spike README) |
| `subscriptions.json` | 8 subscriptions on one graphql-ws socket for 57 s | First 6 events per subscription. CPU emits every ~1.1 s and memory every ~2.2 s. `arraySubscription` emits every 10 s, but every payload is an error. The rest sent nothing |
| `schema/root-*.json` | `__type(name: "Query" \| "Mutation" \| "Subscription")` | 58 queries, 45 mutations, 17 subscriptions |
| `schema/introspection-refused.json` | `IntrospectionQuery` | `400 INTROSPECTION_DISABLED`: the sandbox is off |

The root fields match the published schema at the same tag exactly: [`api/generated-schema.graphql` @ v4.37.2](https://raw.githubusercontent.com/unraid/api/v4.37.2/api/generated-schema.graphql) (sha256 `ae82121994d36886865dff29a507cf1e0848f2d9933a2aed46a91bbf3014cd09`). The schema isn't copied here; take the types from there.

## How it was sanitized

Applied by `spikes/unraid sanitize`. It uses the shared `labkit` scrubber, with the same conventions as the [Traefik](../traefik/README.md) and [Docker](../docker/README.md) fixtures.

| Original | Fixture |
|---|---|
| The **server id** prefixing every id (`<64 hex>:<local id>`) | Re-hashed with a private salt, consistently everywhere |
| **Container ids** | **The Docker fixture's fake ids, matched by container name**, so `docker.json` and `../docker/jupiter/containers.json` join on id (74/74). The 12-character short forms follow |
| Network, endpoint and volume ids, compose hashes, digests: every other 64-hex value | Re-hashed |
| **Disk serials**, inside the array's disk ids (`<model>_<serial>`, the flash `<vendor>_<model>_<serial>-0:0`), as the disks read's key (`<server id>:<serial>`, `serialNum`) and in notification texts | Fakes that keep the format (the flash's stays 16 digits); the model is kept. One fake per serial everywhere, so `disks.json` joins `array.json` on serial |
| `machineId`, `system.uuid`, VM UUIDs, the API key's id | Fakes that keep the format |
| LAN addresses | `10.0.11.x` (jupiter `10.0.11.120`). Docker bridges `172.(16+n).c.d` → `10.(100+n).c.d`. The tailnet address → `203.0.113.x` |
| MAC addresses | `02:42:0a:00:xx:yy` |
| Personal domains (`homepage.href`, `homepage.widget.url`) | `*.family.example` / `*.lab.example`, **pinned to the Docker fixture's choices** |
| A personal GitHub handle (image path, icon URLs) | `homelab-user`, as in the Docker fixture |
| Secret-looking labels (`homepage.widget.key`, `.password`) | Fakes in the same format; `homepage.widget.username` becomes `admin` |
| Emails (image `maintainer` labels) | `redacted@example.com` |
| Two share names that look personal | `archive_a`, `archive_b`, everywhere (share list, ids, mount paths) |
| Share and disk `comment`s | `REDACTED` |

**Kept:**
- the host nickname (jupiter);
- VM names (`jupiter_ubuntu`, `Windows 10`);
- container names;
- generic share names (`appdata`, `media`, `backup`, …);
- pool names;
- folder names under shares;
- hardware models (CPU, board, disk models);
- public icon and project URLs.

The [Docker fixture](../docker/README.md) uses the same replacement (`archive_a`) in its mount paths, so mount paths line up between the two fixtures.

## Leak check

`spikes/unraid` has two tests:
- **`TestFixturesLookSanitized`** always runs. It runs labkit's canary self-test, then the address, MAC and Host() invariants, and checks that every comment is redacted and that nothing key-shaped sits next to `api_key`.
- **`TestLeakCheck`** runs with `LEAKCHECK_VALUES=<private list>`:
  - **The list:** 691 real values seen during both runs, including **both API keys** (the first key and the consent key), both keys' ids, all **16 disk serials** and full disk ids, the server id and all 229 ids, 45 addresses, 89 MACs, domains, emails, the machine id and UUIDs, label secrets, the renamed share names, and every `.env.local` value. Values that are the same on every machine (the all-zero MAC, Docker's default bridge gateways) are left off.
  - **Sanity check first:** it refuses a list without a key, the serials, the full disk ids or the renamed share names. Then it plants the canary, **every API key on the list**, and a real serial, full disk id, address and MAC in a temp file, and must find all of them.
  - **The real scan:** every fixture under `testdata/integrations/`, not just this one, plus the spike's own files. The Docker fixture shares this host's container ids and share names.

Both passed when the fixture was committed, again after the Docker fixture's share-name fix, and again after the consent-key run. `spikes/docker-proxy`'s own leak test passes with this list too.

## What a consumer should get from it

Run `go run . analyze` in `spikes/unraid` for the full table. In short:

- **Identity:** the PrefixedID's local part **is** Docker's container id. That gives the same 74 containers through the Unraid API and through the socket proxy, joined by id and by name. The API folds Docker's `created` into `EXITED` (9 containers), so prefer Docker's state when both sources are present.
- **Desired state:**
  - **Template containers** (`net.unraid.docker.managed=dockerman`, 63 of them): desired state = `autoStart` (36 are on the list, all running). Docker's restart policy is `no` for 30 of those 36.
  - **Compose Manager containers** (11): never on the autostart list (Compose Manager keeps its own, which the API doesn't expose). Fall back to Docker's restart policy.
  - **Running but on no autostart list, restart policy `no`** (2): they won't come back after a reboot. That's a quiet hint, not an alarm.
- **Pools** aren't first-class objects in the API. Pool devices appear in `array.caches`, named `<pool>`, `<pool>2`, …; only the first member carries `fsType` and `fs*`.
- **Units:** `ArrayDisk.size` is in KiB. `fsSize`/`fsUsed`/`fsFree` and `capacity.kilobytes` are 1000-byte units, and the capacity values are strings. Compute percentages within one family.
- **Temperatures** are `null` while a disk is spun down (`isSpinning: false`). Show "spun down", not a stale number.
- **Disks:** join `disks.json` to the array devices on the serial: the last `_` segment of the array id (minus the flash's `-0:0`) equals `disks.serialNum` and the local part of `disks.id`. `disks` adds the model, vendor, firmware and SMART status. Read it on connect only: **it wakes spun-down disks** (`disks-spin.json`). Take temperature and spin state from the array, since `disks.isSpinning` is always false on this version.
- **Shares** report `size: 0` and `cache: null` on this version, so share → pool isn't available. Use `free`/`used`, and get container → share edges from mount paths (`/mnt/user/<share>/…`).
- **Notifications:**
  - `warningsAndAlerts` holds the ones worth surfacing ("Unclean shutdown detected", "pending ecc cnt is 1", "Cache_array disk is high on usage (71%)");
  - most of the rest is hourly Mover Tuning noise;
  - `link` is a webGUI path (`/Main`, `/Settings/Scheduler`), which makes a good deep link.
