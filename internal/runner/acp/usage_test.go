package acp

import (
	"testing"

	"github.com/inhere/gofer/internal/runner"
)

// TestUsageFromUpdate pins the acp usage_update reader (SUP-01 E): ACP leaves the
// payload's shape to the agent, so the reader takes the recognisable subset of
// every spelling seen in the wild (camelCase / snake_case, a numeric string, the
// cost as a number or as a nested object) and reports NOTHING for a payload it
// cannot recognise — a guess would put made-up numbers on the job row.
func TestUsageFromUpdate(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want *runner.Usage
	}{
		{
			name: "used + nested cost",
			raw:  `{"sessionUpdate":"usage_update","used":1234,"cost":{"total":0.01}}`,
			want: &runner.Usage{TotalTokens: 1234, CostUSD: 0.01},
		},
		{
			name: "camelCase counters + cost_usd",
			raw:  `{"sessionUpdate":"usage_update","totalTokens":900,"inputTokens":10,"outputTokens":5,"cacheReadTokens":3,"cacheWriteTokens":4,"cost_usd":0.5}`,
			want: &runner.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 4, TotalTokens: 900, CostUSD: 0.5},
		},
		{
			name: "snake_case counters + scalar cost",
			raw:  `{"sessionUpdate":"usage_update","total_tokens":900,"input_tokens":10,"output_tokens":5,"cache_read_tokens":3,"cache_creation_input_tokens":4,"cost":0.25}`,
			want: &runner.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 4, TotalTokens: 900, CostUSD: 0.25},
		},
		{
			name: "numeric strings",
			raw:  `{"sessionUpdate":"usage_update","used":"1234","cost":"0.75"}`,
			want: &runner.Usage{TotalTokens: 1234, CostUSD: 0.75},
		},
		{
			name: "nothing recognisable",
			raw:  `{"sessionUpdate":"usage_update","foo":"bar","limit":{"window":5}}`,
			want: nil,
		},
		{
			name: "malformed payload",
			raw:  `not json at all`,
			want: nil,
		},
		{
			name: "empty payload",
			raw:  ``,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := usageFromUpdate([]byte(tc.raw))
			if tc.want == nil {
				if got != nil {
					t.Fatalf("usage = %+v, want none", got)
				}
				return
			}
			if got == nil {
				t.Fatal("usage = nil, want a value")
			}
			tc.want.Source = runner.UsageSourceACP
			if *got != *tc.want {
				t.Fatalf("usage = %+v, want %+v", *got, *tc.want)
			}
		})
	}
}
