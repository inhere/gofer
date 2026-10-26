package mcpserver

// The language of the MCP surface (design 2026-10-27-i18n-design.md §4 / §11, P1d):
// the tool descriptions an agent reads at tools/list, and the text it gets back.
//
// Characterization: testdata/descriptions-zh-CN.golden is a dump of tools/list taken
// from the code BEFORE the migration, so the zh-CN side cannot drift while the text
// moves into the catalog. A deliberate wording change updates the golden; an accident
// fails here.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/i18n"
)

// han matches a Han character: English text must carry none (G046).
var han = regexp.MustCompile(`\p{Han}`)

// langBackend is a Backend that reports a language, so a test can build the MCP
// server for the language it wants to look at.
type langBackend struct {
	Backend
	lang i18n.Lang
}

func (b langBackend) Language() i18n.Lang { return b.lang }

// descriptions lists the tools of the operator surface in lang, by name.
func descriptions(t *testing.T, lang i18n.Lang) map[string]string {
	t.Helper()
	jobs, projects, agents := testCore(t)
	session := connectTo(t, newServer(langBackend{Backend: newLocalBackend(jobs, projects, agents), lang: lang}, ""))
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := make(map[string]string, len(res.Tools))
	for _, tl := range res.Tools {
		out[tl.Name] = tl.Description
	}
	return out
}

// TestZhDescriptionsUnchanged pins every operator tool description to the text gofer
// sent before the migration.
func TestZhDescriptionsUnchanged(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "descriptions-zh-CN.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	want := make(map[string]string, 0)
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		name, desc, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("golden line without a tab: %q", line)
		}
		want[name] = desc
	}
	got := descriptions(t, i18n.ZhCN)
	if len(got) != len(want) {
		t.Fatalf("tool count = %d, golden has %d", len(got), len(want))
	}
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if got[name] != want[name] {
			t.Errorf("%s: description changed\n--- got ---\n%s\n--- want ---\n%s", name, got[name], want[name])
		}
	}
}

// TestEnglishDescriptionsHaveNoCJK: an English deployment's agent must read English —
// no Chinese line survives in tools/list.
func TestEnglishDescriptionsHaveNoCJK(t *testing.T) {
	t.Parallel()
	for name, desc := range descriptions(t, i18n.EN) {
		if m := han.FindString(desc); m != "" {
			t.Errorf("%s: English description carries CJK (%q): %s", name, m, desc)
		}
	}
}

// TestMcpTextFollowsLanguage pins both languages of the catalogued text: the zh-CN
// values are what gofer wrote before the migration, the English ones say the same
// thing in English. The expected strings are literals on purpose (a translation
// compared against the template it came from proves nothing).
func TestMcpTextFollowsLanguage(t *testing.T) {
	t.Parallel()
	jobs, projects, agents := testCore(t)
	base := newLocalBackend(jobs, projects, agents)
	zh := langBackend{Backend: base, lang: i18n.ZhCN}
	en := langBackend{Backend: base, lang: i18n.EN}

	cases := []struct {
		key     string
		wantZh  string
		wantRef string
	}{
		{"mcp.term.today", "「今天」", "Today"},
		{"mcp.term.as_advised", "「按建议：X」", "Do as advised: X"},
		{"mcp.term.adopt", "「采纳」", "Adopt"},
		{"mcp.term.dismiss", "「忽略」", "Dismiss"},
		{"mcp.term.knowledge_section", "「## 可复用经验」", "## Reusable lessons"},
		{"mcp.term.needs_review", "「⚠ 待复核（reason）」", "needs review (reason)"},
		{"mcp.term.leave_to_human", "(高危/拿不准, 留给人处理)", "high risk or unsure, leave it to a person"},
		{"mcp.term.relay", "带话", "Relay"},
		{"mcp.term.role_mention", "@<agent 或 role>", "@<agent or role>"},
		{"mcp.term.digest_sections", "改动分组 / 风险 / 测试", "change groups / risks / tests"},
		{"mcp.error.leader_no_reject",
			"a leader job cannot reject a delivery: 验收 is a human's call",
			"a leader job cannot reject a delivery: accepting is a human's call"},
		{"mcp.error.project_scope_denied",
			`project-scoped MCP(--project {scope}): 不能向 project {project} 提交`,
			"project-scoped MCP(--project {scope}): cannot submit to project {project}"},
	}
	for _, c := range cases {
		if got := text(zh, c.key); got != c.wantZh {
			t.Errorf("%s (zh-CN) = %q, want %q", c.key, got, c.wantZh)
		}
		got := text(en, c.key)
		if !strings.Contains(got, c.wantRef) {
			t.Errorf("%s (en) = %q, want it to say %q", c.key, got, c.wantRef)
		}
		if m := han.FindString(got); m != "" {
			t.Errorf("%s (en) carries CJK (%q): %q", c.key, m, got)
		}
	}
}

// TestProjectScopeRefusalIsLocalized: the refusal a scoped MCP hands back is the one
// the agent reads, so it follows the language too — and the zh-CN wording (quotes
// included) is the pre-migration one.
func TestProjectScopeRefusalIsLocalized(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lang i18n.Lang
		want string
	}{
		{i18n.ZhCN, `project-scoped MCP(--project self): 不能向 project "other" 提交`},
		{i18n.EN, `project-scoped MCP(--project self): cannot submit to project "other"`},
	} {
		jobs, projects, agents := testCore(t)
		b := langBackend{Backend: newLocalBackend(jobs, projects, agents), lang: tc.lang}
		session := connectTo(t, newServer(b, "self"))
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "gofer_run_job",
			Arguments: map[string]any{"project_key": "other", "prompt": "x", "agent": "exec", "runner": "local"},
		})
		msg := ""
		switch {
		case err != nil:
			msg = err.Error()
		case res.IsError:
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					msg += tc.Text
				}
			}
		default:
			t.Fatalf("%s: a mismatching project was accepted", tc.lang)
		}
		if !strings.Contains(msg, tc.want) {
			t.Errorf("%s: error = %q, want it to carry %q", tc.lang, msg, tc.want)
		}
	}
}

// TestMcpFilesHaveNoCJK is the scan guard of design §7: a string literal in this
// package's non-test code must not be written in Chinese — the text belongs in
// internal/i18n (the catalog) or is English like the rest of the descriptions.
//
// Comments are not scanned, and test files are out of scope.
func TestMcpFilesHaveNoCJK(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
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
