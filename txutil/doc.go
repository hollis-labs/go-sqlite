// Package txutil provides explicit SQLite write-transaction helpers, lock-error
// classification, retry/backoff, and savepoint support for Go apps using
// modernc.org/sqlite.
//
// The package solves two recurring failure modes:
//
//  1. Read-modify-write paths that BEGIN deferred and race for the writer
//     lock at first write. Use [WithImmediate] to force BEGIN IMMEDIATE so
//     contention shows up at BEGIN time and contends through busy_timeout.
//
//  2. Transient SQLITE_BUSY / SQLITE_LOCKED returned after busy_timeout
//     expires. [IsRetryableLock] classifies these errors; [WithRetry] and
//     [WithImmediateRetry] retry with bounded exponential backoff and
//     optional jitter, respecting ctx.Done() between attempts.
//
// Savepoints inside a writer transaction are supported via [WithSavepoint] and
// the unique-name helper [SavepointName].
//
// Important: the BEGIN IMMEDIATE behavior requires the *sql.DB to be opened
// with the modernc DSN parameter _txlock=immediate. Opening through
// sqlitekit.WriterOptions / sqlitekit.OpenWriter / sqlitekit.OpenSingle (with
// TxLock="immediate") already sets this. If the DSN is not configured for
// immediate, the underlying BEGIN falls back to DEFERRED and writer-lock
// acquisition is delayed until the first write statement, defeating the
// purpose of WithImmediate.
package txutil
