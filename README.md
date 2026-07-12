<p align="center">
  <img src="assets/mongrel.png" alt="MongrelDB logo" width="250" />
</p>

<h1 align="center">MongrelDB Go Client</h1>

<p align="center">
  <b>Pure Go client for MongrelDB - embedded+server database with SQL, vector search, full-text search, and AI-native retrieval.</b>
  <br />
  No cgo and no external dependencies - built on the standard library <code>net/http</code>. The API mirrors the MongrelDB PHP client.
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/visorcraft/mongreldb-go"><img src="https://pkg.go.dev/badge/github.com/visorcraft/mongreldb-go.svg" alt="Go Reference" /></a>
  <a href="https://goreportcard.com/report/github.com/visorcraft/mongreldb-go"><img src="https://goreportcard.com/badge/github.com/visorcraft/mongreldb-go" alt="Go Report Card" /></a>
  <a href="https://github.com/visorcraft/MongrelDB-Go/actions/workflows/ci.yml"><img src="https://github.com/visorcraft/MongrelDB-Go/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="#license"><img src="https://img.shields.io/badge/license-MIT%20OR%20Apache--2.0-blue.svg" alt="License" /></a>
</p>

## Package

| Surface | Package | Install |
|---|---|---|
| Go client | `github.com/visorcraft/mongreldb-go` | `go get github.com/visorcraft/mongreldb-go` |

## Requirements

