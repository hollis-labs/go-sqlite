package txutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TxOptions configures a transaction.
type TxOptions struct {
	// Immediate requests BEGIN IMMEDIATE semantics. The *sql.DB must be opened
	// with the modernc DSN parameter _txlock=immediate (see the package doc).
	// If the DSN is not configured for immediate, BEGIN falls back to DEFERRED.
	Immediate bool

	// ReadOnly maps to sql.TxOptions.ReadOnly. SQLite/modernc does not strictly
	// enforce this, but the hint is preserved so callers can document intent.
	ReadOnly bool
}

// Begin opens a transaction with the given options.
//
// See [TxOptions] for the _txlock=immediate DSN requirement when Immediate is
// true.
func Begin(ctx context.Context, db *sql.DB, opts TxOptions) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: opts.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("txutil: begin: %w", err)
	}
	return tx, nil
}

// BeginImmediate is shorthand for Begin with Immediate=true.
func BeginImmediate(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	return Begin(ctx, db, TxOptions{Immediate: true})
}

// WithTx runs fn inside a transaction. Commits on a nil return from fn,
// rolls back otherwise. A panic in fn triggers a rollback and re-panics.
//
// A rollback failure (other than sql.ErrTxDone) is reported alongside the
// original error.
func WithTx(ctx context.Context, db *sql.DB, opts TxOptions, fn func(*sql.Tx) error) (err error) {
	tx, err := Begin(ctx, db, opts)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if fnErr := fn(tx); fnErr != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("txutil: rollback after %w failed: %v", fnErr, rbErr)
		}
		return fnErr
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("txutil: commit: %w", err)
	}
	return nil
}

// WithImmediate is shorthand for WithTx with Immediate=true.
func WithImmediate(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	return WithTx(ctx, db, TxOptions{Immediate: true}, fn)
}
