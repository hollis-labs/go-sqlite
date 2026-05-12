package sqlitekit

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenSingle_WritesAndReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")

	ctx := context.Background()
	db, err := OpenSingle(ctx, path, OpenOptions{})
	if err != nil {
		t.Fatalf("OpenSingle: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t (v) VALUES (?)`, "hello"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var got string
	if err := db.QueryRowContext(ctx, `SELECT v FROM t WHERE id=1`).Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != "hello" {
		t.Fatalf("unexpected value: %q", got)
	}

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("OpenSingle MaxOpenConns: got %d want 1", got)
	}
}

func TestOpenSingle_VerifiesPragmas(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")

	ctx := context.Background()
	db, err := OpenSingle(ctx, path, OpenOptions{})
	if err != nil {
		t.Fatalf("OpenSingle: %v", err)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Errorf("journal_mode: got %q want wal", journalMode)
	}

	var fk int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys: got %d want 1", fk)
	}

	var busyMs int
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyMs); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	wantBusyMs := int(DefaultBusyTimeout / time.Millisecond)
	if busyMs != wantBusyMs {
		t.Errorf("busy_timeout: got %d want %d", busyMs, wantBusyMs)
	}
}

func TestOpenWriter_ForcesSingleConnection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")

	ctx := context.Background()
	db, err := OpenWriter(ctx, path, OpenOptions{MaxOpenConns: 50, MaxIdleConns: 25})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer db.Close()

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("OpenWriter MaxOpenConns: got %d want 1", got)
	}
	// Max idle is not exposed through DBStats; verify behaviorally by ensuring
	// repeated round trips reuse the single connection.
	verifyMaxOneInUse(t, db)
}

func TestOpenReader_BoundedPoolDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")

	// Create the DB first so reader can open it.
	ctx := context.Background()
	single, err := OpenSingle(ctx, path, OpenOptions{})
	if err != nil {
		t.Fatalf("OpenSingle: %v", err)
	}
	if _, err := single.ExecContext(ctx, `CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	_ = single.Close()

	reader, err := OpenReader(ctx, path, OpenOptions{})
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer reader.Close()

	if got := reader.Stats().MaxOpenConnections; got != DefaultReadMaxOpenConns {
		t.Errorf("OpenReader MaxOpenConns: got %d want %d", got, DefaultReadMaxOpenConns)
	}
}

func TestOpenReader_RespectsCustomMaxOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	ctx := context.Background()
	if db, err := OpenSingle(ctx, path, OpenOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	} else {
		_ = db.Close()
	}

	reader, err := OpenReader(ctx, path, OpenOptions{MaxOpenConns: 9})
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer reader.Close()

	if got := reader.Stats().MaxOpenConnections; got != 9 {
		t.Errorf("OpenReader MaxOpenConns: got %d want 9", got)
	}
}

func TestOpenReadOnly_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.db")

	_, err := OpenReadOnly(context.Background(), path, OpenOptions{})
	if !errors.Is(err, ErrReadOnlyMissingFile) {
		t.Fatalf("expected ErrReadOnlyMissingFile, got %v", err)
	}
}

func TestOpenReadOnly_OverridesCallerMode(t *testing.T) {
	// A caller-supplied Mode like "rwc" must not turn OpenReadOnly into a
	// writable handle. OpenReadOnly is contractually read-only.
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	ctx := context.Background()

	writer, err := OpenSingle(ctx, path, OpenOptions{})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := writer.ExecContext(ctx, `CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = writer.Close()

	ro, err := OpenReadOnly(ctx, path, OpenOptions{
		Options: Options{Mode: "rwc"},
	})
	if err != nil {
		t.Fatalf("OpenReadOnly with caller Mode: %v", err)
	}
	defer ro.Close()

	if _, err := ro.ExecContext(ctx, `INSERT INTO t (id) VALUES (1)`); err == nil {
		t.Fatalf("OpenReadOnly accepted a write after caller supplied Mode=rwc")
	}
}

func TestOpenReadOnly_RefusesWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	ctx := context.Background()

	writer, err := OpenSingle(ctx, path, OpenOptions{})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := writer.ExecContext(ctx, `CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	_ = writer.Close()

	ro, err := OpenReadOnly(ctx, path, OpenOptions{})
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	if _, err := ro.ExecContext(ctx, `INSERT INTO t (id) VALUES (1)`); err == nil {
		t.Fatalf("read-only handle accepted a write")
	}
}

func TestCreateParentDir_CreatesNestedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "app.db")

	ctx := context.Background()
	db, err := OpenSingle(ctx, path, OpenOptions{CreateParentDir: true})
	if err != nil {
		t.Fatalf("OpenSingle with CreateParentDir: %v", err)
	}
	defer db.Close()

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
}

func TestCreateParentDir_DefaultFalseDoesNotCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "app.db")

	ctx := context.Background()
	_, err := OpenSingle(ctx, path, OpenOptions{})
	if err == nil {
		t.Fatalf("expected open to fail without CreateParentDir, got success")
	}
}

func TestOpenWriter_RelativePathWorksFromTempDir(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	ctx := context.Background()
	db, err := OpenWriter(ctx, "app.db", OpenOptions{})
	if err != nil {
		t.Fatalf("OpenWriter relative: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("write to relative path failed: %v", err)
	}
}

func TestOpenWriter_ConcurrentSubmittersDoNotRaceBusy(t *testing.T) {
	// With OpenWriter forcing MaxOpenConns=1, concurrent goroutines should
	// serialize cleanly and never see SQLITE_BUSY even under heavy contention.
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	ctx := context.Background()

	db, err := OpenWriter(ctx, path, OpenOptions{})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE events (id INTEGER PRIMARY KEY, payload TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	const writers = 16
	const each = 25

	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_, err := db.ExecContext(ctx, `INSERT INTO events (payload) VALUES (?)`, "w"+itoa(id))
				if err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != writers*each {
		t.Fatalf("expected %d rows, got %d", writers*each, n)
	}
}

// verifyMaxOneInUse opens many short-lived queries concurrently and asserts
// that the pool never has more than one connection in use at a time. This is
// a behavioral proxy for MaxOpenConns=1, since DBStats does not expose the
// configured MaxIdleConns.
func verifyMaxOneInUse(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	var wg sync.WaitGroup
	violations := make(chan int, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if _, err := db.ExecContext(ctx, `SELECT 1`); err != nil {
					violations <- -1
					return
				}
				if open := db.Stats().InUse; open > 1 {
					violations <- open
					return
				}
			}
		}()
	}
	wg.Wait()
	close(violations)
	for v := range violations {
		if v == -1 {
			t.Fatalf("query failed during contention probe")
		}
		t.Fatalf("InUse exceeded 1: %d", v)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
