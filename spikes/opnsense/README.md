# Spike 4.5 · OPNsense: traffic stream, interfaces, health

Throwaway code that answers one question: **how should the v0.1 OPNsense integration read the router**? Is there a push stream for WAN throughput, what does polling cost, and what is the smallest API privilege that drives the Router widget? It ran read-only against the dogfood router (an OPNsense VM on **pluto**) on 2026-10-03.

Not product code. The fixtures it recorded are in [`testdata/integrations/opnsense/`](../../testdata/integrations/opnsense/README.md). The Frigate half of the spike is in [`../frigate`](../frigate/README.md).

## How it works

| Command | What it does |
|---|---|
| `probe` | Runs the endpoint catalogue ([`endpoints.go`](endpoints.go)) one request at a time with 1.5 s gaps, and writes raw bodies to a directory **outside the repo** |
| `stream` | Opens an SSE endpoint, records each event's arrival time, size and data, and **always closes within 55 s** |
| `shape` | Prints a capture's structure without its strings, so the sanitizer could be written without reading real addresses or names |
| `sanitize` | Turns raw captures into fixtures (see the fixture README for the rules). A shared, git-ignored netmap outside the repo keeps addresses consistent with the Frigate fixtures |
| `needles` | Collects every real value from the raw captures and `.env.local` into a 0600 file outside the repo, for the leak check |

### Safety

- **Read-only, enforced in code.** The transport refuses any method other than `GET`/`HEAD` and any origin other than `OPNSENSE_URL` ([`client_test.go`](client_test.go)).
- **Allow-listed endpoints.** Only `GET` endpoints whose controller action has no state-changing `isPost()` branch were called; each was checked against the opnsense/core 26.7.5 source.
- **Gentle.** Requests are sequential (one connection) with 15 s timeouts and no redirect following.
- **TLS.** Trust-on-first-use with a pinned fingerprint when the certificate doesn't chain. That code path went unused, because the router is reached over plain `http://`.
- **No leaks in logs.** Credentials come from `--env` at runtime and are never logged. Error text has the host and any IPv4 address scrubbed out.

```bash
cd spikes/opnsense
go run . probe  --env ../../../.env.local --out $SCRATCH/opnsense-raw
go run . stream --env ../../../.env.local --out $SCRATCH/opnsense-raw --path /api/diagnostics/traffic/stream/1 --window 20s
go run . stream --env ../../../.env.local --out $SCRATCH/opnsense-raw --path /api/diagnostics/traffic/stream/5 --window 32s
go run . probe  --env ../../../.env.local --out $SCRATCH/opnsense-raw --paths /api/diagnostics/traffic/top/lan
go run . needles  --raw $SCRATCH/opnsense-raw --env ../../../.env.local --out $SCRATCH/opnsense.needles
go run . sanitize --raw $SCRATCH/opnsense-raw --env ../../../.env.local --netmap $SCRATCH/netmap.needles --out ../../testdata/integrations/opnsense
LEAKCHECK_NEEDLES=$SCRATCH/opnsense.needles LEAKCHECK_ENV=../../../.env.local go test -run LeakCheck -v .
```

## Findings

**Verdict: build the Router widget on the SSE traffic stream while it's on screen, plus one cheap counter poll per minute in the background.** The stream exists, is cheap and is accurate. The key Homepage uses is already least-privilege, but it can't see version, memory or gateways. One more read-only privilege (*Lobby: Dashboard*) covers all of v0.1 except gateway latency and loss.

### 1. The API key is least-privilege, not admin

