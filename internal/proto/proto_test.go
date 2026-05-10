package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionSummaryJSONUsesSessionID guards the wire contract documented in
// architecture/api.md: session identifiers are always serialized as
// `session_id`. The SPA (web/src/types.ts) expects this field; the previous
// `json:"id"` tag caused the SPA to render `/sessions/undefined` and loop
// forever on `session_not_found`.
func TestSessionSummaryJSONUsesSessionID(t *testing.T) {
	body, err := json.Marshal(SessionSummary{ID: "sess_test"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, `"session_id":"sess_test"`) {
		t.Errorf("expected session_id field, got: %s", got)
	}
	if strings.Contains(got, `"id":"sess_test"`) {
		t.Errorf("session id should not serialize as bare \"id\", got: %s", got)
	}
}
