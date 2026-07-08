# Transactions

MongrelDB commits every write through a single atomic transaction endpoint
(`POST /kit/txn`). This guide covers the two ways to use it — a one-shot
single op, and a staged batch — plus idempotency keys for safe retries, typed
constraint-violation handling, and rollback.

The engine enforces `UNIQUE`, foreign-key, check, and trigger constraints at
**commit time**. A violation aborts the entire batch: no op in the batch
becomes visible.

---

## Single puts vs. batch transactions

### Single op: `Client.Put`

`Client.Put` is a convenience wrapper that sends a one-op transaction. Use it
when a write is independent and you do not need atomicity across multiple
rows.

```go
// One row, one atomic op. The empty string means "no idempotency key".
res, err := db.Put(ctx, "orders",
	mdb.Cells{1: int64(1), 2: "Alice", 3: 99.5},
	"")
if err != nil {
	log.Fatal(err)
}
_ = res
```

`Client.Upsert`, `Client.Delete`, and `Client.DeleteByPK` are the same shape:
single-op transactions.

### Batch: `Client.Begin` + `Transaction`

When several writes must succeed or fail together, stage them on a
`Transaction` and commit once. All ops go to the server in a single HTTP
request and commit atomically.

```go
txn := db.Begin()
txn.Put("orders", mdb.Cells{1: int64(10), 2: "Dave", 3: 50.0}, false)
txn.Put("orders", mdb.Cells{1: int64(11), 2: "Eve", 3: 75.0}, false)
txn.DeleteByPK("orders", int64(2))

results, err := txn.Commit(ctx, "")
if err != nil {
	log.Fatal(err)
}
fmt.Println("committed", len(results), "ops")
```

The third argument to `Transaction.Put` is `returning`. Set it to `true` to
have the daemon echo the written row back in the result map — useful for
reading server-assigned values.

```go
txn := db.Begin()
txn.Put("orders", mdb.Cells{1: int64(42), 2: "Hal", 3: 12.0}, true /* returning */)
res, _ := txn.Commit(ctx, "")
fmt.Println("server echoed:", res[0])
```

`Transaction.Upsert(table, cells, updateCells, returning)` takes an
`updateCells` map applied on a primary-key conflict. A `nil` `updateCells`
means "do nothing on conflict".

## Idempotency keys for safe retries

Networks drop requests and daemons crash after committing but before
replying. An idempotency key makes a commit safe to retry: the daemon
remembers the key and replays the **original** result on a duplicate commit,
even across restarts.

Pass the key as the second argument to `Commit` (or the fourth to
`Client.Put`/`Client.Upsert`):

```go
// A web handler that must not double-charge, even if the client retries or
// the connection drops after the daemon committed.
func charge(ctx context.Context, db *mdb.Client, orderID string) error {
	txn := db.Begin()
	txn.Put("charges", mdb.Cells{1: orderID, 2: 199.0}, false)

	// Use a stable, business-meaningful key derived from the request.
	// On a retry with the same key the daemon returns the first commit's
	// result instead of inserting a second row.
	_, err := txn.Commit(ctx, "charge:"+orderID)
	return err
}
```

Rules for keys:

- Any non-empty string works. Prefer content-derived, globally-unique values
  (e.g. `"charge:" + orderID`).
- The empty string disables idempotency — a retry will commit again.
- The key scopes the **entire batch**, not individual ops. Reuse the exact
  same ops and key together when retrying.

A safe retry loop:

```go
func commitWithRetry(ctx context.Context, txn *mdb.Transaction, key string) error {
	for attempt := 0; attempt < 3; attempt++ {
		_, err := txn.Commit(ctx, key)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, mdb.ErrConflict):
			return err // a real constraint violation — do not retry
		case errors.Is(err, mdb.ErrAuth):
			return err // caller must fix credentials — do not retry
		}
		// Network/server error (ErrQuery). The idempotency key makes it
		// safe to retry.
		if attempt == 2 {
			return err
		}
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
	return nil
}
```

Note that `Commit` flips the transaction to "committed" internally. The
helper above works because the retry only matters when `Commit` returned an
error, in which case the transaction state is indeterminate to the caller —
build a fresh `Transaction` with the same ops and the same key, or restructure
to rebuild inside the loop. The safest pattern is to construct the
transaction inside the retry loop.

