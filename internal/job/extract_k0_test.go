package job

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/template"
)

// Unit tests of the pieces extracted from resumeJob, applyTemplate and the prompt
// section injection (K0). The behaviour of the callers is pinned by the existing
// resume / template / prompt-section tests and the plan-dispatch golden files.

// newCheckResumeService has a cli agent "withm" that takes a model and "nom" that
// does not (one session family).
func newCheckResumeService(t *testing.T, root string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"withm", "nom", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"withm": {
				Type: agent.TypeCLIAgent, Command: "withm", Args: []string{"-p", "{{prompt}}"},
				SessionFamily: "k0fam",
				ModelArgs:     []string{"--model", "{{model}}"},
			},
			"nom": {Type: agent.TypeCLIAgent, Command: "nom", Args: []string{"-p", "{{prompt}}"}, SessionFamily: "k0fam"},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

func TestCheckResume(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newCheckResumeService(t, root)
	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "withm", Runner: "local", Prompt: "task", Cwd: ".",
		TimeoutSec: 30, SessionID: "sess-check", Model: "m0",
	})

	t.Run("defaults inherit the source", func(t *testing.T) {
		chk, err := s.checkResume(src.ID, "", ResumeOptions{})
		if err != nil {
			t.Fatalf("checkResume: %v", err)
		}
		if chk.Src.ID != src.ID || chk.ResumeAgent != "withm" || chk.TargetAgent != "withm" ||
			chk.Form != ResumeModeBatch || chk.Mode != "" || chk.Model != "m0" || chk.Budget != nil {
			t.Fatalf("unexpected check: %+v", chk)
		}
	})
	t.Run("mode is normalized and picks the form", func(t *testing.T) {
		chk, err := s.checkResume(src.ID, "SERVER", ResumeOptions{Mode: " Interactive "})
		if err != nil {
			t.Fatalf("checkResume: %v", err)
		}
		if chk.Mode != ResumeModeInteractive || chk.Form != ResumeModeInteractive || chk.Runner != "local" {
			t.Fatalf("mode/form/runner = %q/%q/%q", chk.Mode, chk.Form, chk.Runner)
		}
	})
	t.Run("unknown mode", func(t *testing.T) {
		if _, err := s.checkResume(src.ID, "", ResumeOptions{Mode: "warp"}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
	})
	t.Run("unknown job", func(t *testing.T) {
		if _, err := s.checkResume("nope", "", ResumeOptions{}); !errors.Is(err, ErrUnknownJob) {
			t.Fatalf("err = %v, want ErrUnknownJob", err)
		}
	})
	t.Run("cross runner", func(t *testing.T) {
		if _, err := s.checkResume(src.ID, "elsewhere", ResumeOptions{}); !errors.Is(err, ErrCrossRunner) {
			t.Fatalf("err = %v, want ErrCrossRunner", err)
		}
	})
	t.Run("target agent in the family or not", func(t *testing.T) {
		chk, err := s.checkResume(src.ID, "", ResumeOptions{Agent: "nom"})
		if err != nil || chk.TargetAgent != "nom" || chk.ResumeAgent != "withm" {
			t.Fatalf("target/resume = %q/%q err=%v, want nom/withm", chk.TargetAgent, chk.ResumeAgent, err)
		}
		if _, err := s.checkResume(src.ID, "", ResumeOptions{Agent: "ghost"}); !errors.Is(err, ErrResumeUnsupported) {
			t.Fatalf("unknown agent err = %v, want ErrResumeUnsupported", err)
		}
	})
	t.Run("explicit model overrides, a bad one is refused", func(t *testing.T) {
		chk, err := s.checkResume(src.ID, "", ResumeOptions{Model: "m1"})
		if err != nil || chk.Model != "m1" {
			t.Fatalf("model = %q err=%v, want m1", chk.Model, err)
		}
		if _, err := s.checkResume(src.ID, "", ResumeOptions{Model: "bad model;"}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
	})
	t.Run("explicit budget the agent cannot enforce is refused", func(t *testing.T) {
		_, err := s.checkResume(src.ID, "", ResumeOptions{Budget: &Budget{MaxTurns: 3}})
		if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "budget cannot be enforced") {
			t.Fatalf("err = %v, want budget refusal", err)
		}
	})
}

func TestCheckResumeNoSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newClaudeInjectService(t, root)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if _, err := s.checkResume(src.ID, "", ResumeOptions{}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
}

