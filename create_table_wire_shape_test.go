package mongreldb_test

// Offline wire-shape conformance test for Client.CreateTable.
//
// Mirrors test_wire_shape.c (C client) and create_table_wire_shape_spec.rb
// (Ruby client): asserts that the typed Column struct's optional fields
// (EnumVariants, DefaultValue) are serialized verbatim into the outgoing
// /kit/create_table JSON body in snake_case, and that omitempty correctly
// suppresses those keys when the caller does not set them. No daemon is
// required — an httptest.Server captures the request body in-process.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mdb "github.com/visorcraft/mongreldb-go"
)

// stringPtr returns a pointer to s; lets tests populate *string fields inline.
func wireStringPtr(s string) *string { return &s }

// TestCreateTableWireShape captures the /kit/create_table POST body and
// asserts the exact on-wire fragments for enum_variants and default_value,
// plus that both keys are absent for a sibling column that omits them.
func TestCreateTableWireShape(t *testing.T) {
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			rawBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"table_id":1}`))
	}))
	defer srv.Close()

	c := mdb.NewClient(srv.URL)
	cols := []mdb.Column{
		{ID: 1, Name: "id", Type: "int64", PrimaryKey: true, Nullable: false},
		{
			ID:           2,
			Name:         "status",
			Type:         "enum",
			EnumVariants: []string{"active", "inactive", "paused"},
		},
		{
			ID:               3,
			Name:             "created_at",
			Type:             "timestamp_nanos",
			DefaultValue:     wireStringPtr("legacy"),
			DefaultValueJSON: 3,
		},
		{
			ID:               4,
			Name:             "updated_at",
			Type:             "timestamp_nanos",
			DefaultValue:     wireStringPtr("legacy"),
			DefaultValueJSON: 4,
			DefaultExpr:      wireStringPtr("now"),
		},
		{ID: 5, Name: "s", Type: "varchar", DefaultValueJSON: "draft"},
		{ID: 6, Name: "b", Type: "bool", DefaultValueJSON: true},
		{ID: 7, Name: "n", Type: "varchar", DefaultValueJSON: json.RawMessage("null")},
	}
	constraints := map[string]any{"checks": []any{map[string]any{
		"id": 1, "name": "id_present", "expr": map[string]any{"IsNotNull": 1},
	}}}
	if _, err := c.CreateTable(context.Background(), "wire_shape_probe", cols, constraints); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	body := string(rawBody)

	// Required: enum_variants serializes as a JSON string array, in order,
	// with the exact key name the daemon's table-create extractor reads.
	if !strings.Contains(body, `"enum_variants":["active","inactive","paused"]`) {
		t.Errorf("request body missing enum_variants array; got %s", body)
	}
	if !strings.Contains(body, `"default_value":3`) {
		t.Errorf("request body missing scalar default_value; got %s", body)
	}
	if !strings.Contains(body, `"default_expr":"now"`) {
		t.Errorf("request body missing default_expr; got %s", body)
	}
	for _, want := range []string{`"default_value":"draft"`, `"default_value":true`, `"default_value":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %s; got %s", want, body)
		}
	}
	var payload struct {
		Columns []map[string]any `json:"columns"`
	}
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, exists := payload.Columns[3]["default_value"]; exists {
		t.Errorf("default_expr column also emitted default_value; got %s", body)
	}
	if !strings.Contains(body, `"constraints":{"checks":[`) ||
		!strings.Contains(body, `"name":"id_present"`) ||
		!strings.Contains(body, `"IsNotNull":1`) {
		t.Errorf("request body missing constraints.checks; got %s", body)
	}

	// An unset enum list must not leak as null. Explicit default null is valid.
	for _, banned := range []string{`"enum_variants":null`} {
		if strings.Contains(body, banned) {
			t.Errorf("request body unexpectedly contains %q; got %s", banned, body)
		}
	}

	// Sanity: the canonical keys for both columns must still be present.
	for _, want := range []string{`"id":1`, `"name":"id"`, `"ty":"int64"`, `"name":"status"`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %q; got %s", want, body)
		}
	}
}

