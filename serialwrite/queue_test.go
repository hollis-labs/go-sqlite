package serialwrite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/go-sqlite/serialwrite"
	"github.com/hollis-labs/go-sqlite/sqlitekit"
)

func openWriter(t *testing.T, name string) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlitekit.OpenWriter(context.Background(), filepath.Join(dir, name), sqlitekit.OpenOptions{})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE events (id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL, payload TEXT NOT NULL)`,
	); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func runQueue(t *testing.T, q *serialwrite.Queue) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = q.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		q.Stop()
		q.Wait()
	})
	return cancel
}

func TestQueue_ConcurrentSubmitsCommit(t *testing.T) {
	db := openWriter(t, "app.db")
	q := serialwrite.New(db, serialwrite.Options{})
	runQueue(t, q)

	const writers = 16
	const each = 25

	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				err := q.Submit(context.Background(), "insert", func(ctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx,
						`INSERT INTO events (name, payload) VALUES (?, ?)`,
						uniq(w, i), "p",
					)
					return err
				})
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
		t.Fatalf("Submit failed: %v", err)
	}

	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != writers*each {
		t.Fatalf("rows: got %d want %d", n, writers*each)
	}

	stats := q.Stats()
	if stats.Submitted != writers*each {
		t.Errorf("Submitted: got %d want %d", stats.Submitted, writers*each)
	}
	if stats.Completed != writers*each {
		t.Errorf("Completed: got %d want %d", stats.Completed, writers*each)
	}
	if stats.Failed != 0 {
		t.Errorf("Failed: got %d want 0", stats.Failed)
	}
	if stats.OpsInBatches != writers*each {
		t.Errorf("OpsInBatches: got %d want %d", stats.OpsInBatches, writers*each)
	}
}

func TestQueue_FailedOpRollsBackOnlyItsSavepoint(t *testing.T) {
	// Submit a sequence with one intentional failure in the middle. All
	// other ops must commit; the failed op returns the sentinel.
	db := openWriter(t, "app.db")
	q := serialwrite.New(db, serialwrite.Options{
		// Force a single batch by making the window long enough to collect
		// every submission within it.
		MaxBatch:    8,
		BatchWindow: 50 * time.Millisecond,
	})
	runQueue(t, q)

	sentinel := errors.New("intentional savepoint failure")

	type result struct {
		idx int
		err error
	}

	const n = 5
	results := make([]error, n)
	var wg sync.WaitGroup
	resCh := make(chan result, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := q.Submit(context.Background(), "op", func(ctx context.Context, tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO events (name, payload) VALUES (?, ?)`,
					uniq(0, i), "p",
				); err != nil {
					return err
				}
				if i == 2 {
					return sentinel
				}
				return nil
			})
			resCh <- result{idx: i, err: err}
		}(i)
	}
	wg.Wait()
	close(resCh)
	for r := range resCh {
		results[r.idx] = r.err
	}

	if !errors.Is(results[2], sentinel) {
		t.Fatalf("op 2 expected sentinel, got %v", results[2])
	}
	for i, e := range results {
		if i == 2 {
			continue
		}
		if e != nil {
			t.Fatalf("sibling op %d failed: %v", i, e)
		}
	}

	var got int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events`).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	// Four siblings inserted; the failed op's INSERT rolled back with its
	// savepoint.
	if got != n-1 {
		t.Fatalf("expected %d rows, got %d", n-1, got)
	}
}

func TestQueue_PanicInOpIsCaught(t *testing.T) {
	db := openWriter(t, "app.db")
	q := serialwrite.New(db, serialwrite.Options{})
	runQueue(t, q)

	// First op panics; the worker must convert it to an error and keep going.
	err := q.Submit(context.Background(), "panicker", func(ctx context.Context, tx *sql.Tx) error {
		panic("boom")
	})
	if err == nil {
		t.Fatal("expected error from panicking op")
	}

	// Second op should still succeed — worker survived.
	err = q.Submit(context.Background(), "survivor", func(ctx context.Context, tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO events (name, payload) VALUES (?, ?)`, "survivor", "p")
		return e
	})
	if err != nil {
		t.Fatalf("survivor: %v", err)
	}
}

