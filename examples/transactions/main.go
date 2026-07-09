// Example: atomic batch transactions with the MongrelDB Go client.
//
// Run from the repo root:
//
//	go run ./examples/transactions.go
//
// Requires a mongreldb-server daemon running on http://127.0.0.1:8453.
//
// This example creates a table, stages three inserts in a single transaction,
// commits them atomically, verifies the count, then demonstrates idempotent
// retries by re-committing the same batch with the same idempotency key (the
// daemon returns the original result and applies no duplicate rows). It cleans
// up by dropping the table.
package main

import (
	"context"
	"fmt"
	"log"

	mdb "github.com/visorcraft/mongreldb-go"
)

const (
	url   = "http://127.0.0.1:8453"
	table = "example_txn"
)

func main() {
	ctx := context.Background()
	db := mdb.NewClient(url)

	ok, err := db.Health(ctx)
	if err != nil || !ok {
		log.Fatalf("daemon not reachable at %s: %v", url, err)
	}
	fmt.Println("Connected to MongrelDB")

	cols := []mdb.Column{
		{"id": int64(1), "name": "id", "ty": "int64", "primary_key": true, "nullable": false},
		{"id": int64(2), "name": "name", "ty": "varchar", "primary_key": false, "nullable": false},
		{"id": int64(3), "name": "score", "ty": "float64", "primary_key": false, "nullable": false},
	}
	if _, err := db.CreateTable(ctx, table, cols); err != nil {
		log.Fatalf("create table: %v", err)
	}
	fmt.Printf("Created table %q\n", table)

	// Stage three puts and commit them atomically. Either every op lands or
	// none do; a constraint violation rolls back the whole batch.
	txn := db.Begin()
	txn.Put(table, mdb.Cells{1: int64(1), 2: "Alice", 3: 95.5}, false)
	txn.Put(table, mdb.Cells{1: int64(2), 2: "Bob", 3: 82.0}, false)
	txn.Put(table, mdb.Cells{1: int64(3), 2: "Carol", 3: 78.3}, false)
	fmt.Printf("Staged %d operations\n", txn.Count())

	results, err := txn.Commit(ctx, "")
	if err != nil {
		log.Fatalf("commit: %v", err)
	}
	fmt.Printf("Committed atomically: %d operations applied\n", len(results))

	n, err := db.Count(ctx, table)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("Verified row count after commit: %d\n", n)

	// Idempotent retry: stage the same batch again with an idempotency key,
	// then commit a second time with the SAME key. The daemon replays the
	// original result and applies no extra rows.
	retry := db.Begin()
	retry.Put(table, mdb.Cells{1: int64(4), 2: "Dave", 3: 60.0}, false)
	if _, err := retry.Commit(ctx, "example-txn-key"); err != nil {
		log.Fatalf("first idempotent commit: %v", err)
	}
	n, err = db.Count(ctx, table)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("After first idempotent commit: %d rows\n", n)

	retry2 := db.Begin()
	retry2.Put(table, mdb.Cells{1: int64(4), 2: "Dave", 3: 60.0}, false)
	if _, err := retry2.Commit(ctx, "example-txn-key"); err != nil {
		log.Fatalf("duplicate idempotent commit: %v", err)
	}
	n, err = db.Count(ctx, table)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("After duplicate idempotent commit (same key): %d rows (no double-apply)\n", n)

	if err := db.DropTable(ctx, table); err != nil {
		log.Fatalf("drop table: %v", err)
	}
	fmt.Printf("Dropped table %q\n", table)
}
