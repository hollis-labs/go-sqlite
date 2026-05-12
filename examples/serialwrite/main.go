// Package main demonstrates serialwrite.Queue routing a fan-out of
// concurrent writers through one in-process transaction owner.
//
// Run from the repo root:
//
//	go run ./examples/serialwrite
//
// Expected output (counts deterministic, batching/timing varies):
//
//	wrote 200 rows via the serialized writer
//	stats: submitted=200 completed=200 failed=0 batches=<n> ops_in_batches=200 last_batch_size=<n>
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hollis-labs/go-sqlite/serialwrite"
	"github.com/hollis-labs/go-sqlite/sqlitekit"
	_ "modernc.org/sqlite"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir, err := os.MkdirTemp("", "serialwrite-example-*")
	if err != nil {
		log.Fatalf("tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "events.db")

	// OpenWriter sets _txlock=immediate, which serialwrite relies on for
	// BEGIN IMMEDIATE semantics inside its worker.
	db, err := sqlitekit.OpenWriter(ctx, path, sqlitekit.OpenOptions{
		CreateParentDir: true,
	})
	if err != nil {
		log.Fatalf("open writer: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE events (
			id      INTEGER PRIMARY KEY,
			source  TEXT    NOT NULL,
			seq     INTEGER NOT NULL,
			ts_ms   INTEGER NOT NULL
		)`); err != nil {
		log.Fatalf("schema: %v", err)
	}

	q := serialwrite.New(db, serialwrite.Options{
		QueueSize:   256,
		MaxBatch:    16,
		BatchWindow: 2 * time.Millisecond,
	})

	var runWG sync.WaitGroup
	runWG.Add(1)
	go func() {
		defer runWG.Done()
		if err := q.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("worker: %v", err)
		}
	}()

	const sources = 20
	const each = 10
	var wg sync.WaitGroup
	for s := 0; s < sources; s++ {
		wg.Add(1)
		go func(source int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				err := q.Submit(ctx, "append_event", func(ctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx,
						`INSERT INTO events (source, seq, ts_ms) VALUES (?, ?, ?)`,
						fmt.Sprintf("source-%d", source), i, time.Now().UnixMilli(),
					)
					return err
				})
				if err != nil {
					log.Printf("submit: %v", err)
					return
				}
			}
		}(s)
	}
	wg.Wait()

	q.Stop()
	q.Wait()
	runWG.Wait()

	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("wrote %d rows via the serialized writer\n", n)

	s := q.Stats()
	fmt.Printf("stats: submitted=%d completed=%d failed=%d batches=%d ops_in_batches=%d last_batch_size=%d\n",
		s.Submitted, s.Completed, s.Failed, s.Batches, s.OpsInBatches, s.LastBatchSize)
}
