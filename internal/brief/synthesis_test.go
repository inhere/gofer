package brief

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
)

func TestRenderSynthesisInHasDayIDSummaryAndShowCommand(t *testing.T) {
	r := SynthesisRef{Day: "2026-10-30", ID: "ms-0001", Summary: "shipped the scheduler"}
	en := RenderSynthesisIn(i18n.EN, r, 4000)
	for _, want := range []string{"## Project activity", "2026-10-30", "ms-0001", "shipped the scheduler", "not a fact", "gofer memory synthesis show ms-0001"} {
		if !strings.Contains(en, want) {
			t.Errorf("en missing %q:\n%s", want, en)
		}
	}
	if !strings.HasPrefix(en, "\n") || !strings.HasSuffix(en, "\n") {
		t.Errorf("section must be newline-wrapped like the other prime sections: %q", en)
	}
	zh := RenderSynthesisIn(i18n.ZhCN, r, 4000)
	for _, want := range []string{"## 项目动态", "2026-10-30", "ms-0001", "不是事实", "gofer memory synthesis show ms-0001"} {
		if !strings.Contains(zh, want) {
			t.Errorf("zh-CN missing %q:\n%s", want, zh)
		}
	}
	if strings.ContainsAny(en, "项目动态") {
		t.Errorf("en block carries CJK:\n%s", en)
	}
}

func TestRenderSynthesisInEmptyInputsGiveNothing(t *testing.T) {
	for _, r := range []SynthesisRef{
		{Day: "2026-10-30", ID: "ms-1", Summary: "  \n "},
		{Day: "2026-10-30", Summary: "x y z"},
		{ID: "ms-1", Summary: "x y z"},
	} {
		if out := RenderSynthesisIn(i18n.EN, r, 4000); out != "" {
			t.Errorf("%+v rendered %q", r, out)
		}
	}
	if out := RenderSynthesisIn(i18n.EN, SynthesisRef{Day: "d", ID: "i", Summary: "s s s"}, 0); out != "" {
		t.Errorf("no budget must give nothing: %q", out)
	}
}

// A long summary is cut (rune-safe, with an ellipsis) so the whole block stays within the
// cap; the summary is one line even when the model wrote newlines.
func TestRenderSynthesisInTruncatesToTheCap(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhCN} {
		long := strings.Repeat("项目进展很多\n", 300)
		out := RenderSynthesisIn(lang, SynthesisRef{Day: "2026-10-30", ID: "ms-0001", Summary: long}, 100000)
		if out == "" || len(out) > SynthesisPrimeMaxBytes {
			t.Fatalf("%s: len=%d (cap %d)\n%s", lang, len(out), SynthesisPrimeMaxBytes, out)
		}
		if !utf8.ValidString(out) || !strings.Contains(out, "…") {
			t.Errorf("%s: not rune-safe or no ellipsis: %q", lang, out)
		}
		if lines := strings.Count(strings.TrimSpace(out), "\n"); lines != 2 {
			t.Errorf("%s: want heading + one summary line + hint, got %d breaks:\n%s", lang, lines, out)
		}
	}
}

// The remaining prime budget wins over the cap; a budget too small for a useful summary
// drops the whole block instead of cutting it into nothing.
func TestRenderSynthesisInRespectsRemainingBudget(t *testing.T) {
	r := SynthesisRef{Day: "2026-10-30", ID: "ms-0001", Summary: strings.Repeat("progress ", 100)}
	full := RenderSynthesisIn(i18n.EN, SynthesisRef{Day: r.Day, ID: r.ID, Summary: "x"}, 100000)
	if out := RenderSynthesisIn(i18n.EN, r, len(full)+60); out == "" || len(out) > len(full)+60 {
		t.Fatalf("budget %d: len=%d", len(full)+60, len(out))
	}
	if out := RenderSynthesisIn(i18n.EN, r, 200); out != "" {
		t.Fatalf("a 200-byte budget cannot hold the block, got %q", out)
	}
}

func TestSynthesisFresh(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	for day, want := range map[string]bool{
		"2026-11-01": true, "2026-10-30": true,
		"2026-10-29": false, // four dates back: older than SynthesisPrimeMaxAgeDays
		"2026-10-28": false, "": false, "garbage": false,
	} {
		if got := SynthesisFresh(day, now); got != want {
			t.Errorf("SynthesisFresh(%q) = %v, want %v", day, got, want)
		}
	}
}
