package mongreldb

import (
	"context"
	"fmt"
)

// SearchBuilder builds a request for the daemon's POST /kit/search endpoint:
// multi-retriever hybrid search with reciprocal-rank fusion and optional
// exact-vector rerank. This is the scored counterpart to [QueryBuilder]
// (which only AND-s conditions via /kit/query).
//
// Wire format matches the server's KitSearchRequest (flattened retrievers,
// snake_case fusion/rerank tags) so HTTP language clients stay interchangeable.
type SearchBuilder struct {
	client     *Client
	table      string
	must       []map[string]any
	retrievers []map[string]any
	fusion     map[string]any
	rerank     map[string]any
	limit      int
	projection []int64
	explain    bool
	cursor     string
}

// Search starts a hybrid search against table.
func (c *Client) Search(table string) *SearchBuilder {
	return &SearchBuilder{
		client: c,
		table:  table,
		fusion: map[string]any{
			"reciprocal_rank": map[string]any{"constant": uint32(60)},
		},
		limit: 10,
	}
}

// Must adds a hard filter condition (same shapes as [QueryBuilder.Where]).
// Conditions are AND-ed against candidate rows.
func (s *SearchBuilder) Must(condType string, params map[string]any) *SearchBuilder {
	s.must = append(s.must, map[string]any{
		condType: normalizeCondition(condType, params),
	})
	return s
}

// AnnRetriever adds a dense ANN retriever (HNSW) over columnID.
// Wire: {"name","weight","ann":{"column_id","query","k"}} (flattened).
func (s *SearchBuilder) AnnRetriever(name string, columnID int64, query []float64, k int, weight float64) *SearchBuilder {
	s.retrievers = append(s.retrievers, map[string]any{
		"name":   name,
		"weight": weight,
		"ann": map[string]any{
			"column_id": columnID,
			"query":     query,
			"k":         k,
		},
	})
	return s
}

// SparseRetriever adds a sparse (SPLADE-style) retriever.
// terms are [token_id, weight] pairs.
func (s *SearchBuilder) SparseRetriever(name string, columnID int64, terms [][2]float64, k int, weight float64) *SearchBuilder {
	pairs := make([][]any, 0, len(terms))
	for _, t := range terms {
		pairs = append(pairs, []any{uint32(t[0]), t[1]})
	}
	s.retrievers = append(s.retrievers, map[string]any{
		"name":   name,
		"weight": weight,
		"sparse": map[string]any{
			"column_id": columnID,
			"query":     pairs,
			"k":         k,
		},
	})
	return s
}

// MinHashRetriever adds a MinHash set-similarity retriever.
func (s *SearchBuilder) MinHashRetriever(name string, columnID int64, members []string, k int, weight float64) *SearchBuilder {
	s.retrievers = append(s.retrievers, map[string]any{
		"name":   name,
		"weight": weight,
		"min_hash": map[string]any{
			"column_id": columnID,
			"members":   members,
			"k":         k,
		},
	})
	return s
}

// Fusion sets reciprocal-rank fusion (the only supported kind today).
func (s *SearchBuilder) Fusion(constant uint32) *SearchBuilder {
	if constant == 0 {
		constant = 60
	}
	s.fusion = map[string]any{
		"reciprocal_rank": map[string]any{"constant": constant},
	}
	return s
}

// ExactRerank enables exact float-vector rerank after fusion.
// metric is "cosine", "dot_product", or "euclidean".
func (s *SearchBuilder) ExactRerank(embeddingColumn int64, query []float64, metric string, candidateLimit int, weight float64) *SearchBuilder {
	s.rerank = map[string]any{
		"exact_vector": map[string]any{
			"embedding_column": embeddingColumn,
			"query":            query,
			"metric":           metric,
			"candidate_limit":  candidateLimit,
			"weight":           weight,
		},
	}
	return s
}

// Limit caps the number of final hits (default 10).
func (s *SearchBuilder) Limit(limit int) *SearchBuilder {
	s.limit = limit
	return s
}

// Projection restricts returned cell column ids.
func (s *SearchBuilder) Projection(columnIDs []int64) *SearchBuilder {
	s.projection = columnIDs
	return s
}

// Explain requests server-side trace metadata when supported.
func (s *SearchBuilder) Explain(on bool) *SearchBuilder {
	s.explain = on
	return s
}

// Cursor continues a previous page from next_cursor.
func (s *SearchBuilder) Cursor(cursor string) *SearchBuilder {
	s.cursor = cursor
	return s
}

// Build returns the JSON body for POST /kit/search.
func (s *SearchBuilder) Build() (map[string]any, error) {
	if len(s.retrievers) == 0 {
		return nil, fmt.Errorf("search requires at least one retriever")
	}
	if s.limit <= 0 {
		return nil, fmt.Errorf("search limit must be positive")
	}
	payload := map[string]any{
		"table":      s.table,
		"retrievers": s.retrievers,
		"fusion":     s.fusion,
		"limit":      s.limit,
	}
	if len(s.must) > 0 {
		payload["must"] = s.must
	}
	if s.rerank != nil {
		payload["rerank"] = s.rerank
	}
	if s.projection != nil {
		payload["projection"] = s.projection
	}
	if s.explain {
		payload["explain"] = true
	}
	if s.cursor != "" {
		payload["cursor"] = s.cursor
	}
	return payload, nil
}

// SearchResult is the decoded /kit/search response.
type SearchResult struct {
	Hits       []map[string]any
	Trace      any
	NextCursor string
}

// Execute runs the hybrid search.
func (s *SearchBuilder) Execute(ctx context.Context) (*SearchResult, error) {
	payload, err := s.Build()
	if err != nil {
		return nil, err
	}
	body, err := s.client.post(ctx, "/kit/search", payload)
	if err != nil {
		return nil, err
	}
	hits, _ := body["hits"].([]any)
	out := &SearchResult{Hits: make([]map[string]any, 0, len(hits))}
	for _, h := range hits {
		if m, ok := h.(map[string]any); ok {
			out.Hits = append(out.Hits, m)
		}
	}
	if t, ok := body["trace"]; ok {
		out.Trace = t
	}
	if c, ok := body["next_cursor"].(string); ok {
		out.NextCursor = c
	}
	return out, nil
}
