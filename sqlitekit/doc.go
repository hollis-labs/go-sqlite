// Package sqlitekit constructs SQLite DSNs and database/sql handles with
// safe defaults for Go apps using modernc.org/sqlite.
//
// The package solves three problems that recur across SQLite-backed Go apps:
//
//  1. DSN construction. SQLite URI parsing is fussy. Relative paths must use
//     "file:foo.db?...", not "file://foo.db?...", or modernc will fail on the
//     first write. sqlitekit.DSN handles both forms correctly.
//
//  2. Per-connection invariants. PRAGMAs applied at the *sql.DB pool level
//     are not guaranteed to run on every connection. sqlitekit emits WAL,
//     busy_timeout, foreign_keys, synchronous, temp_store, mmap_size, and
//     journal_size_limit as DSN "_pragma" parameters so modernc applies them
//     when each connection is opened.
//
//  3. Pool policy. WAL allows concurrent readers, not concurrent writers.
//     OpenWriter forces a single-connection writer pool with _txlock=immediate;
//     OpenReader returns a bounded pooled handle; OpenSingle is for apps that
//     serialize all access through one handle.
//
// Typical single-handle use:
//
//	db, err := sqlitekit.OpenSingle(ctx, "app.db", sqlitekit.OpenOptions{
//	    CreateParentDir: true,
//	})
//
// Split read/write pools:
//
//	writer, err := sqlitekit.OpenWriter(ctx, "app.db", sqlitekit.OpenOptions{})
//	reader, err := sqlitekit.OpenReader(ctx, "app.db", sqlitekit.OpenOptions{
//	    MaxOpenConns: 4,
//	})
//
// Read-only consumer:
//
//	ro, err := sqlitekit.OpenReadOnly(ctx, "app.db", sqlitekit.OpenOptions{})
//
// Non-goals: this package is not a migration framework, ORM, or app-specific
// store wrapper. It is the layer beneath those.
package sqlitekit
