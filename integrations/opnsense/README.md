# OPNsense (`opnsense`)

Reads an OPNsense router's WAN: link state and throughput, live over OPNsense's traffic stream (server-sent events) while someone watches and from a light counter poll otherwise, plus 30-day totals that the hub accumulates.

**Tier:** silver (repairs, grants read back, push while watched, fixture tests). **Capabilities:** `net.router`, `net.wan`. **Secrets:** `key`, `secret`.

Written from [spike 4.5](../../spikes/opnsense/README.md) and tested against its capture of the dogfood router, [`testdata/integrations/opnsense/`](../../testdata/integrations/opnsense/README.md) (OPNsense 26.7).

## Config

```json
{"url": "http://10.0.11.1", "tls": {"mode": "verify"}, "key": "${secret:…/key}", "secret": "${secret:…/secret}"}
```

| Key | Meaning |
|---|---|
| `url` | The router's address, `http://` or `https://` |
| `tls` | `{"mode": "verify" \| "pin" \| "tofu" \| "insecure", "pin": "<sha256>"}` |
| `key`, `secret` | The API key pair, sent as HTTP basic auth on every request. The Homepage importer moves Homepage's `username` and `password` here |
| `wan` | Optional, default `wan`: the interface to report as the WAN, by its OPNsense identifier (`opt1` for a second uplink, say) |

`url`, `tls`, `key` and `secret` are the keys in [07 · v0.1](../../docs/plan/07-v0.1.md#instance-config-keys). `wan` is an addition, like the checks integration's per-target keys. Homepage's widget `wan` option stays a tile option in the importer.

**Least privilege.** Create an API user (say `chadash`) with no group and only **Reporting: Traffic** (`page-status-trafficgraph`). That's all this version reads. It's also read-only, and the key Homepage uses already has it. Homepage's other privilege, *Diagnostics: System Activity*, isn't needed. Prefer `https://` with `tofu`: over plain HTTP, the key and secret cross the LAN in clear on every request, and the framework warns about it.

## What it reads

| Endpoint | Privilege | When | Cost (spike) |
|---|---|---|---|
| `GET /api/diagnostics/traffic/interface` | Reporting: Traffic | Every minute, watched or not | 5.8 kB, ~0.11 s |
| `GET /api/diagnostics/traffic/stream/5` (SSE) | Reporting: Traffic | **Only while someone watches**, through `Subscribe` | ~1 kB every 5 s, ~0.7 MB an hour |
| `GET /api/core/menu/tree` | any authenticated user | Test connection only | 1.7 kB, ~0.05 s |

**Poll plan.** The coordinator syncs every 60 s whether or not anyone watches (`Poll.Idle` is the interval too). That's the spike's cheap background counter poll; it keeps link state, the sparkline's history and the totals current. Everything else is avoided: `diagnostics/activity/get_activity` (Homepage's CPU and memory source) costs 73 kB and 2.1 s per call, and top talkers run iftop.

**The stream, only while watched.** An open stream holds a PHP worker and a configd process on the router, so it runs only inside `Subscribe`, which the runtime starts while someone watches and cancels when nobody does. One stream per instance, at a 5 s interval. Following the timing captures (`traffic_stream_5s_timing.json`): the first event arrives 0.1–0.2 s after connecting and covers a partial interval; after that, one event every 5.00–5.05 s. So:
- **Rates use the payload's own clock:** `rate = Δbytes × 8 / (time_n − time_{n−1})`. The first event only sets the clock; it's never plotted.
- **A watchdog:** no first event within 15 s, or no event for 15 s (three intervals) after that, ends the stream as stalled. The runtime reopens it with backoff, and the poll covers the rate meanwhile.
- **A window:** each stream is closed and reopened at once every 10 minutes (about 120 kB). Responses then stay small, and the stream's client timeout stays bounded.
- **Who owns the rate.** While stream samples are fresh (less than 15 s old), the minute poll leaves `rx.mbps` and `tx.mbps` alone. Otherwise it publishes the average rate since its last read.

OPNsense sets a `PHPSESSID` cookie even under basic auth; it's ignored, and every request sends basic auth again.

## What it publishes

| Resource | Kind | Attrs | Health |
|---|---|---|---|
| `opnsense:<instance id>/router` | `router`, named after the instance | `platform: opnsense`, `interfaces` (how many the router reports), `ip` (when `url` is an address); label `chadash.role: network-core`; a link to the web UI; caps `net.router`, `net.wan` | `ok`, state `running` |
| `opnsense:<instance id>/wan` | `wan`, named after the interface's description (`WAN`) | `interface` (`wan`), `device` (`vtnet1`), `totals_since` (while the totals cover less than 30 days) | From `link state`: `2` is `ok` (state `online`), `1` is `down` (state `error`), anything else `unknown` |

