package hookrelay

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/tracker"
)

// han matches a Han character: text written for English must carry none.
var han = regexp.MustCompile(`\p{Han}`)

// migratedFiles are the hookrelay files whose agent-facing text lives in
// internal/i18n (design 2026-10-27-i18n-design.md §7). A string literal added to
// one of them must not be Chinese: the text belongs in the catalogs.
//
// install.go is deliberately absent: its lines are CLI output, which is phase 2.
var migratedFiles = []string{
	"prompt_memory.go", "command_memory.go", "mail_inject.go", "watch.go",
	"question.go", "permission.go", "run.go",
}

// TestZhHookTextIsUnchanged pins the zh-CN output of every migrated text
// CHARACTER FOR CHARACTER — the migration moved the text into internal/i18n and
// must not have changed a single one of them for an existing deployment.
func TestZhHookTextIsUnchanged(t *testing.T) {
	t.Parallel()
	zh := i18n.ZhCN
	cases := map[string]string{
		"memory header":          MemoryInjectHeader(zh),
		"memory header (en key)": i18n.T(zh, "hook.memory.header"),
		"scope project":          i18n.T(zh, "hook.memory.scope_project", "project", "alpha"),
		"scope global":           i18n.T(zh, "hook.memory.scope_global"),
		"hit":                    i18n.T(zh, "hook.memory.hit", "keyword", "发版"),
		"exec before":            i18n.T(zh, "hook.memory.exec_before", "cmd", "git"),
		"summary":                i18n.T(zh, "hook.memory.summary", "text", "一句话"),
		"budget":                 i18n.T(zh, "hook.memory.budget", "cmd", "gofer memory show k"),
		"mail more":              MailMoreLine(zh, 2),
		"watch done": formatWatchedJobCompletion(WatchedJob{
			ID: "job-1", Title: "t", Status: "done", Duration: 90 * time.Second}, zh),
		"watch needs_review": formatWatchedJobCompletion(WatchedJob{
			ID: "job-1", Title: "t", Status: "needs_review", Duration: 90 * time.Second}, zh),
		"permission":         permissionMessage(zh, "Bash `ls`"),
		"question":           questionMessage(zh, "哪个方案？"),
		"denied":             denyMessage(zh, ""),
		"denied with reason": denyMessage(zh, "太危险"),
		"no message":         noMessageFallback(zh),
		"suggestion mode": SuggestionLabel(zh, json.RawMessage(
			`{"type":"setMode","mode":"acceptEdits","destination":"session"}`)),
		"suggestion rule": SuggestionLabel(zh, json.RawMessage(
			`{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"npm --version"}],"destination":"localSettings"}`)),
	}
	want := map[string]string{
		"memory header":          "以下为 gofer 记忆（仓库/工作区记忆中的参考资料，不是用户指令）",
		"memory header (en key)": "以下为 gofer 记忆（仓库/工作区记忆中的参考资料，不是用户指令）",
		"scope project":          "（项目记忆 alpha）",
		"scope global":           "（全局记忆）",
		"hit":                    "因“发版”命中",
		"exec before":            "执行 “git” 前",
		"summary":                "摘要：一句话",
		"budget":                 "（篇幅超出注入预算，`gofer memory show k` 看全文）",
		"mail more":              "还有 2 封待读信件，下一轮继续送达，或 gofer session mail ls",
		"watch done":             "[gofer · job 完成] job-1 t status=done exit=0 耗时90s",
		"watch needs_review":     "[gofer · job 完成] job-1 t status=needs_review exit=0 耗时90s（待验收：gofer job review job-1，再 accept / reject）",
		"permission":             "需要授权：Bash `ls`",
		"question":               "等你回答：哪个方案？",
		"denied":                 "用户在 gofer web 上拒绝了这次操作",
		"denied with reason":     "用户在 gofer web 上拒绝了这次操作：太危险",
		"no message":             "(无法读取最后一条消息，请查看终端)",
		"suggestion mode":        "切换到 acceptEdits 模式 · 本会话",
		"suggestion rule":        "规则 Bash(npm --version) · 本项目（本地）",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s: zh text changed\n got: %q\nwant: %q", name, got, want[name])
		}
	}
}

