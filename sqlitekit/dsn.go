package sqlitekit

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultBusyTimeout is the SQLite busy timeout applied by DefaultOptions.
	DefaultBusyTimeout = 5 * time.Second

	// DefaultReadMaxOpenConns is the default pool size for OpenReader.
	DefaultReadMaxOpenConns = 4

	// DefaultDriverName is the SQL driver name registered by modernc.org/sqlite.
	DefaultDriverName = "sqlite"

	defaultMMapSize         = int64(30_000_000_000) // 30 GB virtual mapping
	defaultJournalSizeLimit = int64(67_108_864)     // 64 MiB
	defaultSynchronous      = "NORMAL"
	defaultTempStore        = "memory"
)

// Options controls SQLite connection-level pragmas and DSN parameters emitted
// by DSN. Fields default to "unset" (zero value) so they can be composed
// against DefaultOptions / WriterOptions / ReaderOptions.
//
// A zero Options{} is intentionally minimal: only the busy timeout is emitted
// (using DefaultBusyTimeout). Callers should usually start from one of the
// preset constructors and tweak fields rather than build Options from scratch.
type Options struct {
	// BusyTimeout sets the connection-level busy timeout. Zero means use
	// DefaultBusyTimeout.
	BusyTimeout time.Duration

	// ForeignKeys enables the foreign_keys pragma when true.
	ForeignKeys bool

	// WAL enables journal_mode=WAL when true. Skip this for in-memory handles.
	WAL bool

	// Synchronous sets the synchronous pragma (e.g. "NORMAL", "FULL"). Empty
	// leaves the pragma unset.
	Synchronous string

	// TempStore sets temp_store (e.g. "memory", "file"). Empty leaves unset.
	TempStore string

	// MMapSize sets mmap_size in bytes. Zero or negative leaves the pragma unset.
	MMapSize int64

	// JournalSizeLimit sets journal_size_limit in bytes. Zero or negative leaves
	// the pragma unset.
	JournalSizeLimit int64

	// CacheKiB sets cache_size in kibibytes (the negative SQLite cache_size form).
	// Positive values are sent as negative ints to SQLite; zero leaves the
	// pragma unset.
	CacheKiB int

	// TxLock sets the _txlock DSN parameter ("deferred", "immediate",
	// "exclusive"). "immediate" is the writer-pool recommendation: it acquires
	// the writer lock at BEGIN time, so concurrent writers serialize cleanly
	// instead of upgrading mid-transaction and racing on SQLITE_BUSY.
	TxLock string

	// ReadOnly opens the database in mode=ro. The handle will reject writes
	// and will not create the file.
	ReadOnly bool

	// Mode overrides the SQLite mode query parameter (e.g. "ro", "rw", "rwc",
	// "memory"). If empty, ReadOnly determines the value. Set this explicitly
	// only when you need fine-grained control beyond ReadOnly.
	Mode string
}

// DefaultOptions returns options suitable for typical app databases:
// WAL, foreign keys on, busy timeout 5s, synchronous NORMAL, temp_store
// memory, mmap_size 30 GB, journal_size_limit 64 MiB.
func DefaultOptions() Options {
	return Options{
		BusyTimeout:      DefaultBusyTimeout,
		ForeignKeys:      true,
		WAL:              true,
		Synchronous:      defaultSynchronous,
		TempStore:        defaultTempStore,
		MMapSize:         defaultMMapSize,
		JournalSizeLimit: defaultJournalSizeLimit,
	}
}

// WriterOptions returns DefaultOptions with TxLock=immediate. Use for the
// writer side of a split read/write pool.
func WriterOptions() Options {
	o := DefaultOptions()
	o.TxLock = "immediate"
	return o
}

// ReaderOptions returns DefaultOptions without TxLock. Use for the read side
// of a split pool, or for read-mostly handles.
func ReaderOptions() Options { return DefaultOptions() }

// DSN returns a modernc.org/sqlite DSN string for path applying opts as
// per-connection pragmas and DSN parameters.
//
// Relative paths use the "file:foo.db?..." form. The authority form
// ("file://foo.db?...") breaks modernc on first write against a real file.
// Absolute paths use the "file:///abs/path?..." form.
//
// In-memory handles (path ":memory:") return the path unchanged with the
// query appended, since URI parsing on the bare token is what modernc expects.
func DSN(path string, opts Options) string {
	q := url.Values{}

	if opts.WAL {
		q.Add("_pragma", "journal_mode(WAL)")
	}

	busyMs := int(opts.BusyTimeout / time.Millisecond)
	if busyMs <= 0 {
		busyMs = int(DefaultBusyTimeout / time.Millisecond)
	}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyMs))

	if opts.ForeignKeys {
		q.Add("_pragma", "foreign_keys(1)")
	}
	if opts.Synchronous != "" {
		q.Add("_pragma", "synchronous("+opts.Synchronous+")")
	}
	if opts.TempStore != "" {
		q.Add("_pragma", "temp_store("+opts.TempStore+")")
	}
	if opts.MMapSize > 0 {
		q.Add("_pragma", fmt.Sprintf("mmap_size(%d)", opts.MMapSize))
	}
	if opts.JournalSizeLimit > 0 {
		q.Add("_pragma", fmt.Sprintf("journal_size_limit(%d)", opts.JournalSizeLimit))
	}
	if opts.CacheKiB != 0 {
		v := opts.CacheKiB
		if v > 0 {
			v = -v
		}
		q.Add("_pragma", fmt.Sprintf("cache_size(%d)", v))
	}
	if opts.TxLock != "" {
		q.Set("_txlock", opts.TxLock)
	}

	mode := opts.Mode
	if mode == "" && opts.ReadOnly {
		mode = "ro"
	}
	if mode != "" {
		q.Set("mode", mode)
	}

	raw := q.Encode()

	if isMemoryPath(path) {
		if raw == "" {
			return path
		}
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		return path + sep + raw
	}

	if filepath.IsAbs(path) {
		u := &url.URL{Scheme: "file", Path: path, RawQuery: raw}
		return u.String()
	}

	escaped := (&url.URL{Path: path}).EscapedPath()
	if raw == "" {
		return "file:" + escaped
	}
	return "file:" + escaped + "?" + raw
}

func isMemoryPath(path string) bool {
	if path == ":memory:" {
		return true
	}
	return strings.HasPrefix(path, "file::memory:")
}
