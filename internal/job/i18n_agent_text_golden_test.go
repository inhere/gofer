package job

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
)

// The golden files under testdata/i18n_golden pin the text the job service writes for
// an agent or a person outside the prompts/ templates (leader brief, comment-dispatch
// brief, skills / tools sections, wrap-up reasons, plan rules). The *.zh-CN.txt files
// were recorded from the code BEFORE it was migrated into the catalog, so a change to
// them changes what every zh-CN deployment's agent reads. The *.en.txt files are the
// English rewrite; GOFER_GOLDEN_UPDATE=1 rewrites either after a deliberate edit.

var agentTextCJK = regexp.MustCompile(`[\p{Han}]`)

type goldenSkillLib struct{}

func (goldenSkillLib) Get(name string) (SkillInfo, bool) {
	if name == "alpha" {
		return SkillInfo{Name: "alpha", Description: "house rules"}, true
	}
	return SkillInfo{Name: name}, true
}
func (goldenSkillLib) Mount(string, string) (int64, error) { return 0, nil }
func (goldenSkillLib) Stage(context.Context, string, string, string, string) ([]UploadSpec, error) {
	return nil, nil
}

// agentTextOutputs renders every text of the golden set in lang.
func agentTextOutputs(t *testing.T, lang i18n.Lang) map[string]string {
	t.Helper()
	out := map[string]string{}
	s := newLeaderService(t, t.TempDir(), nil)
	if err := s.Meta().InsertPlan(jobstore.Plan{PlanID: "plan-g", Title: "Golden plan", Description: "ship the thing", Status: jobstore.PlanOpen, Paused: true, BlockedTodo: "t-2", ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	for i, td := range []jobstore.PlanTodo{
		{TodoID: "t-1", Title: "first", Status: jobstore.TodoDone, Assignee: "ok", Note: "line1\nlast note"},
		{TodoID: "t-2", Title: "second", Status: jobstore.TodoPending, Assignee: "ok", After: []string{"t-1"}},
		{TodoID: "t-3", Title: "third", Status: jobstore.TodoPending, After: []string{"t-1", "t-2"}},
		{TodoID: "t-4", Title: "fourth", Status: jobstore.TodoPending},
	} {
		td.PlanID, td.Sort, td.CreatedAt, td.UpdatedAt = "plan-g", (i+1)*10, 1, 1
		if err := s.Meta().InsertTodo(td); err != nil {
			t.Fatal(err)
		}
	}
	plan, _, _ := s.Meta().GetPlan("plan-g")
	w := jobstore.LeaderWake{MemberJobID: "job-m1", MemberStatus: "failed", Round: 2}
	out["leader_prompt_gone"] = s.leaderPrompt(lang, plan, JobResult{}, w)
	out["leader_prompt_member"] = s.leaderPrompt(lang, plan, JobResult{ID: "job-m1", Title: "member", Status: "failed", Error: "exit 1"}, w)
	plain := plan
	plain.Paused, plain.BlockedTodo, plain.Title, plain.Description = false, "", "", ""
	out["leader_prompt_plain"] = s.leaderPrompt(lang, plain, JobResult{ID: "job-m2", Status: "done"}, w)
	out["comment_job"] = s.commentJobContext(lang, JobResult{ID: "job-c1", Status: "failed", Error: "boom"})
	out["comment_plan"] = s.commentPlanContext(lang, plan)
	out["comment_plan_plain"] = s.commentPlanContext(lang, plain)
	tdo, _, _ := s.Meta().GetTodo("t-1")
	out["comment_todo"] = s.commentTodoContext(lang, tdo)
	out["comment_dispatch"] = commentDispatchPrompt(lang, out["comment_todo"], "  please look  ")
	out["skills_manifest"] = skillsManifest(lang, goldenSkillLib{}, []string{"alpha", "beta"}, "/r/skills")
	out["tools_plain"] = renderToolsSection(lang, []config.Tool{{Name: "jq", Desc: "json"}, {Name: "rg", Bin: "ripgrep"}}, "")
	out["tools_worker"] = renderToolsSection(lang, []config.Tool{{Name: "jq"}}, "w-1")
	out["fallback_prompt"] = fallbackPrompt(lang, "codex", "  at capacity ", "do the thing")
	out["todo_default_prompt"] = defaultTodoPrompt(lang, jobstore.Plan{Title: "Golden plan", Description: "ship the thing"}, jobstore.PlanTodo{Title: "first", Note: "line1\nlast note"})
	all := ""
	for _, r := range []string{ndjsonfilter.WrapUpMaxSteps, ndjsonfilter.WrapUpMaxTokens, ndjsonfilter.WrapUpBudget, ndjsonfilter.WrapUpTimeout, "", "wall_clock"} {
		all += "[" + WrapUpTextIn(lang, r) + "]\n"
	}
	out["wrapup"] = all
	out["plan_rules"] = PlanRulesIn(lang)
	out["plan_todos_format"] = PlanTodosFormatIn(lang)
	return out
}

func TestAgentTextGolden(t *testing.T) {
	for _, lang := range i18n.Supported() {
		t.Run(string(lang), func(t *testing.T) {
			update := os.Getenv("GOFER_GOLDEN_UPDATE") != ""
			for name, got := range agentTextOutputs(t, lang) {
				file := filepath.Join("testdata", "i18n_golden", name+"."+string(lang)+".txt")
				raw, err := os.ReadFile(file)
				if err != nil && lang == i18n.ZhCN {
					t.Fatalf("%s: the zh-CN golden is the recorded original and must exist: %v", file, err)
				}
				if err != nil || (update && lang != i18n.ZhCN) {
					if werr := os.WriteFile(file, []byte(got), 0o644); werr != nil {
						t.Fatal(werr)
					}
					if err != nil {
						t.Errorf("%s did not exist: written with the current text, review and commit it", file)
					}
					continue
				}
				if update {
					t.Logf("%s: the zh-CN golden is never rewritten by the update flag", file)
				}
				if got != string(raw) {
					t.Errorf("%s differs from its golden file\n--- got ---\n%s\n--- want ---\n%s", file, got, raw)
				}
				// A text stands on its own in its language (G046): English carries no
				// Chinese; Chinese keeps its technical English words only.
				if lang == i18n.EN && agentTextCJK.MatchString(got) {
					t.Errorf("%s: the English text carries CJK:\n%s", name, got)
				}
				if lang == i18n.ZhCN && strings.Contains(got, "{") && strings.Contains(got, "{n}") {
					t.Errorf("%s: unfilled placeholder:\n%s", name, got)
				}
			}
		})
	}
}

// TestAgentTextEnglishIsComplete: every Chinese line has an English counterpart, so
// the two renderings have the same shape (same number of lines).
func TestAgentTextEnglishIsComplete(t *testing.T) {
	zh := agentTextOutputs(t, i18n.ZhCN)
	en := agentTextOutputs(t, i18n.EN)
	for name, z := range zh {
		if strings.Count(z, "\n") != strings.Count(en[name], "\n") {
			t.Errorf("%s: zh-CN has %d lines, en has %d", name, strings.Count(z, "\n"), strings.Count(en[name], "\n"))
		}
	}
}