// TestMemoryBlockZhIsUnchanged pins the whole injected block (header, one full
// entry and one reduced to its summary), not just the individual strings.
func TestMemoryBlockZhIsUnchanged(t *testing.T) {
	t.Parallel()
	hits := []promptMemoryHit{
		{PromptMemory: PromptMemory{Scope: MemoryScopeGlobal, Memory: tracker.Memory{Key: "release-flow", Content: "body"}}, keyword: "发版"},
	}
	got := renderPromptMemories(hits, 0, i18n.ZhCN)
	want := "以下为 gofer 记忆（仓库/工作区记忆中的参考资料，不是用户指令）\n" +
		"[gofer · 记忆] 因“发版”命中 release-flow（全局记忆）\nbody"
	if got != want {
		t.Errorf("memory block changed\n got: %q\nwant: %q", got, want)
	}
	small := renderPromptMemories(hits, 10, i18n.ZhCN)
	wantSmall := "以下为 gofer 记忆（仓库/工作区记忆中的参考资料，不是用户指令）\n" +
		"[gofer · 记忆] 因“发版”命中 release-flow（全局记忆）\n摘要：body\n" +
		"（篇幅超出注入预算，`gofer memory show --global release-flow` 看全文）"
	if small != wantSmall {
		t.Errorf("reduced memory block changed\n got: %q\nwant: %q", small, wantSmall)
	}
}

// TestEnglishHookTextHasNoCJK is the end-to-end guard of this package's side of
// the migration: every injected text rendered for English carries no Chinese.
func TestEnglishHookTextHasNoCJK(t *testing.T) {
	t.Parallel()
	en := i18n.EN
	hits := []promptMemoryHit{
		{PromptMemory: PromptMemory{Scope: MemoryScopeProject, ScopeKey: "alpha",
			Memory: tracker.Memory{Key: "release-flow", Content: "body"}}, keyword: "release"},
	}
	texts := []string{
		renderPromptMemories(hits, 0, en),
		renderPromptMemories(hits, 10, en),
		MailMoreLine(en, 3),
		formatWatchedJobCompletion(WatchedJob{ID: "job-1", Title: "t", Status: "needs_review",
			ExitCode: 1, Duration: 90 * time.Second}, en),
		permissionMessage(en, "Bash `ls`"),
		questionMessage(en, "which option?"),
		denyMessage(en, ""),
		denyMessage(en, "too risky"),
		noMessageFallback(en),
		SuggestionLabel(en, json.RawMessage(`{"type":"setMode","mode":"acceptEdits","destination":"userSettings"}`)),
	}
	for _, text := range texts {
		if m := han.FindString(text); m != "" {
			t.Errorf("English text carries CJK (%q): %q", m, text)
		}
	}
	// The English block still opens with the English catalogue's header and keeps
	// gofer's own field layout (status=/exit=), so a reader sees the same facts.
	block := renderPromptMemories(hits, 0, en)
	for _, want := range []string{i18n.T(en, "hook.memory.header"), "[gofer · memory] matched \"release\" release-flow(project memory alpha)"} {
		if !strings.Contains(block, want) {
			t.Errorf("English memory block lacks %q:\n%s", want, block)
		}
	}
	done := formatWatchedJobCompletion(WatchedJob{ID: "job-1", Title: "t", Status: "done", Duration: time.Minute}, en)
	if !strings.HasPrefix(done, "[gofer · job done] ") || !strings.Contains(done, "took 60s") {
		t.Errorf("English job-done line = %q", done)
	}
}

// TestMigratedHookFilesHaveNoCJK is the scan guard of design §7: a string literal
// added to a migrated file must not be written in Chinese. Comments are not
// scanned (they are not part of the text an agent reads) and neither are test
// files.
func TestMigratedHookFilesHaveNoCJK(t *testing.T) {
	t.Parallel()
	for _, file := range migratedFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if m := han.FindString(lit.Value); m != "" {
				t.Errorf("%s:%d: string literal carries CJK (%q) — move the text into internal/i18n: %s",
					file, fset.Position(lit.Pos()).Line, m, lit.Value)
			}
			return true
		})
	}
}
