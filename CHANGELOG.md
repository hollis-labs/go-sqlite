# Changelog

All notable changes to `go-sqlite` are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- `sqlitekit` package — first cut.
  - `Options` — per-connection pragma controls (`BusyTimeout`, `WAL`,
    `ForeignKeys`, `Synchronous`, `TempStore`, `MMapSize`, `JournalSizeLimit`,
    `CacheKiB`, `TxLock`, `ReadOnly`, `Mode`).
  - `DefaultOptions`, `WriterOptions`, `ReaderOptions` preset constructors.
  - `DSN(path, opts)` — modernc-compatible DSN renderer. Handles the
    `file:foo.db` vs `file:///abs/path` URI distinction so relative paths do
    not break on first write.
  - `OpenOptions` — pool/driver config wrapping `Options`.
  - `OpenWriter` — single-connection writer pool with `_txlock=immediate`.
  - `OpenReader` — bounded pool for concurrent reads (default
    `DefaultReadMaxOpenConns=4`).
  - `OpenSingle` — single-connection pool for apps that serialize all access.
  - `OpenReadOnly` — `mode=ro` handle that refuses to create the database
    file. Returns `ErrReadOnlyMissingFile` for missing paths.
  - Sentinels: `ErrReadOnlyMissingFile`.
  - Constants: `DefaultBusyTimeout` (5 s), `DefaultReadMaxOpenConns` (4),
    `DefaultDriverName` (`"sqlite"`).
- `examples/single` and `examples/split` — runnable end-to-end programs.
- `LICENSE` (MIT, Hollis Labs), `README.md`, `CHANGELOG.md`, root `doc.go`,
  and `sqlitekit/doc.go`.

### Notes

- `modernc.org/sqlite` is the only direct runtime dependency. All other
  entries in `go.sum` are transitive.
- Tests run against real SQLite databases under `t.TempDir()`. No CGO
  toolchain or external SQLite install is needed.
- Future packages (`txutil`, `serialwrite`, `sqlitequeue`) ship in their own
  phases; the module is versioned as a whole, so each phase produces a new
  `v0.x` tag with a CHANGELOG entry.
