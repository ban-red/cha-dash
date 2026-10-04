# Spike 4.8 · SQLite driver: modernc vs ncruces

Throwaway code that answers one question: **should the hub keep `modernc.org/sqlite`, or move to `github.com/ncruces/go-sqlite3` for encryption at rest?** It runs the hub's own schema and queries under both drivers, plain and encrypted, and measures speed, memory, size and behaviour. Both drivers are CGO-free.

Not product code. The findings go into the notes below and into [ADR-0015](../../docs/adr/0015-sqlite-driver.md). The hub's only driver import stays in `internal/store/driver_modernc.go` until that ADR is accepted.

## What it compares

| Variant | Driver | What it is |
|---|---|---|
| `modernc` | `modernc.org/sqlite` v1.60.1 | SQLite's C source transpiled to Go, one generated file per GOOS/GOARCH. What the hub uses today, with its DSN unchanged |
| `ncruces` | `github.com/ncruces/go-sqlite3` v0.35.6 | SQLite built to Wasm, then **translated to Go source by wasm2go**. Each connection runs in its own sandboxed linear memory |
| `ncruces+adiantum` | the same, plus `vfs/adiantum` | Every file (database, WAL, temp) encrypted in 4 KiB blocks with Adiantum (XChaCha12, AES, NH, Poly1305) |
| `ncruces+xts` | the same, plus `vfs/xts` | AES-256-XTS in 512-byte sectors. NIST/FIPS primitives only |

