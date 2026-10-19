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
			name: "S4 billing usage in metadata",
			raw:  `{"sessionUpdate":"usage_update","used":34,"_meta":{"input_tokens":18,"output_tokens":52,"total_tokens":34,"cost":{"total":0.006}}}`,
			want: &runner.Usage{InputTokens: 18, OutputTokens: 52, TotalTokens: 34, CostUSD: 0.006},
		},
		{
			name: "metadata counters do not replace context used without billing total",
			raw:  `{"sessionUpdate":"usage_update","used":4096,"_meta":{"input_tokens":18,"output_tokens":52,"cost":{"total":0.006}}}`,
			want: &runner.Usage{InputTokens: 18, OutputTokens: 52, TotalTokens: 4096, CostUSD: 0.006},
		},
		{
			name: "legacy cost-only update",
			raw:  `{"sessionUpdate":"usage_update","cost":{"total":0.006}}`,
			want: &runner.Usage{CostUSD: 0.006},
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

func TestOverlayUsageKeepsCountersForCostOnlyPartialUpdate(t *testing.T) {
	current := &runner.Usage{
		InputTokens: 18, OutputTokens: 52, TotalTokens: 34,
		CostUSD: 0.004, Source: runner.UsageSourceACP,
	}
	next := usageFromUpdate([]byte(`{"sessionUpdate":"usage_update","cost":{"total":0.006}}`))
	got := overlayUsage(current, next)
	want := &runner.Usage{
		InputTokens: 18, OutputTokens: 52, TotalTokens: 34,
		CostUSD: 0.006, Source: runner.UsageSourceACP,
	}
	if *got != *want {
		t.Fatalf("merged usage = %+v, want %+v", *got, *want)
	}
}

// TestUsageFromUpdateAgentBudget: the agent's _meta budget_* keys become the usage's
// agent_budget snapshot, additive to the token tally, and an update without them (or
// without a configured limit) records none.
func TestUsageFromUpdateAgentBudget(t *testing.T) {
	got := usageFromUpdate([]byte(`{"sessionUpdate":"usage_update","used":1100,"_meta":{"input_tokens":600,"output_tokens":500,"total_tokens":1100,"budget_scope":"session","budget_limit":{"tokens":1000,"cost_usd":2},"budget_used":{"tokens":1100,"cost_usd":0.5},"budget_remaining":{"tokens":0,"cost_usd":1.5},"budget_exceeded":true,"budget_reason":"max_tokens","estimated":true}}`))
	if got == nil || got.TotalTokens != 1100 || got.InputTokens != 600 {
		t.Fatalf("usage = %+v, want the tally kept", got)
	}
	want := runner.AgentBudgetUsage{
		Scope: "session", LimitTokens: 1000, UsedTokens: 1100, RemainingTokens: 0,
		LimitCostUSD: 2, UsedCostUSD: 0.5, RemainingCostUSD: 1.5,
		Exceeded: true, Reason: "max_tokens", Estimated: true,
	}
	if got.AgentBudget == nil || *got.AgentBudget != want {
		t.Fatalf("agent_budget = %+v, want %+v", got.AgentBudget, want)
	}

	// A budget-only update (no counters) still yields a snapshot.
	only := usageFromUpdate([]byte(`{"sessionUpdate":"usage_update","_meta":{"budget_scope":"turn","budget_limit":{"cost_usd":1},"budget_used":{"cost_usd":0.25},"budget_remaining":{"cost_usd":0.75},"budget_exceeded":false}}`))
	if only == nil || only.AgentBudget == nil || only.AgentBudget.Exceeded || only.AgentBudget.UsedCostUSD != 0.25 || only.AgentBudget.LimitCostUSD != 1 {
		t.Fatalf("budget-only update = %+v", only)
	}

	// No budget keys / no limit: nothing recorded.
	if u := usageFromUpdate([]byte(`{"sessionUpdate":"usage_update","used":5,"_meta":{"input_tokens":1}}`)); u == nil || u.AgentBudget != nil {
		t.Fatalf("plain update = %+v, want no agent_budget", u)
	}
	if u := usageFromUpdate([]byte(`{"sessionUpdate":"usage_update","used":5,"_meta":{"budget_scope":"turn","budget_limit":{},"budget_exceeded":false}}`)); u == nil || u.AgentBudget != nil {
		t.Fatalf("limitless update = %+v, want no agent_budget", u)
	}

	// The snapshot is replaced by a later one when the tally is merged.
	merged := overlayUsage(got, only)
	if merged.AgentBudget.Exceeded || merged.TotalTokens != 1100 {
		t.Fatalf("merged = %+v, want the newest snapshot over the kept tally", merged)
	}
}
