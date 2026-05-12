package txutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// ErrInvalidSavepointName is returned by [WithSavepoint] when name contains
// characters outside [A-Za-z0-9_] or is empty after sanitization.
var ErrInvalidSavepointName = errors.New("txutil: invalid savepoint name")

var savepointCounter atomic.Uint64

// SavepointName returns a SQLite-safe savepoint identifier of the form
// "sp_<sanitized-prefix>_<counter>". The prefix is sanitized by lowercasing,
// mapping non-identifier characters to '_', collapsing consecutive '_' into
// a single '_', and trimming leading and trailing '_'. An empty or
// fully-invalid prefix yields "sp_op_<counter>".
//
// The counter is process-global and monotonic, so concurrent calls with the
// same prefix produce distinct names.
func SavepointName(prefix string) string {
	clean := sanitizeSavepoint(prefix)
	if clean == "" {
		clean = "op"
	}
	n := savepointCounter.Add(1)
	return fmt.Sprintf("sp_%s_%d", clean, n)
}

// WithSavepoint runs fn inside a SAVEPOINT <name> on tx.
//
// On a nil return from fn the savepoint is released. On a non-nil return or a
// panic, the savepoint is rolled back to and then released, leaving the outer
// transaction intact for the caller to commit or roll back.
//
// Cleanup statements (ROLLBACK TO, RELEASE) run under a context derived from
// ctx with cancellation stripped via [context.WithoutCancel]. If ctx is
// cancelled or times out mid-fn, the savepoint still releases cleanly so the
// outer transaction is not left with an orphan savepoint marker.
//
// name must be a valid SQLite identifier (letters, digits, underscores).
// Use [SavepointName] to construct safe unique names.
func WithSavepoint(ctx context.Context, tx *sql.Tx, name string, fn func() error) (err error) {
	if !isValidSavepointName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidSavepointName, name)
	}
	if _, e := tx.ExecContext(ctx, "SAVEPOINT "+name); e != nil {
		return fmt.Errorf("txutil: SAVEPOINT %s: %w", name, e)
	}
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		if p := recover(); p != nil {
			_, _ = tx.ExecContext(cleanupCtx, "ROLLBACK TO SAVEPOINT "+name)
			_, _ = tx.ExecContext(cleanupCtx, "RELEASE SAVEPOINT "+name)
			panic(p)
		}
		if err != nil {
			if _, rbErr := tx.ExecContext(cleanupCtx, "ROLLBACK TO SAVEPOINT "+name); rbErr != nil {
				err = fmt.Errorf("%w; rollback to savepoint %s also failed: %v", err, name, rbErr)
			}
			if _, relErr := tx.ExecContext(cleanupCtx, "RELEASE SAVEPOINT "+name); relErr != nil {
				err = fmt.Errorf("%w; release savepoint %s also failed: %v", err, name, relErr)
			}
		}
	}()

	if err = fn(); err != nil {
		return err
	}
	if _, e := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name); e != nil {
		err = fmt.Errorf("txutil: RELEASE SAVEPOINT %s: %w", name, e)
		return err
	}
	return nil
}

func sanitizeSavepoint(s string) string {
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	out := make([]byte, 0, len(lower))
	for i := 0; i < len(lower); i++ {
		c := lower[i]
		switch {
		case c >= 'a' && c <= 'z':
			out = append(out, c)
		case c >= '0' && c <= '9':
			out = append(out, c)
		default:
			if len(out) > 0 && out[len(out)-1] != '_' {
				out = append(out, '_')
			}
		}
	}
	return strings.Trim(string(out), "_")
}

func isValidSavepointName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '_' {
			continue
		}
		return false
	}
	return true
}