## Handling constraint violations

Constraint violations arrive as HTTP 409, mapped to an error wrapping
`mdb.ErrConflict`. Use `errors.As` to pull out the `*mdb.ResponseError` and
read the structured `Code` and `OpIndex`:

```go
txn := db.Begin()
txn.Put("orders", mdb.Cells{1: int64(1)}, false) // duplicate PK
_, err := txn.Commit(ctx, "")

var re *mdb.ResponseError
if errors.As(err, &re) {
	switch re.Code {
	case "UNIQUE_VIOLATION":
		log.Printf("duplicate at op %d: %s", ptrToInt(re.OpIndex), re.Message)
	case "FK_VIOLATION":
		log.Printf("missing parent at op %d: %s", ptrToInt(re.OpIndex), re.Message)
	case "CHECK_VIOLATION":
		log.Printf("check failed at op %d: %s", ptrToInt(re.OpIndex), re.Message)
	default:
		log.Printf("other conflict: %s", re.Message)
	}
}

func ptrToInt(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
```

The error envelope from the daemon looks like:

```json
{"status": "aborted", "error": {"code": "UNIQUE_VIOLATION", "message": "...", "op_index": 0}}
```

`OpIndex` points at the offending op within the batch so you can report which
row caused the failure.

For simple category checks, `errors.Is` is enough:

```go
switch {
case errors.Is(err, mdb.ErrConflict):
	// any constraint violation
case errors.Is(err, mdb.ErrNotFound):
	// table or row missing
case errors.Is(err, mdb.ErrAuth):
	// bad credentials
}
```

## Rollback after failure

There are two notions of "rollback":

1. **Server-side.** When `Commit` fails with `ErrConflict`, the engine has
   already discarded the entire batch. Nothing was written; there is no
   server rollback to perform.
2. **Client-side.** `Transaction.Rollback()` clears the locally staged ops.
   Call it to release the `Transaction` when you decide not to commit (for
   example, after a validation error in your own code, before ever sending).

```go
txn := db.Begin()
txn.Put("orders", mdb.Cells{1: int64(1), 2: "Iris", 3: 5.0}, false)

if !businessRuleOk() {
	// Throw the staged ops away locally. Nothing has been sent to the daemon.
	if err := txn.Rollback(); err != nil {
		log.Fatal(err)
	}
	return
}

if _, err := txn.Commit(ctx, ""); err != nil {
	// On conflict the server already rolled back. Rollback() here is a no-op
	// for the data but clears local state. It returns ErrTxnCommitted because
	// the transaction entered the committed state when Commit ran.
	_ = txn.Rollback()
}
```

`Rollback` and `Commit` both return `mdb.ErrTxnCommitted` if the transaction
was already committed. Treat that as a programming error to fix upstream, not
a runtime condition to silence.

### Recovering from a failed batch

Because a failed commit rejects the whole batch, the usual recovery is to
re-issue the ops that are still valid, optionally splitting out the offender:

```go
_, err := txn.Commit(ctx, "")
if errors.Is(err, mdb.ErrConflict) {
	var re *mdb.ResponseError
	errors.As(err, &re)
	if re.OpIndex != nil {
		// Drop the offending op and retry the rest, if your domain allows.
		bad := *re.OpIndex
		retry := append(txnOps[:bad:bad], txnOps[bad+1:]...)
		_ = retry
	}
}
```

A `Transaction` does not expose its staged ops, so keep your own slice if you
need this kind of surgical retry.

## Summary

| Goal | Use |
|------|-----|
| One independent write | `Client.Put` / `Upsert` / `Delete` / `DeleteByPK` |
| Several writes that must commit together | `Client.Begin` + `Transaction.Commit` |
| Retry safely after a network blip | `Commit(ctx, idempotencyKey)` with a stable key |
| Distinguish constraint classes | `errors.As` into `*ResponseError`, read `.Code` |
| Abort before sending | `Transaction.Rollback()` |

See [errors.md](errors.md) for the full error hierarchy and [queries.md](queries.md)
for read patterns.
