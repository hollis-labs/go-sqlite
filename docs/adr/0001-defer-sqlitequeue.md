# ADR 0001: Defer the `sqlitequeue` package

- **Status:** Accepted
- **Date:** 2026-05-12
- **Decision phase:** [Sprint pack — Task 04](https://github.com/hollis-labs/go-sqlite/blob/main/README.md), originated in `agent-workspaces/execution/shared-go/sqlite-concurrency/2026-05-12/`.

## Context

The original sprint pack scoped a fourth package, `sqlitequeue`, intended to
provide:

1. An opinionated opener for SQLite-backed queue databases (`OpenDB`).
2. Optionally, a bridge that wires a `go-queue` SQLite driver onto a DB opened
   by this module.

The motivation was that Clockwork keeps a separate SQLite DB for telemetry-class
writes (`internal/persistence/writequeue`), and Vanta Conduit uses
`hollis-labs/go-queue` for embedding jobs but opens its queue DB without the
same WAL / busy-timeout / pool policy that `sqlitekit` enforces for the main
app DB. A small helper could prevent the queue DB from becoming the next
contention source.

## Decision

**Defer the `sqlitequeue` package.** Apps should open queue DBs directly with
`sqlitekit.OpenSingle` (or `OpenWriter`) and, when using `hollis-labs/go-queue`,
hand the resulting `*sql.DB` to `go-queue/driver/sqlite.New`. The bridge does
not need to live in this module.

## Rationale

### The `OpenDB` half adds no value

The original sketch was:

```go
type Options struct {
    Open sqlitekit.OpenOptions
}

func OpenDB(ctx context.Context, path string, opts Options) (*sql.DB, error) {
    return sqlitekit.OpenSingle(ctx, path, opts.Open)
}
```

That is a one-line passthrough to an existing public API. It does not centralize
any policy that `sqlitekit.OpenSingle` does not already centralize:

- WAL: `sqlitekit.DefaultOptions` sets `WAL=true`.
- Busy timeout: `DefaultBusyTimeout` = 5 s.
- Foreign keys / synchronous / temp_store / mmap_size / journal_size_limit:
  all in `DefaultOptions`.
- Bounded pool: `OpenSingle` forces `MaxOpenConns=1`/`MaxIdleConns=1`, which
  is the writer-friendly default for a queue DB.
- Parent directory creation: `OpenOptions.CreateParentDir` is opt-in via the
  caller, which matches the queue-DB use case.

Adding a thin re-export reduces clarity (consumers now have two ways to do the
same thing) and increases the API surface to maintain across `v0.x` releases.

### The `NewSQLiteDriver` half couples modules

The optional second helper would have wrapped `go-queue/driver/sqlite.New`:

```go
import qsqlite "github.com/hollis-labs/go-queue/driver/sqlite"

func NewSQLiteDriver(db *sql.DB, opts qsqlite.Opts) (queue.Queue, error)
```

This makes `go-sqlite` depend on `go-queue`. Every consumer of just `sqlitekit`
or `txutil` would then pull `go-queue` (and its transitive graph) into their
dependency tree. The portfolio's two SQLite-leaning libraries would also become
co-versioned in a way that complicates upgrades — a breaking change in
`go-queue`'s driver API would block `go-sqlite` releases that have nothing to
do with queues.

The dependency direction is also wrong. `go-queue` already accepts an
externally-opened `*sql.DB` in its constructor; it does not need a wrapper that
opens the DB for it. The single-line idiom:

```go
db, err := sqlitekit.OpenSingle(ctx, "queue.db", sqlitekit.OpenOptions{
    CreateParentDir: true,
})
if err != nil {
    return err
}
q, err := qsqlite.New(db, qsqlite.Opts{})
```

is shorter than the proposed `NewSQLiteDriver(db, opts)` plus its own surface
on top.

### Apps already have what they need

Each named consumer can adopt the documented pattern without a new package:

- **Clockwork** `internal/persistence/writequeue`: replace its own DSN
  construction with `sqlitekit.OpenSingle`. The current implementation already
  uses `appdb.SQLiteDSN`, which is the reference for `sqlitekit.DSN`; the
  migration is a substitution.
- **Vanta Conduit** `cmd/contextd/memory_subsystem.go`: open the embedding /
  decay queue DB with `sqlitekit.OpenSingle`, then pass it to `qsqlite.New`
  as today. No new opener needed.

## Consequences

### What this does

- Keeps `go-sqlite` to three packages — `sqlitekit`, `txutil`, `serialwrite` —
  with no dependency on `go-queue`.
- Clarifies the recommended idiom in README and consumer notes: open the queue
  DB with `sqlitekit.OpenSingle`, hand the `*sql.DB` to whatever queue driver
  the app uses.
- Leaves the option open: if a consumer later needs cross-cutting policy
  beyond what `sqlitekit` exposes (e.g., custom checkpoint cadence, queue-DB
  observability hooks), a future ADR can reverse this decision.

### What this does not do

- Does not block adoption — the recommended idiom is already supported by the
  existing public API.
- Does not deprecate any existing call site. Clockwork's internal writequeue
  can migrate to `sqlitekit.OpenSingle` on its own schedule.

## Revisit if

- A second consumer beyond `go-queue` needs the same opener policy *and*
  cannot use `sqlitekit.OpenSingle` directly.
- Cross-cutting telemetry / metrics hooks for queue DBs become a portfolio
  concern (e.g., uniform `Stats()` across queue back-ends).
- Adding a per-driver registration shim becomes worthwhile (e.g., uniform
  schema migration before `qsqlite.New` runs its own `CREATE TABLE`).

None of these are active concerns as of this ADR.

## References

- Sprint pack: `agent-workspaces/execution/shared-go/sqlite-concurrency/2026-05-12/tasks/04-sqlitequeue.md`.
- Reference implementations: Clockwork `internal/persistence/writequeue/writequeue.go`; Vanta `cmd/contextd/memory_subsystem.go`.
- `go-queue` SQLite driver: `github.com/hollis-labs/go-queue/driver/sqlite`.
- Relevant `go-sqlite` packages: `sqlitekit.OpenSingle`, `sqlitekit.OpenOptions`.
