# Quickstart

Zero to a running MongrelDB Go program in fifteen minutes. This guide assumes a
fresh machine and walks through installing the prerequisites, starting the
daemon, and writing, running, and understanding a complete program.

---

## 1. Prerequisites

You need two things installed: the Go toolchain and a `mongreldb-server`
daemon.

### Install Go 1.22 or newer

MongrelDB Go is standard-library only, so any recent Go works. Verify it:

```sh
go version
# go version go1.22.x ...
```

If you do not have it, install from <https://go.dev/dl/> or your package
manager (e.g. `pacman -S go`, `brew install go`).

### Install mongreldb-server

Fetch a prebuilt server binary from the
[MongrelDB releases](https://github.com/visorcraft/MongrelDB/releases):

```sh
mkdir -p bin
curl -fsSL -o bin/mongreldb-server \
  https://github.com/visorcraft/MongrelDB/releases/download/v0.53.3/mongreldb-server-linux-x64
chmod +x bin/mongreldb-server
```

Verify it runs:

```sh
./bin/mongreldb-server --version
```

## 2. Start the daemon

By default `mongreldb-server` listens on `http://127.0.0.1:8453` and stores
data in the current working directory.

```sh
mkdir -p /tmp/mdb-data && cd /tmp/mdb-data
/path/to/mongreldb-server
```

In another terminal, sanity-check it:

```sh
curl http://127.0.0.1:8453/health
# ok
```

Leave the daemon running for the rest of this guide.

## 3. Create a project and pull in the client

```sh
mkdir mdb-demo && cd mdb-demo
go mod init mdb-demo
go get github.com/visorcraft/mongreldb-go
```

## 4. Write your first program

Create `main.go`:

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

	// 1. Connect to the daemon. Empty URL falls back to http://127.0.0.1:8453.
	db := mdb.NewClient("http://127.0.0.1:8453")

	// 2. Health check before doing anything else.
	if ok, err := db.Health(ctx); err != nil || !ok {
		log.Fatalf("daemon not reachable: %v", err)
	}

	// 3. Create a table. Each column has a stable numeric id, a name, a type,
	//    and flags. The first column is the primary key.
	tid, err := db.CreateTable(ctx, "orders", []mdb.Column{
		{ID: 1, Name: "id", Type: "int64", PrimaryKey: true, Nullable: false},
		{ID: 2, Name: "customer", Type: "varchar", PrimaryKey: false, Nullable: false},
		{ID: 3, Name: "amount", Type: "float64", PrimaryKey: false, Nullable: false},
	})
	if err != nil {
		log.Fatalf("create table: %v", err)
	}
	fmt.Println("created table id:", tid)

	// 4. Insert rows. Cells maps column id -> value. The empty string means
	//    "no idempotency key" (fine for a one-shot demo).
	if _, err := db.Put(ctx, "orders", mdb.Cells{1: int64(1), 2: "Alice", 3: 99.5}, ""); err != nil {
		log.Fatal(err)
	}
	if _, err := db.Put(ctx, "orders", mdb.Cells{1: int64(2), 2: "Bob", 3: 150.0}, ""); err != nil {
		log.Fatal(err)
	}

	// 5. Query with a native index condition. The range index serves this in
	//    sub-millisecond. Projection selects only column ids 1 and 2.
	rows, err := db.Query("orders").
		Where("range", map[string]any{"column": int64(3), "min": 100.0}).
		Projection([]int64{1, 2}).
		Limit(100).
		Execute(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, row := range rows {
		fmt.Println("row:", row)
	}

	// 6. Count the rows.
	n, _ := db.Count(ctx, "orders")
	fmt.Println("total rows:", n)
}
```

Run it:

```sh
go run .
```

You should see:

```
created table id: 1
row: map[1:2 2:Bob]
total rows: 2
```

## 5. What each part does

| Code | What it does |
|------|--------------|
| `mdb.NewClient(url)` | Builds an HTTP client targeting one daemon. Safe to share across goroutines. |
| `db.Health(ctx)` | GET `/health`; returns `true` when the daemon answers. Always check before real work. |
| `db.CreateTable(ctx, name, cols, constraints...)` | POST `/kit/create_table`; optional constraints map carries engine checks. Column `id`s are the on-wire identifiers; use them everywhere else. |
| `db.Put(ctx, table, cells, key)` | Single-op transaction: POST `/kit/txn` with one `put` op. `cells` is flattened to `[col_id, val, ...]`. |
| `db.Query(table).Where(...)` | Builds a `/kit/query` body. `Where` pushes a condition down to a native index. |
| `.Projection([]int64{1,2})` | Server returns only those column ids, saving bandwidth. |
| `.Limit(100)` | Caps the result; check `q.Truncated()` afterward to detect overflow. |
| `.Execute(ctx)` | Sends the query and decodes the `rows` array. |
| `db.Count(ctx, table)` | GET `/tables/{name}/count`. |

## 6. Static defaults and history retention

Column defaults are serialized as typed JSON values. Use `DefaultValueJSON` for
static scalars and `DefaultExpr` for dynamic `"now"` or `"uuid"` defaults. An
explicit JSON null is written as `json.RawMessage("null")`.

```go
import "encoding/json"

nowExpr := "now"
_, err := db.CreateTable(ctx, "events", []mdb.Column{
    {ID: 1, Name: "id", Type: "int64", PrimaryKey: true, Nullable: false},
    {ID: 2, Name: "status", Type: "varchar", DefaultValueJSON: "draft"},
    {ID: 3, Name: "score", Type: "int64", DefaultValueJSON: int64(0)},
    {ID: 4, Name: "enabled", Type: "bool", DefaultValueJSON: true},
    {ID: 5, Name: "archived", Type: "varchar", DefaultValueJSON: json.RawMessage("null")},
    {ID: 6, Name: "created_at", Type: "timestamp_nanos", DefaultExpr: &nowExpr},
})
```

The daemon keeps a configurable window of historical epochs for time-travel
queries. Set the window before writing data you may need to revisit:

```go
if _, err := db.SetHistoryRetentionEpochs(ctx, 1000); err != nil {
    panic(err)
}

// Later, read an earlier version of the table with AS OF EPOCH.
rows, _ := db.SQL(ctx, "SELECT status FROM events AS OF EPOCH 42 WHERE id = 1")
```

## 7. Common pitfalls

**Using the column name instead of the column id.** Every on-wire API uses the
numeric `id` from `CreateTable`, never the `name`. The query builder's
`column` alias maps to the server's `column_id` - pass the int64 id, not the
string name:

```go
// Wrong:
.Where("range", map[string]any{"column": "amount", "min": 100.0})
// Right:
.Where("range", map[string]any{"column": int64(3), "min": 100.0})
```

**Forgetting `context.Context`.** Every call takes a `context.Context` as its
first argument. Pass `context.Background()` if you have nothing better; use
`context.WithTimeout` for bounded calls.

**Treating a single `Put` as non-transactional.** `Put` is a one-op
transaction. A unique constraint violation surfaces as an error wrapping
`mdb.ErrConflict` (HTTP 409), not as a silent no-op.

**Calling `Commit` twice on the same `Transaction`.** The second call returns
`mdb.ErrTxnCommitted`. Create a fresh `db.Begin()` for each logical unit of
work.

**Reusing a `QueryBuilder` and expecting a fresh `Truncated`.** `Truncated()`
reflects the most recent `Execute`. Build a new query, or re-run `Execute`
before reading it.

**Expecting `SQL` to return non-nil rows.** `SQL` requests
`format: "json"`, so a `SELECT` returns its rows decoded into a
`[]map[string]any`. Statements that produce no rows (DDL/DML, or an empty
result set) return an empty slice (not an error).

**Pointing at a daemon that requires auth.** If the daemon was started with
`--auth-token` or `--auth-users`, every call 401s unless you pass
`mdb.WithToken(...)` or `mdb.WithBasicAuth(...)`. See [auth.md](auth.md).

## Next steps

- [transactions.md](transactions.md) - atomic batches, idempotency, retries
- [queries.md](queries.md) - every native index condition
- [sql.md](sql.md) - recursive CTEs, window functions, `CREATE TABLE AS SELECT`
- [auth.md](auth.md) - bearer tokens, basic auth, user/role management
- [errors.md](errors.md) - the full error hierarchy and recovery patterns
