package workflow

import (
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/store"
)

var templateHan = regexp.MustCompile(`\p{Han}`)

// TestBuiltinTemplatePromptsZhCNVerbatim pins the zh-CN prompts of the built-in workflow
// templates to the literals they were before they moved into the prompts templates
// (S3e): the text every zh-CN deployment's agents already read.
func TestBuiltinTemplatePromptsZhCNVerbatim(t *testing.T) {
	t.Parallel()
	plan, err := ResolveBuiltinWorkflowTemplateIn(i18n.ZhCN, "plan-implement", map[string]string{"project": "p", "task": "T"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "为下面的任务写一份实施方案（只读，不要修改文件）：\nT\n\n"; !strings.HasPrefix(plan.Steps[0].Prompt, want) {
		t.Errorf("plan prompt = %q", plan.Steps[0].Prompt)
	}
	if got, want := plan.Steps[1].Prompt, "按下面已审核的实施方案完成改动：\n\n${steps.plan.stdout}"; got != want {
		t.Errorf("implement prompt = %q, want %q", got, want)
	}
	rev, err := ResolveBuiltinWorkflowTemplateIn(i18n.ZhCN, "review-committee", map[string]string{"project": "p", "task": "T"})
	if err != nil {
		t.Fatal(err)
	}
	wantReview := "评审下面的对象：逐条列出发现的问题，每条附 文件:行 证据；不要修改任何文件。\n\n评审对象：T"
	if rev.Steps[0].Prompt != wantReview {
		t.Errorf("review prompt = %q, want %q", rev.Steps[0].Prompt, wantReview)
	}
	wantVerify := "你是评审委员会的核验人。各位评审的独立报告是下列结果目录（每行一个）中的 " + store.StdoutFile + " 文件：\n${steps.reviews.result_dir}\n" +
		"完整阅读每一份报告。对每条发现，打开当前项目里真实的源代码确认是否成立；不成立的剔除并说明原因。" +
		"输出一份汇总报告：已确认的发现（附 文件:行 证据）、被否决的发现（附原因），以及评审遗漏的问题。不要修改任何文件。"
	if rev.Steps[1].Prompt != wantVerify {
		t.Errorf("verify prompt = %q, want %q", rev.Steps[1].Prompt, wantVerify)
	}
}

// TestBuiltinTemplatePromptsEnglishHaveNoCJK: the English prompts stand on their own and
// keep the same steps and variable references as the Chinese ones.
func TestBuiltinTemplatePromptsEnglishHaveNoCJK(t *testing.T) {
	t.Parallel()
	zh, en := BuiltinWorkflowTemplatesIn(i18n.ZhCN), BuiltinWorkflowTemplatesIn(i18n.EN)
	if len(zh) != len(en) {
		t.Fatalf("template count differs: %d vs %d", len(zh), len(en))
	}
	ref := regexp.MustCompile(`\$\{[^}]*\}`)
	for i := range en {
		for j, st := range en[i].Spec.Steps {
			if templateHan.MatchString(st.Prompt) {
				t.Errorf("%s step %q: English prompt carries CJK: %q", en[i].Name, st.Name, st.Prompt)
			}
			if a, b := ref.FindAllString(zh[i].Spec.Steps[j].Prompt, -1), ref.FindAllString(st.Prompt, -1); strings.Join(a, " ") != strings.Join(b, " ") {
				t.Errorf("%s step %q: variable references differ: %v vs %v", en[i].Name, st.Name, a, b)
			}
		}
	}
	got, err := ResolveBuiltinWorkflowTemplateIn(i18n.EN, "plan-implement", map[string]string{"project": "p", "task": "T"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Steps[0].Prompt, "\nT\n\n") || !strings.HasPrefix(got.Steps[0].Prompt, "Write an implementation plan") {
		t.Errorf("English plan prompt = %q", got.Steps[0].Prompt)
	}
}
