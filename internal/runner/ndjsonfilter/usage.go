package ndjsonfilter

import (
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
)

// usageAt reads the usage object a line carries at a dotted path and converts it with
// the shared reader (runner.UsageFromObject). A line without that object — or with a
// payload of another type — reports none.
func usageAt(obj map[string]any, source string, path ...string) *runner.Usage {
	v, ok := lookup(obj, path)
	if !ok {
		return nil
	}
	block, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return runner.UsageFromObject(block, source)
}

// claudeUsage reads a `result` row's accounting: claude reports the counters in
// `usage` and the cost NEXT TO it (`total_cost_usd`), so the cost is folded into a
// copy of the block — the parsed event itself is left untouched, since the event
// stream may still project it (ndjson_fields).
func claudeUsage(obj map[string]any) *runner.Usage {
	block, _ := obj["usage"].(map[string]any)
	folded := make(map[string]any, util.CapSum(len(block), 1))
	for k, v := range block {
		folded[k] = v
	}
	if v, ok := obj["total_cost_usd"]; ok {
		folded["cost_usd"] = v
	}
	return runner.UsageFromObject(folded, runner.UsageSourceNDJSONClaude)
}
