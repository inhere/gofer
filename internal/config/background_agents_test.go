package config

import (
	"strings"
	"testing"
)

func TestBackgroundAgentsBudgetValidation(t *testing.T) {
	if err := (BackgroundAgentsConfig{DailyCostUSD: 1.5, DailyTokens: 1000}).validate(); err != nil {
		t.Fatalf("valid budgets rejected: %v", err)
	}
	if err := (BackgroundAgentsConfig{}).validate(); err != nil {
		t.Fatalf("an empty block is valid (everything off): %v", err)
	}
	for name, c := range map[string]BackgroundAgentsConfig{
		"negative cost":   {DailyCostUSD: -1},
		"negative tokens": {DailyTokens: -1},
		"bad session":     {Session: BackgroundAgentSessionConfig{MaxContextRatio: 2}},
	} {
		err := c.validate()
		if err == nil || !strings.Contains(err.Error(), "background_agents") {
			t.Fatalf("%s: err = %v, want a server.background_agents error", name, err)
		}
	}
}
