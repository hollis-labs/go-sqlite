// Package main demonstrates sqlitekit.OpenSingle for apps that serialize all
// DB access through one *sql.DB.
//
// Run from the repo root:
//
//	go run ./examples/single
//
// Expected output:
//
//	inserted: hello-1
//	inserted: hello-2
//	rows in t: 2
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/hollis-labs/go-sqlite/sqlitekit"
	_ "modernc.org/sqlite"
)

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "sqlitekit-example-*")
	if err != nil {
		log.Fatalf("tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "single.db")

	db, err := sqlitekit.OpenSingle(ctx, path, sqlitekit.OpenOptions{
		CreateParentDir: true,
	})
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		log.Fatalf("create: %v", err)
	}

	for _, v := range []string{"hello-1", "hello-2"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO t (v) VALUES (?)`, v); err != nil {
			log.Fatalf("insert: %v", err)
		}
		fmt.Println("inserted:", v)
	}

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("rows in t: %d\n", n)
}