**Relations:** the instance `monitors` the router; the WAN is `part_of` the router. The router is the instance's service resource.

**Metrics** on the WAN, with the Demo Home's keys and scales:

| Key | Unit | From |
|---|---|---|
| `rx.mbps`, `tx.mbps` | Mb/s | The stream's deltas while watched; the counter poll's average over the last minute otherwise. Unknown after the first read, across a restart, a counter reset or a gap of more than ten minutes |
| `rx.gb_30d`, `tx.gb_30d` | GB (10⁹ bytes) | The hub's totals over the last 30 UTC days, today included. Unknown until a second counter read |

**Not like the Demo Home:** there's no `cpu.pct`, `mem.mib`, `boot_at` or LAN subnet on the router. They need *Lobby: Dashboard*, and nothing was captured with it yet (see Limits).

## The 30-day totals

OPNsense's counters run from boot (864.7 GB down after 13 days on the dogfood router), not over 30 days. So the hub keeps the totals itself, **persisted in the instance's state** (`integration.State`, the `integration_state` table from migration `00003`).

- **How it counts.** Each counter read adds what passed since the last read to the UTC day it passed in, spread in proportion to time when a gap crosses midnight. The state holds the per-day bytes for 31 days and the last read's counters and time, about 1.5 kB as JSON.
- **Restarts.** The first read after a hub restart diffs against the saved counters, so traffic that passed while the hub was down is still counted, spread over the gap. The state is saved every 5 minutes, when the day changes, and when the instance stops. The saved days and counters always match each other, so a crash between saves loses nothing: the next read counts those bytes from the older counters.
- **Router reboots.** A counter that goes down was reset, so it counts from zero. Bytes between the last read and the reboot are lost: at most a minute's worth while the hub runs, more if the router reboots while the hub is down. If the hub is down long enough for the new counter to pass the saved one, the reset goes unnoticed and the totals undercount.
- **Starting out.** Totals cover from the first read (`totals_since`) until 30 days have passed, so the first month shows a partial total. OPNsense doesn't say when this key's counters started (boot time needs *Lobby: Dashboard*), so the since-boot counters can't seed it.
- **Changing `wan`** starts the totals over.

**Why persisted, not in memory.** The tile says "last 30 days". In memory, every hub update or restart would reset it to zero and show a number that's quietly wrong for a month, worse than Homepage's since-boot counters. Persisting needed the smallest framework addition: a per-instance key-value store behind `Deps.State`, in a separate commit with migration `00003` and tests.

## Test connection

1. `GET /api/core/menu/tree`. OPNsense filters the menu by the caller's privileges, so its pages are the grants. *Reporting: Traffic*, *Diagnostics: System Activity*, *Lobby: Dashboard* and *Lobby: License* are read grants. Any other page is a **write** grant, because most OPNsense pages are settings pages and the spike found read-sounding privileges that also write (*Status: Services* can restart services). The framework then warns about write grants on this read-only connection. *Logout* is every user's and isn't listed. A refused menu (`forbidden_403.json`) gives a `grants_unknown` notice instead.
2. `GET /api/diagnostics/traffic/interface`, which must work and must have the `wan` interface.

The version isn't reported: firmware and system information need privileges this integration doesn't ask for.

## Repairs

| Situation | Result |
|---|---|
| 401 (wrong key or secret) | Repair, `auth`, field `key` |
| 403 (`{"status":403,"message":"Forbidden"}`) on the counters or the stream | Repair, `missing_permission`, field `key`, naming *Reporting: Traffic* |
| No such `wan` interface | Config error (`invalid_config`, field `wan`), listing the interfaces there are |
| 404, or an answer that isn't JSON | Repair, `api_changed`, field `url`: not OPNsense's API |
| Anything else | Transient; the coordinator backs off |

A limited key can't tell a missing endpoint from a forbidden one: OPNsense answers `403` for both.

## Limits

- **Only the WAN.** The other interfaces (LAN, VLANs, Tailscale) are in the same counters but aren't published.
- **No gateway health.** An ISP outage with the link still up looks healthy. Gateway status needs *System: Gateways*, which can also edit gateways.
- **No version, CPU, memory, uptime or temperature.** They need *Lobby: Dashboard*. Its responses haven't been captured yet; spike 4.5 recommends recording them first.
- **No utilisation %:** virtio reports a meaningless 10 Gbit/s line rate.
- **Totals in UTC days.** The window is the last 30 UTC days, so it slides at UTC midnight, not local midnight.
