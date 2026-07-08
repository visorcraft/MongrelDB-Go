# Queries

The fluent `QueryBuilder` pushes conditions down to MongrelDB's native indexes
for sub-millisecond lookups — bitmap, learned-range, FM-index full text, HNSW
vector similarity, and more. Each condition type maps to one specialized
index; conditions are AND-ed together.

```go
q := db.Query("orders").
	Where("range", map[string]any{"column": int64(3), "min": 100.0, "max": 500.0}).
	Projection([]int64{1, 2}).
	Limit(100)
rows, err := q.Execute(ctx)
```

This guide covers every condition type, projection, limits and truncation,
combining conditions, and the friendly aliases the builder translates for you.

---

## The basics

Every query starts with `Client.Query(table)` and ends with `Execute(ctx)`:

| Method | Purpose |
|--------|---------|
| `Where(condType, params)` | Add a native condition. Multiple `Where` calls are AND-ed. |
| `Projection(columnIDs)` | Return only these column ids (nil means all columns). |
| `Limit(n)` | Cap the number of rows. |
| `Build()` | Produce the request payload (useful for debugging). |
| `Execute(ctx)` | Send and decode. Records the `truncated` flag. |
| `Truncated()` | Whether the last `Execute` hit the limit. |

The request body produced by `Build()` matches the daemon's `/kit/query`
shape:

```json
{
  "table": "orders",
  "conditions": [{"range": {"column_id": 3, "lo": 100.0, "hi": 500.0}}],
  "projection": [1, 2],
  "limit": 100
}
```

## Condition types

`params` is a `map[string]any`. Column references use the numeric **column
id**, never the column name.

### `pk` — exact primary-key match

The fastest lookup. `value` is the primary-key value.

```go
db.Query("orders").
	Where("pk", map[string]any{"value": int64(42)}).
	Execute(ctx)
```

### `range` — integer range (learned-range index)

Inclusive bounds. Omit `lo` or `hi` for an open range.

```go
db.Query("orders").
	Where("range", map[string]any{
		"column": int64(3), // column id
		"min":    int64(100),
		"max":    int64(500),
	}).
	Execute(ctx)

// Open-ended: amount >= 100
db.Query("orders").
	Where("range", map[string]any{"column": int64(3), "min": int64(100)}).
	Execute(ctx)
```

### `range_f64` — float range with inclusive/exclusive control

Adds `lo_inclusive` / `hi_inclusive` flags (default inclusive).

```go
db.Query("orders").
	Where("range_f64", map[string]any{
		"column":        int64(3),
		"min":           100.0,
		"max":           500.0,
		"min_inclusive": true,
		"max_inclusive": false, // (100.0, 500.0]
	}).
	Execute(ctx)
```

### `bitmap_eq` — equality on a bitmap-indexed column

Best for low-cardinality columns (status, category, booleans).

```go
db.Query("orders").
	Where("bitmap_eq", map[string]any{"column": int64(2), "value": "Alice"}).
	Execute(ctx)
```

### `bitmap_in` — IN predicate on a bitmap-indexed column

Match any of a set of values.

```go
db.Query("orders").
	Where("bitmap_in", map[string]any{
		"column": int64(2),
		"values": []any{"Alice", "Bob", "Carol"},
	}).
	Execute(ctx)
```

### `is_null` / `is_not_null` — null checks

```go
db.Query("orders").Where("is_null", map[string]any{"column": int64(3)}).Execute(ctx)
db.Query("orders").Where("is_not_null", map[string]any{"column": int64(3)}).Execute(ctx)
```

### `fm_contains` — full-text substring search (FM-index)

Substring match within a column. Use `pattern` (the server key) or the
friendly `value` alias — both translate to `pattern` on the wire for FTS
conditions.

```go
db.Query("documents").
	Where("fm_contains", map[string]any{
		"column": int64(2),
		"pattern": "database performance",
	}).
	Limit(10).
	Execute(ctx)

// Friendly alias: "value" -> "pattern" for fm_contains only.
db.Query("documents").
	Where("fm_contains", map[string]any{"column": int64(2), "value": "database"}).
	Execute(ctx)
```

### `fm_contains_all` — multiple substrings, all must match

```go
db.Query("documents").
	Where("fm_contains_all", map[string]any{
		"column":  int64(2),
		"patterns": []any{"database", "performance"},
	}).
	Execute(ctx)
```

### `ann` — dense vector similarity (HNSW)

Approximate nearest-neighbors over a `float32` vector column. `k` is the
result count.

```go
db.Query("embeddings").
	Where("ann", map[string]any{
		"column": int64(2),
		"query":  []float32{0.1, 0.2, 0.3, 0.4},
		"k":      int64(10),
	}).
	Execute(ctx)
```

