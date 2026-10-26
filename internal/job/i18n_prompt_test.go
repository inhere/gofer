package job

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/tracker"
)

// han matches a Han character: a prompt rendered for English must carry none.
var han = regexp.MustCompile(`\p{Han}`)

// TestEnglishPromptHasNoCJK is the end-to-end guard of the i18n migration
// (design 2026-10-27-i18n-design.md §7): with server.language: en, the prompt a
// batch agent really receives is English — no Chinese line survives.
func TestEnglishPromptHasNoCJK(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Server:  config.ServerConfig{Language: "en"},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"cli"}, AllowedRunners: []string{"local"},
				ScopeDiscipline: config.ScopeDisciplineOn, KnowledgeCapture: config.ScopeDisciplineOn},
		},
		Agents: map[string]config.AgentConfig{
			"cli": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"argv", "{{prompt}}"}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "cli", Runner: "local",
		Prompt: "do the thing", Acceptance: "- the thing works", Scope: []string{"internal/**"},
		Cwd: ".", TimeoutSec: 120,
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	out := readJobLog(t, final, store.StdoutFile)
	for _, want := range []string{
		i18n.Title(i18n.EN, i18n.SectionAcceptance),
		i18n.Title(i18n.EN, i18n.SectionContract),
		i18n.Title(i18n.EN, i18n.SectionDeadline),
		"- Scope of this task: internal/**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt lacks %q:\n%s", want, out)
		}
	}
	if m := han.FindString(out); m != "" {
		t.Errorf("English prompt carries CJK (%q):\n%s", m, out)
	}
}

// TestZhPromptIsUnchanged pins the zh-CN side: the migration must not change a
// single character of what an existing deployment's agent reads.
func TestZhPromptIsUnchanged(t *testing.T) {
	t.Parallel()
	cfg := sectionsCfg()
	cfg.Server.Language = "zh-CN"
	req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "do it", Acceptance: "- a", TodoID: "t1", Scope: []string{"a/**"}}
	text, _ := buildPromptSections(cfg, &req, "cli", jobLimit{Sec: 1800})
	wantAcceptance := "## 验收标准\n\n- a\n\n汇报末尾逐条说明是否满足：满足 / 未满足 / 无法验证，并给出依据（测试名、命令输出、文件位置）。"
	if !strings.Contains(text, wantAcceptance) {
		t.Errorf("acceptance section changed:\n%s", text)
	}
	if !strings.Contains(text, "- 只改与本任务直接相关的代码和文档；不要顺手重构、改名或清理范围外的内容。") {
		t.Errorf("contract section changed:\n%s", text)
	}
	if !strings.Contains(text, "- 本任务的改动范围：a/**") {
		t.Errorf("scope line changed:\n%s", text)
	}
	// The memory-flag line is the tracker hint verbatim: one text, two places — a
	// change to either must be a change to both.
	if !strings.Contains(text, tracker.MemoryFlagHint) {
		t.Errorf("contract template no longer carries tracker.MemoryFlagHint:\n%s", text)
	}
}

// TestDeliverBodyFollowsProjectLanguage: the delivery request body speaks the
// language of the job's project — projects.<p>.language over server.language, and
// an accepted alias reads as its canonical language.
func TestDeliverBodyFollowsProjectLanguage(t *testing.T) {
	t.Parallel()
	snap := JobResult{ID: "j1", Title: "T", ProjectKey: "p", Acceptance: "- works",
		Commits: []Commit{{SHA: "0123456789ab", Subject: "fix"}}}
	cfg := &config.Config{
		Server: config.ServerConfig{Language: "en"},
		Projects: map[string]config.ProjectConfig{
			"p":  {Language: "zh"},      // alias of zh-CN
			"en": {Language: "english"}, // alias of en
		},
	}
	snap.ProjectKey = "p"
	if body := (&Service{}).deliveryBody(cfg, snap, requestOfJob(snap)); !strings.Contains(body, "## 验收标准") {
		t.Errorf("zh project body:\n%s", body)
	}
	for _, project := range []string{"en", "other"} { // project alias, then server.language
		snap.ProjectKey = project
		body := (&Service{}).deliveryBody(cfg, snap, requestOfJob(snap))
		if m := han.FindString(body); m != "" {
			t.Errorf("%s: body carries CJK (%q):\n%s", project, m, body)
		}
	}
}

// TestMigratedJobFilesHaveNoCJK is the scan guard of design §7: a string literal
// added to a migrated file must not be written in Chinese — the text belongs in
// internal/i18n (the catalogs, the templates, or the protocol alias table).
//
// Comments are not scanned (Go comments are not part of the text an agent reads),
// and test files are out of scope.
func TestMigratedJobFilesHaveNoCJK(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"prompt_sections.go", "branch_delivery.go"} {
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
				line := fset.Position(lit.Pos()).Line
				t.Errorf("%s:%d: string literal carries CJK (%q) — move the text into internal/i18n: %s",
					file, line, m, lit.Value)
			}
			return true
		})
	}
}

// TestPromptTemplateLanguage: every template renders in the language asked for,
// and an unknown language falls back to the DEPRECATED default rather than to "".
func TestPromptTemplateLanguage(t *testing.T) {
	t.Parallel()
	data := prompts.ContractData{Scope: true}
	if got := prompts.MustRender(i18n.EN, prompts.NameContract, data); !strings.HasPrefix(got, i18n.Title(i18n.EN, i18n.SectionContract)) {
		t.Errorf("en contract = %q", got)
	}
	if got := prompts.MustRender("klingon", prompts.NameContract, data); !strings.HasPrefix(got, i18n.Title(i18n.Default, i18n.SectionContract)) {
		t.Errorf("unknown language contract = %q", got)
	}
}
