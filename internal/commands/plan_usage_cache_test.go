package commands

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

func TestFormatPlanUsageCacheTokens(t *testing.T) {
	got := formatPlanUsage(&client.PlanUsage{Jobs: 1, TotalTokens: 2000, CacheReadTokens: 1500, CacheWriteTokens: 200})
	if !strings.Contains(got, "cache read 1.5k / write 200") {
		t.Fatalf("usage line lacks cache tokens: %q", got)
	}
	if got := formatPlanUsage(&client.PlanUsage{Jobs: 1, TotalTokens: 2000}); strings.Contains(got, "cache") {
		t.Fatalf("no cache tokens, no cache text: %q", got)
	}
}
