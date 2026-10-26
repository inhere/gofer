package job

// The continuation prompts of P1d (design 2026-10-27-i18n-design.md §4 / §11): a
// characterization test that PINS the zh-CN text gofer injected before the migration,
// so the migration cannot change a character of what an existing deployment's agent
// reads. The expected strings below are the pre-migration text VERBATIM, copied from
// the code as it was (the templates are not consulted), and the same test names the
// English original that the migration must keep word for word.

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
)

// TestZhContinuationPromptsUnchanged pins the zh-CN continuation texts.
func TestZhContinuationPromptsUnchanged(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Server: config.ServerConfig{Language: "zh-CN"},
		Projects: map[string]config.ProjectConfig{"self": {}}}
	src := JobResult{ProjectKey: "self"}

	wantAuto := "上一次运行被瞬时的错误中断（stream ended after retries）。" +
		"先用 git status / git log 看清做到哪里了，只补完剩下的工作，不要重做已提交的部分，然后按原来的要求汇报。"
	if got := autoResumePrompt(cfg, src, " stream ended after retries "); got != wantAuto {
		t.Errorf("auto-resume prompt changed:\n got: %s\nwant: %s", got, wantAuto)
	}

	wantUncommitted := "上一轮结束时有 2 个文件改动未提交：a.go、b.go。" +
		"请按功能点检查并本地提交（conventional commit），不要 push，不要提交无关文件；不应提交的请说明原因。"
	if got := uncommittedResumePrompt(i18n.ZhCN, []string{"a.go", "b.go"}); got != wantUncommitted {
		t.Errorf("uncommitted resume prompt changed:\n got: %s\nwant: %s", got, wantUncommitted)
	}

	wantBridge := "本任务接着上一项（wire the thing）在同一个会话里继续。上一项已完成；下面是新的任务说明，以它为准。"
	if got := todoContinuePrompt(i18n.ZhCN, "wire the thing"); got != wantBridge {
		t.Errorf("todo-continue bridge changed:\n got: %s\nwant: %s", got, wantBridge)
	}
}

// TestAutoResumePromptKeepsTheEnglishOriginal: the automatic-continuation prompt was
// English before the migration, and its English text must stay word for word (the
// zh-CN wording is the translation, not the other way round).
func TestAutoResumePromptKeepsTheEnglishOriginal(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Server: config.ServerConfig{Language: "en"},
		Projects: map[string]config.ProjectConfig{"self": {}}}
	want := "The previous run was interrupted by a transient error (stream ended after retries). " +
		"Check git status / git log to see how far you got, finish only the remaining work, " +
		"do not redo committed work, then report as originally asked."
	if got := autoResumePrompt(cfg, JobResult{ProjectKey: "self"}, "stream ended after retries"); got != want {
		t.Errorf("English auto-resume prompt changed:\n got: %s\nwant: %s", got, want)
	}
}

// TestEnglishContinuationPromptsHaveNoCJK: an English deployment's agent must not be
// handed a Chinese line (G046) — the three texts are separate templates and each is
// written in its own language.
func TestEnglishContinuationPromptsHaveNoCJK(t *testing.T) {
	t.Parallel()
	texts := map[string]string{
		"auto-resume":        autoResumePrompt(&config.Config{Server: config.ServerConfig{Language: "en"}}, JobResult{}, "boom"),
		"uncommitted-resume": uncommittedResumePrompt(i18n.EN, []string{"a.go", "b.go"}),
		"todo-continue":      todoContinuePrompt(i18n.EN, "wire the thing"),
	}
	for name, text := range texts {
		if m := han.FindString(text); m != "" {
			t.Errorf("%s (en) carries CJK (%q): %s", name, m, text)
		}
	}
	// Each names what it is about: the error, the files, the item continued.
	if !strings.Contains(texts["auto-resume"], "boom") ||
		!strings.Contains(texts["uncommitted-resume"], "a.go, b.go") ||
		!strings.Contains(texts["todo-continue"], "wire the thing") {
		t.Errorf("an English continuation prompt lost its data: %v", texts)
	}
}

// TestContinuationPromptLanguage: the language is the job's own — fixed at submit
// (JobRequest.Language) and else the project's over the server's, exactly as
// promptLanguage decides it for the rest of a job's text.
func TestContinuationPromptLanguage(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{Language: "en"},
		Projects: map[string]config.ProjectConfig{
			"p":  {Language: "zh"},      // alias of zh-CN
			"en": {Language: "english"}, // alias of en
		},
	}
	// The project decides.
	if got := autoResumePrompt(cfg, JobResult{ProjectKey: "p"}, "boom"); han.MatchString(got) == false {
		t.Errorf("zh project prompt is not Chinese: %s", got)
	}
	// The language fixed when the source job was admitted wins over the config.
	fixed := JobResult{ProjectKey: "p", RequestJSON: `{"language":"en"}`}
	if got := autoResumePrompt(cfg, fixed, "boom"); han.MatchString(got) {
		t.Errorf("a prompt whose job was admitted in English came out Chinese: %s", got)
	}
	// An unset project falls through to the server.
	if got := autoResumePrompt(cfg, JobResult{ProjectKey: "other"}, "boom"); han.MatchString(got) {
		t.Errorf("server language not applied: %s", got)
	}
}
