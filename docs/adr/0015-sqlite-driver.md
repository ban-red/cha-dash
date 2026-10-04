# ADR-0015 · SQLite driver: ncruces, with encryption at rest as an opt-in

**Status:** accepted · **Date:** 2026-10-03 · **Roadmap:** [decision 15](../plan/04-roadmap.md#decisions) · **Evidence:** [spike 4.8 findings](../../spikes/sqlite-driver/README.md#findings)

## Context

The hub keeps everything in one SQLite file in WAL mode, and its driver must be pure Go so the hub stays a static, cross-compiled binary. Two drivers qualify:
- `modernc.org/sqlite`, SQLite's C transpiled to Go, which `internal/store` uses today;
- `github.com/ncruces/go-sqlite3`, SQLite's Wasm build translated to Go. Since v0.33 it translates with wasm2go, so there's no wazero and no compilation at startup. It ships encrypting VFSes.

The vault already seals credentials, so database encryption would protect everything else: layouts that name internal hosts, password hashes, sessions, audit. The roadmap said to prefer ncruces if encryption is cheap. Spike 4.8 ran the hub's schema and queries under both drivers, plain and encrypted, and ran the hub's own test suite on ncruces.

## Decision

**The hub uses `github.com/ncruces/go-sqlite3`, pinned (v0.35.6). Encryption at rest with its Adiantum VFS is supported, but off by default.**
- **The swap changes the driver, not the schema or the queries.** A database written by modernc opens as is, with nothing to migrate.
- **Encryption is an opt-in setting.** Its name is still open. The docs pair it with `CHADASH_SECRET_KEY_FILE`, and the hub warns when the key file sits in the data directory beside the database.
- **One key file serves both.** The database key is `HKDF-SHA256(KEK, "chadash/db/adiantum/v1")`, derived from the vault's key-encryption key, so `secret.key` keeps working and no key bytes serve two ciphers.
- **Adiantum, not XTS.** XTS is slower, and slower still on CPUs without AES instructions, like the Pi 4's. Adiantum's speed didn't change with AES turned off.

## Consequences

- **Speed is level or better.**
  - Session lookups take 13.3 µs against modernc's 16.2 µs (4.0 against 6.3 across four goroutines), and audit appends 19.6 against 24.3 µs.
  - 1 MiB layout saves and loads are level.
  - Under mixed load: 57% more reads and no SQLITE_BUSY.
  - Opening a connection costs ~0.3 ms more. modernc's package init costs 2.7 ms and 3.5 MiB in every process.
- **Encryption costs about 4× on writes and on cold reads of large values, and nothing on hot reads.** A 1 MiB save takes 10 ms instead of 2.5; an audit append, 85 µs instead of 20. That's roughly 45 ms per save on a Pi 5 (extrapolated). It adds ~190 KiB and one module.
- **Memory.** Each ncruces connection keeps its high-water mark until it closes: ~6 MiB per reader after 1 MiB layouts. Closing gives it all back, so the read pool gets a `SetConnMaxIdleTime`, and then ncruces ends below modernc. At rest, either driver adds 5–6 MiB, far inside the 60 MB idle budget.
- **Size.** The hub grows ~1 MiB (+1,068 KiB amd64, +896 KiB arm64) and links 11 modules instead of 17. armv7 grows more (+3.2 MiB), and a 32-bit process caps each connection at 32 MB. Both are inputs for ADR-0016.
- **Risk.** ncruces is v0.x and wasm2go is six months old. It has a tenth of modernc's importers (255 against 3,518). Both projects are effectively single-maintainer. Pin the version and keep driver code in one file. While encryption is off, going back to modernc is a one-file swap.
- **The swap.** [`hubswap/driver.go`](../../spikes/sqlite-driver/hubswap/driver.go) is the plain version. With it in place, all 16 hub packages pass their tests.
  1. `driver_modernc.go` becomes `driver_ncruces.go`:
     - import `github.com/ncruces/go-sqlite3/driver`, with `driverName = "sqlite3"`;
     - `dsn()` returns an escaped `file:` URI, since ncruces reads `_pragma` and `_txlock` only from those;
     - `busy_timeout` comes first, since ncruces runs pragmas in order;
     - add `modeof=<path>`, since ncruces would create `-wal` 0644.
  2. Run `go get github.com/ncruces/go-sqlite3@v0.35.6 && go mod tidy`. That drops modernc and eight more modules.
  3. Add `SetConnMaxIdleTime(time.Minute)` on the read pool in `store.Open`, and a 077 umask at startup, which covers `-shm`.
  4. Keep SQL strings single-quoted (`DQS=0`). Don't import ncruces's deprecated `embed` package. FTS5 is an opt-in `ext/` package.
- **For encryption**, `driver_ncruces.go` exposes `openDB(path, readOnly, key)` in place of `driverName` and `dsn`. It uses `driver.Open` with a connection hook that sets `PRAGMA hexkey` before the other pragmas, which keeps the key out of the DSN. `store.Open` takes the key, and `cmd/chadash` loads the KEK *before* the store, the reverse of today.
- **Key management.**
  - **Losing the key loses the whole database**, not just the credentials. Backups must carry the key, or use `chadash export`.
  - **The hub generates a key only when there is no database yet.** An encrypted database without its key refuses to start.
  - **A wrong key fails at open** with "file is not a database", leaving the file untouched. The plaintext header tells it apart from an unencrypted file.
  - **There is no rekey in place.** Turning encryption on or off, or rotating the KEK, means `VACUUM INTO` a new file and a rename, at startup.
  - **The `sqlite3` shell can't open an encrypted file**, so the hub needs `chadash db decrypt`.
  - **Adiantum is deterministic and unauthenticated.** It hides contents, not which pages changed, and the vault's AEAD stays the integrity layer for credentials.

## Alternatives considered

- **Stay on modernc.** Most adopted, v1, no change. But it has no encryption path, isn't faster here, and links 9 modules where ncruces links 3.
- **ncruces with encryption on by default.** The default key lives beside the database in `/data`, so default-on mostly protects copies of the database alone. In return, every install pays 4× on writes, loses the `sqlite3` shell, and turns a lost key into a lost database. Revisit once `chadash db` tooling exists.
- **The XTS VFS.** FIPS primitives, but 1.1–2.9× slower than Adiantum, and worse without AES instructions.
- **A cgo driver with SQLCipher.** Mature, with page MACs, but cgo breaks static, cross-compiled scratch images.
- **Encrypting more columns in the app, like the vault.** Authenticated, but encrypted columns can't be indexed or queried, and the vault already covers the sensitive ones.
