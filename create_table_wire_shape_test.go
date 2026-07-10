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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mdb "github.com/visorcraft/mongreldb-go"
)

// stringPtr returns a pointer to s; lets tests populate *string fields inline.
func stringPtr(s string) *string { return &s }

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
			Type:         "text",
			EnumVariants: []string{"active", "inactive", "paused"},
			DefaultValue: stringPtr("active"),
		},
	}
	if _, err := c.CreateTable(context.Background(), "wire_shape_probe", cols); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	body := string(rawBody)

	// Required: enum_variants serializes as a JSON string array, in order,
	// with the exact key name the daemon's table-create extractor reads.
	if !strings.Contains(body, `"enum_variants":["active","inactive","paused"]`) {
		t.Errorf("request body missing enum_variants array; got %s", body)
	}
	// Required: default_value is a plain JSON string (not a number, not null).
	if !strings.Contains(body, `"default_value":"active"`) {
		t.Errorf("request body missing default_value string; got %s", body)
	}

	// Omitempty sanity: the id column sets neither field, so those keys must
	// not appear anywhere in the body (regression guard against accidental
	// `null` literals that the engine would later reject).
	for _, banned := range []string{`"enum_variants":null`, `"default_value":null`} {
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

// TestCreateTableColumnOmitsOptionalFieldsWhenUnset is the negative half of
// the wire-shape contract: a column that leaves EnumVariants nil and
// DefaultValue nil must not emit those keys at all, so the wire stays minimal
// for the common (no-enum, no-default) case.
func TestCreateTableColumnOmitsOptionalFieldsWhenUnset(t *testing.T) {
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