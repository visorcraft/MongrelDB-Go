package mongreldb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// CommitHlc is the structural hybrid logical clock from durable recovery (0.64+).
type CommitHlc struct {
	PhysicalMicros uint64 `json:"physical_micros"`
	Logical        uint32 `json:"logical"`
	NodeTiebreaker uint32 `json:"node_tiebreaker"`
}

// DurableOutcome is the nested durable recovery payload on query status/cancel
// responses (parity with the server DurableOutcome / outcome JSON object).
type DurableOutcome struct {
	Committed                 *bool      `json:"committed"`
	CommittedStatements       *int       `json:"committed_statements"`
	LastCommitEpoch           *uint64    `json:"last_commit_epoch"`
	LastCommitEpochText       *string    `json:"last_commit_epoch_text"`
	LastCommitHlc             *CommitHlc `json:"last_commit_hlc"`
	FirstCommitStatementIndex *int       `json:"first_commit_statement_index"`
	LastCommitStatementIndex  *int       `json:"last_commit_statement_index"`
	CompletedStatements       *int       `json:"completed_statements"`
	StatementIndex            *int       `json:"statement_index"`
	Serialization             string     `json:"serialization"`
	SerializationState        string     `json:"serialization_state"`
	TerminalState             *string    `json:"terminal_state"`
}

// QueryStatus is GET /queries/{query_id} (SQL control / durable recovery).
type QueryStatus struct {
	QueryID             string          `json:"query_id"`
	Status              string          `json:"status"`
	State               string          `json:"state"`
	ServerState         string          `json:"server_state"`
	TerminalState       *string         `json:"terminal_state"`
	Operation           string          `json:"operation"`
	Committed           *bool           `json:"committed"`
	CommittedStatements *int            `json:"committed_statements"`
	LastCommitEpoch     *uint64         `json:"last_commit_epoch"`
	LastCommitEpochText *string         `json:"last_commit_epoch_text"`
	LastCommitHlc       *CommitHlc      `json:"last_commit_hlc"`
	FirstCommitStmtIdx  *int            `json:"first_commit_statement_index"`
	LastCommitStmtIdx   *int            `json:"last_commit_statement_index"`
	CompletedStatements *int            `json:"completed_statements"`
	StatementIndex      *int            `json:"statement_index"`
	CancelOutcome       *string         `json:"cancel_outcome"`
	CancellationReason  string          `json:"cancellation_reason"`
	Retryable           bool            `json:"retryable"`
	Outcome             DurableOutcome  `json:"outcome"`
	Durable             *DurableOutcome `json:"durable"`
	TerminalError       json.RawMessage `json:"terminal_error"`
	Trace               json.RawMessage `json:"trace"`
}

// CommitHlc returns the authoritative HLC from nested durable / outcome / top-level.
func (s *QueryStatus) CommitHlc() *CommitHlc {
	if s == nil {
		return nil
	}
	if s.Durable != nil && s.Durable.LastCommitHlc != nil {
		return s.Durable.LastCommitHlc
	}
	if s.Outcome.LastCommitHlc != nil {
		return s.Outcome.LastCommitHlc
	}
	return s.LastCommitHlc
}

// SerializationState prefers nested durable/outcome serialization_state, then serialization.
func (s *QueryStatus) SerializationState() string {
	if s == nil {
		return ""
	}
	if s.Durable != nil {
		if s.Durable.SerializationState != "" {
			return s.Durable.SerializationState
		}
		if s.Durable.Serialization != "" {
			return s.Durable.Serialization
		}
	}
	if s.Outcome.SerializationState != "" {
		return s.Outcome.SerializationState
	}
	return s.Outcome.Serialization
}

// QueryStatus fetches retained SQL execution status for durable recovery.
func (c *Client) QueryStatus(ctx context.Context, queryID string) (*QueryStatus, error) {
	if queryID == "" {
		return nil, fmt.Errorf("mongreldb: query_id is required")
	}
	path := "/queries/" + url.PathEscape(queryID)
	body, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var status QueryStatus
	if err := decodeJSON(body, &status); err != nil {
		return nil, fmt.Errorf("mongreldb: decode query status: %w", err)
	}
	return &status, nil
}

// CancelQuery requests cancellation of a running SQL query.
func (c *Client) CancelQuery(ctx context.Context, queryID string) (map[string]any, error) {
	if queryID == "" {
		return nil, fmt.Errorf("mongreldb: query_id is required")
	}
	path := "/queries/" + url.PathEscape(queryID) + "/cancel"
	body, err := c.post(ctx, path, map[string]any{})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	if err := decodeJSON(body, &out); err != nil {
		return nil, fmt.Errorf("mongreldb: decode cancel response: %w", err)
	}
	return out, nil
}

// ParseQueryStatusJSON decodes a query-status body (test/helpers).
func ParseQueryStatusJSON(data []byte) (*QueryStatus, error) {
	var status QueryStatus
	if err := decodeJSON(data, &status); err != nil {
		return nil, err
	}
	return &status, nil
}
