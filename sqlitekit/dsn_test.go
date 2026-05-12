package sqlitekit

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDSN_RelativePathUsesFileColonForm(t *testing.T) {
	dsn := DSN("app.db", WriterOptions())

	if !strings.HasPrefix(dsn, "file:app.db?") {
		t.Fatalf("relative dsn prefix mismatch: %q", dsn)
	}
	if strings.HasPrefix(dsn, "file://") {
		t.Fatalf("relative dsn must not use authority form: %q", dsn)
	}
}

func TestDSN_RelativePathWithSubdir(t *testing.T) {
	dsn := DSN("data/app.db", DefaultOptions())
	if !strings.HasPrefix(dsn, "file:data/app.db?") {
		t.Fatalf("relative subdir dsn prefix mismatch: %q", dsn)
	}
}

func TestDSN_AbsolutePathUsesFileURL(t *testing.T) {
	dsn := DSN("/var/lib/app/app.db", WriterOptions())
	if !strings.HasPrefix(dsn, "file:///var/lib/app/app.db?") {
		t.Fatalf("absolute dsn prefix mismatch: %q", dsn)
	}
}

func TestDSN_TxLockOnlyWhenRequested(t *testing.T) {
	withLock := DSN("app.db", WriterOptions())
	if !strings.Contains(withLock, "_txlock=immediate") {
		t.Fatalf("writer dsn missing _txlock=immediate: %q", withLock)
	}

	noLock := DSN("app.db", ReaderOptions())
	if strings.Contains(noLock, "_txlock") {
		t.Fatalf("reader dsn must not set _txlock: %q", noLock)
	}
}

func TestDSN_BusyTimeoutDefault(t *testing.T) {
	dsn := DSN("app.db", Options{})
	if !strings.Contains(dsn, "busy_timeout%285000%29") {
		t.Fatalf("default busy_timeout missing or wrong in %q", dsn)
	}
}

func TestDSN_BusyTimeoutOverride(t *testing.T) {
	dsn := DSN("app.db", Options{BusyTimeout: 250 * time.Millisecond})
	if !strings.Contains(dsn, "busy_timeout%28250%29") {
		t.Fatalf("override busy_timeout missing in %q", dsn)
	}
}

func TestDSN_ForeignKeysIncludedWhenSet(t *testing.T) {
	dsn := DSN("app.db", DefaultOptions())
	if !strings.Contains(dsn, "foreign_keys%281%29") {
		t.Fatalf("foreign_keys pragma missing in default options dsn: %q", dsn)
	}

	dsn = DSN("app.db", Options{})
	if strings.Contains(dsn, "foreign_keys") {
		t.Fatalf("foreign_keys pragma must be absent when not requested: %q", dsn)
	}
}

func TestDSN_WALOnlyWhenSet(t *testing.T) {
	dsn := DSN("app.db", DefaultOptions())
	if !strings.Contains(dsn, "journal_mode%28WAL%29") {
		t.Fatalf("WAL pragma missing in default options dsn: %q", dsn)
	}

	dsn = DSN("app.db", Options{})
	if strings.Contains(dsn, "journal_mode") {
		t.Fatalf("WAL pragma must be absent when not requested: %q", dsn)
	}
}

func TestDSN_CacheKiBNegated(t *testing.T) {
	dsn := DSN("app.db", Options{CacheKiB: 64000})
	if !strings.Contains(dsn, "cache_size%28-64000%29") {
		t.Fatalf("expected negative cache_size in %q", dsn)
	}

	dsn = DSN("app.db", Options{CacheKiB: -64000})
	if !strings.Contains(dsn, "cache_size%28-64000%29") {
		t.Fatalf("already-negative cache_size should pass through: %q", dsn)
	}

	dsn = DSN("app.db", Options{})
	if strings.Contains(dsn, "cache_size") {
		t.Fatalf("cache_size must be absent when CacheKiB=0: %q", dsn)
	}
}

func TestDSN_ReadOnlyModeQueryParam(t *testing.T) {
	dsn := DSN("app.db", Options{ReadOnly: true})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if got := parsed.Query().Get("mode"); got != "ro" {
		t.Fatalf("expected mode=ro, got %q (dsn=%q)", got, dsn)
	}
}

func TestDSN_ModeOverridesReadOnly(t *testing.T) {
	dsn := DSN("app.db", Options{ReadOnly: true, Mode: "memory"})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if got := parsed.Query().Get("mode"); got != "memory" {
		t.Fatalf("explicit Mode must win, got %q", got)
	}
}

func TestDSN_SpecialCharactersInPath(t *testing.T) {
	dsn := DSN("data/app with space.db", DefaultOptions())
	if !strings.HasPrefix(dsn, "file:data/app%20with%20space.db?") {
		t.Fatalf("space in path not escaped: %q", dsn)
	}
}

func TestDSN_MemoryPath(t *testing.T) {
	dsn := DSN(":memory:", DefaultOptions())
	if !strings.HasPrefix(dsn, ":memory:?") {
		t.Fatalf("memory path prefix mismatch: %q", dsn)
	}
	if !strings.Contains(dsn, "journal_mode%28WAL%29") {
		t.Fatalf("memory path should still emit pragmas (caller's choice): %q", dsn)
	}
}

func TestDSN_FileMemoryPath(t *testing.T) {
	dsn := DSN("file::memory:?cache=shared", DefaultOptions())
	// Path already carries a "?cache=shared" query; sqlitekit must append
	// further params with "&", not a second "?". Double-"?" would make modernc
	// fold the rest of the query into cache=shared's value and silently drop
	// the pragmas.
	if !strings.HasPrefix(dsn, "file::memory:?cache=shared&") {
		t.Fatalf("file memory path mismatch: %q", dsn)
	}
	if strings.Count(dsn, "?") != 1 {
		t.Fatalf("file memory path must contain exactly one '?': %q", dsn)
	}
	if !strings.Contains(dsn, "journal_mode%28WAL%29") {
		t.Fatalf("pragmas missing from file memory dsn: %q", dsn)
	}
}

func TestDSN_PragmaOrderingDeterministic(t *testing.T) {
	a := DSN("app.db", DefaultOptions())
	b := DSN("app.db", DefaultOptions())
	if a != b {
		t.Fatalf("DSN must be deterministic for equal options:\n a=%q\n b=%q", a, b)
	}
}

func TestDefaultOptions_HasExpectedValues(t *testing.T) {
	o := DefaultOptions()
	if o.BusyTimeout != DefaultBusyTimeout {
		t.Errorf("BusyTimeout: got %v want %v", o.BusyTimeout, DefaultBusyTimeout)
	}
	if !o.WAL {
		t.Errorf("WAL: want true")
	}
	if !o.ForeignKeys {
		t.Errorf("ForeignKeys: want true")
	}
	if o.Synchronous != "NORMAL" {
		t.Errorf("Synchronous: got %q want NORMAL", o.Synchronous)
	}
	if o.TempStore != "memory" {
		t.Errorf("TempStore: got %q want memory", o.TempStore)
	}
}

func TestWriterOptions_AddsImmediateTxLock(t *testing.T) {
	o := WriterOptions()
	if o.TxLock != "immediate" {
		t.Errorf("TxLock: got %q want immediate", o.TxLock)
	}
}

func TestReaderOptions_NoTxLock(t *testing.T) {
	o := ReaderOptions()
	if o.TxLock != "" {
		t.Errorf("TxLock: got %q want empty", o.TxLock)
	}
}
