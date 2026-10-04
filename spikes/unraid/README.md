# Spike 4.2 · Unraid API plumbing (read-only)

Throwaway code that answers one question: **can Cha Dash read an Unraid server through the Unraid API (GraphQL) the way [ADR-0010](../../docs/adr/0010-unraid-stays-unraid.md) assumes?** That means:
- one-click connect through the `ApiKeyAuthorize` consent flow;
- reads for host, array, disks, containers, VMs, shares and notifications;
- live updates over subscriptions.

It runs against the dogfood server **jupiter** and nothing else.

Not product code. The findings go into the notes below; the real integration is written fresh. The sanitized capture is [`testdata/integrations/unraid/`](../../testdata/integrations/unraid/README.md).

**Verdict: yes, with four conditions.**
- **Require API ≥ 4.37.4.** jupiter runs 4.37.2, which predates Unraid's default-deny authorization fix.
- **Check what the key really grants** (`me`) instead of trusting the request.
- **Poll most things.** Only the CPU and memory subscriptions are worth using on this version.
- **Never poll `disks`.** It runs `smartctl -a` on every disk and **wakes spun-down disks** (§11). Read it on connect for serials and SMART; take temperature and spin state from `array`.

The Unraid API and the Docker socket proxy describe the **same 74 containers, joinable by exact id**. Unraid's `autoStart` supplies the desired state that Docker's restart policy can't (spike 4.4).

## Safety rails (enforced in code)

| Rail | Where |
|---|---|
| **Read-only.** Every document is parsed (gqlparser) before it's sent, and the parse fails closed. A `mutation` anywhere in a document refuses the whole document. HTTP may send only queries; the socket may send only subscriptions | `guard.go`, tests in `guard_test.go` and `client_test.go` (a refused mutation never reaches the transport) |
| **Gentle.** Requests are sequential and spaced (`-gap`, default 500 ms), each with a timeout (`-timeout`, default 20 s). A subscription socket lives **≤ 60 s**, close included (57 s here) | `client.go`, `ws.go` |
| **The key goes nowhere else.** Redirects are never followed. The key is sent only on `POST /graphql` and in the socket's `connection_init`. The probe never sends it | `client.go`, `probe.go` |
| **Nothing identifying is printed.** All console output goes through the `Redactor`: the key, the URL, the host, any IPv4 or MAC, `api_key=` parameters and 64-hex tokens | `redact.go`, `redact_test.go` |
| **Raw captures stay out of the repo.** Raw captures, the sanitizer state, the private words file and the leak list live outside the repo (0600). `raw/` and `*.raw.*` are git-ignored here as a backstop | `.gitignore` |

Credentials come from the repo-root `.env.local`: `UNRAID_JUPITER_URL` and `UNRAID_JUPITER_API_KEY`. The program walks up from the working directory to find it, so it works from a worktree too. Only lines matching `^[A-Z0-9_]+=` are read.

No mutation was sent at any point. The guard refused none either, because none was ever written. One read did have a physical side effect: the first `disks` read woke 9 spun-down disks (§11).

## Run it

```bash
cd spikes/unraid
LAB=/somewhere/outside/the/repo
go test ./...                                    # guard, redaction, consent URL, env, fixture invariants, the join
go build -o bin/unraid-spike .
./bin/unraid-spike probe                         # no key: status codes only
./bin/unraid-spike -o $LAB/consent-url.txt consent-url   # writes the consent URL, never prints it
./bin/unraid-spike -out $LAB/raw run             # with the key: reads, scope checks, one 60 s subscription window
./bin/unraid-spike -out $LAB/raw2 -timeout 150s -only SpinBeforeDisks,Disks,SpinAfterDisks run   # re-run named reads
./bin/unraid-spike -out $LAB/raw [-overlay $LAB/raw2] -state $LAB/state.json -words $LAB/words.tsv \
                   -leak $LAB/leak-values.json sanitize   # → ../../testdata/integrations/unraid
LEAKCHECK_VALUES=$LAB/leak-values.json go test -run 'Leak|Sanitized' -v ./...
./bin/unraid-spike analyze                       # container join with the Docker fixture, desired state
```

