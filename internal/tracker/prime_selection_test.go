package tracker

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gookit/goutil/x/assert"
)

// TestSplitPrimeKeywords (gofer-11tz): the comma list of the flag / environment variable
// is trimmed, split, deduplicated case-insensitively and capped, and an empty one says
// "no selection" rather than "a selection of blanks".
func TestSplitPrimeKeywords(t *testing.T) {
	assert.Nil(t, SplitPrimeKeywords(""))
	assert.Nil(t, SplitPrimeKeywords("   "))
	assert.Nil(t, SplitPrimeKeywords(" , ,"))
	assert.Eq(t, []string{"a", "b", "C"}, SplitPrimeKeywords(" a , b ,a, C ,"))
	assert.Eq(t, []string{"x", "b"}, SplitPrimeKeywords("x,b,X"))
	assert.Eq(t, []string{"job", "rules"}, SplitPrimeKeywords("job, rules"))

	many := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		many = append(many, fmt.Sprintf("k%d", i))
	}
	got := SplitPrimeKeywords(strings.Join(many, ","))
	assert.Eq(t, PrimeSelectionMaxKeywords, len(got))
	assert.Eq(t, "k0", got[0])
	assert.Eq(t, fmt.Sprintf("k%d", PrimeSelectionMaxKeywords-1), got[PrimeSelectionMaxKeywords-1])
}

// TestPrimeSelectionText: the dispatcher's keywords join the matching text (a memory
// whose when.keywords carries one of them matches even when the prompt never says it),
// and only the HEAD of the prompt is kept — a rendered task book can be tens of KB.
func TestPrimeSelectionText(t *testing.T) {
	sel := NewPrimeSelection([]string{"prime", "PRIME", " ", "11tz"}, "T1 select material", "the job touches the release process")
	assert.Eq(t, []string{"prime", "11tz"}, sel.Keywords)
	assert.Contains(t, sel.Text, "prime")
	assert.Contains(t, sel.Text, "T1 select material")
	assert.Contains(t, sel.Text, "the job touches the release process")
	assert.False(t, sel.empty())

	long := strings.Repeat("x", PrimeSelectionTextBytes) + "TAIL-WORD"
	assert.NotContains(t, NewPrimeSelection(nil, "", long).Text, "TAIL-WORD")

	// A multi-byte prompt is cut on a rune boundary, never inside one.
	wide := NewPrimeSelection(nil, "", strings.Repeat("中", PrimeSelectionTextBytes))
	assert.True(t, utf8.ValidString(wide.Text))
	assert.True(t, len(wide.Text) <= PrimeSelectionTextBytes)

	// Nothing named (and no prompt) is the plain cwd-scoped prime.
	assert.True(t, PrimeSelection{}.empty())
	assert.True(t, KeywordsOnly([]string{" ", ""}).empty())
	assert.False(t, KeywordsOnly([]string{"job"}).empty())
	// A title or a prompt on its own is already a selection.
	assert.False(t, NewPrimeSelection(nil, "T", "").empty())
}

// TestMemoryMatchesSelection: the two directions of a hit — the memory's own
// when.keywords appear in the task text, or the dispatcher's keyword is carried by the
// memory's key / tags / summary / own when.keywords.
func TestMemoryMatchesSelection(t *testing.T) {
	sel := NewPrimeSelection([]string{"prime-selection"}, "T: rules", "the job touches the release process and rux-plot")
	cases := []struct {
		name string
		mem  Memory
		want bool
	}{
		{"own when.keywords in the task text",
			Memory{Key: "a", MemoryMeta: MemoryMeta{When: &MemoryWhen{Keywords: []string{"release"}}}}, true},
		{"keyword in the key", Memory{Key: "prime-selection-notes"}, true},
		{"keyword in a tag", Memory{Key: "b", Tags: []string{"web", "prime-selection"}}, true},
		{"keyword in the displayed summary",
			Memory{Key: "c", Content: "body", MemoryMeta: MemoryMeta{Summary: "about PRIME-SELECTION"}}, true},
		{"own keyword carries the dispatcher's",
			Memory{Key: "d", MemoryMeta: MemoryMeta{When: &MemoryWhen{Keywords: []string{"rux"}}}}, true},
		{"unrelated memory",
			Memory{Key: "e", Content: "body", MemoryMeta: MemoryMeta{Summary: "unrelated"}}, false},
	}
	for _, tc := range cases {
		assert.Eq(t, tc.want, MemoryMatchesSelection(tc.mem, sel), tc.name)
	}

	// No selection: nothing is relevant (the plain cwd-scoped prime, byte for byte).
	assert.False(t, MemoryMatchesSelection(cases[0].mem, PrimeSelection{}))
	// A session opening has no prompt, but its keywords still reach a memory by key.
	assert.True(t, MemoryMatchesSelection(Memory{Key: "prime-selection"}, KeywordsOnly([]string{"prime-selection"})))
}

