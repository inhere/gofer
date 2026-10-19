package streaming

import (
	"encoding/json"
	"testing"
)

// The n-th "prompt" record of a job is annotated with the attachments of its n-th prompt;
// other records and prompts without attachments carry no field.
func TestACPNormalizerAnnotatesPromptAttachments(t *testing.T) {
	var asked []int
	n := acpNormalizer{promptAttachments: func(i int) any {
		asked = append(asked, i)
		if i == 2 {
			return []map[string]string{{"id": "att-2"}}
		}
		return nil
	}}
	var events []map[string]any
	for _, line := range []string{
		`{"t":"prompt","text":"one"}`,
		`{"t":"message","text":"reply"}`,
		`{"t":"prompt","text":"two"}`,
	} {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		events = append(events, n.record(rec)...)
	}
	if len(events) != 3 {
		t.Fatalf("events = %v", events)
	}
	if _, has := events[0]["attachments"]; has {
		t.Fatalf("prompt 1 must carry no attachments: %v", events[0])
	}
	if _, has := events[1]["attachments"]; has {
		t.Fatalf("a message must never be annotated: %v", events[1])
	}
	got, ok := events[2]["attachments"].([]map[string]string)
	if !ok || len(got) != 1 || got[0]["id"] != "att-2" {
		t.Fatalf("prompt 2 attachments = %v", events[2]["attachments"])
	}
	if len(asked) != 2 || asked[0] != 1 || asked[1] != 2 {
		t.Fatalf("asked for prompts %v, want [1 2]", asked)
	}
}