// TestCreateTableStaticDefaultMatrix verifies the full static-default contract:
// string, integer, boolean, explicit JSON null, literal "now", and dynamic
// default_expr "now"/"uuid". It decodes the request JSON and asserts both the
// preserved scalar types and that default_expr columns do not emit default_value.
func TestCreateTableStaticDefaultMatrix(t *testing.T) {
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"table_id":2}`))
	}))
	defer srv.Close()

	c := mdb.NewClient(srv.URL)
	cols := []mdb.Column{
		{ID: 1, Name: "id", Type: "int64", PrimaryKey: true, Nullable: false},
		// DefaultValueJSON takes precedence over the legacy string DefaultValue.
		{ID: 2, Name: "s", Type: "varchar", DefaultValue: wireStringPtr("legacy"), DefaultValueJSON: "draft"},
		{ID: 3, Name: "i", Type: "int64", DefaultValueJSON: int64(7)},
		{ID: 4, Name: "b", Type: "bool", DefaultValueJSON: true},
		{ID: 5, Name: "n", Type: "varchar", DefaultValueJSON: json.RawMessage("null")},
		// Literal "now" string must stay a literal, not become a dynamic expr.
		{ID: 6, Name: "now_literal", Type: "timestamp_nanos", DefaultValue: wireStringPtr("now"), DefaultValueJSON: "now"},
		{ID: 7, Name: "now_expr", Type: "timestamp_nanos", DefaultExpr: wireStringPtr("now")},
		{ID: 8, Name: "uuid_expr", Type: "uuid", DefaultExpr: wireStringPtr("uuid")},
	}
	if _, err := c.CreateTable(context.Background(), "default_matrix", cols); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	var payload struct {
		Name    string                       `json:"name"`
		Columns []map[string]json.RawMessage `json:"columns"`
	}
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		t.Fatalf("decode request JSON: %v", err)
	}
	if payload.Name != "default_matrix" {
		t.Errorf("table name = %q, want %q", payload.Name, "default_matrix")
	}

	byName := make(map[string]map[string]json.RawMessage, len(payload.Columns))
	for _, col := range payload.Columns {
		var name string
		if err := json.Unmarshal(col["name"], &name); err != nil {
			t.Fatalf("decode column name: %v", err)
		}
		byName[name] = col
	}

	assertRaw := func(t *testing.T, name, key string, want json.RawMessage) {
		t.Helper()
		col, ok := byName[name]
		if !ok {
			t.Fatalf("column %q missing from request", name)
		}
		got, ok := col[key]
		if !ok {
			t.Fatalf("column %q missing key %q", name, key)
		}
		if string(got) != string(want) {
			t.Errorf("column %q %q = %s, want %s", name, key, got, want)
		}
	}
	assertMissing := func(t *testing.T, name, key string) {
		t.Helper()
		col, ok := byName[name]
		if !ok {
			t.Fatalf("column %q missing from request", name)
		}
		if _, ok := col[key]; ok {
			t.Errorf("column %q unexpectedly has key %q", name, key)
		}
	}

	assertRaw(t, "s", "default_value", json.RawMessage(`"draft"`))
	assertRaw(t, "i", "default_value", json.RawMessage(`7`))
	assertRaw(t, "b", "default_value", json.RawMessage(`true`))
	assertRaw(t, "n", "default_value", json.RawMessage(`null`))
	assertRaw(t, "now_literal", "default_value", json.RawMessage(`"now"`))

	assertRaw(t, "now_expr", "default_expr", json.RawMessage(`"now"`))
	assertMissing(t, "now_expr", "default_value")
	assertRaw(t, "uuid_expr", "default_expr", json.RawMessage(`"uuid"`))
	assertMissing(t, "uuid_expr", "default_value")
}

// TestCreateTableColumnOmitsOptionalFieldsWhenUnset is the negative half of
// the wire-shape contract: a column that leaves EnumVariants nil and
// DefaultValue nil must not emit those keys at all, so the wire stays minimal
// for the common (no-enum, no-default) case.
func TestWireShapeOmitsOptionalFieldsWhenUnset(t *testing.T) {
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			rawBody, _ = io.ReadAll(r.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"table_id":1}`))
	}))
	defer srv.Close()

	c := mdb.NewClient(srv.URL)
	cols := []mdb.Column{
		{ID: 1, Name: "id", Type: "int64", PrimaryKey: true, Nullable: false},
		{ID: 2, Name: "name", Type: "text", Nullable: false},
	}
	if _, err := c.CreateTable(context.Background(), "wire_shape_min", cols); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	body := string(rawBody)
	for _, banned := range []string{`"enum_variants"`, `"default_value"`} {
		if strings.Contains(body, banned) {
			t.Errorf("request body unexpectedly contains %q; got %s", banned, body)
		}
	}
	for _, want := range []string{`"name":"id"`, `"name":"name"`, `"ty":"int64"`, `"ty":"text"`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %q; got %s", want, body)
		}
	}
}
