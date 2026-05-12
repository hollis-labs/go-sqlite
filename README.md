# go-sqlite

SQLite concurrency toolkit for Go apps that use `database/sql` with [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite).

This repo is being populated package by package. See open pull requests for incoming work, and [`CHANGELOG.md`](CHANGELOG.md) (added by the first feature PR) for release history.

Planned packages:

- `sqlitekit` — DSN construction and `database/sql` opener defaults.
- `txutil` — `BEGIN IMMEDIATE`, lock-error classification, retry/backoff, savepoints.
- `serialwrite` — optional in-process serialized writer.
- `sqlitequeue` — optional helper for opening a separate SQLite queue DB (or an ADR explaining deferral).

## License

[MIT](LICENSE) © Hollis Labs.
