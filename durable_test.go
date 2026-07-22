package mongreldb

import (
	"encoding/json"
	"testing"
)

func TestParseQueryStatusDurableHlc(t *testing.T) {
	// Fixture mirrors mongreldb-server GET /queries/{id} (0.64+).
	raw := []byte(`{
		"query_id": "abcdefabcdefabcdefabcdefabcdefab",
		"status": "committed",
		"state": "completed",
		"server_state": "completed",
		"terminal_state": "committed",
		"operation": "INSERT",
		"committed": true,
		"committed_statements": 1,
		"last_commit_epoch": 17,
		"last_commit_epoch_text": "17",
		"last_commit_hlc": {
			"physical_micros": 1700000000000000,
			"logical": 3,
			"node_tiebreaker": 7
		},
		"first_commit_statement_index": 0,
		"last_commit_statement_index": 0,
		"completed_statements": 1,
		"statement_index": 0,
		"cancel_outcome": null,
		"cancellation_reason": "none",
		"retryable": false,
		"outcome": {
			"committed": true,
			"committed_statements": 1,
			"last_commit_epoch": 17,
			"last_commit_epoch_text": "17",
			"last_commit_hlc": {
				"physical_micros": 1700000000000000,
				"logical": 3,
				"node_tiebreaker": 7
			},
			"first_commit_statement_index": 0,
			"last_commit_statement_index": 0,
			"completed_statements": 1,
			"statement_index": 0,
			"serialization": "succeeded",
			"serialization_state": "succeeded",
			"terminal_state": "committed"
		},
		"durable": {
			"committed": true,
			"committed_statements": 1,
			"last_commit_epoch": 17,
			"last_commit_epoch_text": "17",
			"last_commit_hlc": {
				"physical_micros": 1700000000000000,
				"logical": 3,
				"node_tiebreaker": 7
			},
			"first_commit_statement_index": 0,
			"last_commit_statement_index": 0,
			"completed_statements": 1,
			"statement_index": 0,
			"serialization": "succeeded",
			"serialization_state": "succeeded",
			"terminal_state": "committed"
		},
		"terminal_error": null
	}`)
	status, err := ParseQueryStatusJSON(raw)
	if err != nil {
		t.Fatalf("ParseQueryStatusJSON: %v", err)
	}
	if status.Committed == nil || !*status.Committed {
		t.Fatalf("committed = %v, want true", status.Committed)
	}
	hlc := status.CommitHlc()
	if hlc == nil {
		t.Fatal("CommitHlc() is nil")
	}
	if hlc.PhysicalMicros != 1700000000000000 || hlc.Logical != 3 || hlc.NodeTiebreaker != 7 {
		t.Fatalf("HLC = %+v", hlc)
	}
	if status.SerializationState() != "succeeded" {
		t.Fatalf("SerializationState = %q", status.SerializationState())
	}
	// Structural access — no string-parsing of free-form status text.
	if status.Outcome.LastCommitEpoch == nil || *status.Outcome.LastCommitEpoch != 17 {
		t.Fatalf("outcome epoch = %v", status.Outcome.LastCommitEpoch)
	}
}

func TestBuildRetrieveTextRequest(t *testing.T) {
	payload, err := BuildRetrieveTextRequest("docs", 3, "cat sat", RetrieveTextOptions{K: 5})
	if err != nil {
		t.Fatal(err)
	}
	if payload["table"] != "docs" || payload["embedding_column"] != uint16(3) || payload["text"] != "cat sat" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["k"] != 5 {
		t.Fatalf("k = %v", payload["k"])
	}
	// Round-trip through JSON encoder used by the HTTP client.
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["embedding_column"].(float64) != 3 {
		t.Fatalf("wire embedding_column = %v", decoded["embedding_column"])
	}
}

func TestMultiRetrieverSearchBuild(t *testing.T) {
	c := NewClient("http://127.0.0.1:9")
	payload, err := c.Search("docs").
		AnnRetriever("ann", 3, []float64{0.1, 0.2}, 10, 1.0).
		SparseRetriever("sparse", 4, [][2]float64{{1, 0.5}, {2, 0.25}}, 10, 0.5).
		Fusion(60).
		Limit(5).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	retrievers, ok := payload["retrievers"].([]map[string]any)
	if !ok {
		// Build stores []map[string]any — re-check type via JSON.
		b, _ := json.Marshal(payload)
		var wire map[string]any
		_ = json.Unmarshal(b, &wire)
		list, ok := wire["retrievers"].([]any)
		if !ok || len(list) != 2 {
			t.Fatalf("retrievers = %#v", wire["retrievers"])
		}
		return
	}
	if len(retrievers) != 2 {
		t.Fatalf("len(retrievers)=%d", len(retrievers))
	}
	_ = retrievers
}
