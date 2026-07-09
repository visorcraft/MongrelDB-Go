// Example: query builder conditions with the MongrelDB Go client.
//
// Run from the repo root:
//
//	go run ./examples/query_builder.go
//
// Requires a mongreldb-server daemon running on http://127.0.0.1:8453.
//
// This example creates a table, inserts five rows with varying values, then
// uses the native query builder to fetch rows by a range condition and by an
// exact primary-key match. It cleans up by dropping the table.
package main

import (
	"context"
	"fmt"
	"log"

	mdb "github.com/visorcraft/mongreldb-go"
)

const (
	url   = "http://127.0.0.1:8453"
	table = "example_query"
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

	// Five rows with varying scores.
	seed := []mdb.Cells{
		{1: int64(1), 2: "Alice", 3: 40.0},
		{1: int64(2), 2: "Bob", 3: 65.0},
		{1: int64(3), 2: "Carol", 3: 82.0},
		{1: int64(4), 2: "Dave", 3: 91.0},
		{1: int64(5), 2: "Eve", 3: 12.5},
	}
	for _, r := range seed {
		if _, err := db.Put(ctx, table, r, ""); err != nil {
			log.Fatalf("put: %v", err)
		}
	}
	fmt.Printf("Inserted %d rows\n", len(seed))

	// Range condition: scores in [60.0, 90.0]. The "column" alias maps to the
	// server's column_id; pass the numeric column id (3), not the name.
	rng, err := db.Query(table).
		Where("range", map[string]any{"column": int64(3), "min": 60.0, "max": 90.0}).
		Execute(ctx)
	if err != nil {
		log.Fatalf("range query: %v", err)
	}
	fmt.Printf("Range query (score in [60,90]) returned %d rows:\n", len(rng))
	for _, r := range rng {
		fmt.Printf("  %v\n", r)
	}

	// Primary-key condition: fetch the single row with id == 4.
	pk, err := db.Query(table).
		Where("pk", map[string]any{"value": int64(4)}).
		Execute(ctx)
	if err != nil {
		log.Fatalf("pk query: %v", err)
	}
	fmt.Printf("PK query (id == 4) returned %d rows:\n", len(pk))
	for _, r := range pk {
		fmt.Printf("  %v\n", r)
	}

	if err := db.DropTable(ctx, table); err != nil {
		log.Fatalf("drop table: %v", err)
	}
	fmt.Printf("Dropped table %q\n", table)
}
