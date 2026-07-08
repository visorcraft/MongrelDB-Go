package mongreldb_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	mdb "github.com/visorcraft/mongreldb-go"
)

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
