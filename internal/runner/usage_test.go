package runner

import "testing"

// tool_calls (either spelling) is read from an agent's usage object and summed by Add.
func TestUsageToolCalls(t *testing.T) {
	u := UsageFromObject(map[string]any{"input_tokens": 3071.0, "output_tokens": 2.0, "tool_calls": 4.0}, "ndjson:x")
	if u == nil || u.ToolCalls != 4 || u.TotalTokens != 3073 {
		t.Fatalf("usage = %+v, want tool_calls 4 / total 3073", u)
	}
	if u := UsageFromObject(map[string]any{"toolCalls": "7"}, "acp:usage_update"); u == nil || u.ToolCalls != 7 {
		t.Fatalf("camelCase / string count: %+v", u)
	}
	sum := &Usage{ToolCalls: 2, TotalTokens: 10}
	sum.Add(&Usage{ToolCalls: 3, TotalTokens: 5, Source: "ndjson:omp"})
	if sum.ToolCalls != 5 || sum.TotalTokens != 15 || sum.Source != "ndjson:omp" {
		t.Fatalf("Add = %+v", sum)
	}
}
