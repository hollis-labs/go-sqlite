package serialwrite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/hollis-labs/go-sqlite/txutil"
)

// Queue is a batching in-process write serializer. Call [Queue.Run] in a
// goroutine to start it, [Queue.Submit] from callers, and [Queue.Stop] +
// [Queue.Wait] for orderly shutdown.
//
// The *sql.DB passed to [New] must be opened with _txlock=immediate (for
// example via sqlitekit.OpenWriter or sqlitekit.OpenSingle constructed via
// WriterOptions). Without it, [txutil.BeginImmediate] falls back to
// DEFERRED and the BUSY-on-write-upgrade failure mode this package is
// supposed to prevent returns.
type Queue struct {
	db          *sql.DB
	ops         chan op
	maxBatch    int
	batchWindow time.Duration
	retry       txutil.RetryOptions
	stats       counters

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

type op struct {
	name string
	fn   Op
	res  chan error
}

// New constructs a [Queue] over db. The queue does not run until [Run] is
// invoked.
func New(db *sql.DB, opts Options) *Queue {
	opts = applyOptionDefaults(opts)
	return &Queue{
		db:          db,
		ops:         make(chan op, opts.QueueSize),
		maxBatch:    opts.MaxBatch,
		batchWindow: opts.BatchWindow,
		retry:       opts.Retry,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
}

// Run drains the queue until ctx is cancelled or [Stop] is called. After
// either, accepted ops still in the channel are drained and acknowledged
// before Run returns.
//
// Run returns ctx.Err() on context cancellation and nil on a clean [Stop].
func (q *Queue) Run(ctx context.Context) error {
	defer close(q.doneCh)
	for {
		select {
		case <-ctx.Done():
			q.drain()
			return ctx.Err()
		case <-q.stopCh:
			q.drain()
			return nil
		case first := <-q.ops:
			batch := q.collect(first)
			q.executeBatch(ctx, batch)
		}
	}
}

// Stop signals Run to drain and exit. Safe to call multiple times.
func (q *Queue) Stop() {
	q.stopOnce.Do(func() { close(q.stopCh) })
}

// Wait blocks until [Run] returns. Safe to call from any goroutine.
func (q *Queue) Wait() {
	<-q.doneCh
}

// Submit enqueues an op and blocks until the worker has acked the result.
// The caller's ctx applies only to the enqueue and result-wait paths;
// the op itself runs under the worker's run context.
//
// Returns [ErrWriterStopped] if the writer has stopped, ctx.Err() if ctx is
// cancelled, or the op's error otherwise.
func (q *Queue) Submit(ctx context.Context, name string, fn Op) error {
	if ctx == nil {
		ctx = context.Background()
	}
	item := op{
		name: name,
		fn:   fn,
		res:  make(chan error, 1),
	}
	q.stats.submitted.Add(1)

	select {
	case q.ops <- item:
	case <-q.stopCh:
		q.stats.failed.Add(1)
		return ErrWriterStopped
	case <-ctx.Done():
		q.stats.failed.Add(1)
		return ctx.Err()
	}

	select {
	case err := <-item.res:
		if err != nil {
			q.stats.failed.Add(1)
		} else {
			q.stats.completed.Add(1)
		}
		return err
	case <-ctx.Done():
		q.stats.failed.Add(1)
		return ctx.Err()
	}
}

// Stats returns a snapshot of cumulative counters.
func (q *Queue) Stats() Stats {
	return q.stats.snapshot(int64(len(q.ops)))
}

// collect waits up to batchWindow for additional ops to join the batch,
// stopping when the batch reaches maxBatch.
func (q *Queue) collect(first op) []op {
	batch := []op{first}
	if q.maxBatch == 1 {
		return batch
	}
	timer := time.NewTimer(q.batchWindow)
	defer timer.Stop()
	for len(batch) < q.maxBatch {
		select {
		case next := <-q.ops:
			batch = append(batch, next)
		case <-timer.C:
			return batch
		}
	}
	return batch
}

// collectGreedy pulls any immediately-available ops into the batch without
// waiting. Used during drain.
func (q *Queue) collectGreedy(first op) []op {
	batch := []op{first}
	for len(batch) < q.maxBatch {
		select {
		case next := <-q.ops:
			batch = append(batch, next)
		default:
			return batch
		}
	}
	return batch
}

func (q *Queue) executeBatch(ctx context.Context, batch []op) {
	if len(batch) == 0 {
		return
	}
	q.stats.batches.Add(1)
	q.stats.opsInBatches.Add(int64(len(batch)))
	q.stats.lastBatchSize.Store(int64(len(batch)))

	results := make([]error, len(batch))
	commitErr := q.runBatch(ctx, batch, results)
	for i, item := range batch {
		if commitErr != nil && results[i] == nil {
			// Commit failed (or BEGIN failed); ops that did not fail their
			// own savepoint share the commit error since their work also
			// rolled back.
			results[i] = commitErr
		}
		item.res <- results[i]
	}
}

func (q *Queue) runBatch(ctx context.Context, batch []op, results []error) error {
	runOnce := func() error {
		// Reset per-op results in case this is a retry attempt.
		for i := range results {
			results[i] = nil
		}
		return txutil.WithImmediate(ctx, q.db, func(tx *sql.Tx) error {
			for i, item := range batch {
				results[i] = runOpInSavepoint(ctx, tx, item)
			}
			return nil
		})
	}
	if q.retry.MaxAttempts > 0 {
		return txutil.WithRetry(ctx, q.retry, runOnce)
	}
	return runOnce()
}

// runOpInSavepoint wraps one op in a SAVEPOINT and converts a panic to an
// error so a misbehaving op does not kill the worker goroutine.
func runOpInSavepoint(ctx context.Context, tx *sql.Tx, item op) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("serialwrite: panic in %q: %v", item.name, p)
		}
	}()
	name := txutil.SavepointName(item.name)
	if e := txutil.WithSavepoint(ctx, tx, name, func() error {
		return item.fn(ctx, tx)
	}); e != nil {
		return e
	}
	return nil
}

// drain processes any ops still in the channel under context.Background so
// shutdown completes even if the run context was already cancelled.
func (q *Queue) drain() {
	ctx := context.Background()
	for {
		select {
		case first := <-q.ops:
			batch := q.collectGreedy(first)
			q.executeBatch(ctx, batch)
		default:
			return
		}
	}
}
