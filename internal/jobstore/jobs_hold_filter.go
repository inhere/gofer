package jobstore

import "fmt"

// Values of ListQuery.Hold: which human-hold (`--hold`) jobs a list keeps.
const (
	// HoldFilterPending keeps human holds still waiting for a decision.
	HoldFilterPending = "pending"
	// HoldFilterDecided keeps human holds that already carry a decision.
	HoldFilterDecided = "decided"
	// HoldFilterAny keeps every human hold (an update park is never one).
	HoldFilterAny = "any"
)

// ValidHoldFilter reports whether v is one of the HoldFilter* values.
func ValidHoldFilter(v string) bool {
	switch v {
	case HoldFilterPending, HoldFilterDecided, HoldFilterAny:
		return true
	}
	return false
}

// holdJSONFieldFmt extracts one hold_json field. SQLite does not promise the evaluation
// order of AND, and json_extract raises an error on malformed or empty JSON, so every
// extraction is guarded by its own CASE.
const holdJSONFieldFmt = "COALESCE(json_extract(CASE WHEN json_valid(hold_json) THEN hold_json END, '$.%s'), '')"

// holdFilterSQL returns the constant WHERE fragment for a HoldFilter* value.
func holdFilterSQL(v string) (string, error) {
	human := "json_valid(hold_json) AND " + fmt.Sprintf(holdJSONFieldFmt, "kind") + " = ''"
	switch v {
	case HoldFilterAny:
		return human, nil
	case HoldFilterPending:
		return human + " AND status = 'awaiting_approval'", nil
	case HoldFilterDecided:
		return human + " AND " + fmt.Sprintf(holdJSONFieldFmt, "decision") + " <> ''", nil
	}
	return "", fmt.Errorf("jobstore: invalid hold filter %q", v)
}

// appendHoldFilter adds the hold condition to where; an empty v adds nothing.
func appendHoldFilter(where *[]string, v string) error {
	if v == "" {
		return nil
	}
	frag, err := holdFilterSQL(v)
	if err != nil {
		return err
	}
	*where = append(*where, "("+frag+")")
	return nil
}
