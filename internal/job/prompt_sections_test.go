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
	// The discipline section (auto mode adds it to an acceptance job) is covered below.
	cfg.Projects["self"] = config.ProjectConfig{ScopeDiscipline: config.ScopeDisciplineOff}
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

func TestInjectScopeDisciplineSection(t *testing.T) {
	withMode := func(mode string) *config.Config {
		cfg := sectionsCfg()
		cfg.Projects["self"] = config.ProjectConfig{ScopeDiscipline: mode}
		return cfg
	}
	has := func(req JobRequest) bool { return strings.Contains(req.Prompt, scopeSectionHeader) }

	t.Run("auto: a plain job gets nothing", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "x"}
		injectPromptSections(withMode(""), &req)
		if req.Prompt != "x" {
			t.Fatalf("prompt = %q", req.Prompt)
		}
	})
	t.Run("auto: todo / review / acceptance / scope jobs get it", func(t *testing.T) {
		cfgReview := withMode("auto")
		cfgReview.Projects["self"] = config.ProjectConfig{RequireReview: true}
		for name, tc := range map[string]struct {
			cfg *config.Config
			req JobRequest
		}{
			"todo":           {withMode("auto"), JobRequest{TodoID: "todo-1"}},
			"review":         {withMode("auto"), JobRequest{Review: true}},
			"require_review": {cfgReview, JobRequest{}},
			"acceptance":     {withMode("auto"), JobRequest{Acceptance: "- a"}},
			"scope":          {withMode("auto"), JobRequest{Scope: []string{"a/**"}}},
		} {
			req := tc.req
			req.ProjectKey, req.Agent, req.Prompt = "self", "cli", "x"
			injectPromptSections(tc.cfg, &req)
			if !has(req) {
				t.Fatalf("%s: no section in %q", name, req.Prompt)
			}
		}
	})
	t.Run("on: every batch agent job; off: none", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "x"}
		injectPromptSections(withMode("on"), &req)
		if !has(req) {
			t.Fatalf("on: %q", req.Prompt)
		}
		req = JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "x", TodoID: "t", Acceptance: "- a"}
		injectPromptSections(withMode("off"), &req)
		if has(req) || !strings.Contains(req.Prompt, acceptanceSectionHeader) {
			t.Fatalf("off must drop only the discipline section: %q", req.Prompt)
		}
	})
	t.Run("excluded shapes and the job-level opt-out", func(t *testing.T) {
		cfg := withMode("on")
		for name, req := range map[string]JobRequest{
			"exec":        {Agent: "run", Cmd: []string{"true"}},
			"interactive": {Agent: "cli", Interactive: true},
			"session":     {Agent: "cli", Session: true},
			"steward":     {Agent: "cli", Steward: true},
			"messenger":   {Agent: "cli", MessengerMeta: &MessengerMeta{}},
			"opt-out":     {Agent: "cli", NoScopeDiscipline: true},
			"re-entry":    {Agent: "cli", RulesResolved: true},
		} {
			req.ProjectKey, req.Prompt = "self", "x"
			injectPromptSections(cfg, &req)
			if has(req) {
				t.Fatalf("%s: unexpected section in %q", name, req.Prompt)
			}
		}
	})
	t.Run("order, scope line and no duplicate", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "x", Acceptance: "- a", Scope: []string{" internal/job/** ", "", "web/src/x.vue"}}
		injectPromptSections(withMode("auto"), &req)
		ai, si := strings.Index(req.Prompt, acceptanceSectionHeader), strings.Index(req.Prompt, scopeSectionHeader)
		if ai < 0 || si < ai {
			t.Fatalf("acceptance must come before the discipline section: %q", req.Prompt)
		}
		if !strings.HasSuffix(req.Prompt, scopeSectionScopeLine+"internal/job/**, web/src/x.vue") {
			t.Fatalf("scope line missing: %q", req.Prompt)
		}
		once := req.Prompt
		injectPromptSections(withMode("auto"), &req)
		if req.Prompt != once {
			t.Fatalf("second injection changed the prompt: %q", req.Prompt)
		}
	})
}
