package mongreldb_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mdb "github.com/visorcraft/mongreldb-go"
)

// stringPtr is a tiny helper for tests that need to populate a *string field
// without writing `&"value"` inline.
func stringPtr(s string) *string { return &s }

// recordingServer is a minimal in-process HTTP server used by the
// config/transport tests (those that don't need a real daemon). It captures the
// Authorization header, path, Content-Type, and raw request body for each call.
type recordingServer struct {
	*httptest.Server
	lastAuth  string
	lastPath  string
	lastCType string
	lastBody  []byte
}

func newRecordingServer(t *testing.T) *recordingServer {
	t.Helper()
	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			rs.lastBody, _ = io.ReadAll(r.Body)
		}
		rs.lastAuth = r.Header.Get("Authorization")
		rs.lastPath = r.URL.Path
		rs.lastCType = r.Header.Get("Content-Type")
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/tables":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case "/sql":
			// The daemon streams Arrow IPC for SELECTs; reply with empty bytes so
			// the client's SQL() returns an empty slice. Content-Type is still set
			// on the request side (what the test asserts).
			w.WriteHeader(http.StatusOK)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not found","code":"NOT_FOUND"}}`))
		}
	}))
	t.Cleanup(rs.Close)
	return rs
}

// base64Std is a tiny wrapper around stdlib base64 used in the basic-auth test.
func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// TestPostSendsJSONContentType confirms the client sets Content-Type:
// application/json on POST bodies (the daemon's extractors require it).
func TestPostSendsJSONContentType(t *testing.T) {
	rs := newRecordingServer(t)
	c := mdb.NewClient(rs.URL)
	if _, err := c.SQL(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("SQL: %v", err)
	}
	if got := rs.lastCType; got != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", got)
	}
	if got := rs.lastPath; got != "/sql" {
		t.Fatalf("expected path /sql, got %q", got)
	}
}

// TestResponseErrorMapping exercises the 404/409/400 status-to-sentinel
// mapping against an in-process server (no daemon needed).
func TestResponseErrorMapping(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantErr  error
		wantMsg  string
		wantCode string
	}{
		{name: "not found", status: 404, body: `{"error":{"message":"no such table","code":"NOT_FOUND"}}`, wantErr: mdb.ErrNotFound, wantMsg: "no such table", wantCode: "NOT_FOUND"},
		{name: "conflict", status: 409, body: `{"error":{"message":"dup","code":"UNIQUE_VIOLATION"}}`, wantErr: mdb.ErrConflict, wantMsg: "dup", wantCode: "UNIQUE_VIOLATION"},
		{name: "bad request", status: 400, body: `{"error":{"message":"bad input","code":"BAD_REQUEST"}}`, wantErr: mdb.ErrQuery, wantMsg: "bad input", wantCode: "BAD_REQUEST"},
		{name: "plain text", status: 500, body: `boom`, wantErr: mdb.ErrQuery, wantMsg: "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := mdb.NewClient(srv.URL)
			_, err := c.Schema(ctx)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("errors.Is(err, %v) = false; err=%v", tc.wantErr, err)
			}
			var re *mdb.ResponseError
			if !errors.As(err, &re) {
				t.Fatalf("expected *ResponseError, got %T", err)
			}
			if re.Status != tc.status {
				t.Fatalf("expected status %d, got %d", tc.status, re.Status)
			}
			if re.Message != tc.wantMsg {
				t.Fatalf("expected message %q, got %q", tc.wantMsg, re.Message)
			}
			if tc.wantCode != "" && re.Code != tc.wantCode {
				t.Fatalf("expected code %q, got %q", tc.wantCode, re.Code)
			}
		})
	}
}