Both bundle SQLite 3.53.4. Encrypted variants derive their key from a 32-byte key-encryption key (the hub's `secret.key`) with HKDF-SHA256, and set it with `PRAGMA hexkey` from the connection hook, so it never appears in the DSN.

> **The brief assumed ncruces runs SQLite on wazero and compiles Wasm at startup. Since v0.33.0 (2026-03-21) it doesn't.** wasm2go translates the Wasm build to Go ahead of time, the Go compiler compiles it with the rest of the binary, and wazero is no longer a dependency. There is no startup compilation, so there is no compilation cache to configure. The cost moved to binary size and to a small per-connection setup (below).

## How it works

`store.go` mirrors `internal/store`: the file created 0600, a one-connection writer with `_txlock=immediate`, four `query_only` readers, the same pragmas (`journal_mode(WAL)`, `busy_timeout(5000)`, `foreign_keys(1)`, `synchronous(NORMAL)`), and goose. The migrations are the hub's own, read at runtime from `../../internal/store/migrations` (`CHADASH_SPIKE_MIGRATIONS` overrides), so the spike can't drift from the shipped schema. `workload.go` copies the hub's queries:

| Operation | Modelled on | Shape |
|---|---|---|
| Layout save | `boards.SaveLayout` | 1 MiB JSON document, revision check, insert, prune to 100 revisions, in one immediate transaction |
| Layout load | `boards.Layout` | Latest 1 MiB revision, as first paint reads it |
| Session lookup | `auth.lookupSession` | Point read by SHA-256 token hash, joined to users. 10 users, 1,000 sessions |
| Audit append | the action engine | One row per autocommit |
| Metrics batch | the planned raw tier | 300 samples (one poll of the dogfood home) in one transaction. Spike-only table `(series_id, at)` STRICT, WITHOUT ROWID |
| Read during write | WAL | Session lookups on the read pool while a writer saves 1 MiB layouts back to back |
| Startup | `store.Open` + `Migrate` | First boot (empty file) and every later boot (reopen, nothing to migrate, first lookup) |

Around that:
- **Correctness tests** (`correctness_test.go`, `encryption_test.go`) assert the same behaviour under every variant.
- **Footprint** (`footprint_test.go`) re-runs the test binary once per variant, so each child process holds one driver's memory. It records first-query latency, startup, package init cost (`GODEBUG=inittrace=1`), RSS (`ps`, and `getrusage` for the peak), `runtime.MemStats`, and a 3-second mixed phase: four readers plus a writer looping over a layout save, ten audit rows and a metrics batch. A `none` child measures the test binary's floor.
- **Connection memory** (`connmem_test.go`): what the pool keeps after large layouts, and what closing idle readers gives back.
- **Size** (`cmd/size/*`): a tiny main per driver, plus the hub itself, cross-compiled with `CGO_ENABLED=0`.
- **The hub's own tests on ncruces** (`hubswap/driver.go`): `go test -overlay` swaps `internal/store/driver_modernc.go` for the ncruces version, with a temporary `-modfile`. Nothing in the repo changes.

## Run it

Everything is local: no servers, no Docker, nothing on the LAN. Binaries are built into a temp dir and deleted.

```bash
cd spikes/sqlite-driver
./run.sh test        # correctness and encryption checks (~3 s)
./run.sh hubtests    # the hub's tests with internal/store on ncruces
./run.sh deps        # dependency tree per driver
./run.sh size        # linux/amd64, arm64 and armv7 sizes
./run.sh footprint   # 5 rounds of child processes (~2 min)
./run.sh connmem     # 3 rounds (~15 s)
./run.sh bench       # 9 benchmarks × 4 variants × 5 runs, -cpu 4 (~5 min)
```

## Machine

Apple M4 Pro (8 performance + 4 efficiency cores), 48 GB, internal SSD (APFS), macOS 26.5.1, Go 1.27.1, darwin/arm64. Benchmarks ran with `-cpu 4`, to match a Raspberry Pi's core count.

**Raspberry Pi numbers are extrapolated, not measured.** A Pi 5 runs single-threaded code like this roughly 4–5× slower per core than an M4 Pro performance core, and a Pi 4 roughly 8–10× slower. SD cards and USB disks make the fsync at each WAL checkpoint (the only sync under `synchronous=NORMAL`) far slower than this SSD. macOS `fsync` doesn't flush the drive cache either, so these write numbers flatter every variant equally.

## Results

Run on 2026-10-03. Medians of 5 runs (benchmarks and footprint) or 3 (connection memory).

### Workload

| Operation | modernc | ncruces | ncruces+adiantum | ncruces+xts |
|---|---|---|---|---|
| First boot: open both pools + migrate | 2.0 ms | 3.4 ms | 3.9 ms | 4.6 ms |
| Every boot: reopen + first lookup | 0.66 ms | 2.3 ms | 2.4 ms | 2.3 ms |
| Layout save, 1 MiB | 2.4 ms | 2.5 ms | **10.0 ms** (4.0×) | 13.4 ms |
| Layout load, 1 MiB | 0.20 ms | 0.22 ms | **0.93 ms** (4.3×) | 2.66 ms |
| Session lookup | 16.2 µs | **13.3 µs** | 13.4 µs | 12.9 µs |
| Session lookup, 4 goroutines (per op) | 6.3 µs | **4.0 µs** | 3.9 µs | 4.0 µs |
| Audit append | 24.3 µs | **19.6 µs** | 84.8 µs (4.3×) | 90.3 µs |
| Metrics batch, 300 rows | 3.1 ms | 3.2 ms | 7.7 ms (2.4×) | 9.4 ms |
| Lookup while saving 1 MiB layouts | 5.7 µs · 398 saves/s | 4.8 µs · 453 saves/s | 4.6 µs · 103 saves/s | 4.8 µs · 70 saves/s |

Factors in brackets are against plain ncruces. Reads and startup varied by under 15% between runs, apart from single outliers. Writes that include a WAL checkpoint (layout save, metrics batch) varied by up to ±40%, plus one 4× outlier, so read those rows as level rather than ranked.

### Footprint, one process per driver

| | modernc | ncruces | ncruces+adiantum | ncruces+xts |
|---|---|---|---|---|
| First query in a fresh process (open + `SELECT 1`) | 0.86 ms | 0.89 ms | 0.78 ms | 0.87 ms |
| Package init | **2.7 ms, 3.5 MiB heap** | 0.01 ms, 1 KiB | 0.01 ms | 0.01 ms |
| Migrate the v0 schema | 1.7 ms | 1.9 ms | 2.4 ms | 2.7 ms |
| Fixed workload¹ | 527 ms | 395 ms | 1,503 ms | 2,059 ms |
| RSS, open and seeded (floor: 12.5 MiB) | 18.3 MiB | 17.7 MiB | 17.5 MiB | 17.5 MiB |
| RSS after the workload² | 67.3 MiB | 85.8 MiB | 85.0 MiB | 86.4 MiB |
| …of which closing the pools gives back | 0 MiB | 31.5 MiB | 31.5 MiB | 31.5 MiB |
| Go heap (`MemStats.HeapSys`) at the end | 41.5 MiB | 42.2 MiB | 42.2 MiB | 42.2 MiB |
| Mixed phase: reads/s · p50 · p99 · max | 126k · 23 µs · 117 µs · 21 ms | 198k · 15 µs · 90 µs · 12 ms | 216k · 15 µs · 77 µs · 10 ms | 187k · 15 µs · 122 µs · 17 ms |
| Mixed phase: write rounds/s | 115 | 134 | 43 | 25 |
| Mixed phase: errors (SQLITE_BUSY or other) | 0 | 0 | 0 | 0 |

¹ 100 layout saves and loads, 10,000 lookups, 2,000 audit rows, 50 metrics batches.
² After GC and `debug.FreeOSMemory`. Peak RSS (`getrusage`) was within 0.2 MiB of this in every case. About 40 MiB of it is Go heap left from the 1 MiB strings, the same under every driver.

### Memory the pool keeps

RSS in MiB, after ten saves of one layout and one load of it on each of the four readers. Then the idle readers are closed, which is what `SetConnMaxIdleTime` would do after its timeout.

| Driver | Layout | Seeded | Warm | Readers closed | Growth | Given back by closing 4 readers |
|---|---|---|---|---|---|---|
| modernc | 64 KiB | 17.9 | 25.8 | 26.2 | +7.9 | 0 |
| ncruces | 64 KiB | 18.3 | 22.8 | 18.0 | +4.5 | 4.8 |
| modernc | 1 MiB | 18.1 | 48.6 | 49.0 | +30.5 | 0 |
| ncruces | 1 MiB | 18.5 | 53.2 | 27.7 | +34.8 | 25.6 |

### Size and dependencies

Stripped (`-s -w`), `-trimpath`, `CGO_ENABLED=0`, KiB. "Added" is against a tiny main that links only `database/sql`; gz is `gzip -9`, a stand-in for the compressed image layer.

| | linux/amd64 | gz | linux/arm64 | gz | linux/arm (v7) | gz |
|---|---|---|---|---|---|---|
| modernc | +4,588 | +2,006 | +4,544 | +1,942 | +4,608 | +1,866 |
| ncruces | +6,012 | +2,774 | +5,696 | +2,515 | +7,872 | +3,057 |
| ncruces + Adiantum VFS | +6,200 | +2,830 | +5,824 | +2,555 | +8,000 | +3,105 |
| **The hub, `cmd/chadash`** | 13,076 → 14,144 (**+1,068**) | | 12,288 → 13,184 (**+896**) | | | |

| | Non-std packages | Modules |
|---|---|---|
| modernc | 36 | 10: `modernc.org/{sqlite,libc,mathutil,memory}`, `x/sys`, bigfft, go-humanize, go-isatty, google/uuid, go-strftime |
| ncruces | 10 | 4: `go-sqlite3`, `go-sqlite3-wasm/v6`, julianday, `x/sys` |
| ncruces + Adiantum | 20 | 6: adds `lukechampine.com/adiantum` and `x/crypto` (already in the hub, for the vault) |

The hub itself links **17 modules with modernc and 11 with ncruces**.

### Maintenance and licence

| | modernc | ncruces |
|---|---|---|
| Releases, last 12 months | 35 (latest v1.60.1, 2026-09-29) | 26 (latest v0.35.6, 2026-09-23) |
| Version line | v1 | v0.x, no stability promise. The internal `go-sqlite3-wasm` module went from v2 to v6 between April and September 2026 |
| Architecture churn | Stable: transpiled C per platform | Moved from wazero to wasm2go in v0.33.0, six months ago |
| Known importers (pkg.go.dev) | 3,518 | 255 (`/driver`) + 96 (root) |
| Maintainers | One main author (most commits) | One main author (most commits) |
| Testing | Per-platform builders | Every commit on 8 Linux architectures, macOS, Windows and the BSDs; SQLite's `mptest` and `speedtest1` |
| Licence | BSD-3-Clause (`libc` adds third-party MIT notices) | MIT; the translated SQLite is MIT-0 on top of SQLite's public domain; Adiantum MIT |

All of these are compatible with AGPL-3.0.

### Correctness

Every check passed identically under all four variants, except where noted.

| Check | Result |
|---|---|
| Pragmas on the writer and each of the 4 readers | ✅ `wal`, `busy_timeout` 5000, `foreign_keys` 1, `synchronous` 1, `query_only` 0 / 1 |
| STRICT tables | ✅ TEXT in INTEGER, `[]byte` in TEXT, `time.Time` in INTEGER and a string in the BLOB `token_hash` are all rejected; `"123"` and `bool` coerce. Same messages, except ncruces prefixes `sqlite3:` and modernc appends the code |
| Foreign keys | ✅ Orphan session rejected; deleting a user cascades sessions and nulls `layouts.created_by` |
| Schema rules | ✅ Single-owner index, NOCASE username, role CHECK, append-only audit trigger |
| Readers are query-only | ✅ "attempt to write a readonly database" |
| `_txlock=immediate` + `busy_timeout` | ✅ A second writer's `BEGIN IMMEDIATE` waits out its busy timeout (300 ms in the test), then gets SQLITE_BUSY. Deferred reads aren't blocked |
| WAL snapshots | ✅ Readers see the last commit while a write is open, without waiting |
| goose | ✅ Version 1; a second `Migrate` applies nothing. A file written by modernc opens under ncruces with nothing to migrate |
| 1 MiB round trip; scan-into-`any` types | ✅ Byte for byte; `int64, string, []byte, nil, float64, string` under both |
| **`-wal` and `-shm` file modes** | ⚠️ modernc gives them the database's 0600. **ncruces creates them 0644** (0666 minus a 022 umask): its Go VFS doesn't copy the database's mode. `modeof=<db path>` in the DSN fixes `-wal`; a 077 umask fixes both (tested) |
| Compile options | ⚠️ ncruces builds with `DQS=0` (double-quoted string literals are errors), foreign keys on by default, `THREADSAFE=0` (one sandbox per connection), and without load_extension or shared cache. FTS5 and R*Tree are opt-in `ext/` packages; modernc builds them in |
| The hub's own test suite on ncruces | ✅ All 16 packages pass with only `driver_modernc.go` swapped (`./run.sh hubtests`) |

### Encryption

| Check | Result |
|---|---|
| Nothing readable on disk | ✅ Database and WAL: no SQLite header, no schema text, no row data, before and after checkpoint. The plain variants are the control |
| Wrong key | ✅ `Open` fails on the first pragma that reads the file: `PRAGMA journal_mode=WAL: sqlite3: file is not a database`. The file is unchanged, and the key isn't in the error |
| No key | modernc: `file is not a database (26)`. ncruces without the VFS: `invalid _pragma: … file is not a database`. The Adiantum VFS without a key: `disk I/O error` |
| Encrypt an existing database | ✅ `VACUUM INTO 'file:new.db?vfs=adiantum&hexkey=…'` turns a modernc-written file into an encrypted one; `integrity_check` ok |
| Decrypt for support | ✅ `VACUUM INTO 'file:plain.db?vfs=os'` from an encrypted connection; modernc opens the copy. Without `vfs=os` the copy inherits the Adiantum VFS and fails |
| Rekey in place | ❌ Not supported. Rotation means `VACUUM INTO` a new file with the new key, then a rename |
| No AES instructions (Pi 4) | With `GODEBUG=cpu.aes=off,cpu.pmull=off`, **Adiantum's cost didn't move** (save 9.4 ms, load 0.91 ms); XTS loads doubled (2.7 → 5.3 ms) |

## Findings

**Verdict: move the hub to ncruces, and offer Adiantum encryption at rest as an opt-in.** On the hub's own workload, plain ncruces is as fast as modernc or faster, links fewer modules and passes the hub's test suite with a one-file swap. Encryption costs little CPU and size but has real operational costs, so it should be a choice, not the default.

**What we learned**

1. **Plain ncruces matches or beats modernc where the hub spends its time.** Session lookups, the read behind every request, take 18% less time on one goroutine and 37% less across four. Audit appends take 19% less. Layout saves, loads and metrics batches are level within noise. Under mixed load, ncruces served 57% more reads with a lower p99 and no errors.
2. **Startup favours modernc, by milliseconds.** Each ncruces connection sets up its own sandbox and runs SQLite's initialisation, about 0.3 ms each, so reopening five connections takes 2.3 ms against 0.66 ms. In return, modernc's package init costs 2.7 ms and 3.5 MiB of heap in *every* process that links it, including each `chadash healthcheck` run. Neither matters against a 300 ms first paint.
3. **There is no Wasm compilation any more.** First query in a fresh process: 0.89 ms against 0.86 ms. Nothing to cache.
4. **Memory: ncruces keeps each connection's high-water mark until it closes, and closing gives it back.** After 1 MiB layouts, each reader held about 6 MiB (about 1.2 MiB after 64 KiB layouts). Closing idle readers returned all of it, ending *below* modernc, whose allocator returned nothing. So with ncruces, give the read pool a `SetConnMaxIdleTime` (a minute or so; reopening costs ~0.3 ms). At rest, either driver adds about 5–6 MiB with five connections open, far inside the 60 MB idle budget.
5. **Size: the hub grows by about 1 MiB** (+1,068 KiB amd64, +896 KiB arm64, before compression), well inside the 40 MB image budget. armv7 grows more (+3.2 MiB over modernc in a tiny main). On 32-bit, ncruces also caps each connection at 32 MB of SQLite memory (256 MB on 64-bit), plenty for the hub. Both are inputs for ADR-0016.
6. **Encryption costs about 4× on writes and on cold reads of large values, and nothing on hot reads.** A 1 MiB layout save goes from 2.5 to 10 ms, an audit append from 20 to 85 µs, a 1 MiB load from 0.22 to 0.93 ms. Session lookups don't change, because their pages are already in SQLite's cache, decrypted. Small commits suffer most: each WAL frame is 24 bytes off the 4 KiB block grid, so every frame write reads, decrypts and re-encrypts two blocks. Extrapolated to a Pi 5, an encrypted 1 MiB save takes ~45 ms (~90 ms on a Pi 4) and an audit append ~0.4 ms. That's fine for a dashboard, where writes are rare and asynchronous.
7. **Adiantum, not XTS.** XTS is slower everywhere (1.1–2.9×), and slower still without AES instructions, which the Pi 4 lacks. Adiantum was designed for exactly that CPU, and turning AES off didn't change its numbers.
8. **The real costs of encryption are operational:**
   - **Losing the key loses the whole database**, not just the integration credentials, as it does today.
   - **`sqlite3 chadash.db` stops working**, for operators and for support, so the hub needs a `chadash db decrypt`.
   - **The default key sits next to the database in `/data`**, so encryption adds little against someone who takes the whole volume. It protects copies of the database on its own, and installs that use `CHADASH_SECRET_KEY_FILE`.
   - **Adiantum is deterministic and unauthenticated.** It hides contents, but not which pages changed between two snapshots, and it doesn't detect tampering. Credentials keep the vault's authenticated encryption, and signed `chadash export` archives cover backups.
9. **Key handling fits the existing model.**
   - **One key file covers both.** The DB key is `HKDF-SHA256(secret.key, "chadash/db/adiantum/v1")`, so `secret.key` and `CHADASH_SECRET_KEY_FILE` work unchanged, and no bytes are used by two ciphers.
   - **Keep the key out of the DSN.** Set it with `PRAGMA hexkey` from the driver's connection hook, then the other pragmas. `_pragma` DSN parameters run before the hook, too early: SQLite would read page 1 with no key.
   - **The startup order flips: key first, then the store.** Today the vault opens after the store and asks it whether secrets exist before minting a key.
   - **Wrong key and plaintext file look the same.** Both fail with "file is not a database". The hub can tell them apart by the plaintext header (`SQLite format 3\0`) and give a precise error.
10. **Swap gotchas, all one-liners.**
    - ncruces reads `_pragma` and `_txlock` only from `file:` URIs. Without the prefix, the whole string, query and all, becomes the file name, and the hub's `journal_mode` check fails loudly.
    - modernc runs `busy_timeout` first whatever the order; ncruces runs pragmas as written. Put `busy_timeout` first.
    - `-wal` and `-shm` need `modeof` or a 077 umask.
    - Don't import `ncruces/go-sqlite3/embed`: it's deprecated and prints a warning at init.
11. **modernc is the safer bet on maturity alone:** about 10× the importers, a v1 line and a stable architecture. ncruces's risks are its youth (wasm2go is six months old) and v0.x churn. Pin the version. While encryption is off, modernc stays a one-file swap back.

**Carry into the hub:** the swap in [ADR-0015](../../docs/adr/0015-sqlite-driver.md#consequences) (`hubswap/driver.go` is the plain version, ready to drop in); `SetConnMaxIdleTime` on the read pool; a 077 umask at startup. If encryption is enabled:
- load the key before the store;
- convert an existing database with `VACUUM INTO` at startup;
- add `chadash db decrypt`;
- warn when encryption is on but the key file sits in the data directory.

## Known gaps (deliberately out of scope)

- No Raspberry Pi run: arm64 numbers come from Apple Silicon and are scaled by rule of thumb. The hub's CI or the dogfood Pi should confirm the encrypted write path once it exists.
- Only macOS was measured, where `fsync` is cheap. On Linux the WAL checkpoint costs more under every variant.
- 1 MiB layouts are the upper end. Most boards are far smaller, which shrinks both the encryption cost and the per-connection memory.
- No crash or power-loss testing of the Adiantum VFS (a partly written 4 KiB block is unreadable; SQLite's journal is meant to cover that). Its tests and SQLite's `mptest` run upstream.
- Litestream-style replication of an encrypted database would need the same VFS. Not evaluated.
