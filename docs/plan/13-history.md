# 13 · History: metrics and state over time

> **Status: steps 1–4 are built, and most of 5** (2026-10-29): the measurement, the store and recorder with sparklines that survive a restart, the query route with ranges on the resource sheet, health history with uptime, and Settings → History (level, sizes, clear). Decided in [ADR-0031](../adr/0031-history-storage.md): standard recording on by default, a separate `history.db`, and the long-term tiers below. It fills in the "Metrics: tiered rollups" row of [03 · Architecture](03-architecture.md#storage) and the History line of [04 · Roadmap](04-roadmap.md) (rollups, the Pulse strip, time travel).

## What exists

- **Sparklines live in memory only.** Each series keeps 48 points, 30 minutes apart, in the emitter (`internal/runtime/emitter.go`). A restart blanks every sparkline for a day.
- **A history point is one sample.** The first sample that lands in a 30-minute slot becomes the point. A spike between samples, or after the first one in a slot, never shows.
- **Health has a `since`, but no past.** The emitter remembers the current status and when it started. Earlier states are gone.
- **Unwatched instances poll every 5 minutes** (`Poll.Idle`). Costly series (Docker container stats) are read only while a sheet is open.
- **What is kept on disk:** layouts (100 revisions a board), the audit log (forever, hash-chained), notification events (30 days), import History. No metrics.

## Goals

1. **Cheap enough for a Pi on an SD card.** A few MB of RAM, a few MB of disk writes an hour, no noticeable CPU, for ~1,000 series.
2. **Sparklines survive a restart**, and show the real shape of the slot (average, with the peak).
3. **A sheet can chart any range** from the last hour to years, fast: one query, under 10 ms, under 2,000 points on the wire.
4. **Long-term recording is the user's choice**, with a disk estimate before they choose it, and a hard cap.
5. **Gaps are gaps.** Missing data is never interpolated or drawn as zero.

Not goals: a general TSDB, PromQL, arbitrary labels, or ingesting metrics Cha Dash doesn't produce. 03 already rules out an embedded TSDB; people who want one point Prometheus or VictoriaMetrics at `/metrics`.

## Design

### Tiers

Samples flow down a cascade of tiers. Each tier is a fixed grid on UTC epoch multiples of its step, so a point's time is implicit and never stored.

| Tier | Step | Where | Kept by default | With long-term on |
|---|---|---|---|---|
| raw | as sampled | memory ring | 2 h | 2 h |
| 1m | 1 min | disk | 48 h | 48 h |
| 15m | 15 min | disk | 14 days | 30 days |
| 1h | 1 h | disk | — | 1 year |
| 1d | 1 day (UTC) | disk | — | forever (or a set number of years) |

- **Every rollup point is `avg`, `min`, `max` and `n`** (samples behind it). `n = 0` is a gap. Averages of averages are weighted by `n`, so a coarse point is exact, not an average of rounded averages.
- **Rollups cascade in memory.** Each series keeps an accumulator for the open minute and an open chunk per tier. When a slot closes, its point goes into the tier's chunk, and the next tier's slot becomes pending. A pending slot folds the points of the tier below once time passes its end. A tier's chunk is a whole number of the next tier's steps, so that fold never reads from disk, and a restart can rebuild it from the open chunks.
- **Raw samples aren't persisted.** Integrations poll every 15–60 s, so a 1-minute `max` already keeps every spike. A raw ring in memory for the sheet's "last hour" view is left for step 3.
- **The sparkline's past comes from the 15m tier**, two points per 30-minute slot, when a series first appears. A slot the sparkline scrolls past becomes its average; the newest point is still the slot's first sample, so the wire changes no more often than before. A faint peak mark from the slot's max is left for later: it changes the series shape.

### Storage: chunks in their own database file

**History goes in `/data/history.db`, a second SQLite file**, opened with its own writer.
- History writes never queue behind config writes, and the other way round.
- The config database stays small, so it's quick to back up. `chadash export` never included metrics anyway.
- Deleting the file loses history and nothing else, which is the easiest "start over".
- `CHADASH_HISTORY_DIR` can move it to an SSD when `/data` is on an SD card or a slow share.
- The same opt-in encryption at rest applies.

**Points are stored in chunks, not rows.** One row holds a run of points for one series and tier, compressed:

The schema as built is `internal/store/history/00001_history.sql`. `chunks` became a rowid table with a unique index on its key, which halved the time a sample costs; this was the first sketch:

```sql
CREATE TABLE series (
    id        INTEGER PRIMARY KEY,
    key       TEXT NOT NULL UNIQUE,   -- "<resource id>#<metric key>", as on the metrics topic
    instance  TEXT NOT NULL,
    unit      TEXT NOT NULL DEFAULT '',
    first_at  INTEGER NOT NULL,       -- Unix ms
    last_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE chunks (
    series  INTEGER NOT NULL,
    tier    INTEGER NOT NULL,         -- 1 = 1m, 2 = 15m, 3 = 1h, 4 = 1d
    start   INTEGER NOT NULL,         -- Unix ms, aligned to the chunk length
    data    BLOB NOT NULL,
    PRIMARY KEY (series, tier, start)
) STRICT, WITHOUT ROWID;
CREATE INDEX chunks_expiry ON chunks (tier, start);
```

| Tier | Chunk length | Points per chunk |
|---|---|---|
| 1m | 1 h | 60 |
| 15m | 6 h | 24 |
| 1h | 1 day | 24 |
| 1d | 30 days | 30 |

Short chunks keep the rewrite of an open chunk small, and each is a whole number of the next tier's steps.

- **The blob:** a version byte, a presence bitmap (one bit a point), `n` as varints, then XOR streams of float32 values: `avg` against the previous average, `min` and `max` against the same point's average. An unchanged value costs a bit. About 200 lines of Go, no dependency.
- **Measured on the Demo Home** (158 series, 48 hours at 30 s): **4.1 bytes a point** in the 1m tier and **7.4** in the 15m tier. XORing min and max against their own past instead was 6.5 for 15m but 4.5 for 1m, which holds most of the points.
- **Integer series IDs** keep the primary key small. A series that leaves the graph keeps its ID and its history until retention takes it, so a sheet for a removed container still has a History tab.
- **Why not one row a point:** at 1,000 series, the 1m tier alone is 2.9 million rows and ~115 MB, and every point is a write. Chunks are about ten times smaller, and the 1m tier writes one row a series an hour.

### Writing

- **The emitter calls `Record` under its lock.** Recording only folds the sample in memory, under the recorder's own short lock; it never touches the disk. A goroutine writes every minute: it encodes under the lock and writes after it, so a slow disk never holds up a sample.
- **Closed chunks are written once**, batched into one transaction a minute, with new series rows.
- **Open chunks are checkpointed every 10 minutes**, and on a clean shutdown with the open minute. A crash loses at most 10 minutes. A failed write is queued again.
- **Retention runs hourly:** one `DELETE … WHERE tier = ? AND start < ?` a tier, with `auto_vacuum = INCREMENTAL` and a small `incremental_vacuum` after it, so the file shrinks without a full `VACUUM`.
- Pragmas: WAL, `synchronous = NORMAL` (a power cut can lose the last commit, not corrupt the file), 8 KiB pages.

### Reading

`GET /api/history?series=<key>[&series=…]&from=<ms>&to=<ms>&points=<n>` picks the coarsest tier that still gives at least `points` points for the range (the raw ring for the last two hours), and answers:

```json
{"series": {"<key>": {"unit": "%", "start": 1793000000000, "step": 60000,
  "avg": [12.1, 13.4, null, …], "min": […], "max": […]}}}
```

- Timestamps are implicit, `start + i × step`, so the payload is mostly numbers. `null` is a gap.
- Visibility matches the `metrics` topic (household included). The route needs its row in `internal/authz/routes.go`.
- The sheet's Metrics tab draws it with uPlot, the library 03 already picked for detailed charts, loaded only with the sheet so the initial bundle doesn't grow.
- Ranges: 1 h, 24 h, 7 d, 30 d, and 1 y and All with long-term on. Daily views over the last year come from the 1h tier, bucketed in the display time zone at query time. The 1d tier is on UTC days and is used only past a year.

### State history

Health changes are tiny and the thing people ask about most ("how long has it been down?", "uptime this month").

As built (`internal/store/history/00002_states.sql`, `internal/history/states.go`):

- **A row only on change**, `(resource, at, status)`, written by the emitter where it keeps `marks`. Status is text: the integration statuses plus two of the hub's own. **`stopped`** is written for every resource at a clean shutdown, so the time the hub was off isn't counted as up. **`gone`** is written when a resource leaves the graph.
- **"Since" survives a restart.** When the emitter first sees a resource, it asks history when its status began; history looks past a `stopped` row. A resource down since 09:00 is still "down since 09:00" after a restart at 10:00, and the row it writes goes after the stop, so the gap stays visible.
- **Uptime** is the share of watched time a resource was ok or attention. Time before its first row, `stopped`, `gone`, `unknown` and `maintenance` don't count either way. `GET /api/history/states` returns the changes, totals and that share; the sheet's History tab draws a week as a bar, with the newest changes.
- **Kept 90 days by default, 2 years with long-term on.** The row in effect at the cutoff always stays, so a resource that never changes keeps its start. A crash writes no `stopped`, so its downtime counts as the last status.

### Recording while nobody is watching

Idle instances poll every 5 minutes, so the 1m tier would be mostly gaps overnight. With recording on, the runtime adds `Interest.Record`: integrations treat it like a watched board for their cheap series, at a **record interval** (default 1 minute, settable to 5). Costly series (per-container stats) stay sheet-only and record only while read. Their gaps are honest.

This raises load on upstreams while nobody looks. The setting says so, and the per-integration rate budgets still apply.

### Settings → History

| Option | Default | Notes |
|---|---|---|
| Recording | **Standard** | **Off**: today's behaviour, nothing on disk. **Standard**: the 1m and 15m tiers and 90 days of states. **Long-term**: adds 1h and 1d |
| Keep each tier | as in the Tiers table | Per-tier days or years, within bounds |
| Record while nobody's watching | every minute | Every minute, every 5 minutes (idle rate) |
| Size limit | 1 GB | Past it, the coarsest tiers' oldest chunks go first, and the Inbox gets an attention note |
| Excluded | none | Per integration, or per metric key (`docker:*#net_rx`) |

- The page shows the series count, the file's size, and **an estimate before a change applies** ("Long-term for 2 years: about 180 MB at today's 812 series"), from measured bytes per point.
- Clear history: all, or per integration. Admin only, audited.
- Turning a tier off stops writing it; its data stays until retention, so turning it back on loses nothing.
- Demo mode records nothing: the simulator already makes its history.

### Budgets

For 1,000 series, at Standard:

| | Target | Measured |
|---|---|---|
| RAM (accumulators, open chunks) | ≤ 8 MB | 16 bytes a point in memory: 1.3 MB at Standard, 2.2 MB at long-term |
| CPU | ≤ 1% of a Pi 4 core | ~1 µs a sample, rollups and writes included (M4 Pro), so ~35 µs a second at 1,000 series every 30 s |
| Disk, 48 hours | ≤ 45 MB | 42 MB in the worst case (every sample changes); the Demo Home's mix would be about half |
| Disk, Standard after 14 days | — | about 60 MB worst case, ~30 MB typical |
| Disk, long-term for 2 years | ≤ 250 MB | not measured; ~110 MB typical by the bytes a point above |
| A sheet's query | ≤ 10 ms hub side | not measured |

`TestBudgetAtStandard` and `TestChunkSizeOnDemoHome` in `internal/history` print the numbers with `-v`.

## Steps

1. **Done. Spike:** encode 48 hours of the Demo Home and of the dogfood home's series in the chunk format. Measure bytes a point by metric, encode and decode time, and the query time for a year of 1h points. Compare with plain float32 arrays.
2. **Done. Store and recorder:** `internal/history` with every tier, retention, checkpoints, and seeding sparklines on boot. Sparkline points become slot averages. The level is `CHADASH_HISTORY`.
3. **Done. Query route and the sheet's Metrics tab:** `GET /api/history`, and Now, 1 h, 24 h, 7 d and 30 d on the sheet. It uses the existing mini graph, not uPlot: averages at up to 720 points draw fine, and it adds nothing to the bundle. The raw ring was left out: the 1-minute tier is fine enough for the last hour.
4. **Done. State history:** the `states` table, "down since" across restarts, `GET /api/history/states`, and uptime with a status bar on the sheet's History tab.
5. **Partly done. Settings → History:** the level (applied at once, kept in `settings`), what `history.db` holds, the estimate per level, and Clear. Not built: per-tier retention, the size limit and exclusions.
6. **`Interest.Record`** and the record interval in the integrations that have idle modes.
7. Later: `/metrics` for Prometheus (03), CSV export from a chart, the Pulse strip and linked scrubbing on top of the query route.

## Alternatives considered

- **A row a point in SQLite:** simplest, but about ten times the disk and one write a sample.
- **An embedded TSDB** (VictoriaMetrics' storage, Prometheus TSDB): far more than we need, megabytes of binary and RAM, and 03 rules it out.
- **History in the main database:** one file to back up, but history writes would share the single writer with config, and a big history would slow backups and `chadash export`.
- **Persisting raw samples:** a 1-minute `max` already keeps every spike, and raw would be most of the writes.
- **zstd over the chunks:** good ratios, but a dependency, and XOR encoding already suits slowly changing gauges.

## Decisions

1. **Standard is on by default** (2026-10-29). Settings → History changes the level at once; `CHADASH_HISTORY` is the default until someone does, and `off` there leaves history out altogether.
2. **A separate `history.db`**, movable with `CHADASH_HISTORY_DIR`.
3. **Long-term tiers** as in the Tiers table.

Still open: **recording while nobody's watching** (step 6). Every minute is the proposed default, but it adds load on upstreams.
