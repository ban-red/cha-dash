# Footprint · the hub and node on small hardware

Measurements behind [ADR-0016](../../docs/adr/0016-hardware-floor.md), the hardware floor. Like the other spikes, the code is throwaway: the method and the numbers are the point.

## What's here

| File | What it does |
|---|---|
| [`sizes.sh`](sizes.sh) | Cross-compiles the hub (with and without the SPA) and the node spike for amd64, arm64 and armv7, prints a size table, deletes the binaries |
| [`footprint_test.go`](footprint_test.go) | Boots the hub in-process and measures memory and CPU, idle and in demo mode with 0–20 WebSocket clients. Build tag `footprint` |
| [`os_darwin_test.go`](os_darwin_test.go), [`os_linux_test.go`](os_linux_test.go) | RSS, the OS's own footprint figure, max RSS and CPU time, per OS |
| [`node/`](node/node_test.go) | A long-running node's idle memory with the Compose SDK and no daemon. Its own module, with spike 4.6's dependencies |

## Run it

```bash
bun run build                                   # the SPA, for the -tags webui builds
spikes/footprint/sizes.sh                       # binary sizes, ~1 min
go test -tags footprint -run '^TestFootprint$' -timeout 30m -v ./spikes/footprint/    # hub, ~9 min
go test -tags footprint -run '^$' -bench Argon2id ./spikes/footprint/                 # one sign-in's hash
(cd spikes/footprint/node && go test -run TestNodeIdle -v)                           # node, ~20 s
```

Nothing here needs Docker, a running hub or anything beyond loopback. The `footprint` tag keeps the hub measurement out of `go test ./...` and CI. `FOOTPRINT_ONLY=<regexp>` picks scenarios, `FOOTPRINT_HOURS=<n>` shortens the demo runs, and `FOOTPRINT_VERBOSE=1` adds the OS's region breakdown.

## Method

**Binary sizes.** Built with the Dockerfile's release flags: `CGO_ENABLED=0`, `-trimpath`, `-ldflags "-s -w"`, `GOOS=linux`, and `GOARCH` amd64, arm64, or arm with `GOARM=7`. The hub is built with and without `-tags webui`. The node is [`spikes/node-compose`](../node-compose/README.md) (Compose SDK and go-git), built by the toolchain its `go.mod` selects, Go 1.26.8. Compressed sizes are `gzip -6`, as the CI image budget measures them.

**Hub.** [`footprint_test.go`](footprint_test.go) wires the hub the way `cmd/chadash`'s `newApp` does: store (SQLite and migrations), vault, auth, graph, topics, the realtime hub and the HTTP server, on `httptest`. Each scenario runs in a fresh child process (the test binary runs itself again), so high-water marks and GC settings don't carry over.
- **Empty hub:** no demo. The graph holds only `chadash:hub`, and the inbox clock ticks every 30 s. It idles for one real minute.
- **Demo:** the Demo Home driver on a fake clock, from 13:00 to 20:30 of the simulated day (900 ticks of 30 s). Three incidents are already open at 13:00. Three more open (Plex token, host memory, qBittorrent OOM), and all six are fixed. The Demo Home has 129 resources, 201 relations, 152 metric series and 24 integration instances. The dogfood home is estimated at ~300 resources and ~25 instances.
- **Clients:** first-run setup signs in once. Then N WebSocket clients say `hello`, subscribe to `graph`, `metrics` and `inbox` in one subscription, count the frames and throw them away.
- **Pacing:** one tick every 60 ms of real time instead of every 30 s, with the coalescing window cut from 250 ms to 20 ms. Each tick still produces what it would in real time: one immediate diff plus one coalesced diff per subscription. Zero resyncs confirm that no client fell behind. The run just skips the idle 30 s between ticks, so CPU per simulated minute is the hub's real cost.
- **Samples:** a stage sample (after each boot component, after sign-in, when ready, at the end, and at the floor) runs a GC first and records:
  - RSS, from `ps`;
  - the OS footprint: macOS's *physical footprint* from `vmmap` (dirty and compressed pages charged to the process, the nearest macOS figure to Linux's anonymous RSS), or `RssAnon` on Linux;
  - live heap and Go-held memory: `runtime/metrics` total minus what the runtime has returned to the OS;
  - goroutines.

  Every 20 ticks, a sample without a GC records RSS and heap in use, for the peaks. `getrusage` gives max RSS and CPU (user plus system), minus the CPU spent forking `ps`. The floor is the sample after `debug.FreeOSMemory`.

**Node.** [`node/node_test.go`](node/node_test.go) links the same SDKs as spike 4.6 (an identical `go.mod`). It points `DOCKER_HOST` at a socket that doesn't exist and `DOCKER_CONFIG` at an empty directory, then initializes the Docker CLI and the Compose service and makes one call, which fails. It creates and commits a stacks repo with go-git, loads one compose file, computes its `ServiceHash`, and idles 10 s. That's a long-running node's floor before a daemon, a hub link or metrics collection come in.