// TestQueryBuilderAliasNormalization checks that friendly aliases map to the
// canonical on-wire keys (column->column_id, min/max->lo/hi) by capturing the
// actual request body sent to the /kit/query endpoint.
func TestQueryBuilderAliasNormalization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"rows":[{"ok":true}],"truncated":false}`))

		// Assert the canonical payload shape after the body is read.
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		conds, _ := got["conditions"].([]any)
		if len(conds) != 1 {
			t.Errorf("expected 1 condition, got %d", len(conds))
			return
		}
		first, _ := conds[0].(map[string]any)
		params, _ := first["range"].(map[string]any)
		for _, k := range []string{"column", "min", "max"} {
			if _, dup := params[k]; dup {
				t.Errorf("friendly alias %q leaked into request: %v", k, params)
			}
		}
		for _, k := range []string{"column_id", "lo", "hi"} {
			if _, ok := params[k]; !ok {
				t.Errorf("canonical key %q missing from request: %v", k, params)
			}
		}
	}))
	defer srv.Close()

	c := mdb.NewClient(srv.URL)
	rows, err := c.Query("orders").
		Where("range", map[string]any{"column": int64(3), "min": 100.0, "max": 150.0}).
		Limit(10).
		Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
}

// TestCreateTableWireShapeForColumnSpec confirms that the typed Column struct
// emits the new optional fields (enum_variants, default_value) verbatim into
// the outgoing /kit/create_table JSON body. The daemon's table-create
// extractor uses snake_case keys; this test guards against silent tag drift
// or accidental omitempty regressions in either field.
func TestCreateTableWireShapeForColumnSpec(t *testing.T) {
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"table_id":1}`))
	}))
	defer srv.Close()

	c := mdb.NewClient(srv.URL)
	cols := []mdb.Column{
		{
			ID:           5,
			Name:         "status",
			Type:         "text",
			EnumVariants: []string{"a", "b", "c"},
			DefaultValue: stringPtr("a"),
		},
	}
	if _, err := c.CreateTable(context.Background(), "shape_probe", cols); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	body := string(rawBody)
	for _, want := range []string{
		`"enum_variants":["a","b","c"]`,
		`"default_value":"a"`,
		`"name":"status"`,
		`"ty":"text"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %q; got %s", want, body)
		}
	}
}

// TestCreateTableColumnOmitsOptionalFieldsWhenUnset confirms that the
// `omitempty` JSON tags on EnumVariants and DefaultValue correctly suppress
// those keys when the caller doesn't set them — so the wire shape stays
// minimal for the common (no-enum, no-default) case.
func TestCreateTableColumnOmitsOptionalFieldsWhenUnset(t *testing.T) {
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"table_id":1}`))
	}))
	defer srv.Close()

	c := mdb.NewClient(srv.URL)
	cols := []mdb.Column{
		{ID: 1, Name: "id", Type: "int64", PrimaryKey: true, Nullable: false},
	}
	if _, err := c.CreateTable(context.Background(), "shape_probe_min", cols); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	body := string(rawBody)
	for _, banned := range []string{
		`"enum_variants"`,
		`"default_value"`,
	} {
		if strings.Contains(body, banned) {
			t.Errorf("request body unexpectedly contains %q; got %s", banned, body)
		}
	}
	// Sanity: the canonical keys must still be present.
	for _, want := range []string{`"id":1`, `"name":"id"`, `"ty":"int64"`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %q; got %s", want, body)
		}
	}
}

// TestHistoryRetentionEpochsSendsGET verifies that HistoryRetentionEpochs and
// EarliestRetainedEpoch send GET /history/retention and decode the exact
// response keys.
func TestHistoryRetentionEpochsSendsGET(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/history/retention" {
			t.Errorf("expected path /history/retention, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"history_retention_epochs":7,"earliest_retained_epoch":3}`))
	}))
	defer srv.Close()

	c := mdb.NewClient(srv.URL)
	got, err := c.HistoryRetentionEpochs(ctx)
	if err != nil {
		t.Fatalf("HistoryRetentionEpochs: %v", err)
	}
	if got != 7 {
		t.Errorf("HistoryRetentionEpochs = %d, want 7", got)
	}

	earliest, err := c.EarliestRetainedEpoch(ctx)
	if err != nil {
		t.Fatalf("EarliestRetainedEpoch: %v", err)
	}
	if earliest != 3 {
		t.Errorf("EarliestRetainedEpoch = %d, want 3", earliest)
	}
}

// TestSetHistoryRetentionEpochsSendsPUT verifies that SetHistoryRetentionEpochs
// sends PUT /history/retention with the exact body key and decodes the response.
func TestSetHistoryRetentionEpochsSendsPUT(t *testing.T) {
	ctx := context.Background()
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if r.Body != nil {
			gotBody, _ = io.ReadAll(r.Body)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"history_retention_epochs":42,"earliest_retained_epoch":1}`))
	}))
	defer srv.Close()

	c := mdb.NewClient(srv.URL)
	resp, err := c.SetHistoryRetentionEpochs(ctx, 42)
	if err != nil {
		t.Fatalf("SetHistoryRetentionEpochs: %v", err)
	}
	if resp.HistoryRetentionEpochs != 42 {
		t.Errorf("resp.HistoryRetentionEpochs = %d, want 42", resp.HistoryRetentionEpochs)
	}
	if resp.EarliestRetainedEpoch != 1 {
		t.Errorf("resp.EarliestRetainedEpoch = %d, want 1", resp.EarliestRetainedEpoch)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/history/retention" {
		t.Errorf("path = %s, want /history/retention", gotPath)
	}

	var req map[string]any
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(req) != 1 {
		t.Errorf("request body has %d keys, want 1", len(req))
	}
	if req["history_retention_epochs"] != float64(42) {
		t.Errorf("request body history_retention_epochs = %v, want 42", req["history_retention_epochs"])
	}
}

// TestHistoryRetentionNon2xxPropagates confirms that a non-2xx response from
// /history/retention is surfaced as a *ResponseError wrapping ErrQuery.
func TestHistoryRetentionNon2xxPropagates(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable","code":"SERVICE_UNAVAILABLE"}}`))
	}))
	defer srv.Close()

	c := mdb.NewClient(srv.URL)
	_, err := c.HistoryRetentionEpochs(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *mdb.ResponseError
	if !errors.As(err, &re) {
		t.Fatalf("expected *ResponseError, got %T", err)
	}
	if re.Status != 503 {
		t.Errorf("status = %d, want 503", re.Status)
	}
	if !errors.Is(err, mdb.ErrQuery) {
		t.Errorf("expected error to wrap ErrQuery")
	}

	_, err = c.SetHistoryRetentionEpochs(ctx, 10)
	if err == nil {
		t.Fatal("expected error for SetHistoryRetentionEpochs, got nil")
	}
	if !errors.As(err, &re) {
		t.Fatalf("expected *ResponseError, got %T", err)
	}
	if re.Status != 503 {
		t.Errorf("set status = %d, want 503", re.Status)
	}
}
