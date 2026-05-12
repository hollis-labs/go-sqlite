# go-sqlite

SQLite concurrency toolkit for Go apps that use `database/sql` with [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite). Solves the recurring failure mode:

> "We enabled WAL and `busy_timeout`, but concurrent Go writers still hit `SQLITE_BUSY` / `SQLITE_LOCKED`."

The module ships small, focused sub-packages. Today: `sqlitekit` (DSN + opener defaults) and `txutil` (BEGIN IMMEDIATE / lock-retry / savepoints). Future releases add `serialwrite` (in-process serialized writer) and an optional `sqlitequeue` helper.

## Status

Pre-1.0 (`v0.1.x`). The public API is stable in shape — `sqlitekit.Options`, `sqlitekit.OpenOptions`, `DSN`, and the four named openers — but minor breaks may still happen between `v0.x` releases. See [`CHANGELOG.md`](CHANGELOG.md) for per-release detail and pin a version in your `go.mod`.

Documentation: [pkg.go.dev/github.com/hollis-labs/go-sqlite](https://pkg.go.dev/github.com/hollis-labs/go-sqlite).

## Install

```bash
go get github.com/hollis-labs/go-sqlite
```

The package only depends on [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), a pure-Go driver. No CGO toolchain is required.

## Why this package exists

SQLite's WAL journal enables *concurrent readers*, not *concurrent writers*. Go apps using `database/sql` routinely hit `SQLITE_BUSY` for three reasons:

1. **PRAGMAs are per-connection.** Calling `db.Exec("PRAGMA journal_mode=WAL")` at startup only affects the first checked-out connection. When the pool spawns a second, that connection has default pragmas. `sqlitekit` emits pragmas as DSN `_pragma=` parameters so modernc applies them when *every* connection opens.
2. **Default `BEGIN` is deferred.** A `BEGIN` transaction does not acquire the writer lock until the first write statement. If two goroutines both `BEGIN`, read, then write, one of them gets `SQLITE_BUSY`. `sqlitekit`'s `OpenWriter` defaults to `_txlock=immediate`, which acquires the writer lock at `BEGIN` time so concurrent writers serialize instead of racing.
3. **Connection pool > 1 is dangerous for writers.** SQLite serializes writes inside the database; two Go connections doing `BEGIN IMMEDIATE` concurrently still produce `SQLITE_BUSY` if the busy timeout expires. The portable fix is a single-connection writer pool. `sqlitekit.OpenWriter` forces `MaxOpenConns=1`; readers go through a separate `OpenReader` handle.

## Usage

### Single-handle apps

For apps that intentionally serialize all DB access through one handle:

```go
import (
    "context"

    "github.com/hollis-labs/go-sqlite/sqlitekit"
    _ "modernc.org/sqlite"
)

db, err := sqlitekit.OpenSingle(ctx, "app.db", sqlitekit.OpenOptions{
    CreateParentDir: true,
})
```

`OpenSingle` forces `MaxOpenConns=1`, applies `DefaultOptions` (WAL, foreign keys, busy timeout 5s, synchronous NORMAL, temp_store memory, mmap_size 30 GB, journal_size_limit 64 MiB), and creates the parent directory when requested.

### Split read/write pools

For apps that want to serve concurrent reads against a single-connection writer:

```go
writer, err := sqlitekit.OpenWriter(ctx, "app.db", sqlitekit.OpenOptions{})
if err != nil {
    return err
}
defer writer.Close()

reader, err := sqlitekit.OpenReader(ctx, "app.db", sqlitekit.OpenOptions{
    MaxOpenConns: 4,
})
if err != nil {
    return err
}
defer reader.Close()
```

`OpenWriter` forces `MaxOpenConns=1` and `_txlock=immediate`. `OpenReader` defaults to a bounded pool (4 connections) without `_txlock`.

### Read-only handle

For consumers that should not be able to create or write the database:

```go
ro, err := sqlitekit.OpenReadOnly(ctx, "app.db", sqlitekit.OpenOptions{})
if errors.Is(err, sqlitekit.ErrReadOnlyMissingFile) {
    // file does not exist; read-only handle refuses to create it.
}
```

`OpenReadOnly` sets `mode=ro` on the DSN, disables WAL (read-only mode cannot create the `-wal`/`-shm` sidecar files), and returns `ErrReadOnlyMissingFile` if the path does not exist.

### Just the DSN

When you want to manage the `*sql.DB` yourself:

```go
dsn := sqlitekit.DSN("app.db", sqlitekit.WriterOptions())
db, err := sql.Open("sqlite", dsn)
```

`DSN` handles two SQLite URI quirks correctly:

- Relative paths use `file:foo.db?...`. The authority form (`file://foo.db?...`) breaks modernc on first write against a real file.
- Absolute paths use `file:///abs/path?...`.

## txutil

`txutil` builds on `sqlitekit` with explicit-transaction helpers. Use it for read-modify-write paths that need writer-lock semantics, or any code that should fail fast on writer contention instead of mid-transaction.

### `WithImmediate` — atomic select-and-mark

```go
err := txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
    rows, err := tx.QueryContext(ctx, `
        SELECT id, payload
        FROM messages
        WHERE to_urn = ? AND delivered_at IS NULL
        ORDER BY created_at ASC
        LIMIT ?`, toURN, limit)
    if err != nil {
        return err
    }
    defer rows.Close()

    var ids []int64
    for rows.Next() {
        var id int64
        var payload string
        if err := rows.Scan(&id, &payload); err != nil {
            return err
        }
        ids = append(ids, id)
    }
    for _, id := range ids {
        if _, err := tx.ExecContext(ctx,
            `UPDATE messages SET delivered_at = ? WHERE id = ?`, now, id); err != nil {
            return err
        }
    }
    return nil
})
```

The transaction begins as `BEGIN IMMEDIATE`, so the writer lock is acquired at `BEGIN` time. A concurrent writer racing on the same flow blocks until our transaction commits or rolls back, and never observes a half-claimed row.

The `_txlock=immediate` DSN parameter is what tells modernc to issue `BEGIN IMMEDIATE`. `sqlitekit.WriterOptions` / `sqlitekit.OpenWriter` / `sqlitekit.OpenSingle` (when constructed via `WriterOptions`) all set it. If you build the DSN yourself, opt in explicitly with `Options{TxLock: "immediate"}`.

### Lock-error classification

```go
if _, err := db.ExecContext(ctx, `...`); err != nil {
    if txutil.IsRetryableLock(err) {
        // SQLITE_BUSY or SQLITE_LOCKED — safe to retry after backoff.
    }
}
```

`IsBusy`, `IsLocked`, and `IsRetryableLock` work against `*sqlite.Error` and any error that wraps one (`errors.As`). Extended forms like `SQLITE_BUSY_RECOVERY`, `SQLITE_BUSY_TIMEOUT`, and `SQLITE_LOCKED_SHAREDCACHE` are covered by primary-code masking.

### Retry

```go
err := txutil.WithImmediateRetry(ctx, db, txutil.RetryOptions{
    MaxAttempts: 5,
    BaseDelay:   1 * time.Millisecond,
    MaxDelay:    100 * time.Millisecond,
    Jitter:      true,
}, func(tx *sql.Tx) error {
    _, err := tx.ExecContext(ctx, `UPDATE counters SET v = v + 1 WHERE id = ?`, id)
    return err
})
```

`WithImmediateRetry` runs the closure in a fresh `BEGIN IMMEDIATE` transaction on each attempt. Lock errors are retried with bounded exponential backoff; non-lock errors are returned immediately. `ctx.Done()` preempts the sleep between attempts.

The retry classifier is pluggable via `RetryOptions.IsRetryable` if you want to retry on additional errors.

### Savepoints

```go
err := txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
    for _, item := range items {
        name := txutil.SavepointName(item.Kind)
        if err := txutil.WithSavepoint(ctx, tx, name, func() error {
            return insertItem(ctx, tx, item)
        }); err != nil {
            log.Printf("skip %s: %v", item.ID, err)
            // Outer tx is still alive; loop continues.
        }
    }
    return nil
})
```

`SavepointName` returns a sanitized, process-unique identifier. `WithSavepoint` releases on success or rolls back to and releases on failure, leaving the outer transaction usable.

## API Overview

Package `sqlitekit` (`github.com/hollis-labs/go-sqlite/sqlitekit`):

- `Options` — per-connection pragmas and DSN parameters (`BusyTimeout`, `WAL`, `ForeignKeys`, `Synchronous`, `TempStore`, `MMapSize`, `JournalSizeLimit`, `CacheKiB`, `TxLock`, `ReadOnly`, `Mode`).
- `DefaultOptions()` — sensible defaults for typical app DBs.
- `WriterOptions()` — `DefaultOptions` + `TxLock="immediate"`.
- `ReaderOptions()` — `DefaultOptions` without `TxLock`.
- `DSN(path, opts)` — render a modernc-compatible DSN.
- `OpenOptions` — opener config: embedded `Options`, `DriverName`, `MaxOpenConns`, `MaxIdleConns`, `ConnMaxLifetime`, `CreateParentDir`.
- `OpenWriter` — single-connection writer pool with `_txlock=immediate`.
- `OpenReader` — bounded pool for concurrent reads.
- `OpenSingle` — single-connection pool for apps that serialize all access.
- `OpenReadOnly` — bounded pool with `mode=ro`; refuses missing files.
- Sentinels: `ErrReadOnlyMissingFile`.
- Constants: `DefaultBusyTimeout`, `DefaultReadMaxOpenConns`, `DefaultDriverName`.

Package `txutil` (`github.com/hollis-labs/go-sqlite/txutil`):

- `TxOptions` — `Immediate`, `ReadOnly`.
- `Begin(ctx, db, opts)` / `BeginImmediate(ctx, db)` — open a transaction.
- `WithTx(ctx, db, opts, fn)` / `WithImmediate(ctx, db, fn)` — closure form. Commits on `nil`, rolls back on error/panic.
- `IsBusy(err)` / `IsLocked(err)` / `IsRetryableLock(err)` — lock-error classifiers.
- `RetryOptions` — `MaxAttempts`, `BaseDelay`, `MaxDelay`, `Jitter`, `IsRetryable`.
- `WithRetry(ctx, opts, fn)` / `WithImmediateRetry(ctx, db, opts, fn)` — bounded exponential backoff with optional jitter.
- `SavepointName(prefix)` / `WithSavepoint(ctx, tx, name, fn)` — savepoint helpers.
- Sentinels: `ErrInvalidSavepointName`.

## Architecture notes

**WAL ≠ concurrent writes.** WAL allows readers to proceed against the last consistent snapshot while a writer is active, but only one writer holds the database lock at a time. Two Go goroutines doing `BEGIN; INSERT; COMMIT` against separate pool connections still race for the writer lock, and the loser gets `SQLITE_BUSY` once `busy_timeout` expires.

**Single-writer pool is the portable fix.** Go's `database/sql` pool, plus `MaxOpenConns=1`, gives you an in-process queue for writers. Combined with `_txlock=immediate` on `BEGIN`, contention shows up as goroutines waiting on the connection mutex rather than `SQLITE_BUSY` errors. `sqlitekit.OpenWriter` is exactly this configuration.

**DSN pragmas vs `db.Exec("PRAGMA ...")`.** Pragmas that affect connection state — `journal_mode`, `busy_timeout`, `foreign_keys`, `synchronous`, `temp_store`, `mmap_size` — must be set on each connection. `db.Exec` only configures whichever connection happens to be checked out. modernc's `_pragma=` DSN parameter runs the pragma on every new connection the driver opens, which is what `sqlitekit.DSN` emits.

**Cross-process contention is out of scope.** If a daemon and an MCP server both open the same SQLite file, they each have their own in-process writer pool, and SQLite's file lock is the only thing serializing them. `busy_timeout` helps; `txutil.WithImmediate` makes contention show up at `BEGIN` time so it can be retried cleanly; `serialwrite` (still to come) does not help across processes.

**`BEGIN IMMEDIATE` vs deferred.** A default `BEGIN` is deferred: SQLite holds no lock until the first write statement. Two goroutines both `BEGIN`, both `SELECT`, both `INSERT` — and one of them gets `SQLITE_BUSY`, because the writer lock could not be acquired at write time. `BEGIN IMMEDIATE` acquires the writer lock at `BEGIN`, so the contender either gets the lock or blocks (up to `busy_timeout`) and then fails fast at `BEGIN` time. The failure mode is the same shape but happens before any work, which makes retry safe.

## Examples

Runnable end-to-end examples live in [`examples/`](examples/):

- [`examples/single`](examples/single) — `OpenSingle` with a small write+read loop.
- [`examples/split`](examples/split) — `OpenWriter` + `OpenReader` with concurrent fan-out writers.
- [`examples/inbox`](examples/inbox) — `txutil.WithImmediate` driving an atomic select-and-mark inbox claim.

Run from the repo root:

```bash
go run ./examples/single
go run ./examples/split
go run ./examples/inbox
```

## Testing

```bash
go test ./...
go test -race ./...
```

Tests use temporary directories and real SQLite databases via the pure-Go modernc driver, so no CGO toolchain or external SQLite install is required. No environment variables or fixtures.

## Roadmap

Upcoming packages:

- `serialwrite` — optional in-process serialized writer with batching/savepoints, for hot-write paths that benefit from coalescing.
- `sqlitequeue` — optional helper for opening a separate queue DB (or an ADR explaining why the existing openers are enough).

See [`CHANGELOG.md`](CHANGELOG.md) for what landed in each release.

## License

[MIT](LICENSE) © Hollis Labs.
