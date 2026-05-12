package serialwrite

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"time"

	"github.com/hollis-labs/go-sqlite/txutil"
)

// Op is the function signature for a unit of work submitted to a [Writer].
// It runs inside a SAVEPOINT on the worker's BEGIN IMMEDIATE transaction.
// A non-nil return rolls back the savepoint only; other ops in the same
// batch may still commit.
//
// The ctx passed to the op is the worker's run context, not the caller's
// Submit context. The caller's ctx only controls enqueue and result waiting.
type Op func(ctx context.Context, tx *sql.Tx) error

// Writer is the contract implemented by both [Queue] and [Direct].
type Writer interface {
	// Submit blocks until fn has committed or failed.
	Submit(ctx context.Context, name string, fn Op) error

	// Stats returns a snapshot of cumulative counters. Safe to call
	// concurrently with Submit.
	Stats() Stats
}

// Options configures a [Queue] or [Direct] writer.
//
// Zero values use defaults: QueueSize=256, MaxBatch=32, BatchWindow=2ms.
// Retry is the zero RetryOptions, which disables retry; pass a non-zero
// RetryOptions to opt in.
type Options struct {
	// QueueSize caps the in-flight queue depth (Queue mode only). Submitters
	// block when full; the channel acts as backpressure.
	QueueSize int

	// MaxBatch caps the number of ops the worker bundles into one outer
	// BEGIN IMMEDIATE transaction. Higher values amortize fsync cost; lower
	// values reduce tail latency.
	MaxBatch int

	// BatchWindow is the maximum time the worker waits for additional ops
	// after seeing the first one of a batch.
	BatchWindow time.Duration

	// Retry, when non-zero, wraps each transaction in
	// [txutil.WithRetry] so transient lock errors (SQLITE_BUSY,
	// SQLITE_LOCKED) are retried before failing the batch. Defaults to
	// no retry; the in-process serializer rarely needs it.
	Retry txutil.RetryOptions
}

// Stats is a snapshot of cumulative counters for a [Writer].
type Stats struct {
	// Submitted is the total number of Submit calls observed.
	Submitted int64
	// Completed is the number of Submit calls that returned nil.
	Completed int64
	// Failed is the number of Submit calls that returned a non-nil error
	// (including context cancellations and writer-stopped errors).
	Failed int64
	// Batches is the number of outer BEGIN IMMEDIATE transactions the
	// worker has executed.
	Batches int64
	// OpsInBatches is the total number of ops the worker has processed
	// across all batches.
	OpsInBatches int64
	// LastBatchSize is the size of the most recent batch.
	LastBatchSize int64
	// QueueDepth is the current channel-buffered queue depth (Queue mode).
	// Direct mode reports zero.
	QueueDepth int64
}

// ErrWriterStopped is returned by [Queue.Submit] when the writer is stopped
// before the op can be enqueued.
var ErrWriterStopped = errors.New("serialwrite: writer stopped")

type counters struct {
	submitted     atomic.Int64
	completed     atomic.Int64
	failed        atomic.Int64
	batches       atomic.Int64
	opsInBatches  atomic.Int64
	lastBatchSize atomic.Int64
}

func (c *counters) snapshot(queueDepth int64) Stats {
	return Stats{
		Submitted:     c.submitted.Load(),
		Completed:     c.completed.Load(),
		Failed:        c.failed.Load(),
		Batches:       c.batches.Load(),
		OpsInBatches:  c.opsInBatches.Load(),
		LastBatchSize: c.lastBatchSize.Load(),
		QueueDepth:    queueDepth,
	}
}

func applyOptionDefaults(opts Options) Options {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 256
	}
	if opts.MaxBatch <= 0 {
		opts.MaxBatch = 32
	}
	if opts.BatchWindow <= 0 {
		opts.BatchWindow = 2 * time.Millisecond
	}
	return opts
}
