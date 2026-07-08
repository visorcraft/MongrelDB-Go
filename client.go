// Package mongreldb is the pure-Go HTTP client for [MongrelDB].
//
// It talks to a running mongreldb-server daemon's JSON API over the standard
// library net/http client - no cgo, no external dependencies. The surface
// mirrors the MongrelDB PHP client: typed CRUD, a fluent query builder that
// pushes conditions down to the engine's native indexes, idempotent batch
// transactions, full SQL access, and schema introspection.
//
// Connect with [NewClient] and a base URL:
//
//	db := mongreldb.NewClient("http://127.0.0.1:8453")
//	ok, _ := db.Health(context.Background())
//
// [MongrelDB]: https://www.MongrelDB.com
package mongreldb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the daemon address used when none is supplied.
const DefaultBaseURL = "http://127.0.0.1:8453"

// Cells is a column-id-to-value map. The client flattens it to the server's
// on-wire [col_id, value, col_id, value, ...] array before sending. Pair order
// is irrelevant - each value is preceded by its own column id.
type Cells map[int64]any

// Column describes one column in a CREATE TABLE request. It is sent to the
// server verbatim; the recognized keys are id, name, ty, primary_key, and
// nullable, matching the daemon's table-create extractor.
type Column = map[string]any

// Client is the MongrelDB HTTP client. Create one with [NewClient] and use its
// methods for health, table management, CRUD, query, SQL, and schema. A Client
// is safe for concurrent use by multiple goroutines once configured.
type Client struct {
	baseURL  string
	token    string
	username string
	password string
	http     *http.Client
}

// Option configures a [Client].
type Option func(*Client)

// WithToken authenticates requests with a Bearer token (--auth-token mode).
// When set, it takes precedence over basic-auth credentials.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithBasicAuth authenticates requests with HTTP Basic credentials
// (--auth-users mode). Ignored if [WithToken] is also supplied.
func WithBasicAuth(username, password string) Option {
	return func(c *Client) {
		c.username = username
		c.password = password
	}
}

// WithHTTPClient installs a custom *http.Client (e.g. with a custom transport).
// Overrides [WithTimeout].
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithTimeout sets the per-request timeout by configuring the underlying
// *http.Client. Overrides [WithHTTPClient] if called after it.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if c.http == nil {
			c.http = &http.Client{}
		}
		c.http.Timeout = d
	}
}

