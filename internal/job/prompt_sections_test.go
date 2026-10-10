package job

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// sectionsCfg is a config with one cli-agent and the built-in exec agent.
func sectionsCfg() *config.Config {
	return &config.Config{
		Projects: map[string]config.ProjectConfig{"self": {}},
		Agents: map[string]config.AgentConfig{
			"cli": {Type: agent.TypeCLIAgent, Command: "x"},
			"run": {Type: agent.TypeExec},
		},
	}
}

func TestInjectAcceptanceSection(t *testing.T) {
	cfg := sectionsCfg()
	t.Run("agent job gets the section at the end", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "do it\n", Acceptance: "- a\n- b"}
		injectPromptSections(cfg, &req)
		want := "do it\n\n## 验收标准\n\n- a\n- b\n\n" + acceptanceSectionTail
		if req.Prompt != want {
			t.Fatalf("prompt = %q, want %q", req.Prompt, want)
		}
	})
	t.Run("exec job is not touched", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "run", Cmd: []string{"true"}, Acceptance: "- a"}
		injectPromptSections(cfg, &req)
		if req.Prompt != "" {
			t.Fatalf("exec prompt = %q, want empty", req.Prompt)
		}
	})
	t.Run("continuation / worker re-entry is not touched", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "go on", Acceptance: "- a", RulesResolved: true}
		injectPromptSections(cfg, &req)
		if req.Prompt != "go on" {
			t.Fatalf("prompt = %q, want unchanged", req.Prompt)
		}
	})
	t.Run("a prompt that already carries it is not duplicated", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "x", Acceptance: "- a"}
		injectPromptSections(cfg, &req)
		once := req.Prompt
		injectPromptSections(cfg, &req)
		if req.Prompt != once || strings.Count(req.Prompt, acceptanceSectionHeader) != 1 {
			t.Fatalf("second injection changed the prompt: %q", req.Prompt)
		}
	})
	t.Run("no acceptance no section", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "x", Acceptance: "  "}
		injectPromptSections(cfg, &req)
		if req.Prompt != "x" {
			t.Fatalf("prompt = %q", req.Prompt)
		}
	})
}

// TestTodoDispatchCarriesAcceptance: a todo's acceptance reaches the job request, the
// prompt (once) and the job result; resubmitting the persisted request (a rerun) does
// not append the section a second time.
func TestTodoDispatchCarriesAcceptance(t *testing.T) {
	t.Parallel()
	s := newDispatchService(t, t.TempDir())
	seedDispatchPlan(t, s,
		jobstore.Plan{PlanID: "plan-acc", Title: "P", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1},
		jobstore.PlanTodo{TodoID: "todo-acc", Title: "T", Assignee: "omp", Acceptance: "- tests pass", CreatedAt: 1, UpdatedAt: 1})
	setTodoStatus(t, s, "todo-acc", jobstore.TodoReady)

	d, err := s.MaybeDispatchTodo("todo-acc", "alice")
	if err != nil || d.Job == nil {
		t.Fatalf("dispatch: %+v err=%v", d, err)
	}
	final, _ := s.Wait(d.Job.ID)
	req := requestOf(t, final)
	if req.Acceptance != "- tests pass" || final.Acceptance != "- tests pass" {
		t.Fatalf("acceptance request=%q result=%q", req.Acceptance, final.Acceptance)
	}
	if strings.Count(req.Prompt, acceptanceSectionHeader) != 1 || !strings.Contains(req.Prompt, "- tests pass") {
		t.Fatalf("prompt does not carry the section once: %q", req.Prompt)
	}

	rerun := JobRequest{ProjectKey: req.ProjectKey, Agent: req.Agent, Runner: req.Runner, Prompt: req.Prompt, Acceptance: req.Acceptance}
	res, err := s.Submit(rerun)
	if err != nil {
		t.Fatalf("rerun submit: %v", err)
	}
	again, _ := s.Wait(res.ID)
	if n := strings.Count(requestOf(t, again).Prompt, acceptanceSectionHeader); n != 1 {
		t.Fatalf("rerun prompt has %d acceptance sections", n)
	}
}
