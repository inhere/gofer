package work

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// han matches a Han character: text written for English must carry none.
var han = regexp.MustCompile(`\p{Han}`)

// migratedFiles are the work files whose agent-facing text lives in internal/i18n
// (design 2026-10-27-i18n-design.md §7). A string literal added to one of them must
// not be Chinese.
//
// The list is empty for now: requests.go — the one file this step migrated — still
// carries the request ledger's journal lines and failure reasons ("会话记录已不存在"
// and friends), which are service text the web shows (phase 3 of the design). Add the
// file here once those move too.
var migratedFiles []string

// TestZhWorkRequestIsUnchanged pins the zh-CN request text CHARACTER FOR CHARACTER
// against what the source wrote before the migration.
func TestZhWorkRequestIsUnchanged(t *testing.T) {
	t.Parallel()
	zh := i18n.ZhCN
	cases := map[string]string{
		"handoff":        requestText(zh, jobstore.WorkRequestHandoff, "wrq-1", "wr-1", "标题", jobstore.WorkParked),
		"report":         requestText(zh, jobstore.WorkRequestReport, "wrq-2", "wr-1", "标题", jobstore.WorkActive),
		"status":         statusLabelIn(zh, jobstore.WorkNeedsOnsite),
		"unknown status": statusLabelIn(zh, "custom"),
	}
	want := map[string]string{
		"handoff": "[gofer · 工作项交接] 工作项 wr-1「标题」被标为已搁置（请求 wrq-1），请写一段交接：做到哪了、卡在哪、回来后第一步。运行 " +
			"`gofer work report wr-1 --request wrq-1 --summary \"<做到哪了>\" --blocker \"<卡在哪>\" --next \"<回来第一步>\"`" +
			"（不需要的字段可省略）。",
		"report": "[gofer · 工作项汇报] 工作项 wr-1「标题」当前进展（请求 wrq-2）：运行 " +
			"`gofer work report wr-1 --request wrq-2 --goal \"<目标>\" --status <active|needs_me|waiting_resource|needs_onsite|review|parked> " +
			"--blocker \"<卡在哪>\" --next \"<下一步>\" --summary \"<做到哪了>\"`（不需要的字段可省略；若已不再受阻请用 --status active）。",
		"status":         "需现场",
		"unknown status": "custom",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s: zh text changed\n got: %q\nwant: %q", name, got, want[name])
		}
	}
}

// TestEnglishWorkRequestHasNoCJK: an item whose project speaks English asks for a
// hand-over / report in English, under the English prefix.
func TestEnglishWorkRequestHasNoCJK(t *testing.T) {
	t.Parallel()
	en := i18n.EN
	for _, kind := range []string{jobstore.WorkRequestHandoff, jobstore.WorkRequestReport} {
		text := requestText(en, kind, "wrq-1", "wr-1", "my item", jobstore.WorkNeedsOnsite)
		if m := han.FindString(text); m != "" {
			t.Errorf("%s: English text carries CJK (%q): %q", kind, m, text)
		}
		if !msgprefix.IsSystem(text) {
			t.Errorf("%s: %q does not read as a gofer system message", kind, text)
		}
	}
	handoff := requestText(en, jobstore.WorkRequestHandoff, "wrq-1", "wr-1", "my item", jobstore.WorkNeedsOnsite)
	for _, want := range []string{"[gofer · work handoff] ", "Work item wr-1", "\"my item\"", "needs me on site", "gofer work report wr-1 --request wrq-1"} {
		if !contains(handoff, want) {
			t.Errorf("English hand-over request lacks %q:\n%s", want, handoff)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestMigratedWorkFilesHaveNoCJK is the scan guard of design §7.
func TestMigratedWorkFilesHaveNoCJK(t *testing.T) {
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
