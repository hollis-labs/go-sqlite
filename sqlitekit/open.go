package sqlitekit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// OpenOptions configures opener behavior on top of Options.
//
// Open* functions force certain fields for their flavour (writer/single force
// MaxOpenConns=1 and TxLock=immediate; read-only forces ReadOnly=true) so a
// zero OpenOptions is a safe starting point.
type OpenOptions struct {
	// Options controls per-connection pragmas and DSN parameters. If Options
	// is the zero value, the Open* function fills in flavour-appropriate
	// defaults (WriterOptions / ReaderOptions / DefaultOptions).
	Options

	// DriverName overrides the registered SQL driver name. Defaults to
	// DefaultDriverName ("sqlite") for modernc.org/sqlite.
	DriverName string

	// MaxOpenConns sets the connection pool max. OpenWriter, OpenSingle, and
	// OpenReadOnly's pool override this with their own flavour defaults; see
	// each function's docs.
	MaxOpenConns int

	// MaxIdleConns sets the idle pool max. If zero, defaults to MaxOpenConns.
	MaxIdleConns int

	// ConnMaxLifetime caps how long a pooled connection may be reused. Zero
	// leaves it unbounded.
	ConnMaxLifetime time.Duration

	// CreateParentDir creates the parent directory of path before opening when
	// true. Ignored for in-memory handles.
	CreateParentDir bool
}

// ErrReadOnlyMissingFile is returned by OpenReadOnly when the database file
// does not exist. Read-only handles refuse to create the database.
var ErrReadOnlyMissingFile = errors.New("sqlitekit: read-only opener requires existing database file")

// OpenWriter opens a single-connection writer pool with _txlock=immediate.
// Pair with OpenReader to serve concurrent reads without writer-pool
// contention.
//
// MaxOpenConns and MaxIdleConns are forced to 1 regardless of the values in
// opts; SQLite serializes writes anyway, and a single connection makes
// SQLITE_BUSY impossible from inside this process.
func OpenWriter(ctx context.Context, path string, opts OpenOptions) (*sql.DB, error) {
	if isZeroOptions(opts.Options) {
		opts.Options = WriterOptions()
	} else if opts.TxLock == "" {
		opts.TxLock = "immediate"
	}
	opts.MaxOpenConns = 1
	opts.MaxIdleConns = 1
	return openInternal(ctx, path, opts)
}

// OpenReader opens a bounded pool for concurrent reads (no _txlock). The
// handle is technically still writable; use OpenWriter or OpenSingle for write
// traffic.
//
// If opts.MaxOpenConns is zero, it defaults to DefaultReadMaxOpenConns.
func OpenReader(ctx context.Context, path string, opts OpenOptions) (*sql.DB, error) {
	if isZeroOptions(opts.Options) {
		opts.Options = ReaderOptions()
	}
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = DefaultReadMaxOpenConns
	}
	return openInternal(ctx, path, opts)
}

// OpenSingle opens a single-connection pool for apps that intentionally
// serialize all DB access through one handle. MaxOpenConns/MaxIdleConns are
// forced to 1.
func OpenSingle(ctx context.Context, path string, opts OpenOptions) (*sql.DB, error) {
	if isZeroOptions(opts.Options) {
		opts.Options = DefaultOptions()
	}
	opts.MaxOpenConns = 1
	opts.MaxIdleConns = 1
	return openInternal(ctx, path, opts)
}

// OpenReadOnly opens a bounded pool in mode=ro that will not create the
// database file. Returns ErrReadOnlyMissingFile if path does not exist.
//
// WAL is disabled on the DSN: mode=ro cannot create the -wal/-shm sidecar
// files. busy_timeout still applies for blocking on writers held by another
// process.
func OpenReadOnly(ctx context.Context, path string, opts OpenOptions) (*sql.DB, error) {
	if !isMemoryPath(path) {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, ErrReadOnlyMissingFile
			}
			return nil, fmt.Errorf("sqlitekit: stat %q: %w", path, err)
		}
	}
	if isZeroOptions(opts.Options) {
		opts.Options = DefaultOptions()
	}
	opts.ReadOnly = true
	opts.WAL = false
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = DefaultReadMaxOpenConns
	}
	return openInternal(ctx, path, opts)
}

func openInternal(ctx context.Context, path string, opts OpenOptions) (*sql.DB, error) {
	if opts.CreateParentDir && !isMemoryPath(path) {
		dir := filepath.Dir(path)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("sqlitekit: create parent dir %q: %w", dir, err)
			}
		}
	}

	driver := opts.DriverName
	if driver == "" {
		driver = DefaultDriverName
	}

	db, err := sql.Open(driver, DSN(path, opts.Options))
	if err != nil {
		return nil, fmt.Errorf("sqlitekit: open %q: %w", path, err)
	}
	if opts.MaxOpenConns > 0 {
		db.SetMaxOpenConns(opts.MaxOpenConns)
	}
	idle := opts.MaxIdleConns
	if idle <= 0 && opts.MaxOpenConns > 0 {
		idle = opts.MaxOpenConns
	}
	if idle > 0 {
		db.SetMaxIdleConns(idle)
	}
	if opts.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(opts.ConnMaxLifetime)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlitekit: ping %q: %w", path, err)
	}
	return db, nil
}

func isZeroOptions(o Options) bool { return o == Options{} }
