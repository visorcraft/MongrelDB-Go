// Example: basic CRUD operations with the MongrelDB Go client.
//
// Run from the repo root:
//
//	go run ./examples/basic_crud
//
// Requires a mongreldb-server daemon running on http://127.0.0.1:8453.
// Start one with:
//
//	mkdir -p /tmp/mdb-data && mongreldb-server /tmp/mdb-data
//
// This example creates a table, inserts three rows, counts them, queries all
// rows, upserts (updates) one row by primary key, deletes one row, then drops
// the table to clean up. Progress is printed at every step.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	mdb "github.com/visorcraft/mongreldb-go"
)

func main() {
	const url = "http://127.0.0.1:8453"
	// Unique name per run so re-running the example never collides with a
	// leftover table from a previous (possibly failed) run.
	table := fmt.Sprintf("example_crud_%d", time.Now().UnixNano())

	ctx := context.Background()
	db := mdb.NewClient(url)

	// Health check first; bail out if the daemon is not reachable.
	ok, err := db.Health(ctx)
	if err != nil {
		log.Fatalf("daemon not reachable at %s: %v", url, err)
	}
	if !ok {
		log.Fatalf("daemon not reachable at %s", url)
	}
	fmt.Println("Connected to MongrelDB")

	// Create the table. Column ids are stable on-wire identifiers used
	// everywhere else. Schema: id (int64 PK), name (varchar), score (float64).
	cols := []mdb.Column{
		{ID: 1, Name: "id", Type: "int64", PrimaryKey: true, Nullable: false},
		{ID: 2, Name: "name", Type: "varchar", PrimaryKey: false, Nullable: false},
		{ID: 3, Name: "score", Type: "float64", PrimaryKey: false, Nullable: false},
	}
	tid, err := db.CreateTable(ctx, table, cols)
	if err != nil {
		log.Fatalf("create table: %v", err)
	}
	fmt.Printf("Created table %q (id %d)\n", table, tid)

	// Always drop the table on exit, even if a step below fails.
	defer func() {
		if err := db.DropTable(ctx, table); err != nil {
			log.Printf("drop table: %v", err)
		} else {
			fmt.Printf("Dropped table %q\n", table)
		}
	}()

	// Insert three rows. Cells map column id -> value.
	rows := []mdb.Cells{
		{1: int64(1), 2: "Alice", 3: 95.5},
		{1: int64(2), 2: "Bob", 3: 82.0},
		{1: int64(3), 2: "Carol", 3: 78.3},
	}
	for _, r := range rows {
		if _, err := db.Put(ctx, table, r, ""); err != nil {
			log.Fatalf("put: %v", err)
		}
	}
	fmt.Println("Inserted 3 rows")

	n, err := db.Count(ctx, table)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("Total rows: %d\n", n)

	// Query all rows (no conditions).
	all, err := db.Query(table).Execute(ctx)
	if err != nil {
		log.Fatalf("query: %v", err)
	}
	fmt.Printf("Query returned %d rows:\n", len(all))
	for _, r := range all {
		fmt.Printf("  %v\n", r)
	}

	// Upsert (update) Alice's score to 100.0. updateCells supplies the values
	// written on a primary-key conflict.
	if _, err := db.Upsert(ctx, table,
		mdb.Cells{1: int64(1), 2: "Alice", 3: 100.0},
		mdb.Cells{2: "Alice", 3: 100.0}, ""); err != nil {
		log.Fatalf("upsert: %v", err)
	}
	fmt.Println("Upserted Alice's score to 100.0")

	n, err = db.Count(ctx, table)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("Total rows after upsert: %d\n", n)

	// Delete Carol (primary key 3).
	if err := db.DeleteByPK(ctx, table, int64(3)); err != nil {
		log.Fatalf("delete: %v", err)
	}
	n, err = db.Count(ctx, table)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("Deleted Carol; remaining rows: %d\n", n)
}