| Evidence | Result |
|---|---|
| 26 catalogue GETs | `200` only for `diagnostics/traffic/*`, `diagnostics/activity/*` and `core/menu/tree`. **`403 {"status":403,"message":"Forbidden"}`** for firmware, system status, every dashboard endpoint (system information, resources, time, temperature, disk, CPU), interfaces overview, netstat, RRD health, gateways, ARP, Kea and dnsmasq leases, pf statistics and services |
| `GET /api/core/menu/tree` (filtered by the caller's ACL) | Only *Lobby › Logout*, *Reporting › Traffic*, *System › Diagnostics › Activity* |

So the key's user holds exactly **Reporting: Traffic** (`page-status-trafficgraph`) and **Diagnostics: System Activity** (`page-diagnostics-system-activity`). Those are the two privileges Homepage's OPNsense widget documents.

- **Version.** It can't be read with these privileges: firmware status and system information are both `403`. The SSE endpoint exists, so the router runs **≥ 24.7**, where `traffic/stream` first appears; the ACL name *Reporting: Traffic* dates from 25.1. A limited key also can't tell a missing endpoint from a forbidden one, because both return `403`.
- **Auth failures.** A wrong key would get `401` (per the docs; not exercised, so the spike made no failed logins on the router). `401` means *Needs repair*; `403` means *Permission-limited*.

### 2. The traffic stream: SSE, verified

`GET /api/diagnostics/traffic/stream/{interval}` returns `text/event-stream`. The backend is `configd interface stream traffic`, which runs `traffic_stats.php <interval>` in a loop.

| Measured | 1 s interval (20 s window) | 5 s interval (32 s window) |
|---|---|---|
| First event | 213 ms after connect, a **partial** interval | 111 ms, partial |
| Gap between events | 1005–1050 ms | 5004–5049 ms |
| Payload | 938–986 B per event (8 interfaces) | 934–1003 B |
| Bandwidth | ≈ 1 kB/s ≈ 3.5 MB/h | ≈ 0.2 kB/s ≈ 0.7 MB/h |
| Server-side close | none within the window; the spike closed it | none |

- **Framing.** Each message is `event: message`, then `data: {"interfaces":{"<if>":{"inbytes","outbytes","inpkts","outpkts","inerrs","outerrs","collisions","name"},…},"time":<float>}`, then a blank line. There are no `id:` fields, so reconnects can't resume and we just reopen.
- **Values are deltas for the interval, not counters.** `rate = delta × 8 / (time_n − time_{n−1})`. In the 20 s sample the WAN ran 0.1–1.7 Mb/s down and 0.1–1.5 Mb/s up.
- **Cost on the router.** The stream response sets no cookie, but each open stream holds a PHP worker and a configd stream process for as long as it runs.
- **CPU has the same mechanism.** `GET /api/diagnostics/cpu_usage/stream` is SSE too, and needs *Lobby: Dashboard*. It returned `403` here.

### 3. What polling costs

| Endpoint | Size | Latency | Notes |
|---|---|---|---|
| `diagnostics/traffic/interface` | 5.8 kB | 106–115 ms | Absolute counters since attach for 8 interfaces, plus `link state`, MTU, driver. All values are strings; keys have spaces |
| `diagnostics/activity/get_activity` | **72.9 kB** | **2.1 s** | Runs top(1) in thread mode: 368 rows including every process's arguments. Homepage polls this for CPU and memory. **Avoid it** |
| `diagnostics/traffic/top/lan` | 2.8 kB | 2.8 s | Runs iftop for 2 s, plus reverse DNS; returns client addresses and names. On demand only (the Large widget's "top clients", v0.3) |
| Any `403` | 36 B | ~40 ms | Cheap, and safe to use as a capability probe |

Every non-stream response sets a `PHPSESSID` cookie even under basic auth. The integration should ignore it and keep sending basic auth.

### 4. Mapping to the Home Graph and the Router widget

| Graph item | Source (privilege) | Recorded? |
|---|---|---|
| `router:opnsense` state, `boot_at` | top(1) header `up 13+08:52:19` (Activity); `system_time.uptime`/`boottime` (Lobby) | top only |
| `router:opnsense` `version`, update available | `system_information.versions`/`updates` (Lobby) | no (`403`) |
| `metric router/cpu.pct` | top header `100 − idle` (Activity), or the `cpu_usage/stream` SSE (Lobby) | top only: 2.9% |
| `metric router/mem.*` | top header `Mem: 162M Active … 14G Free` (≈ 16 GiB VM; ARC 497M), or `system_resources.memory` (Lobby) | top only |
| `metric router/temp.c` | `system_temperature[]` (Lobby). Probably empty on a VM | no |
| `wan` resource, `state` up/down | `traffic/interface` → `interfaces.wan["link state"] == "2"` (Traffic). Only physical link; an ISP outage with link up needs gateways | yes |
| `metric wan/rx.mbps`, `tx.mbps` (sparkline) | the stream's `wan` deltas (Traffic) | yes |
| `metric wan/rx_bytes`, `tx_bytes` (totals) | `traffic/interface` `bytes received`/`transmitted`. **Since boot** (864.7 GB / 423.1 GB here); 30-day totals must be accumulated by the hub | yes |
| `net_device` per interface (lan, 5 VLANs, Tailscale) | `traffic/interface` (Traffic): driver, `vlan0.100`, `tailscale0`, user labels | yes |
| Gateway latency, loss, status (Attention / Down) | `routes/gateway/status` → `status` (`none`, `loss`, `delay`, `delay+loss`, `down`, `force_down`), `loss`, `delay`, `stddev` (System: Gateways) | no (`403`) |
| `net.devices` count | `diagnostics/interface/get_arp` row count (Diagnostics: ARP Table). Count only: rows hold MACs and hostnames | no (`403`) |
| DHCP lease count | `kea/leases4/search` or `dnsmasq/leases/search` (GET works; no `isPost` check) | no (`403`) |
| Firewall state-table size | `diagnostics/firewall/pf_states` → `current`/`limit` (Lobby) | no (`403`) |
| Services up/down | `core/service/search` (Status: Services) | no (`403`) |

**Router widget:**

| Widget size | Data |
|---|---|
| Glyph, Strip, Small "↓ now" | The latest stream delta |
| Sparkline | Stream samples bucketed by the hub, or 60 s counter diffs when nobody watches |
| "30-day ↓/↑ totals" | Hub-accumulated counter diffs. Treat a counter that goes down as a reset |
| CPU | Needs Lobby (or the expensive top) |

**Widget states:**

| State | Trigger |
|---|---|
| Healthy | Link up; gateway `none` if permitted |
| Attention | Gateway `loss`/`delay`, or CPU pressure |
| Down | Link state ≠ 2, or gateway `down` |
| Needs repair | `401` |
| Permission-limited | `403` on the endpoints a size needs. Show the totals and say which privilege adds the rest |

### 5. Streaming vs polling: the v0.1 strategy

1. **While a Router widget is visible:** open **one** stream per instance at 5 s, shared by every widget and tab. Use 1 s only when a "now" readout is focused. The hub's coalescer caps diffs at 4/s anyway. Close the stream when nobody watches; it holds a router worker.
2. **Always, in the background:** poll `traffic/interface` every 60 s (6 kB, about 0.1 s). That gives link state, the 1-minute history behind sparklines, and totals.
3. **Every 5 minutes:** poll gateways, if the privilege is granted (dpinger already averages). Version: on connect and hourly.
4. **Never on a timer:** top talkers, ARP or lease *contents*, `get_activity`.

### 6. Least-privilege recommendation

Create a dedicated API user, `chadash`, with **no group**, and give it:

| Privilege (26.7 name) | ACL id | Grants | Why |
|---|---|---|---|
| **Reporting: Traffic** | `page-status-trafficgraph` | `api/diagnostics/traffic/*` | Counters, the SSE stream, top talkers. Read-only |
| **Lobby: Dashboard** | `page-system-login-logout` | `api/diagnostics/system/system_{information,resources,time,temperature,disk,mbuf,swap}`, `api/diagnostics/cpu_usage/*`, `api/diagnostics/firewall/pf_states`, `api/core/dashboard/*` | Version, memory, uptime, temperature, CPU stream, state-table size. Read-only, apart from saving that user's own dashboard layout |
| *optional* **System: Gateways** | `page-system-gateways` | `api/routes/gateway/status` **and `api/routing/settings/*`** | Gateway latency and loss. **It can also edit gateways**: there's no read-only gateway-status privilege in 26.7. The wizard should label it "can change gateways" and leave it off by default |
| *optional* Diagnostics: ARP Table | `page-diagnostics-arptable` | `api/diagnostics/interface/get_arp*`, `search_arp*` | Device count. Read-only, but returns every client's MAC and hostname; the hub stores only the count |

Then **remove *Diagnostics: System Activity*.** Lobby replaces it at a fraction of the cost.

Don't grant any of these. Each read privilege also grants writes:

| Privilege | Also grants |
|---|---|
| *Status: Services* | Start, stop and restart |
| *Status: Interfaces* | `reload_interface` |
| *System: Firmware* | Update, upgrade and reboot |
| *Services: DHCP: Kea(v4)* or *Dnsmasq DNS/DHCP: Settings* | Full DHCP configuration |
| *System: Static Routes* | Routes (`api/routes/*`) |

### 7. Not exercised: these need POST

They read only, per the source, but take POST:

| Endpoint | Note |
|---|---|
| `diagnostics/firewall/query_states` | GET returns an empty page |
| `diagnostics/firewall/query_pf_top` | |
| `core/firmware/status` as POST | POST runs a synchronous mirror probe; GET only reads the cached result |

Never called, because they change state: firmware `check`/`update`/`upgrade`/`reboot`/`poweroff`, `core/system/{reboot,halt,dismiss_status}`, `core/service/{start,stop,restart}`, `interfaces/overview/reload_interface`, `diagnostics/interface/{flush_arp,del_route,carp_status}`, `diagnostics/firewall/{kill_states,flush_states,flush_sources,del_state}`, `kea/leases4/del_lease`, `diagnostics/systemhealth/{reconfigure,del_rrd}`.

Also never called, though GET: `core/backup/*` (config with secrets), `core/firmware/get_firmware_config` (may hold a subscription key), `auth/user/*` (usernames).

### 8. Surprises

- **The Homepage key isn't admin.** That's good news, and it turns the dogfood home's "upgrade" story around: what Cha Dash should *add* is one read privilege.
- **Snake_case matters.** 26.x ACL patterns are written in snake_case, and some are exact matches (`api/diagnostics/system/system_information`). A camelCase URL (`systemInformation`) fails an exact pattern even for a user who has the privilege. Always call snake_case paths.
- **The "WAN totals" are since boot,** not 30-day. Homepage shows the boot counters (852/418 GB in the dogfood snapshot; 864.7/423.1 GB today, 13 days after boot).
- **VLANs report `"unknown type 135"`**, Tailscale reports `PPP`/`tun0`, and virtio reports a 10 Gbit/s line rate. Don't derive utilisation % from it.
- **The stream's first event is a short interval.** Plotting it as a rate draws a spike or a dip.
- **Plain HTTP:** the key and secret cross the LAN in clear on every request. That's the Inbox hint the importer already plans.

### 9. Where the Demo Home differs

| Sim | Reality |
|---|---|
| `mem.mib` max 8192 | The VM has ≈ 16 GiB (14G free; 162M active, 1.1G wired, 497M ARC) |
| One `wan` resource | 8 interfaces: WAN, LAN, 5 VLANs (one is the camera VLAN), Tailscale |
| `rx.gb_30d`/`tx.gb_30d` from the API | Not an API value. The hub must accumulate; the API gives since-boot counters |
| `boot_at` 20–45 days ago | 13 days. Frigate's uptime is also ≈ 13.4 days, which hints at a shared power event, and the graph could correlate it |
| Evening WAN ~45 Mb/s | 0.1–1.7 Mb/s at 19:47 on a quiet evening. The sim's shape is fine; its level is high |

## Recommendations for v0.1

1. **Ship the stream-plus-poll design above.** Model the stream as the instance's `Subscribe`, started and stopped by the coordinator's interest tracking.
2. **Setup wizard.**
   - Generate the privilege recipe in section 6 with a copy button.
   - Probe `system_information`, and on `403` show "Permission-limited: add *Lobby: Dashboard* for version, memory and CPU".
   - Offer gateways as an opt-in with the write warning.
3. **Adapters.**
   - Key on version when readable; otherwise feature-detect via `403`/`200`.
   - Parse string-typed numeric fields.
   - Accept `unknown type N` for interface types.
4. **Never request `get_activity` on a timer.** Parse its header only, as a fallback when Lobby is missing.
5. **Credential hint:** plain-HTTP OPNsense. Recommend HTTPS with TOFU pinning; the client code here already supports it.
6. **Re-run this spike** once *Lobby: Dashboard* (and optionally *System: Gateways*) is granted, to record `system_*`, `cpu_usage/stream` and `routes/gateway/status` fixtures.
