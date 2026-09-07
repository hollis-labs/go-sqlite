# go-sqlite

A SQLite concurrency toolkit for Go apps on `database/sql` with the pure-Go
`modernc.org/sqlite` driver, aimed squarely at the "we enabled WAL and
`busy_timeout` and still get `SQLITE_BUSY`" failure mode. It ships DSN and
opener defaults, transaction helpers, and an in-process write serializer. It is
not an ORM, a migration tool or a query builder.

## Start Here

- `README.md`'s "Why this package exists" explains the three causes of
  `SQLITE_BUSY`, and is the reasoning behind every default here.
- `sqlitekit/` owns `Options`, `DSN` and the four named openers.
- `txutil/` owns BEGIN IMMEDIATE, lock retry and savepoints.
- `serialwrite/` owns the in-process write serializer.
- `docs/adr/0001-defer-sqlitequeue.md` records why `sqlitequeue` was deferred
  and the recommended queue-DB opener idiom.
- `examples/single`, `examples/split`, `examples/serialwrite` and
  `examples/inbox` are the four runnable shapes.

## Commands

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

No CGO toolchain is needed. There is no CI workflow or Makefile in this repo.

## Boundaries

Pragmas are emitted as DSN `_pragma=` parameters, never as a startup
`db.Exec("PRAGMA ...")`. Pragmas are per-connection, so an `Exec` at startup
configures only the first pooled connection and silently leaves the rest on
defaults — that is bug one of the three the README names, and the DSN tests
pin the emitted string.

`OpenWriter` forces `MaxOpenConns=1` and defaults `_txlock=immediate`. Both are
load-bearing: a writer pool larger than one reintroduces `SQLITE_BUSY` under
contention, and deferred `BEGIN` does not take the writer lock until the first
write, so two read-then-write transactions race
(`TestBeginImmediate_AcquiresWriterLockAtBeginTime`,
`TestBeginImmediate_DeferredComparison`). Readers use a separate `OpenReader`
handle rather than widening the writer pool.

DSN construction is deterministic, including pragma ordering
(`TestDSN_PragmaOrderingDeterministic`), and paths are encoded carefully —
absolute paths as file URLs, relative as `file:` form, with special characters
escaped. Those cases are tested individually because a mis-encoded DSN opens
the wrong database rather than failing.

`IsBusy` and `IsLocked` must return false for non-SQLite errors
(`TestIsBusyAndIsLocked_ReturnFalseForNonSqliteErrors`). A broad match there
turns unrelated failures into silent retries.
