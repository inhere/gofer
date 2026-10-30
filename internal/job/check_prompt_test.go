package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestSubmitCheckPromptCarriesAndInherits: check_prompt and its source ride the request
// into request_json and the result, default their source to explicit, and a resume keeps
// both (the continuation checks against the same requirement).
func TestSubmitCheckPromptCarriesAndInherits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newResumeRunnableService(t, root, "codex")
	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "task", Cwd: ".",
		SessionID: "sess-check", CheckPrompt: "  the report lists the changed files \n",
	})
	if src.CheckPrompt != "the report lists the changed files" || src.CheckPromptSource != CheckPromptSourceExplicit {
		t.Fatalf("result check = %q/%q", src.CheckPrompt, src.CheckPromptSource)
	}
	req := templateRequest(t, src)
	if req.CheckPrompt != src.CheckPrompt || req.CheckPromptSource != CheckPromptSourceExplicit {
		t.Fatalf("request_json check = %q/%q", req.CheckPrompt, req.CheckPromptSource)
	}
	next, err := s.ResumeJob(src.ID, "continue", "", "caller-check")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(next.ID); s.Wait(next.ID) })
	if got := templateRequest(t, next); got.CheckPrompt != src.CheckPrompt || got.CheckPromptSource != CheckPromptSourceExplicit {
		t.Fatalf("resume request check = %q/%q, want inherited", got.CheckPrompt, got.CheckPromptSource)
	}
}

func TestSubmitRejectsBadCheckPrompt(t *testing.T) {
	t.Parallel()
	s := newResumeRunnableService(t, t.TempDir(), "codex")
	base := JobRequest{ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "x", Cwd: ".", TimeoutSec: 30}
	big := base
	big.CheckPrompt = strings.Repeat("x", CheckPromptMaxBytes+1)
	if _, err := s.Submit(big); !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "check_prompt") {
		t.Fatalf("oversized: err = %v", err)
	}
	bad := base
	bad.CheckPrompt, bad.CheckPromptSource = "req", "magic"
	if _, err := s.Submit(bad); !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "check_prompt_source") {
		t.Fatalf("bad source: err = %v", err)
	}
}

// TestPlanTodoDispatchCarriesCheckPrompt: the dispatched request is built from the todo.
func TestPlanTodoDispatchCarriesCheckPrompt(t *testing.T) {
	t.Parallel()
	s := newResumeRunnableService(t, t.TempDir(), "codex")
	todo := jobstore.PlanTodo{TodoID: "todo-x", Title: "t", Assignee: "codex", ProjectKey: "self",
		CheckPrompt: "req", CheckPromptSource: CheckPromptSourcePromptBlock}
	req, reason := s.normalTodoRequest(jobstore.Plan{PlanID: "plan-x"}, todo, "tester")
	if reason != "" {
		t.Fatalf("dispatch refused: %s", reason)
	}
	if req.CheckPrompt != "req" || req.CheckPromptSource != CheckPromptSourcePromptBlock {
		t.Fatalf("request check = %q/%q", req.CheckPrompt, req.CheckPromptSource)
	}
}

// TestTemplateDefaultsCheckPrompt: a task book supplies the requirement only when the
// caller gave none.
func TestTemplateDefaultsCheckPrompt(t *testing.T) {
	s, root := newTemplateService(t)
	writeTemplate(t, root, "chk", "---\nagent: fake\nrunner: local\ncheck_prompt: from the task book\n---\nbody\n")
	got := submitAndWait(t, s, JobRequest{ProjectKey: "self", Cwd: ".", Template: "chk"})
	if r := templateRequest(t, got); r.CheckPrompt != "from the task book" || r.CheckPromptSource != CheckPromptSourceExplicit {
		t.Fatalf("template request check = %q/%q", r.CheckPrompt, r.CheckPromptSource)
	}
	own := submitAndWait(t, s, JobRequest{ProjectKey: "self", Cwd: ".", Template: "chk", CheckPrompt: "mine"})
	if r := templateRequest(t, own); r.CheckPrompt != "mine" {
		t.Fatalf("explicit check_prompt lost to the template: %q", r.CheckPrompt)
	}
}
