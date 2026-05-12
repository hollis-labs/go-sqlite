# Changelog

All notable changes to `go-sqlite` are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Decisions

- **`sqlitequeue` package deferred.** The proposed wrapper `OpenDB` would
  be a one-line passthrough to `sqlitekit.OpenSingle`, and the optional
  `go-queue` bridge would couple `go-sqlite` to `go-queue` for every
  consumer of `sqlitekit` alone. Apps should open queue DBs with
  `sqlitekit.OpenSingle` and hand the resulting `*sql.DB` to their queue
  driver of choice (e.g. `go-queue/driver/sqlite.New`). See
  [`docs/adr/0001-defer-sqlitequeue.md`](docs/adr/0001-defer-sqlitequeue.md).

### Added

- `serialwrite` package — optional in-process write serializer.
  - `Op` — `func(ctx, *sql.Tx) error`. Runs inside a SAVEPOINT on the
    worker's BEGIN IMMEDIATE transaction so a single failing op does not
    invalidate sibling ops in the same batch.
  - `Writer` — interface implemented by both `Queue` and `Direct`
    (`Submit(ctx, name, fn) error`, `Stats() Stats`).
  - `Options` — `QueueSize` (default 256), `MaxBatch` (default 32),
    `BatchWindow` (default 2ms), `Retry` (`*txutil.RetryOptions`; `nil`
    disables retry — pass a non-nil pointer to opt in).
  - `Stats` — `Submitted`, `Completed`, `Failed`, `Batches`,
    `OpsInBatches`, `LastBatchSize`, `QueueDepth`. Safe to call
    concurrently.
  - `New(db, opts) *Queue` — batching worker. Call `Run(ctx)` in a
    goroutine; `Stop()` + `Wait()` for orderly drain. Panics in an op are
    caught, attributed to that op's error, and the worker keeps running.
  - `NewDirect(db, opts) *Direct` — synchronous, no goroutine. Useful for
    tests and apps that want the `Writer` interface without lifecycle
    management.
  - Sentinels: `ErrWriterStopped`.
- `examples/serialwrite` — runnable demo of `serialwrite.Queue` batching
  20 concurrent producers (200 inserts) through one transaction owner.
- `txutil` package — explicit write transactions and lock-retry helpers.
  - `BeginImmediate(ctx, db)` — open a transaction. Contract marker for
    the `_txlock=immediate` DSN requirement (set automatically by
    `sqlitekit.WriterOptions` / `sqlitekit.OpenWriter` / `sqlitekit.OpenSingle`
    constructed via `WriterOptions`).
  - `WithImmediate(ctx, db, fn)` — closure form. Commits on `nil`, rolls
    back on non-nil; panics propagate after rollback; commit failures
    trigger rollback to release the connection.
  - `IsBusy`, `IsLocked`, `IsRetryableLock` — modernc `*sqlite.Error`
    classification covering primary and extended forms via `Code() & 0xff`.
  - `RetryOptions` (`MaxAttempts`, `BaseDelay`, `MaxDelay`, `Jitter`
    opt-in, `IsRetryable`) plus `WithRetry(ctx, opts, fn)` and
    `WithImmediateRetry(ctx, db, opts, fn)` — bounded exponential backoff
    with optional jitter; `ctx.Done()` preempts the sleep.
  - `SavepointName(prefix)` — sanitized, process-unique SQLite-safe
    identifier. `WithSavepoint(ctx, tx, name, fn)` — release on success,
    rollback-to + release on error/panic without poisoning the outer
    transaction. Cleanup runs under `context.WithoutCancel(ctx)` so a
    mid-`fn` cancellation does not orphan the savepoint.
  - Sentinels: `ErrInvalidSavepointName`.
- `examples/inbox` — runnable demo of `txutil.WithImmediate` driving an
  atomic select-and-mark inbox claim (the Mux/Vanta/Nanite pattern).
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
- `txutil` BEGIN IMMEDIATE behavior is proven by paired contention tests
  (`TestBeginImmediate_AcquiresWriterLockAtBeginTime` plus the deferred
  counter-example), the retry-recovery test, the
  constraint-not-retried test, the savepoint
  rollback-does-not-poison-outer-tx test, and the
  cleanup-survives-ctx-cancel test that proves a cancelled inner ctx
  does not orphan a savepoint.
- `serialwrite` behavior is proven by 16-goroutine × 25-insert fan-out
  (`TestQueue_ConcurrentSubmitsCommit`), savepoint isolation
  (`TestQueue_FailedOpRollsBackOnlyItsSavepoint`), panic recovery
  (`TestQueue_PanicInOpIsCaught`), shutdown drain
  (`TestQueue_StopDrainsAcceptedOps`), and ctx-cancel preemption at
  enqueue and ack waits.
- Future packages (`sqlitequeue`) ship in their own phase; the module is
  versioned as a whole, so each phase produces a new `v0.x` tag with a
  CHANGELOG entry.
