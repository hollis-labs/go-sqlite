package serialwrite

import (
	"context"
	"database/sql"

	"github.com/hollis-labs/go-sqlite/txutil"
)

// Direct is a non-batching, synchronous [Writer]. Each Submit runs the op
// in its own BEGIN IMMEDIATE transaction. Useful for tests and for apps
// that want the [Writer] interface without managing a goroutine.
//
// Direct does not need Run / Stop / Wait. Stats reports the same counters
// as [Queue] for parity; LastBatchSize is always 1 and QueueDepth is always
// 0.
type Direct struct {
	db    *sql.DB
	retry txutil.RetryOptions
	stats counters
}

// NewDirect constructs a [Direct] writer over db.
//
// The QueueSize / MaxBatch / BatchWindow options are ignored. Only
// Options.Retry is honored.
func NewDirect(db *sql.DB, opts Options) *Direct {
	return &Direct{db: db, retry: opts.Retry}
}

// Submit runs fn synchronously in a BEGIN IMMEDIATE transaction. Returns the
// op's error or, if Options.Retry was set, the final error after retry.
func (d *Direct) Submit(ctx context.Context, name string, fn Op) error {
	if ctx == nil {
		ctx = context.Background()
	}
	d.stats.submitted.Add(1)

	run := func(tx *sql.Tx) error {
		return fn(ctx, tx)
	}
	var err error
	if d.retry.MaxAttempts > 0 {
		err = txutil.WithImmediateRetry(ctx, d.db, d.retry, run)
	} else {
		err = txutil.WithImmediate(ctx, d.db, run)
	}
	if err != nil {
		d.stats.failed.Add(1)
		return err
	}
	d.stats.completed.Add(1)
	d.stats.batches.Add(1)
	d.stats.opsInBatches.Add(1)
	d.stats.lastBatchSize.Store(1)
	_ = name // reserved for symmetry with Queue.Submit; not currently used.
	return nil
}

// Stats returns a snapshot of cumulative counters.
func (d *Direct) Stats() Stats { return d.stats.snapshot(0) }
