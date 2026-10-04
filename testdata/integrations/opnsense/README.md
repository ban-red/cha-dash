# OPNsense fixtures: dogfood router

Recorded on 2026-10-03 by [spike 4.5](../../../spikes/opnsense/README.md) against the owner's router: an OPNsense VM on **pluto**, reached over plain HTTP with the API key Homepage already uses. They seed the v0.1 OPNsense integration's contract tests.

The key turned out to be **least-privilege, not admin**: it holds exactly *Reporting: Traffic* and *Diagnostics: System Activity*. So the fixtures cover what those two privileges return, plus the `403` every other endpoint gives. Everything else (version, memory, temperature, gateways, ARP, DHCP, services) is listed in `endpoints.json` as `403` and still needs recording once the key gets *Lobby: Dashboard* (see the spike README).

## Files

| File | Request | What it is |
|---|---|---|
| `traffic_interface.json` | `GET /api/diagnostics/traffic/interface` | Counters for all 8 interfaces since attach: bytes, packets, errors, drops, `link state` (`"2"` = up), MTU, driver. **Every number is a string** and keys contain spaces |
| `traffic_stream_1s.sse` | `GET /api/diagnostics/traffic/stream/1` | The SSE wire format, verbatim: 8 of 20 events. Each `data:` carries per-interface **deltas** for the last interval plus a float `time`. The first event is a partial interval (about 0.2 s after connect) |
| `traffic_stream_1s_timing.json` | same | Arrival time and size of all 20 events in a 20 s window |
| `traffic_stream_5s.sse`, `traffic_stream_5s_timing.json` | `GET /api/diagnostics/traffic/stream/5` | The same at a 5 s interval: 4 of 7 events in a 32 s window |
| `traffic_top_lan.json` | `GET /api/diagnostics/traffic/top/lan` | Top talkers on LAN (iftop for 2 s, plus reverse DNS): 4 hosts with their peers |
| `activity.json` | `GET /api/diagnostics/activity/get_activity` | top(1): 7 header lines (load, uptime, CPU, memory, ARC, swap) plus the process list, cut to the 10 busiest of 368 rows |
| `menu_tree.json` | `GET /api/core/menu/tree` | The menu as this key sees it. It's filtered by the caller's ACL, so it doubles as a privilege fingerprint |
| `endpoints.json` | 26 GETs plus top talkers | Status, size, latency, headers and the privilege that grants each endpoint (OPNsense 26.7 ACL names) |
| `forbidden_403.json` | any endpoint without the privilege | `{"status":403,"message":"Forbidden"}`, the body for a valid key that lacks the privilege. A bad key gets a `401` instead (per OPNsense docs; not exercised) |

## How these were sanitized

| Original | Fixture |
|---|---|
| LAN addresses (private range) | `10.0.11.x`, last octet kept, so `.202` is Frigate on minig5 and `.120` is jupiter, as in the other fixtures |
| The camera VLAN's addresses | `10.0.21.x`, last octet kept (the same mapping as the Frigate fixtures) |
| Public addresses (top-talker peers) | `203.0.113.x` (TEST-NET-3) |
| Reverse-DNS client names | `client-<octet>.lab.example` |
| User-chosen interface descriptions on `opt1`–`opt6` | `Net A` … `Net E`, `Tailnet` (stock `WAN` and `LAN` kept) |
| Process command lines | The program name only (arguments can carry addresses and labels) |
| A non-system username in the process list | `user` |
| Cookie values | Never recorded; only the name (`PHPSESSID`) |

Kept verbatim: every counter, timestamp, header, driver and device name (`vtnet0`, `vlan0.100`, `tailscale0`), the SSE framing, and the top(1) header lines.

The fixtures contain no MAC addresses, because none of the reachable endpoints return them. A leak check (`spikes/opnsense/leakcheck_test.go`) scanned these files for every real address, label, name and `.env.local` value seen during capture, and found nothing.

## Facts worth testing against

- **Rates come from deltas.** In the stream, `rate = delta_bytes × 8 / (time_n − time_{n−1})`, using the payload's own `time` field, not arrival time. Drop the first event.
- **Cadence is the interval ±50 ms.** At 1 s, the gaps were 1005–1050 ms. At 5 s, they were 5004–5049 ms.
- **Payload size doesn't depend on the interval.** About 0.93–1.0 kB per event for 8 interfaces, so 1 s ≈ 3.5 MB/h and 5 s ≈ 0.7 MB/h.
- **The interface types are raw FreeBSD values.** VLANs show up as `"unknown type 135"` (IFT_L2VLAN), and Tailscale as `"PPP"` on `tun0`. Virtio reports a meaningless `10000000000 bit/s` line rate, so utilisation % can't be derived from it.
- **The WAN counters are since attach** (about 13 days here, matching the top(1) uptime). They are the "WAN ↓/↑ totals" Homepage shows. They are **not** 30-day totals.
