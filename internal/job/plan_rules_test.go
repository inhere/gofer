package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

const samplePlan = "# 方案\n\n格式示例：\n```gofer-todos\n- title: quoted example\n```\n\n正式步骤：\n\n" +
	"```gofer-todos\n" +
	"- title: 存储层\n" +
	"  acceptance:\n" +
	"    - 迁移可重复\n" +
	"    - 旧数据可读\n" +
	"  scope: internal/store/**, internal/jobstore/**\n" +
	"- title: 接口层\n" +
	"  scope: [internal/api/**]\n" +
	"  check: go test ./internal/...\n" +
	"- title: 前端\n" +
	"- title: 文档\n" +
	"  after: [1, 接口层]\n" +
	"- title: 独立清理\n" +
	"  after: []\n" +
	"```\n"

func TestParsePlanTodos(t *testing.T) {
	specs, err := ParsePlanTodos(samplePlan)
	assert.NoErr(t, err)
	assert.Len(t, specs, 5) // the LAST block wins, not the quoted example
	assert.Eq(t, "存储层", specs[0].Title)
	assert.Eq(t, "- 迁移可重复\n- 旧数据可读", specs[0].Acceptance)
	assert.Eq(t, []string{"internal/store/**", "internal/jobstore/**"}, specs[0].Scope)
	assert.False(t, specs[0].AfterSet)
	assert.Eq(t, "go test ./internal/...", specs[1].Check)
	assert.Eq(t, []string{"1", "接口层"}, specs[3].After)
	assert.True(t, specs[4].AfterSet)
	assert.Len(t, specs[4].After, 0)

	for name, text := range map[string]string{
		"no block":    "just prose",
		"not a list":  "```gofer-todos\ntitle: x\n```",
		"empty":       "```gofer-todos\n```",
		"no title":    "```gofer-todos\n- acceptance: x\n```",
		"unknown key": "```gofer-todos\n- title: x\n  acceptence: typo\n```",
		"bad check":   "```gofer-todos\n- title: x\n  check: [a, b]\n```",
	} {
		_, err := ParsePlanTodos(text)
		assert.Err(t, err, name)
	}
}

func TestBuildTodoImport(t *testing.T) {
	specs, err := ParsePlanTodos(samplePlan)
	assert.NoErr(t, err)
	items, err := BuildTodoImport(specs, "codex")
	assert.NoErr(t, err)
	// 存储层, 接口层, 检查点：接口层, 前端, 文档, 独立清理
	assert.Len(t, items, 6)
	assert.Eq(t, "codex", items[0].Assignee)
	assert.Len(t, items[0].After, 0) // first step: a root
	assert.Eq(t, []int{0}, items[1].After)
	assert.Eq(t, "exec", items[2].Assignee)
	assert.Eq(t, "go test ./internal/...", items[2].Cmd)
	assert.Eq(t, []int{1}, items[2].After)
	assert.True(t, strings.HasPrefix(items[2].Title, "检查点："))
	// The step after a checkpoint waits for the check, not the step.
	assert.Eq(t, []int{2}, items[3].After)
	// after [1, 接口层]: step 1 itself, and 接口层's checkpoint.
	assert.Eq(t, []int{0, 2}, items[4].After)
	assert.Len(t, items[5].After, 0)
	assert.Eq(t, 5, items[5].Step)

	bad := func(text, assign string) {
		t.Helper()
		specs, err := ParsePlanTodos(text)
		assert.NoErr(t, err)
		_, err = BuildTodoImport(specs, assign)
		assert.Err(t, err, text)
	}
	bad("```gofer-todos\n- title: a\n  after: [2]\n- title: b\n```", "")           // forward reference
	bad("```gofer-todos\n- title: a\n  after: a\n```", "")                         // itself
	bad("```gofer-todos\n- title: a\n- title: b\n  after: [9]\n```", "")           // no such step
	bad("```gofer-todos\n- title: a\n- title: b\n  after: nope\n```", "")          // no such title
	bad("```gofer-todos\n- title: a\n- title: a\n- title: c\n  after: a\n```", "") // ambiguous title
	bad("```gofer-todos\n- title: a\n```", "exec")                                 // steps are agent work
}

func TestPlannerGuidanceCarriesRulesAndFormat(t *testing.T) {
	for _, want := range []string{PlanRules, "```" + PlanTodosFence, "检查点", "待确认问题"} {
		assert.True(t, strings.Contains(PlannerGuidance, want), want)
	}
	// The format's own example must be importable.
	specs, err := ParsePlanTodos(PlanTodosFormat)
	assert.NoErr(t, err)
	items, err := BuildTodoImport(specs, "")
	assert.NoErr(t, err)
	assert.Len(t, items, 3)
	assert.True(t, !strings.Contains(PlannerGuidance, "${") && !strings.Contains(PlannerGuidance, "{{"), "guidance must not look like a template variable")
}

// TestSkillQuotesPlanRules keeps the gofer-usage skill on the single source: its plan
// section quotes PlanRules verbatim.
func TestSkillQuotesPlanRules(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "skills", "gofer-usage", "references", "commands.md"))
	assert.NoErr(t, err)
	assert.True(t, strings.Contains(string(b), "```\n"+PlanRules+"\n```"), "skills/gofer-usage/references/commands.md must quote job.PlanRules verbatim")
}
