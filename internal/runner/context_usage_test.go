package runner

import "testing"

func TestContextFromLine(t *testing.T) {
	cases := []struct {
		name string
		line map[string]any
		want *ContextUsage
	}{
		{"suga turn_end", map[string]any{"type": "turn_end", "context": map[string]any{"est_tokens": 12000.0, "window": 200000.0, "ratio": 0.06}},
			&ContextUsage{UsedTokens: 12000, LimitTokens: 200000, Ratio: 0.06, Estimated: true, Source: ContextSourceTurnEnd}},
		{"context keys on the row", map[string]any{"type": "usage", "context_used_tokens": 5.0, "context_limit_tokens": 10.0},
			&ContextUsage{UsedTokens: 5, LimitTokens: 10, Source: ContextSourceUsage}},
		{"context keys inside usage", map[string]any{"usage": map[string]any{"contextUsedTokens": "50", "contextLimitTokens": 100.0}},
			&ContextUsage{UsedTokens: 50, LimitTokens: 100, Source: ContextSourceUsage}},
		{"claude result picks the largest window", map[string]any{"type": "result", "modelUsage": map[string]any{
			"small": map[string]any{"inputTokens": 10.0, "contextWindow": 1000.0},
			"big":   map[string]any{"inputTokens": 100.0, "cacheReadInputTokens": 400.0, "cacheCreationInputTokens": 100.0, "contextWindow": 200000.0},
		}}, &ContextUsage{UsedTokens: 600, LimitTokens: 200000, Estimated: true, Source: ContextSourceClaude}},
		{"claude without a window", map[string]any{"type": "result", "modelUsage": map[string]any{"m": map[string]any{"inputTokens": 10.0}}}, nil},
		{"empty context object", map[string]any{"type": "turn_end", "context": map[string]any{}}, nil},
		{"billing usage only", map[string]any{"usage": map[string]any{"input_tokens": 10.0}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ContextFromLine(tc.line)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("ContextFromLine = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestContextFraction(t *testing.T) {
	var nilCtx *ContextUsage
	if _, ok := nilCtx.Fraction(); ok {
		t.Fatal("nil context must be unknown")
	}
	if f, ok := (&ContextUsage{Ratio: 0.5, UsedTokens: 1, LimitTokens: 100}).Fraction(); !ok || f != 0.5 {
		t.Fatalf("the agent's own ratio wins: %v %v", f, ok)
	}
	if f, ok := (&ContextUsage{UsedTokens: 25, LimitTokens: 100}).Fraction(); !ok || f != 0.25 {
		t.Fatalf("computed = %v %v", f, ok)
	}
	for _, c := range []*ContextUsage{{UsedTokens: 25}, {LimitTokens: 100}, {}} {
		if _, ok := c.Fraction(); ok {
			t.Fatalf("%+v is not a known level", c)
		}
	}
}

func TestContextFromACPUpdate(t *testing.T) {
	if c := ContextFromACPUpdate(map[string]any{"used": 1000.0, "size": 10000.0}); c == nil || c.UsedTokens != 1000 || c.LimitTokens != 10000 || c.Source != ContextSourceACP {
		t.Fatalf("context = %+v", c)
	}
	if c := ContextFromACPUpdate(map[string]any{"used": 1000.0}); c != nil {
		t.Fatalf("used without a window is not a context level: %+v", c)
	}
}

func TestUsageAddKeepsTheLatestContext(t *testing.T) {
	u := &Usage{InputTokens: 1, Context: &ContextUsage{Ratio: 0.1}}
	u.Add(&Usage{InputTokens: 2, Context: &ContextUsage{Ratio: 0.4}})
	if u.InputTokens != 3 || u.Context.Ratio != 0.4 {
		t.Fatalf("usage = %+v ctx=%+v", u, u.Context)
	}
	u.Add(&Usage{InputTokens: 1})
	if u.Context == nil || u.Context.Ratio != 0.4 {
		t.Fatal("a later usage without a context must not erase it")
	}
}
