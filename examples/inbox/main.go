// Package main demonstrates txutil.WithImmediate for an atomic select-and-mark
// pattern. This is the shape that Mux's inbox and similar
// "claim N rows and update them" flows need to keep race-free under writer
// contention.
//
// Run from the repo root:
//
//	go run ./examples/inbox
//
// Expected output:
//
//	claimed 3 messages
//	to=urn:agent:alpha id=2: hello-2
//	to=urn:agent:alpha id=3: hello-3
//	to=urn:agent:alpha id=4: hello-4
//	rows left undelivered: 1
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/hollis-labs/go-sqlite/sqlitekit"
	"github.com/hollis-labs/go-sqlite/txutil"
	_ "modernc.org/sqlite"
)

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "txutil-inbox-*")
	if err != nil {
		log.Fatalf("tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "inbox.db")

	// OpenWriter sets _txlock=immediate in the DSN, which is what makes
	// db.BeginTx() inside WithImmediate issue BEGIN IMMEDIATE.
	db, err := sqlitekit.OpenWriter(ctx, path, sqlitekit.OpenOptions{
		CreateParentDir: true,
	})
	if err != nil {
		log.Fatalf("open writer: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE messages (
			id            INTEGER PRIMARY KEY,
			to_urn        TEXT NOT NULL,
			payload       TEXT NOT NULL,
			created_at    INTEGER NOT NULL,
			delivered_at  INTEGER
		)`); err != nil {
		log.Fatalf("schema: %v", err)
	}

	seed := []struct {
		to, payload string
	}{
		{"urn:agent:other", "ignore-me"},
		{"urn:agent:alpha", "hello-2"},
		{"urn:agent:alpha", "hello-3"},
		{"urn:agent:alpha", "hello-4"},
		{"urn:agent:alpha", "hello-5"},
	}
	for _, s := range seed {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO messages (to_urn, payload, created_at) VALUES (?, ?, ?)`,
			s.to, s.payload, time.Now().UnixMilli(),
		); err != nil {
			log.Fatalf("seed: %v", err)
		}
	}

	// Atomic select-and-mark: pick up to 3 undelivered messages for alpha and
	// mark them delivered, all under the writer lock acquired at BEGIN time.
	// Without BEGIN IMMEDIATE, a concurrent goroutine could observe the rows
	// between our SELECT and UPDATE and double-deliver.
	const recipient = "urn:agent:alpha"
	const limit = 3

	type message struct {
		id      int64
		payload string
	}
	var claimed []message

	err = txutil.WithImmediate(ctx, db, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, payload
			FROM messages
			WHERE to_urn = ? AND delivered_at IS NULL
			ORDER BY created_at ASC
			LIMIT ?`, recipient, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m message
			if err := rows.Scan(&m.id, &m.payload); err != nil {
				return err
			}
			claimed = append(claimed, m)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		now := time.Now().UnixMilli()
		for _, m := range claimed {
			if _, err := tx.ExecContext(ctx,
				`UPDATE messages SET delivered_at = ? WHERE id = ?`,
				now, m.id,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Fatalf("claim: %v", err)
	}

	fmt.Printf("claimed %d messages\n", len(claimed))
	for _, m := range claimed {
		fmt.Printf("to=%s id=%d: %s\n", recipient, m.id, m.payload)
	}

	var undelivered int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE to_urn = ? AND delivered_at IS NULL`,
		recipient,
	).Scan(&undelivered); err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("rows left undelivered: %d\n", undelivered)
}
