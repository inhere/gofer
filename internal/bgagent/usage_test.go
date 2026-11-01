package bgagent

import (
	"bytes"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
)

func TestSnapshotFromUsage(t *testing.T) {
	if s := SnapshotFromUsage(nil); s.RatioKnown || s.ContextLimit != 0 {
		t.Fatalf("nil usage = %+v, want unknown", s)
	}
	if s := SnapshotFromUsage(&runner.Usage{InputTokens: 5}); s.RatioKnown {
		t.Fatalf("usage without context = %+v, want unknown ratio", s)
	}
	s := SnapshotFromUsage(&runner.Usage{Context: &runner.ContextUsage{UsedTokens: 12000, LimitTokens: 200000, Ratio: 0.06, Source: runner.ContextSourceTurnEnd}})
	if !s.RatioKnown || s.Ratio != 0.06 || s.ContextUsed != 12000 || s.ContextLimit != 200000 {
		t.Fatalf("suga shape = %+v", s)
	}
	// ratio only: stored against a fixed scale so used / limit reproduces it
	s = SnapshotFromUsage(&runner.Usage{Context: &runner.ContextUsage{Ratio: 0.42}})
	if !s.RatioKnown || math.Abs(float64(s.ContextUsed)/float64(s.ContextLimit)-0.42) > 1e-6 {
		t.Fatalf("ratio only = %+v", s)
	}
	// est_tokens / window with no ratio: computed
	s = SnapshotFromUsage(&runner.Usage{Context: &runner.ContextUsage{UsedTokens: 50, LimitTokens: 200}})
	if !s.RatioKnown || s.Ratio != 0.25 {
		t.Fatalf("computed = %+v", s)
	}
	// a window alone, or a fill alone, is not a level
	for _, c := range []*runner.ContextUsage{{LimitTokens: 1000}, {UsedTokens: 1000}} {
		if s := SnapshotFromUsage(&runner.Usage{Context: c}); s.RatioKnown {
			t.Fatalf("%+v must be unknown, got %+v", c, s)
		}
	}
}

// feed runs NDJSON lines through the capture-time filter the job layer uses and returns
// what the stream said about usage and context.
func feed(t *testing.T, opt ndjsonfilter.Options, lines ...string) UsageSnapshot {
	t.Helper()
	var events bytes.Buffer
	f := ndjsonfilter.New(io.Discard, &events, opt)
	if _, err := f.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	u := f.Usage()
	if sig := f.Signals(); sig.Context != nil {
		if u == nil {
			u = &runner.Usage{}
		}
		c := *sig.Context
		u.Context = &c
	}
	return SnapshotFromUsage(u)
}

