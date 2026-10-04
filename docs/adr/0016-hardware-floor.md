# ADR-0016 · Hardware floor: amd64 and arm64 fully supported, armv7 best-effort

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 16](../plan/04-roadmap.md#decisions) · **Evidence:** [footprint spike](../../spikes/footprint/README.md)

## Context

"Light enough for a Pi" is a principle ([01 · Vision](../plan/01-vision.md), principle 14), and the plan promises multi-arch images: "amd64 and arm64; armv7 best-effort". Before CI and the image matrix grow, we need to decide which platforms we promise, what each promise costs, and how small a box Cha Dash fits on. The footprint spike cross-compiled both binaries for all three platforms, measured the hub in-process (idle and in demo mode with 0–20 tabs), and measured a node's idle floor. The dogfood lab is all amd64, so arm64 has no hardware in the house. CI is its only test bed.

## Decision

**amd64 and arm64 are fully supported. armv7 is best-effort. Nothing else is supported.**

| Tier | Platforms | What it means |
|---|---|---|
| **Supported** | linux/amd64, linux/arm64 | Images and binaries for the hub and the node. The full test suite runs natively on both in CI and blocks merges. Bugs block releases |
| **Best-effort** | linux/arm/v7 | Images and binaries are built and published, and the test suite runs as 32-bit in CI. Nothing runs on armv7 hardware, and a regression doesn't block a release. It stays while it costs no more than a build target. It's dropped, with a release note, if it ever needs arch-specific code or a dependency leaves 32-bit |
| **Not supported** | armv6 (Pi 1, Zero, Zero W), 386, everything else | No images |

**Minimum RAM: 512 MB for a host that runs the hub and its local node, OS and Docker included. 1 GB if it also runs other things.** Cha Dash's own share stays under ~130 MB at its worst moment:
- the hub at about 55 MB, measured the pessimistic way (20 tabs, after a sign-in);
- up to ~40 MB more while two sign-ins hash at once;
- the node at ~35 MB during a deploy.

**The evidence:**
- **Sizes:** the hub is 12.0–12.8 MiB, or 12.9–13.7 MiB with the SPA (5.8–6.3 MB gzip, against a 40 MB image budget). The node is 28.3–30.5 MiB, against ≤ 40 MB.
  - **At the v0.1 beta** (2026-10-04), with all 27 integrations, passkeys, the correlator and export: the stripped hub is 23.4 MiB on amd64 and 21.6 MiB on arm64, without the SPA. Still well inside the 40 MB image budget, which CI checks per platform.
- **armv7 is clean today:** all three binaries build for armv7 with `CGO_ENABLED=0` and no warnings. `go vet` is clean for arm and 386. The hub doesn't use 64-bit atomics, and its timestamps are `int64`. Nothing has run on armv7 hardware yet.
- **Hub memory** (macOS, so upper bounds; see the spike for why):
  - the empty hub peaks at 44 MiB RSS and 31 MiB footprint;
  - the demo home with 20 tabs peaks at 52.7 MiB RSS and 38.9 MiB footprint;
  - the live heap is 2–5 MiB;
  - the biggest single cost is argon2id, at 19 MiB per hash: once at boot, once per sign-in, two at most at a time.
- **Hub CPU:** 2.5 ms per simulated minute, plus ~2 ms per open tab, on an M4 Pro.
- **Node:** 16 MiB footprint and 33 MiB RSS at idle on macOS. Spike 4.6 measured 28–36 MB per deploy on Linux.
- **Why armv7 stays best-effort:**
  - there's no armv7 hardware in the lab or in CI, and emulating every pull request is slow;
  - Docker Engine 28 was the last major version with packages for 32-bit Raspberry Pi OS (Debian armhf continues);
  - the boards that truly need armv7 (Pi 2, older armhf boards) are few. A Pi 3, 4, 5 or Zero 2 W runs a 64-bit OS.

## Consequences

- **CI:**
  - The Go job becomes a matrix of `ubuntu-latest` (amd64) and `ubuntu-24.04-arm` (arm64), both running `go test -race ./...`. GitHub's arm64 runners are free for public repositories.
  - A 32-bit job runs `GOARCH=386 go test ./...` natively on amd64. 386 has armv7's 32-bit `int` and 64-bit-atomics alignment rules, without emulation. The job also runs `GOARCH=arm GOARM=7 go vet ./...` and builds the hub and node for armv7.
  - A non-blocking footprint job runs `go test -tags footprint` on both Linux runners, to replace the macOS upper bounds with real Linux numbers. It becomes a budget check once the numbers are stable.
- **Images:**
  - linux/arm/v7 joins the buildx platforms. Go cross-compiles, so there's no emulation. The Dockerfile passes `GOARM` from `TARGETVARIANT` explicitly, rather than relying on Go's default of 7.
  - The arm64 image gets smoke-tested on the arm64 runner, as well as amd64.
  - The size budget applies per platform.
- **Budgets** ([03 · Architecture](../plan/03-architecture.md#performance-budgets-enforced-in-ci-where-possible)):
  - Hub idle RSS of ≤ 60 MB and ≤ 120 MB both hold with room. Keep them, and re-measure the 120 MB one with real integrations in v0.1.
  - Set the node's idle target at **≤ 40 MB RSS**, and confirm it against a real daemon.
- **Code rules while armv7 is listed:**
  - CGO-free builds everywhere, and new dependencies must build for linux/arm;
  - use the `atomic.Int64`/`atomic.Uint64` types, not the `atomic.AddInt64` functions on struct fields;
  - use `int64` for times, sizes and counters that can pass 2³¹.
- **Memory knobs:**
  - No GOGC or GOMEMLIMIT default: lowering GOGC doubled CPU for nothing, and a soft limit barely moved the peak.
  - Follow-up: when the container has a memory limit, the hub and node set Go's soft limit to ~90% of the cgroup's, unless `GOMEMLIMIT` is set.
  - Keep argon2id at 19 MiB with at most two hashes at once. That cap is what bounds the peak on small boards.
- **Docs:** the install docs list the tiers and the 512 MB floor. A Raspberry Pi note says to use a 64-bit OS on a Pi 3 or newer, and that 32-bit Raspberry Pi OS is stuck at Docker 28.

## Alternatives considered

- **amd64 and arm64 only.** Simpler CI, but armv7 builds cleanly today. Dropping it saves almost nothing, and it would leave out old boards that a 13 MB hub using about 50 MB of RAM suits well.
- **Full armv7 support.** It needs armv7 hardware in CI, or QEMU on every pull request, for a small and shrinking audience, while Docker itself steps back from 32-bit Pi OS.
- **armv6 as well.** Docker's official packages don't cover armv6, so there's nothing to run the images on.
