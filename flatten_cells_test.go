package mongreldb

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFlattenCellsStableOrder(t *testing.T) {
	cells := Cells{
		3: 78.3,
		1: int64(1),
		2: "Alice",
	}
	// Repeated flattenings must match exactly (sorted by col id).
	a := flattenCells(cells)
	b := flattenCells(cells)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("unstable flatten: %#v vs %#v", a, b)
	}
	want := []any{int64(1), int64(1), int64(2), "Alice", int64(3), 78.3}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("got %#v want %#v", a, want)
	}
	// Idempotency-relevant: JSON of the put op must be byte-identical.
	op := map[string]any{"put": map[string]any{"table": "t", "cells": a, "returning": false}}
	j1, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	op2 := map[string]any{"put": map[string]any{"table": "t", "cells": b, "returning": false}}
	j2, err := json.Marshal(op2)
	if err != nil {
		t.Fatal(err)
	}
	if string(j1) != string(j2) {
		t.Fatalf("JSON not stable:\n%s\n%s", j1, j2)
	}
}