func TestNDJSONProviderUsageNormalisation(t *testing.T) {
	sugaOpt := ndjsonfilter.Options{Projector: ndjsonfilter.ProjectorGeneric, UsagePath: "usage", UsageSource: "ndjson:suga"}
	t.Run("suga turn_end context, last wins, bad line ignored", func(t *testing.T) {
		s := feed(t, sugaOpt,
			`{"type":"session","session_id":"s1"}`,
			`{"type":"turn_end","context":{"est_tokens":12000,"window":200000,"ratio":0.06}}`,
			`{this is not json`,
			`{"type":"turn_end","context":{"est_tokens":150000,"window":200000,"ratio":0.75}}`,
			`{"type":"result","result":"ok","usage":{"input_tokens":1200,"output_tokens":400,"cost_usd":0.01}}`)
		if !s.RatioKnown || s.Ratio != 0.75 || s.ContextUsed != 150000 || s.ContextLimit != 200000 || s.Source != runner.ContextSourceTurnEnd {
			t.Fatalf("snapshot = %+v", s)
		}
		if s.Usage == nil || s.Usage.InputTokens != 1200 || s.Usage.CostUSD != 0.01 {
			t.Fatalf("usage = %+v", s.Usage)
		}
	})
	t.Run("suga ratio missing: est_tokens / window", func(t *testing.T) {
		s := feed(t, sugaOpt, `{"type":"turn_end","context":{"est_tokens":50000,"window":200000}}`)
		if !s.RatioKnown || s.Ratio != 0.25 {
			t.Fatalf("snapshot = %+v", s)
		}
	})
	t.Run("suga without a context object: unknown, not zero", func(t *testing.T) {
		s := feed(t, sugaOpt, `{"type":"turn_end","turn":1}`, `{"type":"result","result":"ok","usage":{"input_tokens":3}}`)
		if s.RatioKnown || s.ContextLimit != 0 {
			t.Fatalf("snapshot = %+v", s)
		}
		if s.Usage == nil || s.Usage.InputTokens != 3 {
			t.Fatalf("usage = %+v", s.Usage)
		}
	})
	t.Run("claude result modelUsage window", func(t *testing.T) {
		s := feed(t, ndjsonfilter.Options{Projector: ndjsonfilter.ProjectorClaude},
			`{"type":"system","subtype":"init","model":"m1"}`,
			`{"type":"result","subtype":"success","result":"ok","total_cost_usd":0.02,"usage":{"input_tokens":10,"output_tokens":5},`+
				`"modelUsage":{"m1":{"inputTokens":1000,"cacheReadInputTokens":99000,"cacheCreationInputTokens":0,"contextWindow":200000}}}`)
		if !s.RatioKnown || s.ContextLimit != 200000 || s.ContextUsed != 100000 || s.Ratio != 0.5 || s.Source != runner.ContextSourceClaude {
			t.Fatalf("snapshot = %+v", s)
		}
		if s.Usage == nil || s.Usage.CostUSD != 0.02 {
			t.Fatalf("usage = %+v", s.Usage)
		}
	})
	t.Run("claude without a reported window is unknown", func(t *testing.T) {
		s := feed(t, ndjsonfilter.Options{Projector: ndjsonfilter.ProjectorClaude},
			`{"type":"result","subtype":"success","result":"ok","usage":{"input_tokens":10},"modelUsage":{"m1":{"inputTokens":10}}}`)
		if s.RatioKnown {
			t.Fatalf("a window must not be guessed: %+v", s)
		}
	})
	t.Run("omp reports usage but no window: unknown", func(t *testing.T) {
		s := feed(t, ndjsonfilter.Options{Projector: ndjsonfilter.ProjectorOMP},
			`{"type":"session","model":"m"}`,
			`{"type":"message_end","message":{"role":"assistant","usage":{"input":100,"output":20,"cost":{"total":0.001}}}}`,
			`{"type":"turn_end"}`)
		if s.RatioKnown {
			t.Fatalf("omp has no window: %+v", s)
		}
		if s.Usage == nil || s.Usage.InputTokens != 100 {
			t.Fatalf("usage = %+v", s.Usage)
		}
	})
	t.Run("common usage shape with context keys, repeated rows", func(t *testing.T) {
		opt := ndjsonfilter.Options{Projector: ndjsonfilter.ProjectorGeneric, UsagePath: "usage", UsageSource: "ndjson:custom"}
		s := feed(t, opt,
			`{"type":"usage","usage":{"input_tokens":100,"output_tokens":10,"context_used_tokens":1000,"context_limit_tokens":10000}}`,
			`{"type":"usage","usage":{"input_tokens":100,"output_tokens":10,"context_used_tokens":1000,"context_limit_tokens":10000}}`,
			`{"type":"usage","usage":{"input_tokens":900,"output_tokens":40,"context_used_tokens":8000,"context_limit_tokens":10000,"cost_usd":0.5}}`)
		if !s.RatioKnown || s.Ratio != 0.8 || s.Usage == nil || s.Usage.InputTokens != 900 || s.Usage.CostUSD != 0.5 {
			t.Fatalf("snapshot = %+v usage=%+v", s, s.Usage)
		}
	})
}
