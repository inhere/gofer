package brief

import (
	"strings"
	"testing"
)

func fullRefocus() Refocus {
	return Refocus{
		Topic: "会话压缩后重注入",
		Claims: []RefocusClaim{
			{ID: "gofer-o53s", Title: "会话压缩后重注入当前任务", Status: "in_progress"},
			{ID: "gofer-k23r", Title: "终端投递不覆盖输入", Status: "in_progress", Inferred: true},
		},
		Plans: []RefocusPlan{{PlanID: "p-1", Title: "会话收尾", Status: "open", Progress: 2, Total: 5, Next: "写测试"}},
		Jobs:  []RefocusJob{{JobID: "job-9", Title: "全量测试", Status: "running"}},
		Mail:  []RefocusMail{{ID: 12, From: "alice", Subject: "先看这个"}},
	}
}

func TestRenderRefocusAllSections(t *testing.T) {
	out := RenderRefocus(fullRefocus(), false, 4000)
	for _, want := range []string{
		"[gofer · 提示] 上下文刚被压缩",
		"主题：会话压缩后重注入",
		"- gofer-o53s 会话压缩后重注入当前任务（in_progress） → gofer issue brief gofer-o53s",
		"- [可能] gofer-k23r 终端投递不覆盖输入（in_progress） → gofer issue brief gofer-k23r",
		"- p-1 会话收尾（2/5，下一项 写测试） → gofer plan brief p-1",
		"- job-9 全量测试（running） → gofer job show job-9",
		"- 来自 alice：先看这个 → gofer mail show 12",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("section missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRefocusResumeWordingAndBlocked(t *testing.T) {
	r := Refocus{Plans: []RefocusPlan{{PlanID: "p-2", Status: "blocked", Progress: 1, Total: 3, Blocked: "t-9"}}}
	out := RenderRefocus(r, true, 4000)
	if !strings.Contains(out, "会话刚恢复") {
		t.Fatalf("resume wording missing:\n%s", out)
	}
	if !strings.Contains(out, "阻塞于 t-9") {
		t.Fatalf("blocker missing:\n%s", out)
	}
}

func TestRenderRefocusEmptyAndNoBudget(t *testing.T) {
	if out := RenderRefocus(Refocus{}, false, 4000); out != "" {
		t.Fatalf("a session with nothing in flight must render nothing, got:\n%s", out)
	}
	if out := RenderRefocus(fullRefocus(), false, 0); out != "" {
		t.Fatalf("no budget must render nothing, got:\n%s", out)
	}
}

func TestRenderRefocusDropsWholeHeaderWhenNothingFits(t *testing.T) {
	// A header that does not fit the remaining budget must not leave a dangling line.
	out := RenderRefocus(fullRefocus(), false, 40)
	if out != "" {
		t.Fatalf("section should be dropped, got %q", out)
	}
}

func TestRenderRefocusTruncatesAtBudgetBoundary(t *testing.T) {
	full := RenderRefocus(fullRefocus(), false, 4000)
	// A budget between the topic and the whole section keeps the head and drops the
	// tail instead of returning nothing.
	cut := RenderRefocus(fullRefocus(), false, len(full)-40)
	if cut == "" || len(cut) > len(full) {
		t.Fatalf("unexpected cut section: %q", cut)
	}
	if !strings.Contains(cut, "[gofer · 提示]") {
		t.Fatalf("cut section lost its head: %q", cut)
	}
}
