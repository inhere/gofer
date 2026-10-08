package ndjsonfilter

import (
	"io"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
)

func feed(t *testing.T, opt Options, lines ...string) {
	t.Helper()
	f := New(io.Discard, io.Discard, opt)
	if _, err := f.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()
}

func TestMeterClaudeDedupesMessageIDAndCountsTurns(t *testing.T) {
	m := runner.NewBudgetMeter(&config.Budget{MaxTokens: 1 << 40}, nil)
	feed(t, Options{Projector: ProjectorClaude, Meter: m},
		`{"type":"assistant","message":{"id":"a","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100}}}`,
		`{"type":"assistant","message":{"id":"a","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100}}}`,
		`{"type":"assistant","message":{"id":"b","usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"stream_event","event":{}}`,
	)
	if tok, _, turns := m.Spent(); tok != 117 || turns != 2 {
		t.Fatalf("spent = %d tokens / %d turns, want 117 / 2", tok, turns)
	}
}

// The meter sees lines the keep whitelist drops: a budget must not depend on log volume.
func TestMeterIgnoresKeepWhitelist(t *testing.T) {
	m := runner.NewBudgetMeter(&config.Budget{MaxTokens: 1 << 40}, nil)
	feed(t, Options{Projector: ProjectorClaude, Keep: []string{"result"}, Meter: m},
		`{"type":"assistant","message":{"id":"a","usage":{"input_tokens":7,"output_tokens":3}}}`)
	if tok, _, turns := m.Spent(); tok != 10 || turns != 1 {
		t.Fatalf("spent = %d / %d, want 10 / 1", tok, turns)
	}
}

func TestMeterClaudeResultRowCarriesCost(t *testing.T) {
	m := runner.NewBudgetMeter(&config.Budget{MaxCostUSD: 100}, nil)
	feed(t, Options{Projector: ProjectorClaude, Meter: m},
		`{"type":"result","result":"x","total_cost_usd":0.75,"usage":{"input_tokens":4,"output_tokens":4}}`)
	if _, cost, _ := m.Spent(); cost != 0.75 {
		t.Fatalf("cost = %v, want 0.75", cost)
	}
}

func TestMeterOMPAndGenericUsagePath(t *testing.T) {
	m := runner.NewBudgetMeter(&config.Budget{MaxTokens: 1 << 40}, nil)
	feed(t, Options{Projector: ProjectorOMP, Meter: m},
		`{"type":"message_end","message":{"role":"assistant","content":[],"usage":{"input_tokens":30,"output_tokens":10}}}`,
		`{"type":"message_end","message":{"role":"user","content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[],"usage":{"input_tokens":50,"output_tokens":10}}}`)
	if tok, _, turns := m.Spent(); tok != 60 || turns != 2 {
		t.Fatalf("omp spent = %d / %d, want 60 / 2 (running tally, high-water)", tok, turns)
	}
	g := runner.NewBudgetMeter(&config.Budget{MaxTokens: 1 << 40}, nil)
	feed(t, Options{Projector: ProjectorGeneric, UsagePath: "stats.usage", UsageSource: "ndjson:x", Meter: g},
		`{"stats":{"usage":{"total_tokens":42}}}`)
	if tok, _, turns := g.Spent(); tok != 42 || turns != 0 {
		t.Fatalf("generic spent = %d / %d, want 42 / 0", tok, turns)
	}
}
