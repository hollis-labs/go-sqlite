package txutil_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/go-sqlite/txutil"
)

func TestSavepointName_SanitizesAndIsUnique(t *testing.T) {
	a := txutil.SavepointName("ingest.batch-1")
	b := txutil.SavepointName("ingest.batch-1")

	for _, name := range []string{a, b} {
		if !strings.HasPrefix(name, "sp_") {
			t.Errorf("missing sp_ prefix in %q", name)
		}
		for i := 0; i < len(name); i++ {
			c := name[i]
			ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
			if !ok {
				t.Errorf("invalid byte %q in savepoint name %q", c, name)
			}
		}
	}

	if a == b {
		t.Fatalf("SavepointName should return unique names: %q == %q", a, b)
	}
}

func TestSavepointName_EmptyPrefix(t *testing.T) {
	got := txutil.SavepointName("")
	if !strings.HasPrefix(got, "sp_op_") {
		t.Fatalf("empty prefix should yield sp_op_<n>, got %q", got)
	}
}

func TestSavepointName_AllInvalidChars(t *testing.T) {
	got := txutil.SavepointName("///---...")
	if !strings.HasPrefix(got, "sp_op_") {
		t.Fatalf("fully-invalid prefix should yield sp_op_<n>, got %q", got)
	}
}

func TestSavepointName_Concurrent(t *testing.T) {
	const n = 64
	seen := make(map[string]bool, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := txutil.SavepointName("op")
			mu.Lock()
			seen[name] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("expected %d unique names, got %d (collisions in concurrent counter)", n, len(seen))
	}
}

func TestWithSavepoint_ReleasesOnSuccess(t *testing.T) {
	dir := t.TempDir()
	db := openWriter(t, filepath.Join(dir, "app.db"))
	createTable(t, db)

	ctx := context.Background()
	err := txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
		return txutil.WithSavepoint(ctx, tx, txutil.SavepointName("insert"), func() error {
			_, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "alpha")
			return err
		})
	})
	if err != nil {
		t.Fatalf("WithSavepoint success path: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row, got %d", n)
	}
}

func TestWithSavepoint_RollbackDoesNotPoisonOuterTx(t *testing.T) {
	dir := t.TempDir()
	db := openWriter(t, filepath.Join(dir, "app.db"))
	createTable(t, db)

	ctx := context.Background()
	sentinel := errors.New("savepoint fn failure")

	err := txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
		// First inner: succeeds, persists.
		if e := txutil.WithSavepoint(ctx, tx, txutil.SavepointName("ok"), func() error {
			_, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "keeper")
			return err
		}); e != nil {
			return e
		}

		// Second inner: writes then fails. Savepoint rollback must undo this
		// write but leave the outer tx alive.
		if e := txutil.WithSavepoint(ctx, tx, txutil.SavepointName("bad"), func() error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "transient"); err != nil {
				return err
			}
			return sentinel
		}); !errors.Is(e, sentinel) {
			t.Errorf("inner error not propagated: %v", e)
		}

		// Third inner: outer tx must still be usable.
		return txutil.WithSavepoint(ctx, tx, txutil.SavepointName("after"), func() error {
			_, err := tx.ExecContext(ctx, `INSERT INTO items (name) VALUES (?)`, "after_rollback")
			return err
		})
	})
	if err != nil {
		t.Fatalf("outer WithImmediate: %v", err)
	}

	var names []string
	rows, err := db.QueryContext(ctx, `SELECT name FROM items ORDER BY id`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, s)
	}
	want := []string{"keeper", "after_rollback"}
	if len(names) != len(want) {
		t.Fatalf("rows: got %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Fatalf("rows[%d]: got %q want %q (all: %v)", i, n, want[i], names)
		}
	}
}

func TestWithSavepoint_InvalidName(t *testing.T) {
	dir := t.TempDir()
	db := openWriter(t, filepath.Join(dir, "app.db"))
	createTable(t, db)

	ctx := context.Background()
	err := txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
		return txutil.WithSavepoint(ctx, tx, "bad name!", func() error { return nil })
	})
	if !errors.Is(err, txutil.ErrInvalidSavepointName) {
		t.Fatalf("expected ErrInvalidSavepointName, got %v", err)
	}
}

func TestWithSavepoint_CleanupSurvivesCtxCancel(t *testing.T) {
	// If ctx is cancelled before/during fn, the SAVEPOINT cleanup statements
	// must still run (via context.WithoutCancel) so the outer transaction is
	// left in a usable state, not holding an orphan savepoint.
	dir := t.TempDir()
	db := openWriter(t, filepath.Join(dir, "app.db"))
	createTable(t, db)

	outer := context.Background()
	err := txutil.WithImmediate(outer, db, func(tx *sql.Tx) error {
		// Successful inner using the outer ctx — establishes the baseline.
		if e := txutil.WithSavepoint(outer, tx, txutil.SavepointName("seed"), func() error {
			_, err := tx.ExecContext(outer, `INSERT INTO items (name) VALUES (?)`, "seed")
			return err
		}); e != nil {
			return e
		}

		// Cancelled inner: cleanup runs under WithoutCancel(ctx) so it
		// succeeds even though ctx is dead.
		innerCtx, cancel := context.WithCancel(outer)
		cancel()
		_ = txutil.WithSavepoint(innerCtx, tx, txutil.SavepointName("cancelled"), func() error {
			_, _ = tx.ExecContext(innerCtx, `INSERT INTO items (name) VALUES (?)`, "should-not-survive")
			return innerCtx.Err()
		})

		// Outer tx must still accept writes — the cancelled savepoint must
		// have been ROLLBACK TO'd and RELEASE'd cleanly.
		_, err := tx.ExecContext(outer, `INSERT INTO items (name) VALUES (?)`, "after_cancel")
		return err
	})
	if err != nil {
		t.Fatalf("outer WithImmediate: %v", err)
	}

	var names []string
	rows, err := db.QueryContext(outer, `SELECT name FROM items ORDER BY id`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, s)
	}
	want := []string{"seed", "after_cancel"}
	if len(names) != len(want) {
		t.Fatalf("rows: got %v want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Fatalf("rows[%d]: got %q want %q (all: %v)", i, n, want[i], names)
		}
	}
}
