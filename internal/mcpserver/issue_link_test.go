package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestRunJobInputIssueLinkFieldsRoundTrip(t *testing.T) {
	var in runJobInput
	if err := json.Unmarshal([]byte(`{"project_key":"p","agent":"exec","runner":"local","issue_id":"i-1","tracker_id":"t-1"}`), &in); err != nil {
		t.Fatal(err)
	}
	if in.IssueID != "i-1" || in.TrackerID != "t-1" {
		t.Fatalf("issue link fields=%+v", in)
	}
}
