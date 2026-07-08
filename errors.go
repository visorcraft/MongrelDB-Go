package mongreldb

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Typed client errors. Each sentinel corresponds to a class of HTTP status
// returned by the daemon. Use errors.Is to discriminate:
//
//	switch {
//	case errors.Is(err, mongreldb.ErrNotFound):  ...
//	case errors.Is(err, mongreldb.ErrConflict):  ...
//	case errors.Is(err, mongreldb.ErrAuth):      ...
//	case errors.Is(err, mongreldb.ErrQuery):     ...
//	}
var (
	// ErrNotFound is returned for HTTP 404 responses (missing table, schema, etc.).
	ErrNotFound = errors.New("mongreldb: not found")
	// ErrConflict is returned for HTTP 409 responses (unique, foreign key, check,
	// or trigger constraint violations).
	ErrConflict = errors.New("mongreldb: constraint conflict")
	// ErrAuth is returned for HTTP 401 or 403 responses (bad or missing credentials).
	ErrAuth = errors.New("mongreldb: authentication failed")
	// ErrQuery is returned for HTTP 400 or 5xx responses and for any other
	// request-level failure not covered by the more specific sentinels.
	ErrQuery = errors.New("mongreldb: query failed")
)

// ResponseError carries the HTTP status code and the server's decoded error
// envelope for a failed request. It wraps the relevant sentinel
// ([ErrNotFound], [ErrConflict], [ErrAuth], or [ErrQuery]) so that both
// errors.Is(err, mongreldb.ErrXxx) and a type assertion
// (var re *mongreldb.ResponseError) work.
type ResponseError struct {
	// Status is the HTTP status code returned by the daemon.
	Status int
	// Message is the human-readable error message.
	Message string
	// Code is the server's structured error code, when present (e.g.
	// "UNIQUE_VIOLATION", "FK_VIOLATION").
	Code string
	// OpIndex is the offending operation index within a transaction, when the
	// server reports one (constraint violations during commit).
	OpIndex *int
	// sentinel is the wrapped sentinel error used to satisfy errors.Is.
	sentinel error
}

// Error implements the error interface.
func (e *ResponseError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("mongreldb: server error (%d)", e.Status)
}

// Unwrap allows errors.Is to match the wrapped sentinel.
func (e *ResponseError) Unwrap() error {
	if e.sentinel != nil {
		return e.sentinel
	}
	return nil
}

// newResponseError maps an HTTP status code and response body to a
// *ResponseError wrapping the appropriate sentinel. It best-effort decodes the
// server's JSON error envelope ({error: {message, code, op_index}}) and falls
// back to the raw body as the message.
func newResponseError(status int, body []byte) *ResponseError {
	re := &ResponseError{Status: status}

	// Try the structured envelope first, then a bare object with top-level keys.
	if msg, code, opIndex, ok := decodeErrorEnvelope(body); ok {
		re.Message = msg
		re.Code = code
		re.OpIndex = opIndex
	} else if len(body) > 0 {
		re.Message = string(body)
	}

	switch status {
	case 401, 403:
		re.sentinel = ErrAuth
	case 404:
		re.sentinel = ErrNotFound
	case 409:
		re.sentinel = ErrConflict
	default:
		re.sentinel = ErrQuery
	}

	// Default messages when the server gave none.
	if re.Message == "" {
		switch status {
		case 401, 403:
			re.Message = fmt.Sprintf("authentication failed (%d)", status)
		case 404:
			re.Message = "resource not found"
		case 409:
			re.Message = "constraint violation"
		default:
			re.Message = fmt.Sprintf("server error (%d)", status)
		}
	}
	return re
}

// decodeErrorEnvelope pulls the message/code/op_index fields out of the
// server's error response. Returns ok=false if the body is not a recognized
// JSON error envelope.
func decodeErrorEnvelope(body []byte) (message, code string, opIndex *int, ok bool) {
	if len(body) == 0 {
		return
	}
	// Tolerate a leading byte-order mark or whitespace, then require a JSON
	// object so we don't try to decode binary Arrow bodies as JSON.
	trimmed := body
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return
	}

	// Prefer the nested {"error": {...}} envelope used by the daemon.
	var nested struct {
		Error struct {
			Message string          `json:"message"`
			Code    string          `json:"code"`
			OpIndex *int            `json:"op_index"`
			Extra   json.RawMessage `json:"-"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &nested); err == nil && (nested.Error.Message != "" || nested.Error.Code != "" || nested.Error.OpIndex != nil) {
		return nested.Error.Message, nested.Error.Code, nested.Error.OpIndex, true
	}

	// Fall back to a flat {"message": ..., "code": ...} object.
	var flat struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(body, &flat); err == nil && (flat.Message != "" || flat.Code != "") {
		return flat.Message, flat.Code, nil, true
	}
	return
}