// NewClient returns a [Client] for the daemon at url. If url is empty,
// [DefaultBaseURL] is used. Apply [Option] values for auth, timeouts, or a
// custom *http.Client.
func NewClient(url string, opts ...Option) *Client {
	if url == "" {
		url = DefaultBaseURL
	}
	c := &Client{
		baseURL: strings.TrimRight(url, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// BaseURL returns the daemon base URL the client was configured with.
func (c *Client) BaseURL() string { return c.baseURL }

// ── Health & tables ────────────────────────────────────────────────────────

// Health reports whether the daemon is reachable and healthy.
func (c *Client) Health(ctx context.Context) (bool, error) {
	_, err := c.get(ctx, "/health")
	return err == nil, nil
}

// TableNames lists all table names in the database.
func (c *Client) TableNames(ctx context.Context) ([]string, error) {
	body, err := c.get(ctx, "/tables")
	if err != nil {
		return nil, err
	}
	var names []string
	if len(body) == 0 {
		return names, nil
	}
	// The endpoint returns a bare JSON array of strings.
	if err := json.Unmarshal(body, &names); err != nil {
		return nil, fmt.Errorf("mongreldb: decode table list: %w", err)
	}
	return names, nil
}

// CreateTable creates a table named name with the given columns and returns
// the assigned table id.
func (c *Client) CreateTable(ctx context.Context, name string, columns []Column) (int64, error) {
	body, err := c.post(ctx, "/kit/create_table", map[string]any{
		"name":    name,
		"columns": columns,
	})
	if err != nil {
		return 0, err
	}
	var resp struct {
		TableID int64 `json:"table_id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &resp); err != nil {
			return 0, fmt.Errorf("mongreldb: decode create_table response: %w", err)
		}
	}
	return resp.TableID, nil
}

// DropTable drops a table by name.
func (c *Client) DropTable(ctx context.Context, name string) error {
	_, err := c.delete(ctx, "/tables/"+urlPathEscape(name))
	return err
}

// Count returns the row count for a table.
func (c *Client) Count(ctx context.Context, table string) (int64, error) {
	body, err := c.get(ctx, "/tables/"+urlPathEscape(table)+"/count")
	if err != nil {
		return 0, err
	}
	var resp struct {
		Count int64 `json:"count"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &resp); err != nil {
			return 0, fmt.Errorf("mongreldb: decode count response: %w", err)
		}
	}
	return resp.Count, nil
}

// ── CRUD (via the Kit typed transaction endpoint) ──────────────────────────

// Put inserts a row. idempotencyKey, if non-empty, makes the commit safe to
// retry - the daemon returns the original result on duplicate commits.
//
// Returns the per-operation result object (the first element of the server's
// results array).
func (c *Client) Put(ctx context.Context, table string, cells Cells, idempotencyKey string) (map[string]any, error) {
	results, err := c.commitOne(ctx, []map[string]any{{
		"put": map[string]any{
			"table": table,
			"cells": flattenCells(cells),
		},
	}}, idempotencyKey)
	if err != nil {
		return nil, err
	}
	return firstResult(results), nil
}

// Upsert inserts a row, or updates it on a primary-key conflict. updateCells,
// when non-nil, supplies the values written on conflict (nil means DO NOTHING).
func (c *Client) Upsert(ctx context.Context, table string, cells Cells, updateCells Cells, idempotencyKey string) (map[string]any, error) {
	op := map[string]any{
		"table": table,
		"cells": flattenCells(cells),
	}
	if updateCells != nil {
		op["update_cells"] = flattenCells(updateCells)
	}
	results, err := c.commitOne(ctx, []map[string]any{{"upsert": op}}, idempotencyKey)
	if err != nil {
		return nil, err
	}
	return firstResult(results), nil
}

// Delete removes a row by its internal row id.
func (c *Client) Delete(ctx context.Context, table string, rowID int64) error {
	_, err := c.commitOne(ctx, []map[string]any{{
		"delete": map[string]any{
			"table":  table,
			"row_id": rowID,
		},
	}}, "")
	return err
}

// DeleteByPK removes a row by its primary-key value.
func (c *Client) DeleteByPK(ctx context.Context, table string, pk any) error {
	_, err := c.commitOne(ctx, []map[string]any{{
		"delete_by_pk": map[string]any{
			"table": table,
			"pk":    pk,
		},
	}}, "")
	return err
}

// commitOne sends a single-op transaction and returns the results array.
func (c *Client) commitOne(ctx context.Context, ops []map[string]any, idempotencyKey string) ([]map[string]any, error) {
	payload := map[string]any{"ops": ops}
	if idempotencyKey != "" {
		payload["idempotency_key"] = idempotencyKey
	}
	body, err := c.post(ctx, "/kit/txn", payload)
	if err != nil {
		return nil, err
	}
	return decodeResults(body)
}

// ── Query ──────────────────────────────────────────────────────────────────

// Query starts a fluent [QueryBuilder] against table.
func (c *Client) Query(table string) *QueryBuilder {
	return &QueryBuilder{client: c, table: table}
}

// ── SQL ────────────────────────────────────────────────────────────────────

// SQL executes a SQL statement via the /sql endpoint. When the daemon returns
// a JSON result set, the rows are decoded and returned; for statements that
// yield no rows (DDL/DML) or a non-JSON (Arrow IPC) body, it returns an empty
// slice and a nil error.
func (c *Client) SQL(ctx context.Context, sql string) ([]map[string]any, error) {
	body, err := c.post(ctx, "/sql", map[string]any{"sql": sql})
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []map[string]any{}, nil
	}
	// The /sql endpoint generally streams Arrow IPC bytes for SELECTs; only
	// decode when the body is actually JSON to avoid noise.
	if trimmed[0] == '{' || trimmed[0] == '[' {
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err == nil {
			return rows, nil
		}
		// A single JSON object (e.g. an error envelope) is not a row set.
		var obj map[string]any
		if err := json.Unmarshal(body, &obj); err == nil {
			return []map[string]any{}, nil
		}
	}
	return []map[string]any{}, nil
}

// ── Schema ─────────────────────────────────────────────────────────────────

// Schema returns the full schema catalog: a table-name-to-descriptor map.
func (c *Client) Schema(ctx context.Context) (map[string]map[string]any, error) {
	body, err := c.get(ctx, "/kit/schema")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tables map[string]map[string]any `json:"tables"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("mongreldb: decode schema response: %w", err)
		}
	}
	if resp.Tables == nil {
		resp.Tables = map[string]map[string]any{}
	}
	return resp.Tables, nil
}

// SchemaFor returns the descriptor for a single table.
func (c *Client) SchemaFor(ctx context.Context, table string) (map[string]any, error) {
	body, err := c.get(ctx, "/kit/schema/"+urlPathEscape(table))
	if err != nil {
		return nil, err
	}
	var desc map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &desc); err != nil {
			return nil, fmt.Errorf("mongreldb: decode schema-for response: %w", err)
		}
	}
	if desc == nil {
		desc = map[string]any{}
	}
	return desc, nil
}

// ── Maintenance ────────────────────────────────────────────────────────────

// Compact merges sorted runs across all tables.
func (c *Client) Compact(ctx context.Context) (map[string]any, error) {
	return c.postDecode(ctx, "/compact")
}

// CompactTable merges sorted runs for a single table.
func (c *Client) CompactTable(ctx context.Context, table string) (map[string]any, error) {
	return c.postDecode(ctx, "/tables/"+urlPathEscape(table)+"/compact")
}

// postDecode POSTs an empty body and decodes the JSON object response.
func (c *Client) postDecode(ctx context.Context, path string) (map[string]any, error) {
	body, err := c.post(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("mongreldb: decode response: %w", err)
		}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// ── HTTP plumbing ──────────────────────────────────────────────────────────

// get performs a GET and returns the response body, mapping HTTP errors to
// typed client errors.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

// post performs a POST with a JSON body (Content-Type: application/json) and
// returns the response body. A nil body sends an empty request.
func (c *Client) post(ctx context.Context, path string, body any) ([]byte, error) {
	return c.do(ctx, http.MethodPost, path, body)
}

// delete performs a DELETE.
func (c *Client) delete(ctx context.Context, path string) ([]byte, error) {
	return c.do(ctx, http.MethodDelete, path, nil)
}

// do builds and runs one request. The server's JSON extractors require an
// explicit Content-Type header on any request carrying a JSON body, so one is
// added whenever the body is non-nil. Non-2xx responses are mapped to typed
// client errors via newResponseError.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("mongreldb: encode request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/"+strings.TrimLeft(path, "/"), reader)
	if err != nil {
		return nil, fmt.Errorf("mongreldb: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.applyAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mongreldb: request %s %s failed: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("mongreldb: read response: %w", readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, newResponseError(resp.StatusCode, data)
	}
	return data, nil
}

// applyAuth sets the Authorization header according to the configured
// credentials. A bearer token takes precedence over basic auth.
func (c *Client) applyAuth(req *http.Request) {
	switch {
	case c.token != "":
		req.Header.Set("Authorization", "Bearer "+c.token)
	case c.username != "":
		creds := c.username + ":" + c.password
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))
	}
}

// CommitTxn sends a batch of staged operations atomically. Exposed for the
// [Transaction] type; returns the per-operation results array.
func (c *Client) CommitTxn(ctx context.Context, ops []map[string]any, idempotencyKey string) ([]map[string]any, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	payload := map[string]any{"ops": ops}
	if idempotencyKey != "" {
		payload["idempotency_key"] = idempotencyKey
	}
	body, err := c.post(ctx, "/kit/txn", payload)
	if err != nil {
		return nil, err
	}
	return decodeResults(body)
}

// ── Helpers ────────────────────────────────────────────────────────────────

// flattenCells converts a Cells map to the server's flat
// [col_id, value, col_id, value, ...] array. Pair order is not significant.
func flattenCells(cells Cells) []any {
	flat := make([]any, 0, len(cells)*2)
	for id, val := range cells {
		flat = append(flat, id, val)
	}
	return flat
}

// decodeResults pulls the results array out of a /kit/txn response.
func decodeResults(body []byte) ([]map[string]any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return []map[string]any{}, nil
	}
	var resp struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("mongreldb: decode txn response: %w", err)
	}
	if resp.Results == nil {
		resp.Results = []map[string]any{}
	}
	return resp.Results, nil
}

// firstResult returns the first element of results, or an empty map if none.
func firstResult(results []map[string]any) map[string]any {
	if len(results) == 0 {
		return map[string]any{}
	}
	return results[0]
}

// urlPathEscape percent-escapes a path segment (used for table names that may
// contain characters unsafe in a URL). It does not escape the forward slash.
func urlPathEscape(seg string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, r := range seg {
		c := byte(0)
		if r < 128 {
			c = byte(r)
		}
		// Leave unreserved characters and the path separator intact.
		switch {
		case r == '/':
			b.WriteByte('/')
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			// Encode as UTF-8 bytes.
			bs := []byte(string(r))
			for _, bb := range bs {
				b.WriteByte('%')
				b.WriteByte(hex[bb>>4])
				b.WriteByte(hex[bb&0x0f])
			}
		}
	}
	return b.String()
}
