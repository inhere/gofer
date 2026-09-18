package ndjsonfilter

import (
	"testing"

	"github.com/inhere/gofer/internal/runner"
)

// ompUsageSession is the session row the omp usage fixtures open with.
const ompUsageSession = `{"type":"session","id":"0f9c1e2a-1111-4a2b-8c3d-9e8f7a6b5c4d"}`

// TestOMPUsageCaptured: an `omp --mode json` run reports its token/cost accounting
// on the completed assistant message (SUP-01 E). The capture keeps the LAST such
// message — the run's final tally, not a mid-run snapshot — reads both the
// camelCase and the snake_case spelling of the counters, and names its source.
func TestOMPUsageCaptured(t *testing.T) {
	camel := []string{
		ompUsageSession,
		`{"type":"message_end","turn":1,"message":{"role":"assistant","content":[{"type":"text","text":"first"}],"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":5,"cacheWriteTokens":7,"totalTokens":132,"cost":{"total":0.001}}}}`,
	}
	f, _, _ := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP}, camel)
	u := f.Usage()
	if u == nil {
		t.Fatal("no usage captured from a completed assistant message")
	}
	if u.Source != runner.UsageSourceNDJSONOMP {
		t.Fatalf("usage source = %q, want %q", u.Source, runner.UsageSourceNDJSONOMP)
	}
	if u.InputTokens != 100 || u.OutputTokens != 20 || u.CacheReadTokens != 5 || u.CacheWriteTokens != 7 || u.TotalTokens != 132 {
		t.Fatalf("usage = %+v, want in 100 / out 20 / cacheRead 5 / cacheWrite 7 / total 132", u)
	}
	if u.CostUSD != 0.001 {
		t.Fatalf("cost_usd = %v, want 0.001 (message.usage.cost.total)", u.CostUSD)
	}

	// Two later message_end rows: a USER one (never a usage source) and the run's
	// real final tally, written in the snake_case spelling. The last ASSISTANT
	// message is what a caller must see.
	lines := append(camel,
		`{"type":"message_end","turn":2,"message":{"role":"user","content":[{"type":"text","text":"hi"}],"usage":{"inputTokens":99999}}}`,
		`{"type":"message_end","turn":2,"message":{"role":"assistant","content":[{"type":"text","text":"second"}],"usage":{"input_tokens":300,"output_tokens":50,"cache_read_tokens":289,"total_tokens":639,"total_cost_usd":0.0032}}}`,
	)
	f2, _, _ := project(t, Options{Keep: ompKeep, Projector: ProjectorOMP}, lines)
	last := f2.Usage()
	if last == nil {
		t.Fatal("no usage captured from the run's final assistant message")
	}
	if last.InputTokens != 300 || last.OutputTokens != 50 || last.CacheReadTokens != 289 || last.CacheWriteTokens != 0 || last.TotalTokens != 639 {
		t.Fatalf("usage = %+v, want the LAST assistant message's tally (in 300 / out 50 / cacheRead 289 / total 639)", last)
	}
	if last.CostUSD != 0.0032 {
		t.Fatalf("cost_usd = %v, want 0.0032 (total_cost_usd)", last.CostUSD)
	}
}

// TestClaudeUsageCaptured: a `claude --output-format stream-json` run reports its
// usage on the terminal `result` row, with no total_tokens field — the total is
// the four counters the agent did report, and the cost rides total_cost_usd.
func TestClaudeUsageCaptured(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"` + claudeSampleSessionID + `"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"` + claudeSampleSessionID + `"` +
			`,"total_cost_usd":0.0123,"usage":{"input_tokens":1200,"output_tokens":80,"cache_read_input_tokens":289,"cache_creation_input_tokens":11}}`,
	}
	f, _, _ := project(t, Options{Keep: []string{"system", "result"}, Projector: ProjectorClaude}, lines)
	u := f.Usage()
	if u == nil {
		t.Fatal("no usage captured from the claude result row")
	}
	if u.Source != runner.UsageSourceNDJSONClaude {
		t.Fatalf("usage source = %q, want %q", u.Source, runner.UsageSourceNDJSONClaude)
	}
	if u.InputTokens != 1200 || u.OutputTokens != 80 || u.CacheReadTokens != 289 || u.CacheWriteTokens != 11 {
		t.Fatalf("usage = %+v, want in 1200 / out 80 / cacheRead 289 / cacheWrite 11", u)
	}
	if u.TotalTokens != 1580 {
		t.Fatalf("total_tokens = %d, want the sum of the reported counters (1580)", u.TotalTokens)
	}
	if u.CostUSD != 0.0123 {
		t.Fatalf("cost_usd = %v, want 0.0123 (total_cost_usd)", u.CostUSD)
	}
}

// TestGenericProjectorNoUsage: an agent with no built-in projector has no
// documented usage shape, so the capture reports none — the very same `result`
// row that yields usage under the claude projector must not be guessed at under
// the generic one (while still reaching the event stream).
func TestGenericProjectorNoUsage(t *testing.T) {
	lines := []string{
		`{"type":"result","subtype":"success","result":"done","total_cost_usd":0.0123,"usage":{"input_tokens":1200,"output_tokens":80}}`,
	}
	f, _, events := project(t, Options{Projector: ProjectorGeneric}, lines)
	if u := f.Usage(); u != nil {
		t.Fatalf("usage = %+v, want none: the generic projector parses no usage shape", u)
	}
	if events == "" {
		t.Fatal("the generic projector must still pass the row through to the event stream")
	}
}