// TestRenderPrimeRuleTaskSelection (gofer-11tz): the material the task named is injected
// in full and ranked first, while everything else stays exactly as the cwd-scoped prime
// listed it. A hit rule whose when.paths point at ANOTHER subtree stays an index line:
// the path is the author's statement about where the rule applies.
func TestRenderPrimeRuleTaskSelection(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	memories := []Memory{
		{Key: "go-rule", Content: "GO-RULE-FULL", UpdatedAt: "2026-10-01T00:00:00Z",
			MemoryMeta: MemoryMeta{Kind: MemoryKindRule, When: &MemoryWhen{Paths: []string{"go/**"}, Keywords: []string{"prime-11tz"}}}},
		{Key: "kw-rule", Content: "KW-RULE-FULL", UpdatedAt: "2026-10-02T00:00:00Z",
			MemoryMeta: MemoryMeta{Kind: MemoryKindRule, When: &MemoryWhen{Keywords: []string{"prime-11tz"}}}},
		{Key: "task-note", Content: "task note", UpdatedAt: "2026-10-03T00:00:00Z",
			MemoryMeta: MemoryMeta{Summary: "PRIME-11TZ-NOTE"}},
		{Key: "plain-note", Content: "plain note", UpdatedAt: "2026-10-08T00:00:00Z",
			MemoryMeta: MemoryMeta{Summary: "PLAIN-NOTE"}},
	}

	out := renderPrimeRule("POLICY", memories, "java/service", true, NewPrimeSelection([]string{"prime-11tz"}, "", ""), now)
	// A hit rule that only declares trigger keywords applies here: full text.
	assert.Contains(t, out, "- kw-rule: KW-RULE-FULL")
	// A hit rule scoped to go/** does not: the path still wins, but the hit ranks it
	// before every un-hit entry of the index.
	assert.NotContains(t, out, "- go-rule: GO-RULE-FULL")
	assert.Contains(t, out, "- go-rule（规则）（相关）")
	assert.Contains(t, out, "- task-note（相关） · PRIME-11TZ-NOTE")
	assert.True(t, strings.Index(out, "- task-note") < strings.Index(out, "- plain-note"),
		"the hit note is ranked first:\n%s", out)
	// Without a selection the very same memories keep the plain cwd-scoped prime.
	plain := renderPrimeRule("POLICY", memories, "java/service", true, PrimeSelection{}, now)
	assert.NotContains(t, plain, "（相关）")
	assert.Contains(t, plain, "- kw-rule（规则）")
	assert.NotContains(t, plain, "- kw-rule: KW-RULE-FULL")
	// A selection that matches nothing is byte-identical to no selection at all.
	same := renderPrimeRule("POLICY", memories, "java/service", true, NewPrimeSelection([]string{"nothing-matches"}, "", ""), now)
	assert.Eq(t, plain, same)
}

// TestPrimeRuleBudgetKeepsTheRelevantRule (gofer-11tz): the byte budget is spent on the
// task's material first — a hit rule that sorts last by key still gets its full text,
// while the rules of other subsystems are what drops to the index.
func TestPrimeRuleBudgetKeepsTheRelevantRule(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	memories := []Memory{{Key: "zz-task-rule", Content: "ZZ-TASK-RULE-FULL", UpdatedAt: "2026-10-01T00:00:00Z",
		MemoryMeta: MemoryMeta{Kind: MemoryKindRule, When: &MemoryWhen{Keywords: []string{"budget-11tz"}}}}}
	for i := 0; i < 40; i++ {
		memories = append(memories, Memory{Key: fmt.Sprintf("r%02d", i), Content: strings.Repeat("x", 400),
			UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindRule, Summary: "s"}})
	}

	out := renderPrimeRule("POLICY", memories, "", true, NewPrimeSelection([]string{"budget-11tz"}, "", ""), now)
	assert.Contains(t, out, "- zz-task-rule: ZZ-TASK-RULE-FULL")
	// The rules that did not fit dropped to the index instead.
	assert.Contains(t, out, "- r18（规则） · s")

	// Unselected, the same rule is the FIRST to fall out of the full-text section
	// (plain key order, and its when.keywords alone do not make it apply here).
	plain := renderPrimeRule("POLICY", memories, "", true, PrimeSelection{}, now)
	assert.NotContains(t, plain, "- zz-task-rule: ZZ-TASK-RULE-FULL")
}

// TestRenderScopedPrimeSectionSelection: the server-backed sections (global / project
// memories) share the view, so the session's keywords reach them too.
func TestRenderScopedPrimeSectionSelection(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	items := []Memory{
		{Key: "g-web", Content: "web body", UpdatedAt: "2026-10-01T00:00:00Z", MemoryMeta: MemoryMeta{Summary: "WEB-ONE"}},
		{Key: "g-job", Content: "job body", UpdatedAt: "2026-10-02T00:00:00Z",
			MemoryMeta: MemoryMeta{Summary: "JOB-ONE", When: &MemoryWhen{Keywords: []string{"job"}}}},
	}
	opts := ScopedPrimeOptions{Budget: -1, SummaryLimit: -1, Now: now, Selection: KeywordsOnly([]string{"job"})}
	out := RenderScopedPrimeSection("全局记忆", items, opts)
	assert.Contains(t, out, "- [其他] g-job（相关） · JOB-ONE")
	assert.True(t, strings.Index(out, "- [其他] g-job") < strings.Index(out, "- [其他] g-web"), "scoped hit ranked first:\n%s", out)

	opts.Selection = PrimeSelection{}
	assert.NotContains(t, RenderScopedPrimeSection("全局记忆", items, opts), "（相关）")
}