`words.tsv` holds `real<TAB>replacement` lines for names the scrubber can't recognize: personal-looking share names.

`-overlay` takes a second run's **key-dependent reads** (disks, `me`, the scope checks, the spin-state reads) and keeps everything else from the first run. The first run's containers were captured alongside the Docker fixture, so the two fixtures keep joining. The same `state.json` keeps every fake identical: re-sanitizing the first run alone reproduces its fixtures byte for byte. The leak list is merged, so values from earlier runs (the first API key) stay on it.

## Findings

Captured on 2026-10-04 (UTC) from jupiter: **Unraid 7.3.2, API 4.37.2**, over plain http, in two runs:
1. **First key** (`dash`, 03:08 UTC): everything except disks. Its grants were wrong (§3).
2. **Consent key** (`Cha Dash spike`, 03:45 UTC): the full run again. Only the key-dependent reads went into the fixtures: disks, `me`, the scope checks (`capture-consent-key.json`).

### 1. Availability, transport, versions
- **Built in from Unraid 7.2.** Earlier releases need the Unraid Connect plugin. The API (NestJS + Apollo) sits behind the webGUI's nginx and is released on its own cadence. jupiter's API (4.37.2) is three releases behind the latest (4.37.5).
- **Endpoint:** `POST /graphql` on the webGUI's own origin, with the key in `x-api-key`. A CSRF token is needed only for cookie (webGUI session) auth.
- **Errors come back as HTTP 200** with `errors[].extensions.code`: `UNAUTHENTICATED`, `FORBIDDEN`, `INTERNAL_SERVER_ERROR`. Map the codes, not the HTTP status. The exception is introspection (400).
- **Latency** (sequential, LAN):

  | Read | Time | Size |
  |---|---|---|
  | `docker.containers` | 176 ms | 174 KB for 74 containers |
  | `notifications` | 291 ms | |
  | `info` | 307 ms | |
  | `info.networkInterfaces` | **2.6 s** (1.5 s in the second run) | 49 interfaces |
  | `disks` (second run) | **> 20 s, timed out** with 9 devices spun down; **1.4 s** with all 16 spinning | 6 KB for 16 disks |
  | Everything else | under 25 ms | |

  A refused `vars` read took **5.6 s** before answering FORBIDDEN in the first run, but 6 ms in the second. So it isn't inherent to the refusal; the cause is unknown.

