package mongreldb

import (
	"context"
	"errors"
)

// ErrTxnCommitted is returned when [Transaction.Commit] or
// [Transaction.Rollback] is called on a transaction that has already been
// committed.
var ErrTxnCommitted = errors.New("mongreldb: transaction already committed")

// Transaction stages operations locally and commits them atomically in a
// single /kit/txn request. The engine enforces unique, foreign-key, check, and
// trigger constraints at commit time; on any violation all operations roll
// back and Commit returns a [ResponseError] wrapping [ErrConflict].
//
// A Transaction is not safe to reuse after Commit; create a new one with
// [Client.Begin].
type Transaction struct {
	client    *Client
	ops       []map[string]any
	committed bool
}

// Begin starts a new batch transaction.
func (c *Client) Begin() *Transaction {
	return &Transaction{client: c}
}

// Put stages an insert. returning, when true, asks the daemon to echo the row
// in the per-operation result.
func (t *Transaction) Put(table string, cells Cells, returning bool) *Transaction {
	t.ops = append(t.ops, map[string]any{
		"put": map[string]any{
			"table":     table,
			"cells":     flattenCells(cells),
			"returning": returning,
		},
	})
	return t
}

// Upsert stages an insert-or-update. updateCells, when non-nil, supplies the
// values written on a primary-key conflict (nil means DO NOTHING).
func (t *Transaction) Upsert(table string, cells Cells, updateCells Cells, returning bool) *Transaction {
	op := map[string]any{
		"table":     table,
		"cells":     flattenCells(cells),
		"returning": returning,
	}
	if updateCells != nil {
		op["update_cells"] = flattenCells(updateCells)
	}
	t.ops = append(t.ops, map[string]any{"upsert": op})
	return t
}

// Delete stages a delete by the internal row id.
func (t *Transaction) Delete(table string, rowID int64) *Transaction {
	t.ops = append(t.ops, map[string]any{
		"delete": map[string]any{
			"table":  table,
			"row_id": rowID,
		},
	})
	return t
}

// DeleteByPK stages a delete by primary-key value.
func (t *Transaction) DeleteByPK(table string, pk any) *Transaction {
	t.ops = append(t.ops, map[string]any{
		"delete_by_pk": map[string]any{
			"table": table,
			"pk":    pk,
		},
	})
	return t
}

// Count returns the number of staged operations.
func (t *Transaction) Count() int { return len(t.ops) }

// Commit sends all staged operations atomically and returns the per-operation
// results. idempotencyKey, when non-empty, makes the commit safe to retry -
// the daemon returns the original response on duplicate commits.
//
// Returns [ErrTxnCommitted] if called twice on the same transaction.
func (t *Transaction) Commit(ctx context.Context, idempotencyKey string) ([]map[string]any, error) {
	if t.committed {
		return nil, ErrTxnCommitted
	}
	t.committed = true
	if len(t.ops) == 0 {
		return []map[string]any{}, nil
	}
	return t.client.CommitTxn(ctx, t.ops, idempotencyKey)
}

// Rollback discards all staged operations. Returns [ErrTxnCommitted] if the
// transaction was already committed.
func (t *Transaction) Rollback() error {
	if t.committed {
		return ErrTxnCommitted
	}
	t.ops = nil
	return nil
}
