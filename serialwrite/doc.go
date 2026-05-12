// Package serialwrite is an in-process write serializer for SQLite-backed Go
// apps that want to keep read pools open while routing correctness-critical
// writes through one transaction owner.
//
// The package exposes two implementations of the Writer interface:
//
//   - [Queue] runs a single worker goroutine that batches submitted ops into
//     one BEGIN IMMEDIATE transaction. Each op gets its own SAVEPOINT, so a
//     single failing op does not invalidate sibling ops in the same batch.
//     Best for hot write paths with many small writes per second.
//
//   - [Direct] runs each op synchronously in its own BEGIN IMMEDIATE
//     transaction with no batching or goroutine. Best for tests and apps
//     that want the [Writer] interface without lifecycle management.
//
// Both modes call [Submit] and block until the op has committed or failed.
//
// Use this package when [github.com/hollis-labs/go-sqlite/sqlitekit.OpenSingle]
// is not enough — typically when:
//
//   - the app keeps a separate read pool open and needs all writes to flow
//     through one owner;
//   - the app has bursty write traffic that benefits from batching multiple
//     ops into one fsync; or
//   - the app needs uniform retry / savepoint semantics across writers.
//
// Do not use this package for:
//
//   - low-write apps that can simply call [sqlitekit.OpenSingle] and use
//     [txutil.WithImmediate] directly;
//
//   - durable background work, queued retries across process restarts, or
//     cron-style scheduling — those need a persistent queue (see
//     github.com/hollis-labs/go-queue);
//
//   - cross-process serialization. serialwrite serializes within one Go
//     process. Two processes opening the same SQLite file each get their own
//     in-process serializer, and SQLite's file lock is the only thing
//     coordinating them. busy_timeout helps; serialwrite does not.
//
// The package depends on the stdlib and the sibling
// [github.com/hollis-labs/go-sqlite/txutil] package only.
package serialwrite
