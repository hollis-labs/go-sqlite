package txutil_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/go-sqlite/sqlitekit"
	"github.com/hollis-labs/go-sqlite/txutil"
)

// openWriter opens a single-connection writer pool via sqlitekit so the DSN
// carries _txlock=immediate.
func openWriter(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sqlitekit.OpenWriter(context.Background(), path, sqlitekit.OpenOptions{})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openImmediateMulti opens a DB with TxLock=immediate but a multi-connection
// pool. Two BeginImmediate calls will then check out separate connections and
// race for SQLite's writer lock at the file level — used to prove that BEGIN
// is IMMEDIATE (locks at BEGIN time, not on first write).
func openImmediateMulti(t *testing.T, path string, busyTimeout time.Duration) *sql.DB {
	t.Helper()
	opts := sqlitekit.OpenOptions{
		Options: sqlitekit.WriterOptions(),
	}
	opts.Options.BusyTimeout = busyTimeout
	// Bypass the single-connection forcing of OpenWriter by going through Open*
	// with a manual MaxOpenConns. We need a generic opener; sqlitekit does not
	// expose one, so emit the DSN and let database/sql build the pool.
	dsn := sqlitekit.DSN(path, opts.Options)
	db, err := sql.Open(sqlitekit.DefaultDriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE IF NOT EXISTS items (id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL, v INTEGER NOT NULL DEFAULT 0)`,
	); err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func TestWithImmediate_CommitsOnNilError(t *testing.T) {
	dir := t.TempDir()
	db := openWriter(t, filepath.Join(dir, "app.db"))
	createTable(t, db)

	ctx := context.Background()
	err := txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "alpha")
		return err
	})
	if err != nil {
		t.Fatalf("WithImmediate: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row after commit, got %d", n)
	}
}

func TestWithImmediate_RollsBackOnError(t *testing.T) {
	dir := t.TempDir()
	db := openWriter(t, filepath.Join(dir, "app.db"))
	createTable(t, db)

	sentinel := errors.New("intentional failure")

	ctx := context.Background()
	err := txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "alpha"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 rows after rollback, got %d", n)
	}
}

func TestWithImmediate_RollsBackOnPanic(t *testing.T) {
	dir := t.TempDir()
	db := openWriter(t, filepath.Join(dir, "app.db"))
	createTable(t, db)

	ctx := context.Background()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic to propagate")
		}
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
			t.Fatalf("count after panic: %v", err)
		}
		if n != 0 {
			t.Fatalf("expected 0 rows after panic+rollback, got %d", n)
		}
	}()

	_ = txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
		_, _ = tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "alpha")
		panic("boom")
	})
}

func TestBeginImmediate_AcquiresWriterLockAtBeginTime(t *testing.T) {
	// Proof of BEGIN IMMEDIATE: with two connections sharing one file under
	// _txlock=immediate and a short busy_timeout, the second BEGIN must fail
	// with SQLITE_BUSY before any write statement is issued. Under BEGIN
	// DEFERRED (the alternative) both BEGINs would succeed and the second
	// would only fail when it actually tried to write.
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")

	// Seed the schema.
	seed := openWriter(t, path)
	createTable(t, seed)
	_ = seed.Close()

	db := openImmediateMulti(t, path, 50*time.Millisecond)

	ctx := context.Background()
	tx1, err := txutil.BeginImmediate(ctx, db)
	if err != nil {
		t.Fatalf("first BeginImmediate: %v", err)
	}
	defer tx1.Rollback()

	start := time.Now()
	_, err = txutil.BeginImmediate(ctx, db)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("second BeginImmediate succeeded; expected SQLITE_BUSY (proof of IMMEDIATE acquisition at BEGIN time)")
	}
	if !txutil.IsBusy(err) && !txutil.IsLocked(err) {
		t.Fatalf("expected SQLITE_BUSY/LOCKED, got %v", err)
	}
	// The second BEGIN should have blocked for roughly the busy_timeout (50ms)
	// before returning, since SQLite waits before giving up. Allow generous
	// slack.
	if elapsed < 30*time.Millisecond {
		t.Fatalf("second BEGIN returned too fast (%v) — busy_timeout may not be applying", elapsed)
	}
}

func TestBeginImmediate_DeferredComparison(t *testing.T) {
	// Counter-example: opening WITHOUT _txlock=immediate makes two BEGINs
	// succeed because SQLite uses BEGIN DEFERRED. This isolates the difference.
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")

	// Seed.
	seed := openWriter(t, path)
	createTable(t, seed)
	_ = seed.Close()

	// Open without _txlock=immediate.
	opts := sqlitekit.DefaultOptions()
	opts.BusyTimeout = 50 * time.Millisecond
	dsn := sqlitekit.DSN(path, opts)
	db, err := sql.Open(sqlitekit.DefaultDriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	ctx := context.Background()
	tx1, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	defer tx1.Rollback()

	tx2, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("second Begin (deferred) should succeed but got %v", err)
	}
	_ = tx2.Rollback()
}

func TestBeginImmediate_HonorsContextCancel(t *testing.T) {
	dir := t.TempDir()
	db := openWriter(t, filepath.Join(dir, "app.db"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := txutil.BeginImmediate(ctx, db)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestIsBusyAndIsLocked_ReturnFalseForNonSqliteErrors(t *testing.T) {
	if txutil.IsBusy(nil) {
		t.Errorf("IsBusy(nil) = true")
	}
	if txutil.IsLocked(nil) {
		t.Errorf("IsLocked(nil) = true")
	}
	if txutil.IsBusy(errors.New("not a sqlite error")) {
		t.Errorf("IsBusy on plain error = true")
	}
	if txutil.IsLocked(errors.New("not a sqlite error")) {
		t.Errorf("IsLocked on plain error = true")
	}
	if txutil.IsRetryableLock(nil) {
		t.Errorf("IsRetryableLock(nil) = true")
	}
}

func TestIsBusy_RecognizesRealContention(t *testing.T) {
	// Generate a real SQLITE_BUSY via two-connection contention and verify
	// IsBusy classifies it.
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	seed := openWriter(t, path)
	createTable(t, seed)
	_ = seed.Close()

	db := openImmediateMulti(t, path, 20*time.Millisecond)
	ctx := context.Background()

	tx1, err := txutil.BeginImmediate(ctx, db)
	if err != nil {
		t.Fatalf("first BeginImmediate: %v", err)
	}
	defer tx1.Rollback()

	_, err = txutil.BeginImmediate(ctx, db)
	if err == nil {
		t.Fatal("expected SQLITE_BUSY")
	}
	if !txutil.IsBusy(err) && !txutil.IsLocked(err) {
		t.Fatalf("err not classified: %v", err)
	}
	if !txutil.IsRetryableLock(err) {
		t.Errorf("IsRetryableLock failed for known lock error: %v", err)
	}
	// String form should also confirm it is a sqlite contention error.
	if !strings.Contains(strings.ToUpper(err.Error()), "BUSY") &&
		!strings.Contains(strings.ToUpper(err.Error()), "LOCK") {
		t.Errorf("error text does not mention busy/lock: %q", err)
	}
}

func TestWithImmediateRetry_RecoversAfterTransientLock(t *testing.T) {
	// Hold a writer tx open for ~80ms; WithImmediateRetry should retry past
	// the SQLITE_BUSY window and succeed once tx1 commits.
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	seed := openWriter(t, path)
	createTable(t, seed)
	_ = seed.Close()

	db := openImmediateMulti(t, path, 20*time.Millisecond)
	ctx := context.Background()

	hold := make(chan struct{})
	released := make(chan struct{})

	go func() {
		defer close(released)
		tx, err := txutil.BeginImmediate(ctx, db)
		if err != nil {
			t.Errorf("holder BeginImmediate: %v", err)
			return
		}
		close(hold)
		time.Sleep(80 * time.Millisecond)
		if _, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "holder"); err != nil {
			t.Errorf("holder insert: %v", err)
			_ = tx.Rollback()
			return
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("holder commit: %v", err)
		}
	}()

	<-hold

	err := txutil.WithImmediateRetry(ctx, db, txutil.RetryOptions{
		MaxAttempts: 20,
		BaseDelay:   5 * time.Millisecond,
		MaxDelay:    25 * time.Millisecond,
	}, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "retrier")
		return err
	})
	<-released
	if err != nil {
		t.Fatalf("WithImmediateRetry: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 rows after retry success, got %d", n)
	}
}

func TestWithRetry_StopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	seed := openWriter(t, path)
	createTable(t, seed)
	_ = seed.Close()

	db := openImmediateMulti(t, path, 20*time.Millisecond)

	// Hold the writer indefinitely.
	parent := context.Background()
	holder, err := txutil.BeginImmediate(parent, db)
	if err != nil {
		t.Fatalf("holder Begin: %v", err)
	}
	defer holder.Rollback()

	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	var retryErr error
	go func() {
		defer wg.Done()
		retryErr = txutil.WithImmediateRetry(ctx, db, txutil.RetryOptions{
			MaxAttempts: 100,
			BaseDelay:   5 * time.Millisecond,
			MaxDelay:    20 * time.Millisecond,
		}, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "x")
			return err
		})
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	wg.Wait()

	if !errors.Is(retryErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", retryErr)
	}
}

func TestWithRetry_DoesNotRetryNonLockErrors(t *testing.T) {
	dir := t.TempDir()
	db := openWriter(t, filepath.Join(dir, "app.db"))
	createTable(t, db)

	ctx := context.Background()
	// Pre-seed a row to make the UNIQUE constraint fail on the second insert.
	if err := txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "alpha")
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	attempts := 0
	err := txutil.WithImmediateRetry(ctx, db, txutil.RetryOptions{
		MaxAttempts: 5,
		BaseDelay:   time.Millisecond,
	}, func(tx *sql.Tx) error {
		attempts++
		_, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "alpha")
		return err
	})
	if err == nil {
		t.Fatal("expected UNIQUE constraint error")
	}
	if txutil.IsBusy(err) || txutil.IsLocked(err) {
		t.Fatalf("constraint error misclassified as lock: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("retry kept calling fn for a non-lock error: attempts=%d", attempts)
	}
}

func TestWithRetry_CustomClassifier(t *testing.T) {
	attempts := 0
	sentinel := errors.New("retryable test error")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := txutil.WithRetry(ctx, txutil.RetryOptions{
		MaxAttempts: 3,
		BaseDelay:   time.Microsecond,
		IsRetryable: func(err error) bool { return errors.Is(err, sentinel) },
	}, func() error {
		attempts++
		if attempts < 3 {
			return sentinel
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithRetry: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}