func TestCheckResumeDropsInheritedModelTheTargetCannotTake(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newCheckResumeService(t, root)
	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "withm", Runner: "local", Prompt: "task", Cwd: ".",
		TimeoutSec: 30, SessionID: "sess-withm", Model: "m0",
	})
	// Continue with the family member that has no model_args: the inherited model is
	// dropped, an explicit one is refused.
	chk, err := s.checkResume(src.ID, "", ResumeOptions{Agent: "nom"})
	if err != nil {
		t.Fatalf("checkResume: %v", err)
	}
	if chk.TargetAgent != "nom" || chk.Model != "" {
		t.Fatalf("target/model = %q/%q, want nom with the model dropped", chk.TargetAgent, chk.Model)
	}
	if _, err := s.checkResume(src.ID, "", ResumeOptions{Agent: "nom", Model: "m1"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("explicit model on nom err = %v, want ErrInvalidRequest", err)
	}
}

func TestNormalizeResumeMode(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"": "", " BATCH ": "batch", "Session": "session", "interactive": "interactive"} {
		if got, err := normalizeResumeMode(in); err != nil || got != want {
			t.Fatalf("normalizeResumeMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeResumeMode("x"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestRenderTemplateAndApplyTemplateMeta(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newDispatchService(t, root)
	cfg := s.config()
	writeTemplate(t, root, "k0-tpl", "---\nvars:\n  who: {required: true}\nmodel: tm\nagent: omp\ntimeout_sec: 90\ntags: [a]\nreview: true\n---\nHello {{who}}\n")

	t.Run("no template is a no-op for both", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Prompt: "p"}
		tpl, err := s.renderTemplate(cfg, &req)
		if err != nil || tpl != nil {
			t.Fatalf("renderTemplate = %v, %v; want nil, nil", tpl, err)
		}
		applyTemplateMeta(&req, tpl)
		if req.Prompt != "p" || req.Runner != "" || req.Agent != "" {
			t.Fatalf("request changed: %+v", req)
		}
	})
	t.Run("render touches the prompt only", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Template: "k0-tpl", TemplateVars: map[string]string{"who": "W"}, Prompt: "extra"}
		tpl, err := s.renderTemplate(cfg, &req)
		if err != nil || tpl == nil {
			t.Fatalf("renderTemplate: %v, %v", tpl, err)
		}
		if req.Prompt != "Hello W\n\nextra" && req.Prompt != "Hello W\n\nextra\n" {
			t.Fatalf("prompt = %q", req.Prompt)
		}
		if req.Agent != "" || req.Model != "" || req.Runner != "" || req.TimeoutSec != 0 || req.Review || len(req.Tags) != 0 {
			t.Fatalf("renderTemplate applied metadata: %+v", req)
		}
		applyTemplateMeta(&req, tpl)
		if req.Agent != "omp" || req.Model != "tm" || req.TimeoutSec != 90 || !req.Review ||
			len(req.Tags) != 1 || req.Runner != builtinLocalRunner {
			t.Fatalf("applyTemplateMeta result: %+v", req)
		}
	})
	t.Run("explicit request fields win and no-verify blocks the template verify", func(t *testing.T) {
		tpl := &template.Template{Meta: template.Meta{
			Model: "tm", TimeoutSec: 90, Verify: []string{"v"}, VerifyTimeoutSec: 5, Runner: "SERVER",
			Budget: &template.Budget{MaxTurns: 4},
		}}
		req := JobRequest{Model: "mine", TimeoutSec: 10, NoVerify: true, Budget: &Budget{MaxTokens: 100}}
		applyTemplateMeta(&req, tpl)
		if req.Model != "mine" || req.TimeoutSec != 10 || len(req.Verify) != 0 || req.VerifyTimeoutSec != 5 || req.Runner != builtinLocalRunner {
			t.Fatalf("request = %+v", req)
		}
		if req.Budget == nil || req.Budget.MaxTokens != 100 || req.Budget.MaxTurns != 4 {
			t.Fatalf("budget = %+v, want request tokens + template turns", req.Budget)
		}
	})
	t.Run("missing var is a 400 before anything is applied", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Template: "k0-tpl"}
		if _, err := s.renderTemplate(cfg, &req); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("err = %v, want ErrInvalidRequest", err)
		}
	})
}

