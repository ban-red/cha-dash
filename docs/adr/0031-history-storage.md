# ADR-0031 · History: rollup tiers in compressed chunks, in their own database

**Status:** accepted · **Date:** 2026-10-29 · Fills in the Metrics row of [03 · Architecture](../plan/03-architecture.md#storage). Detail: [13 · History](../plan/13-history.md)

## Context

Sparklines live in memory, so a restart blanks them for a day. Each point is a single sample, so spikes between samples never show. Health keeps its current status but not its past. The roadmap wants history that a sheet can chart over any range, and some people want it kept for years. All of that has to be cheap on a Pi with an SD card, which rules out an embedded TSDB (03).

## Decision

**Every series is rolled up in memory into fixed-grid tiers (1 minute, 15 minutes, and with long-term on, 1 hour and 1 day). Each point is `avg`, `min`, `max` and a sample count. Points are stored as compressed chunks in a second SQLite file, `history.db`. Standard recording is on by default.**

- **Levels:** `off` (nothing on disk; the sparklines work as before), `standard` (the default: 1-minute points for 48 hours, 15-minute points for 14 days) and `long-term` (also 15-minute points for 30 days, 1-hour points for a year, 1-day points forever). Admins pick one in Settings → History, and it applies at once. `CHADASH_HISTORY` is the default until they do; `CHADASH_HISTORY=off` leaves history out altogether.
- **Times are implicit.** Each tier is a grid on UTC epoch multiples of its step. A chunk stores its start in Unix milliseconds, and a point's time is its index.
- **Chunks:** a presence bitmap, sample counts as varints, then Gorilla-style XOR streams of float32 values. `min` and `max` are XORed against the same point's `avg`, so a point from one sample costs two bits more than its average.
- **Higher tiers fold from the tier below** when their slot closes. A tier's chunk length is a multiple of the next tier's step, so a slot is always inside one lower chunk, and a restart rebuilds it from disk.
- **Writes:** closed chunks are written once. Open chunks are written every 10 minutes and at shutdown, so a crash loses at most 10 minutes. Retention is one `DELETE` per tier, hourly.
- **Health changes** are rows in `history.db` too, one per change, with `stopped` at a clean shutdown and `gone` when a resource leaves. They keep "since" across restarts and give uptime. Kept 90 days, or 2 years with long-term.
- **Sparklines** load their past from the 15-minute tier when a series first appears, and a finished 30-minute slot shows its average instead of its first sample.

## Consequences

- Every install writes to disk by default: a few MB an hour, and 30–60 MB in total at 1,000 series after 14 days, by the measurements in 13 · History. `CHADASH_HISTORY=off` restores the old behaviour.
- Values are float32: about seven significant digits, which is plenty for a chart. Exact counters belong in the graph's attributes, not in history.
- Deleting `history.db` loses history and nothing else. `chadash export` never included metrics, and still doesn't.
- Days in the 1-day tier are UTC days. Local-day views for the last year come from the 1-hour tier.
- Idle instances still poll every 5 minutes, so the 1-minute tier has gaps while nobody watches. Recording while unwatched (`Interest.Record`) is a later step, because it adds load on upstreams.

## Alternatives considered

- **One row per point:** about ten times the disk, and a write for every sample.
- **An embedded TSDB:** megabytes of binary and RAM for features we don't use. 03 rules it out.
- **History tables in the main database:** history writes would queue behind config writes on the single writer, and backups would grow with history.
- **Persisting raw samples:** a 1-minute `max` already keeps every spike, and raw samples would be most of the writes.
