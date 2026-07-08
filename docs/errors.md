# Error handling

Every non-2xx response from the daemon is mapped to a typed Go error. This is
the complete reference: the sentinel errors, the `*ResponseError` carrier,
the HTTP-status mapping, the daemon's error envelope, and recovery patterns
for each category.

---

## The error model

The client uses two complementary mechanisms:

1. **Sentinel errors** — `ErrAuth`, `ErrNotFound`, `ErrConflict`, `ErrQuery`.
   Match them with `errors.Is` to branch on the *category* of failure.
2. **`*ResponseError`** — a concrete type carrying the HTTP status code and
   the decoded server envelope (`code`, `message`, `op_index`). Match it with
   `errors.As` when you need the details.

Every error returned from a client call is either a plain `fmt.Errorf`-wrapped
error (for client-side failures like a bad context or transport error) or a
`*ResponseError` that wraps the relevant sentinel.

## Sentinel reference

| Sentinel | Meaning | Typical cause |
|----------|---------|---------------|
| `mdb.ErrNotFound` | HTTP 404 | Missing table, missing schema, dropped resource |
| `mdb.ErrConflict` | HTTP 409 | Unique, foreign-key, check, or trigger violation at commit |
| `mdb.ErrAuth` | HTTP 401 or 403 | Missing/bad credentials against an auth-enabled daemon |
| `mdb.ErrQuery` | HTTP 400 or 5xx | Malformed request, server-side failure, everything else not covered |

There is also one transaction-state sentinel:

| Sentinel | Meaning |
|----------|---------|
| `mdb.ErrTxnCommitted` | `Commit` or `Rollback` called on an already-committed `Transaction` |

## `*ResponseError` reference

```go
type ResponseError struct {
    Status  int     // HTTP status code
    Message string  // human-readable message from the daemon
    Code    string  // structured error code, e.g. "UNIQUE_VIOLATION"
    OpIndex *int    // offending op index within a batch, when reported
    // wraps the relevant sentinel so errors.Is works
}
```

The daemon's JSON error envelope (decoded into the fields above):

```json
{
  "status": "aborted",
  "error": {
    "code": "UNIQUE_VIOLATION",
    "message": "duplicate key in column 1",
    "op_index": 0
  }
}
```

Structured codes you will commonly see in `Code`:

| `Code` | Meaning |
|--------|---------|
| `UNIQUE_VIOLATION` | A unique/PK constraint rejected the commit |
| `FK_VIOLATION` | A foreign-key reference was missing |
| `CHECK_VIOLATION` | A check constraint or trigger rejected the commit |
| `NOT_FOUND` | A named resource (table, schema) does not exist |

## HTTP status → sentinel mapping

| HTTP status | Sentinel | Notes |
|-------------|----------|-------|
| 401, 403 | `ErrAuth` | Bad/missing credentials |
| 404 | `ErrNotFound` | Resource not found |
| 409 | `ErrConflict` | Constraint violation at commit |
| 400 | `ErrQuery` | Malformed request / bad query |
| 5xx | `ErrQuery` | Daemon-side failure |
| other non-2xx | `ErrQuery` | Catch-all |
| 2xx | (no error) | Success |

## Discriminating errors

### By category — `errors.Is`

```go
_, err := db.SchemaFor(ctx, "missing_table")
switch {
case errors.Is(err, mdb.ErrNotFound):
	log.Println("table does not exist")
case errors.Is(err, mdb.ErrConflict):
	log.Println("unexpected conflict on a read")
case errors.Is(err, mdb.ErrAuth):
	log.Println("bad credentials")
case errors.Is(err, mdb.ErrQuery):
	log.Println("server error or malformed request")
case err != nil:
	log.Println("other error:", err)
}
```

### By details — `errors.As`

```go
var re *mdb.ResponseError
if errors.As(err, &re) {
	fmt.Printf("status=%d code=%s op=%v msg=%s\n",
		re.Status, re.Code, re.OpIndex, re.Message)
}
```

`errors.As` succeeds whenever the error is (or wraps) a `*ResponseError`, so
you can combine it with `errors.Is`:

```go
if errors.Is(err, mdb.ErrConflict) {
	var re *mdb.ResponseError
	if errors.As(err, &re) {
		log.Printf("constraint %s at op %d: %s", re.Code, ptrToInt(re.OpIndex), re.Message)
	}
}
```

## Recovery patterns

### Auth failure — do not retry blindly

A retry will not fix bad credentials. Surface the error to the caller or
operator.

```go
if errors.Is(err, mdb.ErrAuth) {
	// Refresh credentials from your secret store, or fail fast.
	return fmt.Errorf("credentials rejected; refresh token: %w", err)
}
```

### Not found — fall back, do not crash

For lookups by primary key, a 404 may be a normal "absent" result.

```go
rows, err := db.Query("orders").Where("pk", map[string]any{"value": id}).Execute(ctx)
if err != nil {
	if errors.Is(err, mdb.ErrNotFound) {
		return nil, nil // table missing — treat as empty
	}
	return nil, err
}
```

Note: a `pk` query against an existing table returns zero rows, not a 404;
`ErrNotFound` here means the table itself is missing.

### Constraint conflict — report the offending op

```go
_, err := txn.Commit(ctx, "")
if errors.Is(err, mdb.ErrConflict) {
	var re *mdb.ResponseError
	errors.As(err, &re)
	if re.OpIndex != nil {
		return fmt.Errorf("op %d violated %s: %s", *re.OpIndex, re.Code, re.Message)
	}
	return fmt.Errorf("conflict %s: %s", re.Code, re.Message)
}
```

The engine already rolled back the whole batch — there is nothing to undo.

### Transient failure — retry with an idempotency key

`ErrQuery` covers transport and 5xx failures. With an idempotency key,
retrying a transaction is safe (see [transactions.md](transactions.md)).

```go
func run(ctx context.Context, txn *mdb.Transaction, key string) error {
	_, err := txn.Commit(ctx, key)
	if err == nil {
		return nil
	}
	if errors.Is(err, mdb.ErrAuth) || errors.Is(err, mdb.ErrConflict) {
		return err // not transient
	}
	// ErrQuery / network — caller may retry with the same key.
	return err
}
```

### Transaction-state error

`ErrTxnCommitted` is a programming bug. Fix the control flow rather than
catching it.

```go
if _, err := txn.Commit(ctx, ""); err != nil {
	if errors.Is(err, mdb.ErrTxnCommitted) {
		panic("logic error: committed the same transaction twice")
	}
}
```

## Quick reference

```go
import (
	"errors"
	mdb "github.com/visorcraft/mongreldb-go"
)

// Category check:
errors.Is(err, mdb.ErrNotFound)
errors.Is(err, mdb.ErrConflict)
errors.Is(err, mdb.ErrAuth)
errors.Is(err, mdb.ErrQuery)

// Detail extraction:
var re *mdb.ResponseError
if errors.As(err, &re) {
	_ = re.Status   // int
	_ = re.Code     // string, e.g. "UNIQUE_VIOLATION"
	_ = re.Message  // string
	_ = re.OpIndex  // *int
}
```

## Next steps

- [transactions.md](transactions.md) — constraint handling and retries in context
- [auth.md](auth.md) — credential management