- **Go 1.22 or newer**
- A running [`mongreldb-server`](https://github.com/visorcraft/MongrelDB) daemon

## What It Provides

- **Typed CRUD** over the Kit transaction endpoint: `Put`, `Upsert` (insert-or-update on PK conflict), `Delete` by row id or primary key, all with optional idempotency keys for safe retries.
- **Fluent query builder** that pushes conditions down to the engine's specialized indexes for sub-millisecond lookups: bitmap equality/IN, learned-range, null checks, FM-index full-text search, HNSW vector similarity (`ann`), and sparse vector match. Friendly aliases (`column` → `column_id`, `min`/`max` → `lo`/`hi`) are translated to the server's on-wire keys.
- **Idempotent batch transactions** - operations staged locally and committed atomically, with the engine enforcing unique, foreign-key, and check constraints at commit time. Idempotency keys return the original response on duplicate commits, even after a crash.
- **Full SQL access** through the DataFusion-backed `/sql` endpoint: recursive CTEs, window functions, `CREATE TABLE AS SELECT`, materialized views, and multi-statement execution.
- **Schema management**: typed table creation, full schema catalog, and per-table descriptors.
- **User/role/credentials management** via SQL: Argon2id-hashed catalog users, roles, and `GRANT`/`REVOKE` table-level permissions, all executed through `SQL`.
- **Maintenance**: compaction (all tables or per-table).
- **Pluggable transport**: bring your own `*http.Client`, or set a per-request timeout. Bearer token and HTTP Basic auth are first-class options.
- **Typed errors**: `ErrAuth` (401/403), `ErrNotFound` (404), `ErrConflict` (409, with error code + op index), and `ErrQuery` (everything else), all exposed as `errors.Is`-matchable sentinels plus a `*ResponseError` carrying the status code and decoded server envelope.

## Examples

Task-focused, commented guides live in [`docs/`](docs):

- [Quickstart](docs/quickstart.md) - install, start the daemon, write and run a complete program.
- [Transactions](docs/transactions.md) - batch commits, idempotency keys, constraint handling.
- [Queries](docs/queries.md) - every native condition type and the index it pushes down to.
- [SQL](docs/sql.md) - recursive CTEs, window functions, advanced SQL.
- [Authentication](docs/auth.md) - Bearer token, HTTP Basic, and open modes.
- [Errors](docs/errors.md) - sentinel errors, `*ResponseError`, and recovery patterns.

## Quick Example

```go
package main

import (
	"context"
	"fmt"
	"log"

	mdb "github.com/visorcraft/mongreldb-go"
)

func main() {
	ctx := context.Background()

	// Connect to a running mongreldb-server daemon.
	db := mdb.NewClient("http://127.0.0.1:8453")

	// Create a table. Column ids are stable on-wire identifiers.
	db.CreateTable(ctx, "orders", []mdb.Column{
		{ID: 1, Name: "id", Type: "int64", PrimaryKey: true, Nullable: false},
		{ID: 2, Name: "customer", Type: "varchar", PrimaryKey: false, Nullable: false},
		{ID: 3, Name: "amount", Type: "float64", PrimaryKey: false, Nullable: false},
	})

	// Insert rows (cells map column id -> value).
	db.Put(ctx, "orders", mdb.Cells{1: int64(1), 2: "Alice", 3: 99.50}, "")
	db.Put(ctx, "orders", mdb.Cells{1: int64(2), 2: "Bob", 3: 150.00}, "")

	// Upsert (insert or update on PK conflict).
	db.Upsert(ctx, "orders",
		mdb.Cells{1: int64(1), 2: "Alice", 3: 120.00},
		mdb.Cells{3: 120.00}, "")

	// Query with a native index condition (learned-range index).
	rows, err := db.Query("orders").
		Where("range", map[string]any{"column": int64(3), "min": 100.0}).
		Projection([]int64{1, 2}).
		Limit(100).
		Execute(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("rows:", len(rows))

	n, _ := db.Count(ctx, "orders")
	fmt.Println("count:", n) // 2

	// Run SQL.
	db.SQL(ctx, "UPDATE orders SET amount = 200.0 WHERE customer = 'Bob'")
}
```

## Authentication

```go
// Bearer token (--auth-token mode)
db := mdb.NewClient("http://127.0.0.1:8453", mdb.WithToken("my-secret-token"))

// HTTP Basic (--auth-users mode)
db := mdb.NewClient("http://127.0.0.1:8453", mdb.WithBasicAuth("admin", "s3cret"))

// Custom transport / timeout
db := mdb.NewClient("http://127.0.0.1:8453",
	mdb.WithHTTPClient(&http.Client{Timeout: 60 * time.Second}))
```

## Batch transactions

Operations are staged locally and committed atomically. The engine enforces
unique, foreign-key, and check constraints at commit time. Pass an optional
constraints map to `CreateTable` to provision engine checks in the same call.

`Column.DefaultValueJSON` preserves a static JSON scalar and takes precedence
over legacy string `DefaultValue`. `DefaultExpr` selects dynamic `"now"` or
`"uuid"` defaults and takes precedence server-side. Explicit null is
representable with `json.RawMessage("null")`; a literal `"now"` string default
must use `DefaultValueJSON`, not `DefaultExpr`.

```go
checks := map[string]any{"checks": []any{
	map[string]any{"id": 1, "name": "id_present", "expr": map[string]any{"IsNotNull": 1}},
}}
_, err := db.CreateTable(ctx, "orders", cols, checks)
```

```go
txn := db.Begin()
txn.Put("orders", mdb.Cells{1: int64(10), 2: "Dave", 3: 50.00}, false)
txn.Put("orders", mdb.Cells{1: int64(11), 2: "Eve", 3: 75.00}, false)
txn.DeleteByPK("orders", int64(2))

results, err := txn.Commit(ctx, "") // atomic - all or nothing
if err != nil {
	// A constraint violation rolls back every op.
	var re *mdb.ResponseError
	if errors.As(err, &re) && re.Code == "UNIQUE_VIOLATION" {
		log.Println("duplicate:", re.Message, "at op", re.OpIndex)
	}
	_ = txn.Rollback() // discard locally as well
}
_ = results

// Idempotent commit - safe to retry; the daemon returns the original response.
txn2 := db.Begin()
txn2.Put("orders", mdb.Cells{1: int64(20), 2: "Frank", 3: 100.00}, false)
txn2.Commit(ctx, "order-20-create")
```

## Native query builder

Conditions push down to the engine's specialized indexes. The builder accepts
friendly aliases that are translated to the server's on-wire keys: `column`
(→ `column_id`), `min`/`max` (→ `lo`/`hi`). The canonical keys are also
accepted directly.

```go
// Bitmap equality (low-cardinality columns).
db.Query("orders").Where("bitmap_eq", map[string]any{"column": int64(2), "value": "Alice"}).Execute(ctx)

// Range query (learned-range index).
db.Query("orders").
	Where("range", map[string]any{"column": int64(3), "min": 50.0, "max": 150.0}).
	Limit(100).Execute(ctx)

// Full-text search (FM-index).
db.Query("documents").
	Where("fm_contains", map[string]any{"column": int64(2), "pattern": "database performance"}).
	Limit(10).Execute(ctx)

// Vector similarity search (HNSW).
db.Query("embeddings").
	Where("ann", map[string]any{"column": int64(2), "query": []float32{0.1, 0.2, 0.3}, "k": 10}).
	Execute(ctx)

// Check whether a result was capped by the limit.
q := db.Query("orders").Where("range", map[string]any{"column": int64(3), "min": int64(0)}).Limit(100)
rows, _ := q.Execute(ctx)
if q.Truncated() {
	// result set hit the limit; more matches exist on the server
}
_ = rows
```

## SQL

```go
db.SQL(ctx, "INSERT INTO orders (id, customer, amount) VALUES (99, 'Zoe', 999.0)")
db.SQL(ctx, "CREATE TABLE archive AS SELECT * FROM orders WHERE amount > 500")

// Recursive CTEs and window functions
db.SQL(ctx, "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM r WHERE n<10) SELECT n FROM r")
db.SQL(ctx, "SELECT id, ROW_NUMBER() OVER (PARTITION BY customer ORDER BY amount DESC) FROM orders")
```

## History retention and time travel

The daemon keeps a sliding window of historical commit epochs. Use the retention
API to control how far back `AS OF EPOCH` SQL queries can read. The routes are
database-wide and require `ADMIN` permission when the daemon runs with auth.

```go
ret, err := db.SetHistoryRetentionEpochs(ctx, 1000)
if err != nil {
    log.Fatal(err)
}
fmt.Println("window:", ret.HistoryRetentionEpochs)
fmt.Println("earliest:", ret.EarliestRetainedEpoch)

// Read a previous version of the table.
rows, _ := db.SQL(ctx, "SELECT value FROM orders AS OF EPOCH 42 WHERE id = 1")
```

Increasing the retention window prevents newly written epochs from being
reclaimed; it cannot restore history that has already been pruned, so
`EarliestRetainedEpoch` never moves backward.

## User & role management

User, role, and permission management is performed through SQL against the
daemon's catalog. Passwords are Argon2id-hashed server-side.

```go
db.SQL(ctx, "CREATE USER admin WITH PASSWORD 's3cret-pw'")
db.SQL(ctx, "ALTER USER admin SET ADMIN TRUE")

db.SQL(ctx, "CREATE ROLE analyst")
db.SQL(ctx, "GRANT select ON orders TO analyst") // table-level permission
db.SQL(ctx, "GRANT analyst TO alice")

db.SQL(ctx, "SELECT username FROM catalog.users") // list users
db.SQL(ctx, "SELECT name FROM catalog.roles")     // list roles
```

## Error handling

Every non-2xx response is mapped to a typed error. Match with `errors.Is` for
the category, or `errors.As` into a `*ResponseError` for the status code and
the server's decoded error envelope.

```go
_, err := db.SchemaFor(ctx, "missing_table")
switch {
case errors.Is(err, mdb.ErrNotFound):
	log.Println("not found")
case errors.Is(err, mdb.ErrConflict):
	var re *mdb.ResponseError
	errors.As(err, &re)
	log.Printf("constraint %s at op %d", re.Code, re.OpIndex)
case errors.Is(err, mdb.ErrAuth):
	log.Println("not authorized")
case errors.Is(err, mdb.ErrQuery):
	log.Println("query/server error")
}

// Or inspect directly:
var re *mdb.ResponseError
if errors.As(err, &re) {
	fmt.Println(re.Status, re.Code, re.Message) // e.g. 404 NOT_FOUND no such table
}
```

## API reference

### `Client`

| Method | Description |
|--------|-------------|
| `NewClient(url string, opts ...Option) *Client` | Construct a client (url defaults to `http://127.0.0.1:8453`) |
| `WithToken`, `WithBasicAuth`, `WithHTTPClient`, `WithTimeout` | Options: Bearer token, Basic auth, custom `*http.Client`, request timeout |
| `Health(ctx) (bool, error)` | Check daemon health |
| `TableNames(ctx) ([]string, error)` | List table names |
| `CreateTable(ctx, name, columns, constraints...) (int64, error)` | Create a table, optionally attach engine constraints; returns the table id |
| `DropTable(ctx, name) error` | Drop a table |
| `Count(ctx, table) (int64, error)` | Row count |
| `Put(ctx, table, cells, idempotencyKey) (map[string]any, error)` | Insert a row |
| `Upsert(ctx, table, cells, updateCells, key) (map[string]any, error)` | Upsert a row |
| `Delete(ctx, table, rowID) error` | Delete by row id |
| `DeleteByPK(ctx, table, pk) error` | Delete by primary key |
| `Query(table) *QueryBuilder` | Start a native query |
| `SQL(ctx, sql) ([]map[string]any, error)` | Execute SQL |
| `Schema(ctx) (map[string]map[string]any, error)` | Full schema catalog |
| `SchemaFor(ctx, table) (map[string]any, error)` | Single-table descriptor |
| `HistoryRetention(ctx) (HistoryRetention, error)` | Get both retention values |
| `SetHistoryRetentionEpochs(ctx, epochs) (HistoryRetention, error)` | Set the durable history-retention window |
| `HistoryRetentionEpochs(ctx) (uint64, error)` | Current retention window size in epochs |
| `EarliestRetainedEpoch(ctx) (uint64, error)` | Earliest epoch still readable via `AS OF EPOCH` |
| `Compact(ctx) (map[string]any, error)` | Compact all tables |
| `CompactTable(ctx, name) (map[string]any, error)` | Compact one table |
| `Begin() *Transaction` | Start a batch |

### `QueryBuilder`

| Method | Description |
|--------|-------------|
| `Where(type, params) *QueryBuilder` | Add a native condition (AND-ed) |
| `Projection(columnIDs []int64) *QueryBuilder` | Set column projection |
| `Limit(limit int64) *QueryBuilder` | Set row limit |
| `Build() map[string]any` | Build the request payload |
| `Execute(ctx) ([]map[string]any, error)` | Run the query |
| `Truncated() bool` | Whether the last `Execute` result hit the limit |

### `Transaction`

| Method | Description |
|--------|-------------|
| `Put(table, cells, returning) *Transaction` | Stage an insert |
| `Upsert(table, cells, updateCells, returning) *Transaction` | Stage an upsert |
| `Delete(table, rowID) *Transaction` | Stage a delete by row id |
| `DeleteByPK(table, pk) *Transaction` | Stage a delete by primary key |
| `Count() int` | Number of staged operations |
| `Commit(ctx, idempotencyKey) ([]map[string]any, error)` | Commit atomically |
| `Rollback() error` | Discard all operations |

## Building and testing

The test suite is a live integration suite: it boots a real `mongreldb-server`
daemon and exercises the full client surface against it. It skips
automatically when no daemon is available.

```sh
go build ./...
go vet ./...

# Run the offline unit tests (no daemon needed):
go test -short ./...

# Run the full live suite. The harness boots mongreldb-server itself if it can
# find the binary (in this order):
#   1. the MONGRELDB_SERVER env var
#   2. ./bin/mongreldb-server
#   3. mongreldb-server on PATH
# Or point it at an already-running daemon with MONGRELDB_URL.
MONGRELDB_SERVER=./bin/mongreldb-server go test -v ./...
```

Fetch a prebuilt server binary from the [MongrelDB releases](https://github.com/visorcraft/MongrelDB/releases):

```sh
mkdir -p bin
curl -fsSL -o bin/mongreldb-server \
  https://github.com/visorcraft/MongrelDB/releases/download/v0.49.0/mongreldb-server-linux-x64
chmod +x bin/mongreldb-server
```

## Contributing

Contributions are welcome. Please:

1. Open an issue first for non-trivial changes.
2. Add focused tests near your change - the suite must stay green.
3. Run `gofmt -l .` and `go vet ./...` before submitting.
4. Keep the client cgo-free and dependency-free (standard library only).

## License

Dual-licensed under the **MIT License** or the **Apache License, Version 2.0**,
at your option. See [MIT](LICENSE-MIT) OR [Apache-2.0](LICENSE-APACHE) for the full text.

`SPDX-License-Identifier: MIT OR Apache-2.0`
