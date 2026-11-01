package acp

import (
	"testing"

	"github.com/inhere/gofer/internal/runner"
)

// An ACP usage_update's standard used / size fields are the context level; the billing
// counters of a later update overlay without erasing it.
func TestUsageFromUpdateReadsTheContextWindow(t *testing.T) {
	u := usageFromUpdate([]byte(`{"sessionUpdate":"usage_update","used":30000,"size":200000}`))
	if u == nil || u.Context == nil || u.Context.UsedTokens != 30000 || u.Context.LimitTokens != 200000 || u.Context.Source != runner.ContextSourceACP {
		t.Fatalf("usage = %+v", u)
	}
	// no window: no context level (used alone may be a billing figure)
	if u := usageFromUpdate([]byte(`{"used":30000}`)); u != nil && u.Context != nil {
		t.Fatalf("a window-less used must not become a context: %+v", u.Context)
	}
	// a cost-only update keeps the level of the one before it
	cur := usageFromUpdate([]byte(`{"used":50000,"size":100000}`))
	cur = overlayUsage(cur, usageFromUpdate([]byte(`{"_meta":{"cost_usd":0.5}}`)))
	if cur.Context == nil || cur.Context.UsedTokens != 50000 || cur.CostUSD != 0.5 {
		t.Fatalf("merged = %+v ctx=%+v", cur, cur.Context)
	}
	cur = overlayUsage(cur, usageFromUpdate([]byte(`{"used":90000,"size":100000}`)))
	if cur.Context.UsedTokens != 90000 {
		t.Fatalf("the newer level replaces the older: %+v", cur.Context)
	}
}
