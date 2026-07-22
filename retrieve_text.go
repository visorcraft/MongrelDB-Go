package mongreldb

import (
	"context"
	"fmt"
)

// TextRetrieveResult is POST /kit/retrieve_text (0.64+).
type TextRetrieveResult struct {
	Hits       []map[string]any `json:"hits"`
	Provenance map[string]any   `json:"provenance"`
}

// RetrieveTextOptions configures text → embed → ANN retrieve.
type RetrieveTextOptions struct {
	K          int
	DeadlineMs *uint64
	MaxWork    *uint64
}

// BuildRetrieveTextRequest returns the JSON body for POST /kit/retrieve_text.
func BuildRetrieveTextRequest(table string, embeddingColumn uint16, text string, opts RetrieveTextOptions) (map[string]any, error) {
	if table == "" {
		return nil, fmt.Errorf("mongreldb: table is required")
	}
	if text == "" {
		return nil, fmt.Errorf("mongreldb: text is required")
	}
	payload := map[string]any{
		"table":            table,
		"embedding_column": embeddingColumn,
		"text":             text,
	}
	if opts.K > 0 {
		payload["k"] = opts.K
	}
	if opts.DeadlineMs != nil {
		payload["deadline_ms"] = *opts.DeadlineMs
	}
	if opts.MaxWork != nil {
		payload["max_work"] = *opts.MaxWork
	}
	return payload, nil
}

// RetrieveText embeds text under the active semantic identity for embeddingColumn
// and runs ANN retrieval (POST /kit/retrieve_text).
func (c *Client) RetrieveText(ctx context.Context, table string, embeddingColumn uint16, text string, opts RetrieveTextOptions) (*TextRetrieveResult, error) {
	payload, err := BuildRetrieveTextRequest(table, embeddingColumn, text, opts)
	if err != nil {
		return nil, err
	}
	body, err := c.post(ctx, "/kit/retrieve_text", payload)
	if err != nil {
		return nil, err
	}
	var result TextRetrieveResult
	if err := decodeJSON(body, &result); err != nil {
		return nil, fmt.Errorf("mongreldb: decode retrieve_text response: %w", err)
	}
	if result.Hits == nil {
		result.Hits = []map[string]any{}
	}
	return &result, nil
}