## Results

Apple M4 Pro, macOS 26.5, Go 1.27.1 (hub) and 1.26.8 (node module), 2026-10-03. Memory in MiB unless it says MB.

### Binary sizes

| Platform | Hub (no SPA) | Hub + SPA | Hub + SPA, gzip | Node spike | Node, gzip |
|---|---:|---:|---:|---:|---:|
| amd64 | 12.8 MiB | 13.7 MiB | 6.3 MB | 30.5 MiB | 11.7 MB |
| arm64 | 12.0 MiB | 12.9 MiB | 5.8 MB | 28.3 MiB | 10.5 MB |
| armv7 | 12.5 MiB | 13.4 MiB | 6.0 MB | 28.5 MiB | 10.9 MB |

**armv7 builds cleanly:** no errors or warnings from the hub, with or without the SPA, or from the node with the Compose SDK. `GOOS=linux GOARCH=arm GOARM=7 go vet` is clean for the hub module and the node spike, and so is `GOARCH=386` for the hub module. 64-bit atomics alignment isn't a risk today: the hub's non-test code doesn't import `sync/atomic`, and the one test that does uses `atomic.Int64`, which the compiler aligns on 32-bit. Timestamps are `int64` throughout; Unix milliseconds would overflow a 32-bit `int`. modernc.org/sqlite lists linux/arm as a supported target. **Nothing ran on armv7:** there was no hardware or emulator in this wave.

### Hub memory and CPU

| Scenario | Footprint ready | Footprint peak | RSS peak | Heap live (end) | Heap in use (peak) | Go-held floor | Goroutines | CPU per minute |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Process baseline, nothing booted | 9.9 | 9.9 | 18.6 | 2.1 | 3.4 | 9.7 | 20 | — |
| Baseline + one argon2id hash | 29.3 | 29.3 | 38.0 | 2.1 | 3.4 | 9.6 | 20 | — |
| Empty hub, 0 clients | 30.4 | 30.5 | 42.7 | 2.2 | 4.5 | 10.3 | 24 | 1.0 ms (real) |
| Empty hub, 1 client | 30.9 | 31.4 | 44.0 | 2.3 | 4.9 | 10.9 | 31 | 1.8 ms (real) |
| Demo, 0 clients | 32.6 | 33.4 | 46.3 | 2.8 | 7.1 | 13.5 | 23 | 2.5 ms |
| Demo, 1 client | 37.1 | 38.8 | 52.1 | 3.1 | 7.6 | 17.5 | 30 | 5.5 ms |
| Demo, 5 clients | 37.3 | 38.7 | 52.2 | 3.5 | 9.3 | 15.5 | 46 | 11.0 ms |
| Demo, 20 clients | 35.0 | 38.9 | 52.7 | 4.7 | 16.4 | 18.2 | 106 | 43.6 ms |
| Demo, 20 clients, `GOGC=50` | 38.9 | 40.7 | 55.4 | 4.2 | 10.9 | 17.0 | 106 | 90.2 ms |
| Demo, 20 clients, `GOMEMLIMIT=32MiB` | 36.1 | 38.1 | 52.9 | 4.9 | 14.0 | 17.7 | 106 | 52.0 ms |

"Footprint ready" is after sign-in and with every client holding its snapshot. CPU is per simulated minute for demo rows and per real minute for the empty hub. Each client received ~125 KB per simulated minute, and no client resynced.

**Where boot memory goes** (footprint, demo, 0 clients): process 10.4 → + store 11.6 → + vault 12.0 → **+ auth 31.0** → + graph 32.6 → + server 32.6. The empty hub is the same without the graph step.

**One argon2id hash** at the hub's cost (19 MiB, t=2, p=1) takes **17 ms** and allocates 19.9 MB on this machine.

### Node idle

| Step | RSS | Footprint | Heap live | Go-held | Goroutines |
|---|---:|---:|---:|---:|---:|
| Process (SDKs linked, init done) | 27.9 | 12.8 | 1.7 | 11.0 | 20 |
| + Docker CLI and Compose service | 28.2 | 13.0 | 1.7 | 10.5 | 20 |
| + one call (fails: no daemon) | 29.0 | 13.2 | 1.7 | 10.4 | 20 |
| + stacks repo (go-git init and commit) | 30.3 | 14.2 | 2.5 | 11.3 | 20 |
| + static plan (load, `ServiceHash`) | 33.0 | 15.9 | 2.5 | 14.1 | 20 |
| Idle 10 s later | 33.0 | 15.9 | 2.5 | 12.5 | 20 |

## What the numbers say