func TestQueue_SubmitCancelledCtxBeforeEnqueue(t *testing.T) {
	db := openWriter(t, "app.db")
	q := serialwrite.New(db, serialwrite.Options{QueueSize: 1})
	// Do NOT run the queue — we want the channel to fill.

	// Fill the queue to capacity.
	go func() {
		_ = q.Submit(context.Background(), "filler", func(ctx context.Context, tx *sql.Tx) error {
			time.Sleep(time.Second)
			return nil
		})
	}()
	// Wait for the channel to have one item.
	deadline := time.Now().Add(time.Second)
	for q.Stats().QueueDepth == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := q.Submit(ctx, "rejected", func(ctx context.Context, tx *sql.Tx) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestQueue_SubmitCancelledWhileWaiting(t *testing.T) {
	// Run a queue, then submit two ops: one that holds the worker for ~80ms,
	// then a second whose Submit ctx cancels while waiting for the ack. The
	// second Submit must return context.Canceled. The worker still runs the
	// op (and commits its work) — the ctx only controls the caller's wait.
	db := openWriter(t, "app.db")
	q := serialwrite.New(db, serialwrite.Options{})
	runQueue(t, q)

	// Block the worker on the first op.
	started := make(chan struct{})
	go func() {
		_ = q.Submit(context.Background(), "blocker", func(ctx context.Context, tx *sql.Tx) error {
			close(started)
			time.Sleep(80 * time.Millisecond)
			_, err := tx.ExecContext(ctx, `INSERT INTO events (name, payload) VALUES (?, ?)`, "blocker", "p")
			return err
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := q.Submit(ctx, "waiter", func(ctx context.Context, tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO events (name, payload) VALUES (?, ?)`, "waiter", "p")
		return e
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Eventually both rows commit even though the waiter caller gave up.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var n int
		_ = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events`).Scan(&n)
		if n == 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("worker did not finish blocker+waiter ops within deadline")
}

func TestQueue_StopDrainsAcceptedOps(t *testing.T) {
	db := openWriter(t, "app.db")
	q := serialwrite.New(db, serialwrite.Options{
		MaxBatch:    8,
		BatchWindow: 30 * time.Millisecond,
	})
	runQueue(t, q)

	const n = 12
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := q.Submit(context.Background(), "op", func(ctx context.Context, tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, `INSERT INTO events (name, payload) VALUES (?, ?)`, uniq(0, i), "p")
				return e
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}

	// Wait until all submissions have at least been enqueued/started.
	deadline := time.Now().Add(time.Second)
	for q.Stats().Submitted < n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	q.Stop()
	q.Wait()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Submit error during drain: %v", err)
	}

	var got int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events`).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != n {
		t.Fatalf("rows: got %d want %d (drain incomplete)", got, n)
	}
}

func TestQueue_SubmitAfterStopReturnsErrWriterStopped(t *testing.T) {
	db := openWriter(t, "app.db")
	q := serialwrite.New(db, serialwrite.Options{})
	// Don't even Run the queue. Stop immediately.
	q.Stop()

	err := q.Submit(context.Background(), "late", func(ctx context.Context, tx *sql.Tx) error { return nil })
	if !errors.Is(err, serialwrite.ErrWriterStopped) {
		t.Fatalf("expected ErrWriterStopped, got %v", err)
	}
}

func TestQueue_SubmitAfterStopIsDeterministic(t *testing.T) {
	// Without the Submit pre-check, Go's select could non-deterministically
	// pick the q.ops <- item case even when stopCh is closed, enqueueing an
	// op that never gets processed (and leaving Submit blocked forever
	// waiting on item.res). Run a tight loop to flush out the race.
	db := openWriter(t, "app.db")

	for i := 0; i < 100; i++ {
		q := serialwrite.New(db, serialwrite.Options{})
		q.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := q.Submit(ctx, "late", func(ctx context.Context, tx *sql.Tx) error { return nil })
		cancel()
		if !errors.Is(err, serialwrite.ErrWriterStopped) {
			t.Fatalf("iteration %d: expected ErrWriterStopped, got %v", i, err)
		}
	}
}

func TestQueue_SubmitDoesNotHangAfterRunExit(t *testing.T) {
	// Race-trigger: many Submit goroutines and Stop fire concurrently. Some
	// Submits will land before Stop; some will race with Stop and possibly
	// enqueue after Run has decided to drain-and-exit. None must hang —
	// they should either return successfully (drained) or with
	// ErrWriterStopped / context.Canceled.
	db := openWriter(t, "app.db")
	q := serialwrite.New(db, serialwrite.Options{
		MaxBatch:    4,
		BatchWindow: time.Millisecond,
	})
	runQueue(t, q)

	const n = 50
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			results[i] = q.Submit(ctx, "race", func(ctx context.Context, tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, `INSERT INTO events (name, payload) VALUES (?, ?)`, uniq(99, i), "p")
				return e
			})
		}(i)
	}
	// Stop while submitters are in flight.
	time.Sleep(2 * time.Millisecond)
	q.Stop()
	q.Wait()
	wg.Wait()

	// Every Submit must have returned; none should be context.DeadlineExceeded
	// (which would indicate a hang).
	for i, err := range results {
		if err == nil {
			continue
		}
		if errors.Is(err, serialwrite.ErrWriterStopped) {
			continue
		}
		if errors.Is(err, context.Canceled) {
			continue
		}
		t.Fatalf("Submit %d returned unexpected error (possible hang): %v", i, err)
	}
}

func TestQueue_RunReturnsCtxErrOnCancel(t *testing.T) {
	db := openWriter(t, "app.db")
	q := serialwrite.New(db, serialwrite.Options{})

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- q.Run(ctx)
	}()

	cancel()
	select {
	case err := <-runErrCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run: got %v want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
	q.Wait()
}

func TestDirect_RunsSynchronously(t *testing.T) {
	db := openWriter(t, "app.db")
	d := serialwrite.NewDirect(db, serialwrite.Options{})

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := d.Submit(context.Background(), "op", func(ctx context.Context, tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, `INSERT INTO events (name, payload) VALUES (?, ?)`, uniq(0, i), "p")
				return e
			})
			if err != nil {
				t.Errorf("Direct.Submit: %v", err)
			}
		}(i)
	}
	wg.Wait()

	var got int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events`).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != n {
		t.Fatalf("rows: got %d want %d", got, n)
	}

	stats := d.Stats()
	if stats.Submitted != n {
		t.Errorf("Submitted: got %d want %d", stats.Submitted, n)
	}
	if stats.Completed != n {
		t.Errorf("Completed: got %d want %d", stats.Completed, n)
	}
	if stats.LastBatchSize != 1 {
		t.Errorf("LastBatchSize: got %d want 1", stats.LastBatchSize)
	}
	if stats.QueueDepth != 0 {
		t.Errorf("QueueDepth: got %d want 0", stats.QueueDepth)
	}
}

func TestDirect_PropagatesError(t *testing.T) {
	db := openWriter(t, "app.db")
	d := serialwrite.NewDirect(db, serialwrite.Options{})

	sentinel := errors.New("nope")
	err := d.Submit(context.Background(), "op", func(ctx context.Context, tx *sql.Tx) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	if d.Stats().Failed != 1 {
		t.Errorf("Failed: got %d want 1", d.Stats().Failed)
	}
}

func uniq(w, i int) string {
	// Build a unique row name without pulling in fmt; keeps the test loop tight.
	return "w" + itoa(w) + "_" + itoa(i)
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
