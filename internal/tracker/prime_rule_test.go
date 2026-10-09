package tracker

import (
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestPrimeRuleRulesAndIndex(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	out := renderPrimeRule("POLICY", []Memory{
		{Key: "b-rule", Content: "RULE-B-FULL", UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindRule}},
		{Key: "a-rule", Content: "RULE-A-FULL", Tags: []string{"prime"}, UpdatedAt: "2026-10-01T00:00:00Z"},
		{Key: "n1", Content: "NOTE-BODY first sentence. more", UpdatedAt: "2026-10-05T00:00:00Z"},
		{Key: "h-live", Content: "LIVE-HANDOFF", UpdatedAt: "2026-10-08T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff}},
		{Key: "h-dead", Content: "DEAD-HANDOFF", UpdatedAt: "2026-09-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff}},
	}, now)
	assert.Contains(t, out, "提交策略：POLICY\n规则（全文）：\n- a-rule: RULE-A-FULL\n- b-rule: RULE-B-FULL\n")
	assert.Contains(t, out, "其他记忆（索引，按需 `gofer memory show <key>`）：\n- h-live（交接） · LIVE-HANDOFF\n- n1 · NOTE-BODY first sentence.\n")
	assert.NotContains(t, out, "more")
	assert.NotContains(t, out, "h-dead")

	// The byte cap still holds: oversized rules drop to the index.
	var many []Memory
	for i := 0; i < 40; i++ {
		many = append(many, Memory{Key: "r" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Content: strings.Repeat("x", 400), UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindRule, Summary: "s"}})
	}
	out = renderPrimeRule("POLICY", many, now)
	assert.True(t, len(out) <= PrimeMaxBytes-64, len(out))
	assert.Contains(t, out, "（规则） · s")
}