1. **The hub's own memory is small.** The live heap is 2.2 MiB for the empty hub and 4.7 MiB for the demo home with 20 tabs open. SQLite and the vault add about 1.5 MiB of footprint. The Demo Home's graph, metrics and inbox add another ~1.5 MiB.
2. **Password hashing is the biggest single cost.** Auth computes its timing-equaliser hash at boot (+19 MiB), and every sign-in costs the same again, at most two at a time. Everything else at boot adds up to about 3 MiB. On a small board, budget for the hashes, not the graph.
3. **macOS keeps charging for memory Go has freed.** After one hash and `FreeOSMemory`, the runtime holds 9.6 MiB, but the OS footprint stays at 25.8–29.3 MiB. Linux drops those pages from RSS as soon as the scavenger returns them (`MADV_DONTNEED`, Go's default since 1.16). So the macOS RSS and footprint here are upper bounds. The steady state on Linux should be nearer the Go-held floor, plus ~1.5 MiB for SQLite, plus whichever pages of the 13 MB binary get touched: **roughly 20–35 MB RSS**, rising 20–40 MB during sign-ins until the scavenger catches up.
4. **Budgets hold with room**, even measured the pessimistic way:
   - The empty hub peaks at 44.0 MiB RSS (46 MB) against **≤ 60 MB**.
   - The demo home with 20 tabs peaks at 52.7 MiB RSS (55 MB) against **≤ 120 MB**. It even fits the 60 MB budget.
   - The hub image is 6.3 MB of binary, gzipped, against the 40 MB image budget.
   - The node binary is 28–31 MiB against **≤ 40 MB**.
5. **Clients are cheap.** Each WebSocket tab adds ~0.5 MiB of heap at peak and ~4 goroutines. Those figures include both ends of each socket, because the clients run in the same process, so the hub's real share is lower. The jump from 0 to 1 client is mostly the sign-in's hash.
6. **CPU is a rounding error.** The demo home costs 2.5 ms of CPU per simulated minute with nobody watching, and ~2 ms more per open tab. An idle empty hub uses 1 ms a minute. A Pi is perhaps 5–15× slower per core than an M4 Pro, which still leaves 20 tabs under 1% of one core. A sign-in's hash (17 ms here) should take a few hundred milliseconds on a Pi.
7. **The node is the heavier binary, not the heavier process.** It holds 16 MiB footprint and 33 MiB RSS at idle on macOS, where RSS counts clean pages of a 46 MB unstripped test binary. Spike 4.6 measured 28–36 MB peak RSS per deploy on Linux. **≤ 40 MB idle RSS** is a reasonable node target, to confirm against a real daemon.

## GOGC and GOMEMLIMIT

- **`GOGC=50`** doubled CPU (43.6 → 90.2 ms per minute, 2.6× the GCs) and saved nothing the OS could see: the footprint peak went *up*, from 38.9 to 40.7. Leave GOGC alone.
- **`GOMEMLIMIT=32MiB`** cost 19% more CPU and moved the footprint peak by under 1 MiB. The heap is too small for a soft limit to matter, and the one big swing, a 19 MiB hash, is an allocation no limit can refuse.
- **Recommendation: no `GOMEMLIMIT` default.** When someone gives the container a memory limit (`mem_limit` in compose), set `GOMEMLIMIT` to about 90% of it, so the GC gets aggressive before the kernel OOM-kills the process. Go doesn't derive it from cgroups by itself. The hub and node could read `/sys/fs/cgroup/memory.max` at startup and set it when `GOMEMLIMIT` isn't set. That's a small follow-up, not a default.

## Caveats

- **An M4 Pro is not a Pi.** Memory figures carry over to Linux on arm64 closely, since it's the same Go runtime and the same data. Absolute CPU and hash times don't.
- **macOS accounting differs from Linux.** RSS includes clean shared pages, and freed pages stay charged, as point 3 explains. The footprint figure is closer to Linux's anonymous RSS. **Linux numbers should come from CI**: the harness already reads `/proc` on Linux, so a job on the amd64 and arm64 runners gives real ones.
- **Page size:** 16 KiB on Apple silicon and on the Raspberry Pi 5's default kernel; 4 KiB on a Pi 4 and most amd64 hosts. Small effect, but it moves RSS by a few hundred KiB.
- **The test binary isn't `chadash`.** It adds the testing package and the client code; the baseline row shows the floor. The SPA (`-tags webui`) isn't embedded in the test, and embedded files are only paged in when served.
- **The demo isn't real integrations.** There are no pollers, HTTP clients, TLS sessions or metric rollup writes, and SQLite's page cache stays nearly empty. It can grow to ~2 MiB per connection, five connections. Re-measure against the 120 MB budget in v0.1, with the dogfood integrations, on Linux.
- **The node probe** has no daemon, no hub link, no event stream and no deploy.
- **Single runs.** The table is one run per scenario. Footprint numbers moved by up to ~4 MiB between two full runs, and the conclusions didn't.

## Noticed along the way

Each `metrics` diff replaces whole series, 48-point history included, every 30 s. That's ~125 KB per tab per simulated minute, or ~180 MB a day for a tab left open. Fine on a LAN, but worth trimming to `value`/`as_of` updates before remote access over a tunnel or mobile data matters.
