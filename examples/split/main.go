// Package main demonstrates sqlitekit.OpenWriter + OpenReader for apps that
// want to serve concurrent reads against a single-connection writer pool.
//
// Run from the repo root:
//
//	go run ./examples/split
//
// Expected output (counts and ordering may vary slightly under contention):
//
//	wrote 50 rows via the writer pool
//	read 50 rows via the reader pool
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/hollis-labs/go-sqlite/sqlitekit"
	_ "modernc.org/sqlite"
)

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "sqlitekit-split-*")
	if err != nil {
		log.Fatalf("tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "split.db")

	writer, err := sqlitekit.OpenWriter(ctx, path, sqlitekit.OpenOptions{
		CreateParentDir: true,
	})
	if err != nil {
		log.Fatalf("open writer: %v", err)
	}
	defer writer.Close()

	if _, err := writer.ExecContext(ctx, `CREATE TABLE events (id INTEGER PRIMARY KEY, payload TEXT)`); err != nil {
		log.Fatalf("create: %v", err)
	}

	// Fan-out writers through the same single-connection writer pool. SQLite
	// serializes them inside the pool; SQLITE_BUSY cannot occur from this
	// process.
	const fanout = 10
	const each = 5
	var wg sync.WaitGroup
	for i := 0; i < fanout; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if _, err := writer.ExecContext(ctx, `INSERT INTO events (payload) VALUES (?)`, fmt.Sprintf("w%d-%d", id, j)); err != nil {
					log.Fatalf("insert: %v", err)
				}
			}
		}(i)
	}
	wg.Wait()
	fmt.Printf("wrote %d rows via the writer pool\n", fanout*each)

	reader, err := sqlitekit.OpenReader(ctx, path, sqlitekit.OpenOptions{
		MaxOpenConns: 4,
	})
	if err != nil {
		log.Fatalf("open reader: %v", err)
	}
	defer reader.Close()

	var n int
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("read %d rows via the reader pool\n", n)
}
