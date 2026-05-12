// Package gosqlite is the module root for github.com/hollis-labs/go-sqlite.
//
// The module ships small, focused sub-packages that solve the recurring
// "WAL is enabled but Go writers still hit SQLITE_BUSY" failure mode:
//
//   - sqlitekit:   DSN construction and database/sql opener defaults.
//   - txutil:      BEGIN IMMEDIATE / lock classification / retry / savepoints.
//   - serialwrite: in-process write serializer with batching and savepoints.
//
// A proposed sqlitequeue wrapper was deferred — apps should open queue DBs
// with sqlitekit.OpenSingle and pass the *sql.DB to their queue driver of
// choice. See docs/adr/0001-defer-sqlitequeue.md for the rationale.
//
// See README.md for the package landscape and usage patterns.
package gosqlite