func TestEffectiveTimeoutMatchesNormalizeAndLimit(t *testing.T) {
	t.Parallel()
	cfg := sectionsCfg()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	req := JobRequest{ProjectKey: "self", Agent: "cli", TimeoutSec: 600}
	timeout, clamped, limit := effectiveTimeout(cfg, &req, now)
	wantT, wantC := normalizeTimeout(600, false, true, cfg.EffectiveMaxTimeoutSec("self"))
	if timeout != wantT || clamped != wantC {
		t.Fatalf("timeout = %v/%v, want %v/%v", timeout, clamped, wantT, wantC)
	}
	if want := jobTimeLimit(now, wantT, &req, 0); limit != want {
		t.Fatalf("limit = %+v, want %+v", limit, want)
	}
	if limit.Sec != 600 || !limit.Deadline.Equal(now.Add(600*time.Second)) {
		t.Fatalf("limit = %+v", limit)
	}
	// The project ceiling clamps the request.
	cfg.Projects["self"] = config.ProjectConfig{MaxTimeoutSec: 120}
	timeout, clamped, limit = effectiveTimeout(cfg, &req, now)
	if !clamped || timeout != 120*time.Second || limit.Sec != 120 {
		t.Fatalf("clamped = %v timeout=%v limit=%+v, want 120s clamp", clamped, timeout, limit)
	}
}

func TestBuildPromptSections(t *testing.T) {
	t.Parallel()
	cfg := sectionsCfg()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	limit := jobTimeLimit(now, 30*time.Minute, &JobRequest{}, 0)

	t.Run("acceptance, contract and deadline in submit order", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "task", Acceptance: "- a", TodoID: "t1", Scope: []string{" docs/** "}}
		text, kc := buildPromptSections(cfg, &req, "cli", limit)
		if !kc {
			t.Fatalf("knowledgeCapture = false, want true for a todo job")
		}
		iA, iC, iD := strings.Index(text, acceptanceSectionHeader), strings.Index(text, scopeSectionHeader), strings.Index(text, deadlineLinePrefix)
		if iA < 0 || iC < iA || iD < iC {
			t.Fatalf("section order wrong: %q", text)
		}
		if !strings.Contains(text, scopeSectionScopeLine+"docs/**") || !strings.Contains(text, knowledgeSectionLine) {
			t.Fatalf("contract lines missing: %q", text)
		}
		if strings.HasPrefix(text, "task") {
			t.Fatalf("text must hold the sections only, got %q", text)
		}
	})
	t.Run("same text as injectPromptSections + appendJobDeadlineLine", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "task", Acceptance: "- a", TodoID: "t1"}
		text, kc := buildPromptSections(cfg, &req, req.Agent, limit)
		inj := req
		injectPromptSections(cfg, &inj)
		full := appendJobDeadlineLine(cfg, &inj, inj.Prompt, limit)
		if want := "task\n\n" + text; full != want || inj.KnowledgeCapture != kc {
			t.Fatalf("full = %q kc=%v; want %q kc=%v", full, inj.KnowledgeCapture, want, kc)
		}
	})
	t.Run("exec is decided from the real agent, not req.Agent", func(t *testing.T) {
		carrier := JobRequest{ProjectKey: "self", Agent: "run", Cmd: []string{"x"}, Prompt: "task", Acceptance: "- a", TodoID: "t1"}
		if text, kc := buildPromptSections(cfg, &carrier, "run", limit); text != "" || kc {
			t.Fatalf("exec real agent got %q kc=%v, want none", text, kc)
		}
		// A request whose own agent is the exec carrier but whose real agent is a cli one.
		if text, _ := buildPromptSections(cfg, &carrier, "cli", limit); !strings.Contains(text, acceptanceSectionHeader) {
			t.Fatalf("cli real agent got %q, want the sections", text)
		}
	})
	t.Run("no scope discipline drops the contract, interactive drops it and the deadline", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "task", Acceptance: "- a", NoScopeDiscipline: true}
		text, kc := buildPromptSections(cfg, &req, "cli", limit)
		if kc || strings.Contains(text, scopeSectionHeader) || !strings.Contains(text, deadlineLinePrefix) {
			t.Fatalf("NoScopeDiscipline text = %q kc=%v", text, kc)
		}
		req.Interactive = true
		text, _ = buildPromptSections(cfg, &req, "cli", limit)
		if strings.Contains(text, scopeSectionHeader) || strings.Contains(text, deadlineLinePrefix) {
			t.Fatalf("interactive text = %q", text)
		}
	})
	t.Run("no limit no deadline line; sections already in the prompt are not repeated", func(t *testing.T) {
		req := JobRequest{ProjectKey: "self", Agent: "cli", Prompt: "task\n\n" + acceptanceSectionHeader + "\n\nx", Acceptance: "- a", TodoID: "t1"}
		text, _ := buildPromptSections(cfg, &req, "cli", jobLimit{})
		if strings.Contains(text, acceptanceSectionHeader) || strings.Contains(text, deadlineLinePrefix) || !strings.Contains(text, scopeSectionHeader) {
			t.Fatalf("text = %q", text)
		}
	})
}