### `sparse_match` — sparse vector match

For sparse/bag-of-words vectors.

```go
db.Query("docs").
	Where("sparse_match", map[string]any{
		"column": int64(2),
		"query":  map[string]any{"0": 1.0, "7": 0.5, "42": 2.0},
		"k":      int64(10),
	}).
	Execute(ctx)
```

### `min_hash_similar` — MinHash similarity

Near-duplicate detection via MinHash signatures.

```go
db.Query("pages").
	Where("min_hash_similar", map[string]any{
		"column": int64(2),
		"query":  []int64{12, 99, 421, 7},
		"k":      int64(5),
	}).
	Execute(ctx)
```

## Projection (column selection)

`Projection([]int64{...})` restricts the columns in each returned row. Pass
`nil` (or skip the call) for all columns. Projecting to only the columns you
need cuts bandwidth and decode cost.

```go
// Return only the id and customer columns.
db.Query("orders").
	Where("range", map[string]any{"column": int64(3), "min": int64(100)}).
	Projection([]int64{1, 2}).
	Execute(ctx)
```

Returned rows are `map[string]any` keyed by the column id as a JSON-decoded
key (a string like `"2"`). Access accordingly:

```go
rows, _ := db.Query("orders").Projection([]int64{1, 2}).Execute(ctx)
for _, r := range rows {
	customer, _ := r["2"].(string)
	fmt.Println(customer)
}
```

## Limit and the truncated flag

`Limit(n)` caps the result. When the server has more matches than the limit
allows, it returns the first `n` and sets `truncated: true`. Read it with
`Truncated()` **after** `Execute`.

```go
q := db.Query("orders").
	Where("range", map[string]any{"column": int64(3), "min": int64(0)}).
	Limit(100)
rows, err := q.Execute(ctx)
if err != nil {
	log.Fatal(err)
}
if q.Truncated() {
	// 100 rows came back but more exist on the server. Either raise the
	// limit, page with a range predicate on the PK, or accept the cap.
	log.Printf("result capped at %d; more rows available", len(rows))
}
```

`Truncated()` returns `false` until `Execute` has run, so build a fresh query
for each independent lookup.

## Multiple AND conditions

Chain `Where` calls. Every condition must match; the server intersects the
index results.

```go
// Customer is Alice AND amount is between 100 and 500.
db.Query("orders").
	Where("bitmap_eq", map[string]any{"column": int64(2), "value": "Alice"}).
	Where("range", map[string]any{"column": int64(3), "min": int64(100), "max": int64(500)}).
	Projection([]int64{1, 3}).
	Limit(50).
	Execute(ctx)
```

Because each `Where` targets a different specialized index, the engine can
pick the most selective one to drive the lookup and intersect the rest.

## Friendly alias translation

The builder accepts readable parameter names and translates them to the
server's canonical on-wire keys. Both spellings work, so use whichever is
clearer in context.

| You write | Sent as | Applies to |
|-----------|---------|------------|
| `column` | `column_id` | all condition types |
| `min` | `lo` | `range`, `range_f64` |
| `max` | `hi` | `range`, `range_f64` |
| `min_inclusive` | `lo_inclusive` | `range_f64` |
| `max_inclusive` | `hi_inclusive` | `range_f64` |
| `value` | `pattern` | `fm_contains`, `fm_contains_all` only |

The `value` → `pattern` alias applies **only** to FTS conditions, because
`pk` and `bitmap_eq` use `value` as their canonical key. For those, write
`value` directly.

```go
// pk: "value" stays "value" (canonical)
.Where("pk", map[string]any{"value": int64(42)})

// fm_contains: "value" is translated to "pattern"
.Where("fm_contains", map[string]any{"column": int64(2), "value": "search term"})
// equivalent to:
.Where("fm_contains", map[string]any{"column_id": int64(2), "pattern": "search term"})
```

## Putting it together

A realistic combined lookup — bitmap equality + range + projection + limit +
truncation check:

```go
func topSpenders(ctx context.Context, db *mdb.Client, customer string) ([]map[string]any, error) {
	q := db.Query("orders").
		Where("bitmap_eq", map[string]any{"column": int64(2), "value": customer}).
		Where("range", map[string]any{"column": int64(3), "min": int64(100)}).
		Projection([]int64{1, 3}).
		Limit(50)
	rows, err := q.Execute(ctx)
	if err != nil {
		return nil, err
	}
	if q.Truncated() {
		log.Printf("warning: topSpenders result capped at 50")
	}
	return rows, nil
}
```

For arbitrary predicates, joins, and aggregations that the native indexes do
not cover, use SQL instead — see [sql.md](sql.md).
