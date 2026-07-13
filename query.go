package mongreldb

import (
	"context"
	"fmt"
)

// QueryBuilder builds a request for the daemon's /kit/query endpoint, where
// conditions push down to the engine's specialized indexes for sub-millisecond
// lookups.
//
// Condition parameters accept friendly aliases that are translated to the
// server's exact on-wire keys before sending (see [QueryBuilder.Where]):
//
//   - column         -> column_id
//   - min / max      -> lo / hi
//   - min_inclusive  -> lo_inclusive
//   - max_inclusive  -> hi_inclusive
//
// The server's canonical keys are accepted directly too.
type QueryBuilder struct {
	client     *Client
	table      string
	conditions []map[string]any // each is {type: {normalized params}}
	projection []int64
	limit      *int64
	offset     *int64
	lastTrunc  bool
}

// Where adds a native condition. Available condition types include:
//
//   - pk              exact primary-key match ({"value": pk})
//   - bitmap_eq       equality on a bitmap-indexed column
//   - bitmap_in       IN predicate on a bitmap-indexed column
//   - range           integer range predicate (lo/hi, inclusive)
//   - range_f64       float range predicate (lo/hi + lo_inclusive/hi_inclusive)
//   - is_null         null check
//   - is_not_null     non-null check
//   - fm_contains     full-text substring search (FM-index)
//   - fm_contains_all multiple substring patterns (all must match)
//   - ann             dense vector similarity search (HNSW)
//   - sparse_match    sparse vector match
//   - min_hash_similar MinHash similarity search
//
// Conditions are AND-ed. The builder returns itself for chaining.
func (q *QueryBuilder) Where(condType string, params map[string]any) *QueryBuilder {
	q.conditions = append(q.conditions, map[string]any{
		condType: normalizeCondition(condType, params),
	})
	return q
}

// Projection sets the column ids to return (nil means all columns).
func (q *QueryBuilder) Projection(columnIDs []int64) *QueryBuilder {
	q.projection = columnIDs
	return q
}

// Limit caps the number of rows returned.
func (q *QueryBuilder) Limit(limit int64) *QueryBuilder {
	l := limit
	q.limit = &l
	return q
}

// Offset skips matching rows before applying the limit.
func (q *QueryBuilder) Offset(offset int64) *QueryBuilder {
	o := offset
	q.offset = &o
	return q
}

// Build returns the request payload that will be sent to /kit/query.
func (q *QueryBuilder) Build() map[string]any {
	payload := map[string]any{"table": q.table}
	if len(q.conditions) > 0 {
		// The daemon expects externally-tagged conditions: [{type: {...}}, ...]
		payload["conditions"] = q.conditions
	}
	if q.projection != nil {
		payload["projection"] = q.projection
	}
	if q.limit != nil {
		payload["limit"] = *q.limit
	}
	if q.offset != nil {
		payload["offset"] = *q.offset
	}
	return payload
}

// Execute runs the query and returns the matching rows. It also records
// whether the result was truncated by the [QueryBuilder.Limit]; check it with
// [QueryBuilder.Truncated].
func (q *QueryBuilder) Execute(ctx context.Context) ([]map[string]any, error) {
	body, err := q.client.post(ctx, "/kit/query", q.Build())
	if err != nil {
		return nil, err
	}

	var resp struct {
		Rows      []map[string]any `json:"rows"`
		Truncated bool             `json:"truncated"`
	}
	if len(body) > 0 {
		if err := decodeJSON(body, &resp); err != nil {
			return nil, fmt.Errorf("mongreldb: decode query response: %w", err)
		}
	}
	q.lastTrunc = resp.Truncated
	if resp.Rows == nil {
		resp.Rows = []map[string]any{}
	}
	return resp.Rows, nil
}

// Truncated reports whether the most recent [QueryBuilder.Execute] result was
// capped by the query limit. It returns false until Execute has been called.
func (q *QueryBuilder) Truncated() bool { return q.lastTrunc }

// normalizeCondition translates friendly parameter aliases to the server's
// canonical on-wire keys. Both spellings are accepted, so callers may use
// whichever is clearer.
//
// Generic aliases (applied to all condition types):
//
//	column         -> column_id
//	min            -> lo
//	max            -> hi
//	min_inclusive  -> lo_inclusive
//	max_inclusive  -> hi_inclusive
//
// Type-specific aliases:
//
//	fm_contains / fm_contains_all: value -> pattern
//	(other types like pk/bitmap_eq use "value" as their canonical key, so the
//	value->pattern alias must NOT apply globally)
func normalizeCondition(condType string, params map[string]any) map[string]any {
	aliases := map[string]string{
		"column":        "column_id",
		"min":           "lo",
		"max":           "hi",
		"min_inclusive": "lo_inclusive",
		"max_inclusive": "hi_inclusive",
	}
	// The docs historically used "value" for the FTS pattern; the server's
	// fm_contains key is "pattern". Only apply this for FTS conditions, since
	// pk/bitmap_eq use "value" canonically.
	if condType == "fm_contains" || condType == "fm_contains_all" {
		aliases["value"] = "pattern"
	}

	normalized := make(map[string]any, len(params))
	for key, val := range params {
		if canonical, ok := aliases[key]; ok {
			normalized[canonical] = val
		} else {
			normalized[key] = val
		}
	}
	return normalized
}
