# SQL

MongrelDB ships a DataFusion-backed SQL engine at `POST /sql`. From Go, run
SQL with `Client.SQL`:

```go
rows, err := db.SQL(ctx, "SELECT 1")
```

This guide covers the SQL surface - DDL, DML, `CREATE TABLE AS SELECT`,
recursive CTEs, and window functions - and when to reach for SQL versus the
native query builder.

---

## How `SQL` behaves

`Client.SQL(ctx, sql)` sends `{"sql": "...", "format": "json"}` to `/sql`. The
client requests the JSON result format, so it returns the decoded rows when the
statement produces a result set, and an empty slice with a nil error otherwise.

In practice:

- **DDL and DML** (`CREATE TABLE`, `INSERT`, `UPDATE`, `DELETE`) reply with a
  non-JSON status body. `SQL` returns `[]`, nil - success is the signal.
- **`SELECT`** returns a JSON array of row objects (keyed by column name), which
  `SQL` decodes into `[]map[string]any`. An empty result set returns `[]`, nil.

Errors are mapped to the same typed sentinels as everything else: an HTTP 400
or 5xx wraps `mdb.ErrQuery`; 409 wraps `mdb.ErrConflict`; and so on. See
[errors.md](errors.md).

```go
if _, err := db.SQL(ctx, "INSERT INTO orders (id, customer, amount) VALUES (99, 'Zoe', 999.0)"); err != nil {
	var re *mdb.ResponseError
	if errors.As(err, &re) && re.Code == "UNIQUE_VIOLATION" {
		log.Println("duplicate row:", re.Message)
	}
}
```

## CREATE TABLE

Define a table in SQL instead of via `Client.CreateTable`. Column ids are
assigned by the server when not stated.

```go
db.SQL(ctx, `
CREATE TABLE products (
  id          INT64 PRIMARY KEY,
  name        VARCHAR,
  price       FLOAT64,
  category    VARCHAR,
  in_stock    BOOLEAN
)`)
```

## INSERT

```go
db.SQL(ctx, "INSERT INTO products (id, name, price, category, in_stock) VALUES (1, 'Widget', 9.99, 'tools', true)")
db.SQL(ctx, "INSERT INTO products VALUES (2, 'Gadget', 19.99, 'tools', true)")
```

For bulk inserts, the native batch transaction (`Client.Begin`) is usually
faster because it stages ops in one round trip without re-parsing SQL.

## UPDATE

```go
db.SQL(ctx, "UPDATE products SET price = 14.99 WHERE id = 1")
db.SQL(ctx, "UPDATE orders SET amount = 200.0 WHERE customer = 'Bob'")
```

## DELETE

```go
db.SQL(ctx, "DELETE FROM products WHERE in_stock = false")
db.SQL(ctx, "DELETE FROM products WHERE id = 2")
```

## SELECT

```go
rows, err := db.SQL(ctx, "SELECT id, name FROM products WHERE category = 'tools' ORDER BY price")
db.SQL(ctx, "SELECT category, COUNT(*) AS n FROM products GROUP BY category")
```

`SQL` requests `format: "json"`, so a SELECT returns its rows decoded into a
`[]map[string]any`. Access columns by name, e.g. `row["price"]`.

## CREATE TABLE AS SELECT

Materialize a query result into a new table. Great for snapshots, rollups,
and denormalized aggregates.

```go
// Snapshot all high-value orders into a new table.
db.SQL(ctx, "CREATE TABLE archive AS SELECT * FROM orders WHERE amount > 500")

// Roll up sales by customer.
db.SQL(ctx, `
CREATE TABLE sales_by_customer AS
SELECT customer, SUM(amount) AS total
FROM orders
GROUP BY customer
`)
```

The new table inherits column types from the query. Query it afterward with
the native builder or SQL.

## Recursive CTEs

`WITH RECURSIVE` is fully supported. Classic use cases: series generation,
hierarchy/graph traversal.

```go
// Generate the numbers 1..10.
db.SQL(ctx, `
WITH RECURSive r(n) AS (
  SELECT 1
  UNION ALL
  SELECT n + 1 FROM r WHERE n < 10
)
SELECT n FROM r
`)
```

A common practical example is walking an adjacency list:

```go
db.SQL(ctx, `
WITH RECURSIVE descendants(id) AS (
  SELECT id FROM categories WHERE id = 1
  UNION ALL
  SELECT c.id FROM categories c
  JOIN descendants d ON c.parent_id = d.id
)
SELECT id FROM descendants
`)
```

## Window functions

Window functions compute aggregates/rankings across a moving window without
collapsing rows. Useful for top-N-per-group, running totals, and row numbers.

```go
// Row number within each customer, ordered by amount descending.
db.SQL(ctx, `
SELECT id, customer, amount,
       ROW_NUMBER() OVER (PARTITION BY customer ORDER BY amount DESC) AS rn
FROM orders
`)

// Running total per customer.
db.SQL(ctx, `
SELECT id, customer, amount,
       SUM(amount) OVER (PARTITION BY customer ORDER BY id) AS running_total
FROM orders
`)
```

`RANK()`, `DENSE_RANK()`, `LAG()`, `LEAD()`, `NTILE()`, and the usual
window-frame clauses are available through DataFusion.

## When to use SQL vs. the query builder

Both read from the same tables, but they are optimized for different jobs.

| Reach for | When |
|-----------|------|
| **`QueryBuilder`** | Point lookups, range scans, bitmap filters, full-text, and vector similarity that map to a native index. Sub-millisecond, no parser overhead, and rows decode into Go maps directly. |
| **SQL** | DDL (`CREATE TABLE`, schemas, materialized views), multi-statement setup, joins, recursive CTEs, window functions, and arbitrary aggregates. Also the natural choice for admin scripts and one-off analysis. |

Rules of thumb:

- Need a typed Go `[]map[string]any` of matching rows? Use the query builder.
- Building/dropping tables, or running a `CREATE TABLE AS SELECT`? Use SQL.
- Joining multiple tables, computing rankings, or walking a graph? Use SQL.
- Filtering by one or more indexed columns? Use the query builder - it is
  faster and avoids SQL parsing/decoding overhead.

Mix freely: create tables with SQL, write rows with `Client.Put`, read them
back with `QueryBuilder`, and run analytics with SQL.

## Next steps

- [queries.md](queries.md) - every native index condition in detail
- [transactions.md](transactions.md) - bulk inserts via batch transactions
- [errors.md](errors.md) - handling SQL execution errors
