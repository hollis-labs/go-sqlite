package txutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// BeginImmediate opens a transaction that begins with writer-lock acquisition.
//
// The actual BEGIN IMMEDIATE behavior is driven by the modernc DSN parameter
// _txlock=immediate, not by this call. The *sql.DB must therefore be opened
// with TxLock="immediate" (sqlitekit.WriterOptions / sqlitekit.OpenWriter /
// sqlitekit.OpenSingle constructed via WriterOptions already set this). If
// the DSN is not configured for immediate, modernc issues BEGIN DEFERRED and
// writer-lock acquisition is delayed until the first write — which is the
// exact failure mode this package exists to prevent.
//
// This function is a contract marker: when txutil.BeginImmediate appears in
// code, the reader knows IMMEDIATE semantics are required at the DSN layer.
func BeginImmediate(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("txutil: begin immediate: %w", err)
	}
	return tx, nil
}

// WithImmediate runs fn inside a transaction opened by [BeginImmediate].
//
// On a nil return from fn the transaction commits. On a non-nil return the
// transaction rolls back and fn's error is returned. A panic inside fn
// triggers rollback and re-raises the panic.
//
// If commit fails the helper attempts a rollback to release the connection
// and reports the commit error. Rollback failures other than sql.ErrTxDone
// are reported alongside the original error.
func WithImmediate(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) (err error) {
	tx, err := BeginImmediate(ctx, db)
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
	if commitErr := tx.Commit(); commitErr != nil {
		// On a commit failure the transaction may still be holding the
		// connection. Attempt rollback to release it; sql.ErrTxDone is
		// expected when the driver already finalized the tx.
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("txutil: commit failed: %w (rollback also failed: %v)", commitErr, rbErr)
		}
		return fmt.Errorf("txutil: commit: %w", commitErr)
	}
	return nil
}