### 2. Introspection
- **Off unless the GraphQL sandbox is on** (Settings → Management Access → Developer Options), **with or without a key**. Any request named `IntrospectionQuery` or containing `{ __schema {` gets `400 INTROSPECTION_DISABLED`.
- **`__type(name: …)` is deliberately allowed**, even without a key. The spike used it only for the root types:

  | Root type | Live | Published schema at v4.37.2 |
  |---|---|---|
  | Query | 58 fields | identical |
  | Mutation | 45 fields | identical |
  | Subscription | 17 fields | identical |

  The published schema is [`api/generated-schema.graphql`](https://raw.githubusercontent.com/unraid/api/v4.37.2/api/generated-schema.graphql). Every field our queries use exists there.
- **For the product:** pin queries to a schema per API version, and treat `GRAPHQL_VALIDATION_FAILED` as "upstream changed" (a Repair). Runtime discovery isn't available.

### 3. The key: what was granted vs what was asked

**The consent key (second run) is exactly what the consent URL asked for.** The maintainer issued it from `unraid-spike consent-url`. The consent redirect worked: the key came through the consent flow and arrived on the blocked-port localhost callback (§9). `me` confirms nothing was edited on the way:

| | Requested (consent URL) | Granted (`me`) | Match |
|---|---|---|---|
| Name | `Cha Dash spike` | `Cha Dash spike` | ✓ |
| Description | the spike's description | the same | ✓ |
| `info` · `array` · `disk` · `docker` · `vms` · `share` · `notifications` · `me` | `read_any` each | `READ_ANY` each | ✓ 8 of 8 |
| Anything else | – | nothing | ✓ no extras |
| Roles | none | none | ✓ |

The run diffs this automatically: `missing vs request: – · extra vs request: –`, recorded in `capture-consent-key.json` (`granted_missing`, `granted_extra`, `key_label_as_requested`).

**The first key didn't come out of our consent request.** It was named `dash` ("for dash"): either it was created under Settings → API Keys, or the consent dialog was edited. `me` showed:

| | Scopes |
|---|---|
| Requested (8) | `info`, `array`, `disk`, `docker`, `vms`, `share`, `notifications`, `me` · all `read_any` |
| Granted (16) | the same, **except `disk:create_any` instead of `disk:read_any`**, plus `cloud`, `logs`, `network`, `online`, `os`, `servers`, `services`, `welcome` (all `read_any`) |
| Effect | `disks` → **FORBIDDEN**, so no SMART or serial data. `logFiles` and `server` **succeeded**, where the spike expected a refusal |

`DISK:CREATE_ANY` gates no operation in this schema; the disk resolvers only read. It was still a write grant on a key meant to be read-only. The first key is gone from `.env.local`; it stays on the private leak list.

**For the product:** the first key is exactly the case the "Test connection" step must catch.
- Read `me` and diff it against the capability set.
- Flag any `CREATE`/`UPDATE`/`DELETE` grant on a read-only connection.
- Turn missing reads into Repairs ("Disks need DISK: Read").

### 4. Scope boundaries, and an authorization gap in API 4.37.2

| Out-of-scope read | Resource | First key | Consent key |
|---|---|---|---|
| `apiKeys` | `API_KEY` | FORBIDDEN ✓ | FORBIDDEN ✓ |
| `flash` | `FLASH` | FORBIDDEN ✓ | FORBIDDEN ✓ |
| `vars` | `VARS` | FORBIDDEN ✓ | FORBIDDEN ✓ |
| `logFiles` | `LOGS` | Data (the key had `logs`) | **FORBIDDEN ✓** |
| `server` | `SERVERS` | Data (the key had `servers`) | **FORBIDDEN ✓** |
| `cloud` | `CLOUD` | not probed | FORBIDDEN ✓ |
| `network` (Connect plugin) | `NETWORK` | not probed | FORBIDDEN ✓ |
| `online` | `ONLINE` | not probed | FORBIDDEN ✓ |
| `services` | `SERVICES` | not probed | FORBIDDEN ✓ |
| `upsDevices` | `CONFIG`, in theory | `INTERNAL_SERVER_ERROR` | **`INTERNAL_SERVER_ERROR` again: the resolver still runs** |
| `upsUpdates` subscription | `CONFIG`, in theory | Accepted silently; nothing in 57 s | The same |

- **`OS` and `WELCOME` gate no resolver on 4.37.2** (checked against every resolver at the tag), so there's nothing to probe for them.
- **`cloud` is probed only when `me` shows the key lacks it.** Its resolver makes the server check its connection to Unraid's cloud (DNS and HTTP from jupiter), so the spike won't trigger it with a key that holds `CLOUD`.

**Why the UPS reads got through (with both keys):** in v4.37.2 the UPS resolver carries **no permission metadata at all**, covering `upsDevices`, `upsConfiguration`, the `configureUps` mutation and `upsUpdates`. That version allows any handler without metadata.
- Unraid fixed this in **v4.37.4** ([unraid/api#2074](https://github.com/unraid/api/pull/2074), "require explicit GraphQL authorization"). It made the guard default-deny.
- The fix also lists notification mutations, OIDC settings and client secrets, sensitive settings fields and flash-backup initiation as previously reachable by authenticated read-only keys.
- **Consequence:** on API < 4.37.4, a "read-only" key isn't read-only.

The spike never called a mutation, so none of this was exercised beyond the UPS read, once per key. The consent key, with exactly the 8 requested grants, still reached the UPS resolver: the gap doesn't depend on what the key holds.

**For the product:**
- The integration should read `info.versions.core.api`.
- Below 4.37.4, raise a security notice in the Inbox: "Update Unraid: this API version lets any key change UPS settings and notifications".
- **jupiter should be updated.**

### 5. Subscriptions vs polling
One graphql-ws socket (`graphql-transport-ws`) carried 8 subscriptions for 57 s. The handshake returned 101 and the ack came in 1 ms.

| Subscription | Events | Notes |
|---|---|---|
| `systemMetricsCpu` | 52, every ~1.1 s | Server-side poller (1 s) that runs only while subscribed |
| `systemMetricsMemory` | 25, every ~2.2 s | Poller (2 s) |
| `arraySubscription` | 6, every 10 s, **all errors** | "Cannot return null for non-nullable field Subscription.arraySubscription". **Broken on 4.37.2** |
| `dockerContainerStats` | 0 | Backed by a `docker stats` stream on the server; nothing arrived in 57 s |
| `parityHistorySubscription`, `notificationAdded`, `notificationsOverview` | 0 | Event-driven; nothing happened |
| `upsUpdates` | 0 | Out of scope, but accepted (see §4) |

The second run (consent key) repeated the window with the same pattern: CPU 49 events, memory 24, `arraySubscription` 5 errors, nothing else.

**Recommendation for v0.1:**
- **Subscribe** to CPU and memory **only while a host-metrics tile is on screen**.
- **Poll everything else**, at the Coordinator's jittered intervals:
  - array + parity status every 60 s (14 ms);
  - containers every 60 s (176 ms, 174 KB; it can trigger the server's template sync, see §8);
  - VMs every 60 s;
  - notifications overview every 60 s, plus `warningsAndAlerts` on change;
  - shares every 5 min;
  - network interfaces on connect and hourly;
  - parity history daily;
  - **disks never on a timer:** on connect and on demand only (§11). Temperatures and spin state come with the array poll.
- **Container state changes** come faster and cheaper from Docker events through the socket proxy (spike 4.4) than from polling Unraid.
- **Re-test** `arraySubscription` and `dockerContainerStats` on ≥ 4.37.4 before relying on them.

### 6. Identity correlation: Unraid ↔ Docker socket proxy
- **Host:** the Unraid host's `br0` address is the Docker endpoint's address (`tcp://10.0.11.120:2375` in the fixtures), and also the host in `webUiUrl` and `lanIpPorts`. So `docker:jupiter` **runs_on** `unraid:jupiter`, matched on network identity (correlator key 2). The proxy refuses `/info`, so Docker can't name its host; the address is the link.
- **Instance identity:** every Unraid id is a PrefixedID `<64-hex server id>:<local id>`, with the same prefix on every object. It's stable per server, so a re-added server (same prefix) can be recognized as the same one. One exception: the boot flash's id in `array.boot` (`<vendor>_<model>_<serial>-0:0`) has no prefix; in `disks` the flash is `<server id>:<serial>` like every other disk. Treat its serial as sensitive: the flash ties to the license.
- **Containers:** **the local part of the container's PrefixedID is Docker's full container id.** Joined against the Docker fixture: **74/74 by id, 74/74 by name** (`TestContainersJoinWithDockerFixture`). Use id first (correlator key 1) and name as the fallback.
- **State:** the API folds Docker's `created` into `EXITED` (9 containers; its enum has no `CREATED`). When both sources are present, take state from Docker.
- **Who adds what:**

  | Source | Contributes |
  |---|---|
  | Unraid only | `autoStart`, `autoStartOrder`, `autoStartWait`, `templatePath` (template containers), `isOrphaned`, a resolved `webUiUrl`, `iconUrl`, `lanIpPorts` |
  | Docker only | health, restart count and policy, OOM/exit code, env (redact), live events |
  | Both | ports, labels, mounts, networks |

### 7. Desired state from autostart (the spike 4.4 question)

**Where it lives:**
- `DockerContainer.autoStart` (Boolean), `autoStartOrder` (Int: the line number in the list) and `autoStartWait` (seconds), on `docker { containers }`, under scope `docker:read_any`.
- The API reads Unraid's autostart file: one line per container, `<name> [wait]`, in start order. It matches by the container's primary name (`Names[0]`). The file is absent while the array is stopped.

Joined with the Docker fixture (`go run . analyze`):

| Manager | autoStart | Docker | Restart policy | Count | Desired → verdict |
|---|---|---|---|---|---|
| dockerman | true | running | `no` | 30 | running → ok |
| dockerman | true | running | `unless-stopped` / `always` | 5 / 1 | running → ok |
| dockerman | false | running | `unless-stopped` | 1 | running → ok (Docker restarts it after a reboot) |
| dockerman | false | running | `no` | **2** | **running now, but nothing starts it after a reboot** → quiet hint |
| dockerman | false | exited / created | `no` | 15 / 9 | stopped on purpose |
| composeman | false | running | `always` / `unless-stopped` | 4 / 1 | running → ok |
| composeman | false | exited | `no` / `unless-stopped` | 4 / 2 | stopped on purpose |

**The rule:**
- For template containers (`net.unraid.docker.managed=dockerman`), desired state = `autoStart`, OR a Docker restart policy of `always`/`unless-stopped`.
- Compose Manager containers are never on Unraid's list. The plugin keeps its own per-stack autostart, which the API doesn't expose, so they fall back to Docker's restart policy. An exited `unless-stopped` container counts as stopped on purpose, because Docker would have restarted it after a crash.

**On jupiter:**
- No container is in "should run but doesn't" today.
- 30 of the 36 autostart containers rely on Unraid alone, with restart policy `no`.

### 8. Graph mapping

| Unraid API | Graph resource (sim ids) | Key | Edges |
|---|---|---|---|
| `info` + `metrics` | `unraid:jupiter` (host) | Server id prefix; `machineId`; `br0` address | `provides_network_for` from the router (OPNsense) |
| `array` | `unraid:jupiter/array` | | `part_of` host. Attributes: state, `capacity` (sum of the data disks' fs), `parityCheckStatus`, `parityHistory` |
| `array.parities` / `array.disks` | `unraid:jupiter/disk/<name>` | **Serial**: the last `_` segment of the disk id = `disks.serialNum` = the local part of `disks.id` (§11) | `part_of` array. Attributes: role, fs, size, temp (null while spun down), `isSpinning`, `numErrors`; from `disks`: model, vendor, firmware, SMART status |
| `array.caches` | `unraid:jupiter/pool/<pool>`, plus its member disks | Pool name = the first member's name; members are `<pool>`, `<pool>2`, … Member disks keyed by serial, as above | member disk `part_of` pool, pool `part_of` host. Only the first member carries `fsType`/`fs*` |
| `array.boot` | the flash device | Serial, as above. Treat as secret | `part_of` host |
| `disks` | **Not resources of their own.** Enriches the disk resources above | `serialNum` | Read on connect, never polled (§11) |
| `shares` | `unraid:jupiter/share/<name>` | Name | `include`/`exclude` list disks. **Share → pool isn't exposed on 4.37.2** (`cache: null`, `size: 0`) |
| `docker.containers` | Merged into `docker:jupiter/<name>` | **Docker container id** (the PrefixedID local part) | `runs_on` engine. `stores_on` share, from mounts (`/mnt/user/<share>/…`; `/mnt/user0/…` is the array-only view). Desired state from §7 |
| `docker.networks` | Engine networks | | |
| `vms.domains` | `vm:jupiter/<uuid>` | libvirt UUID (PrefixedID local part) | `runs_on` host. `vnet*` interfaces show running VMs |
| `notifications` | **Not resources.** Inbox / timeline items | Notification id (a file name with an epoch) | Importance ALERT → needs attention; WARNING → Inbox; INFO → timeline. `link` is a webGUI path, so a deep link |

**Units and shapes:**
- `ArrayDisk.size` is KiB, while `fsSize`/`fsUsed`/`fsFree` and `capacity.kilobytes` are 1000-byte units; capacity values come as **strings**.
- Percentages within one family are safe.
- Containers carry `networkSettings` (IPs, MACs) and labels (`homepage.*` secrets included), so **redact on the hub before anything leaves it**.
- `docker.containers` can trigger Unraid's template-mapping sync on the server (`syncMissingContainers` → `scanTemplates`). It's harmless, but reads aren't strictly side-effect-free.

**jupiter vs the Demo Home simulator** (for whoever next touches `sim/`):

| | jupiter | Simulator |
|---|---|---|
| Array | 7 data disks + 1 parity | 6 data |
| Pools | 5 pools with 7 devices (zfs and xfs, NVMe and SATA) | 2 |
| Shares | 19 | 3 |
| Containers | 74 (63 templates, 11 Compose Manager) | 11 |
| VMs | 2 | – |
| CPU | 14 cores / 20 threads | |
| Memory | ~64 GB | |
| Unraid | 7.3.2 | |

Worth scripting: a "running without autostart" hint, a spun-down disk with no temperature, and an `arraySubscription` that errors.

### 9. The consent flow (`ApiKeyAuthorize`), from phase 1

```
<webGUI>/Tools/ApiKeyAuthorize?name=…&description=…&scopes=a:read_any,b:read_any&redirect_uri=…&state=…
```

- **Path:** `/Tools/ApiKeyAuthorize` is what Unraid's own URL generator builds. The docs show `/ApiKeyAuthorize`, which the webGUI also routes. The user must be logged in; otherwise they get a 302 to `/login`.
- **`name` becomes the key's name:** **letters, numbers and spaces only**, so "Cha Dash (spike)" fails at creation.
- **Scopes are `resource:action`:**
  - Actions: `create_any`, `read_any`, `update_any`, `delete_any`, `_own` variants, or `*`.
  - Grouping works: `a+b:read_any`.
  - Roles: `role:admin|viewer|guest|connect`.
  - **The docs' `docker:read` silently decodes to nothing** on the consent page; use `read_any`.
  - `role:viewer` reads every resource except `API_KEY`: logs, flash, vars, registration and config included.
- **`redirect_uri` must be `https://…` or exactly `http://localhost…`.** There's no automatic redirect: the user clicks "Send Key to <name>", which goes to `redirect_uri?api_key=<key>&state=<state>`, so the key travels in the query string. Cancel sends `?error=access_denied`. Without a `redirect_uri`, the page only displays the key ("Create API Key from Template").
- **The user can edit everything before creating the key**, and §3 shows it happens (the first key). Hence `me`.
- **The spike's no-server callback:** `http://localhost:1/cha-dash-spike/unraid-callback`. Port 1 is on every browser's blocked-port list, so the redirect stops at an error page with the key readable in the address bar, and never connects anywhere.
- **The redirect works end to end.** The consent key came through it: the consent page created a key with the requested name, description and exactly the 8 requested grants, and "Send Key" delivered it to the callback (§3).

**For the product:** a hub on plain `http://<LAN-IP>` **can't receive the callback**. One-click connect needs one of:
- the hub behind HTTPS;
- the copy-paste fallback (no `redirect_uri`).

The callback handler must vault the key immediately and redirect to a clean URL, so it doesn't sit in history or proxy logs.

### 10. Unauthenticated probe (phase 1)

| Check | Status |
|---|---|
| `GET /` | 302 → `/Main` |
| `GET /Tools/ApiKeyAuthorize` | 302 → `/login` |
| `GET /graphql` (sandbox page) | 403 `SANDBOX_DISABLED` |
| `POST {__typename}` | 200 |
| `POST {isSSOEnabled}` (public field) | 200 |
| Introspection | 400 `INTROSPECTION_DISABLED` |
| `__type(name:"Query")` | 200 (no key needed) |
| `info { versions }` | 200 `UNAUTHENTICATED` |
| graphql-ws | 101, acknowledged without a key |

### 11. Disks (consent key)

**Mapping.** `disks` returns the same 16 devices as `array` (boot, parity, 7 data, 7 pool members), and each joins on its serial:

| Source | Disk key | Example (fixture fakes) |
|---|---|---|
| `array.*.id` | `<server id>:<model>_<serial>` | `…:WDC_WD181KFGX-68AFPN0_5KBNMTCF` |
| `array.boot.id` | `<vendor>_<model>_<serial>-0:0`, **no prefix** | `Samsung_Flash_Drive_8231230990224417-0:0` |
| `disks.id` | `<server id>:<serial>`, **the flash included** | `…:5KBNMTCF` |
| `disks.serialNum` | `<serial>` | `5KBNMTCF` |

- The serial is the longest alphanumeric run in the array id's last `_` segment (`diskSerial`). jupiter's serials are 8 to 20 characters. **The flash's is 16 digits**, all numeric, with the `-0:0` suffix to strip.
- **16/16 join by serial, and by device** (`/dev/sdb` ↔ `sdb`). `TestDisksJoinArrayBySerial` checks it on the fixtures.
- **The fixtures key disks by sanitized serial:** the same fake serial in `array.json`, `disks.json` and the notification texts. All 16 serials and all 16 full disk ids are on the leak list.

| Array slot | Device | Model (`disks.name`) | `disks.interfaceType` | emhttp `transport` | SMART | Temp °C |
|---|---|---|---|---|---|---|
| flash | sdm | Flash Drive (Samsung) | USB | usb | UNKNOWN | – |
| parity | sdb | WDC WD181KFGX-68AFPN0 | SAS | ata | OK | 33 |
| disk1–disk7 | sdg, sda, sdd, sde, sdc, sdf, sdh | WD 8–18 TB, one Toshiba 8 TB | SAS | ata | OK | 33–46 |
| cache_appnvme, cache_appnvme2 (zfs) | nvme2n1, nvme0n1 | Corsair MP600 PRO LPX, CT2000P3PSSD8 | PCIE | nvme | OK | 47, 45 |
| cache_array, cache_array2 (zfs) | sdi, sdj | TEAM T2532TB | SATA | ata | OK | 42, 38 |
| cache_disks, cache_mx_beta (xfs) | sdk, sdl | CT1000MX500SSD1, Crucial_CT525MX300SSD1 | SATA | ata | OK | 29, 32 |
| cache_nvme_beta (xfs) | nvme1n1 | XPG GAMMIX S11 Pro | PCIE | nvme | OK | 44 |

**What `disks` adds over `array`:** the model, vendor, firmware and SMART status. Temperatures matched `array.*.temp` exactly once the disks were awake, so they add nothing.

**`disks` wakes spun-down disks.** The resolver always runs systeminformation's `diskLayout()`, whatever fields are asked. In the pinned version (5.31.7), that runs `smartctl -a -j` on every disk **without `-n standby`**, one disk after another. The `temperature` field adds `smartctl -n standby -A` per disk, which wouldn't wake anything on its own. On jupiter (`disks-spin.json`, emhttp's view):

| | Spun down before | `disks` | Spun down after |
|---|---|---|---|
| First read | 9: parity, disk1, disk3–7, cache_disks, cache_mx_beta | **Timed out at 20 s** (the spike's limit) | parity awake within the 20 s |
| Rerun, ~3 min later | **none**: the server evidently finished the first read after the client gave up, waking all 9 | 1.4 s | none |

- **For the product:** read `disks` on connect and on demand ("refresh SMART"), never on a timer. A dashboard polling it would keep every disk in the array spinning. Take temperature and spin state from `array` (`temp` is null while spun down, which is the right display).
- The first read was this spike's doing. Unraid spins the disks down again after its own spin-down delay.

**Quirks on 4.37.2:**
- **`disks.isSpinning` is always false.** The resolver matches emhttp's id (`<model>_<serial>`) against the bare serial, which never matches. Use `array.*.isSpinning`.
- **`interfaceType` says SAS for SATA disks behind a SAS HBA** (lsblk's transport), where emhttp says `ata`.
- **`vendor` is a guess:** `ATA` for the TEAM SSDs, empty for two NVMe drives.
- **The flash reports `type: HD`**, SMART `UNKNOWN` and no temperature.
- **ZFS pool members list no partitions:** lsblk's `zfs_member` isn't in the API's filesystem mapping, so it's dropped. The parity has none either (no filesystem).
- `size` is in bytes here, unlike `ArrayDisk.size` (KiB).

## The scope recipe for users

**Read-only, v0.1.** Request it through the consent URL, then verify it with `me`. On jupiter, this exact request produced exactly these 8 grants (§3):

```
info:read_any, array:read_any, disk:read_any, docker:read_any, vms:read_any,
share:read_any, notifications:read_any, me:read_any
```

| Scope | Gives | Why |
|---|---|---|
| `info` | Host identity, versions, network interfaces, metrics, CPU/memory subscriptions | Host card, `host.metrics`, the version check (§4) |
| `array` | Array, parity status and history, pool devices | `storage.pools`, the array card |
| `disk` | Physical disks: serial, model, firmware, SMART status (spin state is broken, §11) | `storage.disks`; serials are the correlator's exact key. Read on connect only |
| `docker` | Containers incl. autostart, networks, stats | `containers`, desired state, the join with the socket proxy |
| `vms` | VM list and state | `compute.guests` |
| `share` | Shares, free/used | `storage.shares`, backup coverage |
| `notifications` | Unraid's own notifications | The Inbox |
| `me` | The key's own grants | Test connection: diff against what's needed |

Not requested:
- **UPS** (`config:read_any`, which also opens settings, SSO/OIDC and plugins). Offer it as an opt-in, or use NUT.
- **`role:viewer`**.
- **Anything with create, update or delete.** That waits for v0.2 control, as a separate, explicit upgrade.

**Minimum API version: 4.37.4.**

## Surprises
1. **"Read-only" isn't, on API < 4.37.4.** Handlers without permission metadata were open to any key; UPS is the one we observed (§4).
2. **The first key differed from every expectation:** a write grant on DISK, the DISK read missing, and 8 extra resources (§3). The consent key matched the request exactly.
3. **Reading `disks` wakes spun-down disks,** and keeps working server-side after the client times out (§11).
4. **Introspection is a sandbox toggle, not an auth decision,** and `__type` works without a key.
5. **`arraySubscription` is broken on 4.37.2:** every payload is an error. `dockerContainerStats` stayed silent. So is `disks.isSpinning`: always false (§11).
6. **The API hides Docker's `created` state** behind `EXITED`.
7. **Units are mixed:** KiB for array disk size, bytes in `disks`, 1000-byte units for filesystem sizes, strings for capacity.
8. **Pools have no object of their own.** Membership comes from device names.
9. **Shares have no pool or size** on this version.
10. **The flash's id is unprefixed in `array` and carries its serial** (prefixed in `disks`). Notification texts carry disk serials too, so sanitizers must look beyond structured fields. The first sanitizer pass missed the flash; the leak check's needle list was fixed and re-run.
11. **A refused `vars` read took 5.6 s** in the first run (6 ms in the second). A refusal isn't necessarily cheap.
12. **The docs' scope examples don't parse; key names reject punctuation; redirects must be https or localhost** (phase 1).

## Recommendations
1. **Require API ≥ 4.37.4** for the Unraid integration; below that, raise an Inbox security notice. **Update jupiter.**
2. **Test connection = `me` + version.** Diff the grants against the capability set, flag write grants on a read connection, and turn missing reads into Repairs.
3. **Poll, don't subscribe,** except CPU/memory while visible (§5). Take container lifecycle from Docker events via the socket proxy.
4. **Merge containers by Docker id** (the PrefixedID local part), falling back to the name. Take state and health from Docker, desired state from Unraid (§7).
5. **One-click connect needs the hub on HTTPS.** Otherwise use copy-paste. Vault the key on arrival and strip it from the URL. The consent flow itself is proven: requested scopes in, exactly those grants out (§3, §9).
6. **Read `disks` on connect and on demand, never on a timer** (§11). Key disks by serial; take temperature and spin state from `array`.
