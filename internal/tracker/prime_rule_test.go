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
	}, "", false, now)
	assert.Contains(t, out, "提交策略：POLICY\n规则（全文）：\n- a-rule: RULE-A-FULL\n- b-rule: RULE-B-FULL\n")
	assert.Contains(t, out, "其他记忆（索引，按需 `gofer memory show <key>`）：\n- h-live（交接） · LIVE-HANDOFF\n- n1 · NOTE-BODY first sentence.\n")
	assert.NotContains(t, out, "more")
	assert.NotContains(t, out, "h-dead")

	// The byte cap still holds: oversized rules drop to the index.
	var many []Memory
	for i := 0; i < 40; i++ {
		many = append(many, Memory{Key: "r" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Content: strings.Repeat("x", 400), UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindRule, Summary: "s"}})
	}
	out = renderPrimeRule("POLICY", many, "", false, now)
	assert.True(t, len(out) <= PrimeMaxBytes-64, len(out))
	assert.Contains(t, out, "（规则） · s")
}

// TestPrimeRuleScopesMemoriesToCwd (gofer-krzy): the dispatched-job rules select their
// material by the JOB's cwd exactly like the session prime — a rule whose when.paths
// do not cover the cwd is not injected in full, and the index lists only what applies
// to that cwd, matches first.
func TestPrimeRuleScopesMemoriesToCwd(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	memories := []Memory{
		{Key: "java-rule", Content: "JAVA-RULE-FULL", UpdatedAt: "2026-10-01T00:00:00Z",
			MemoryMeta: MemoryMeta{Kind: MemoryKindRule, When: &MemoryWhen{Paths: []string{"java/**"}}}},
		{Key: "go-rule", Content: "GO-RULE-FULL", UpdatedAt: "2026-10-02T00:00:00Z",
			MemoryMeta: MemoryMeta{Kind: MemoryKindRule, When: &MemoryWhen{Paths: []string{"go/**"}}}},
		{Key: "shared-rule", Content: "SHARED-RULE-FULL", UpdatedAt: "2026-10-03T00:00:00Z",
			MemoryMeta: MemoryMeta{Kind: MemoryKindRule}},
		{Key: "java-note", Content: "java note", UpdatedAt: "2026-10-04T00:00:00Z",
			MemoryMeta: MemoryMeta{Summary: "JAVA-NOTE", When: &MemoryWhen{Paths: []string{"java/**"}}}},
		{Key: "go-note", Content: "go note", UpdatedAt: "2026-10-05T00:00:00Z",
			MemoryMeta: MemoryMeta{Summary: "GO-NOTE", When: &MemoryWhen{Paths: []string{"go/**"}}}},
		{Key: "shared-note", Content: "shared note", UpdatedAt: "2026-10-06T00:00:00Z",
			MemoryMeta: MemoryMeta{Summary: "SHARED-NOTE"}},
	}

	out := renderPrimeRule("POLICY", memories, "java/service", true, now)
	assert.Contains(t, out, "- java-rule: JAVA-RULE-FULL")
	assert.Contains(t, out, "- shared-rule: SHARED-RULE-FULL")
	// Another subsystem's memory is neither injected in full nor listed: this job never
	// works there, so even pointing at it is noise (the issue's whole complaint).
	assert.NotContains(t, out, "GO-RULE-FULL")
	assert.NotContains(t, out, "go-rule")
	assert.Contains(t, out, "- java-note · JAVA-NOTE")
	assert.Contains(t, out, "- shared-note · SHARED-NOTE")
	assert.NotContains(t, out, "GO-NOTE")

	// At the repository root only the memories that apply there survive: an unscoped
	// memory, and one that opts into the root with an explicit `**`.
	rootMemories := append([]Memory{},
		Memory{Key: "root-note", Content: "root note", UpdatedAt: "2026-10-07T00:00:00Z", MemoryMeta: MemoryMeta{Summary: "ROOT-NOTE"}},
		Memory{Key: "any-rule", Content: "ANY-RULE-FULL", UpdatedAt: "2026-10-08T00:00:00Z",
			MemoryMeta: MemoryMeta{Kind: MemoryKindRule, When: &MemoryWhen{Paths: []string{"**"}}}})
	rootMemories = append(rootMemories, memories...)
	root := renderPrimeRule("POLICY", rootMemories, "", true, now)
	assert.Contains(t, root, "- shared-rule: SHARED-RULE-FULL")
	assert.Contains(t, root, "- any-rule: ANY-RULE-FULL")
	assert.Contains(t, root, "- root-note · ROOT-NOTE")
	assert.NotContains(t, root, "GO-RULE-FULL")
	assert.NotContains(t, root, "go-rule")
	assert.NotContains(t, root, "JAVA-NOTE")

	// An unknown cwd never hides anything: it is the unscoped (legacy) selection.
	unknown := renderPrimeRule("POLICY", memories, "", false, now)
	assert.Contains(t, unknown, "- java-rule: JAVA-RULE-FULL")
	assert.Contains(t, unknown, "- go-rule: GO-RULE-FULL")
	assert.Contains(t, unknown, "- java-note · JAVA-NOTE")
}
