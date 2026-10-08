package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestCheckModel(t *testing.T) {
	for _, ok := range []string{"", "opus", "claude-opus-4-1", "gpt-5.1-codex", "anthropic/claude-3.5"} {
		if err := CheckModel(ok); err != nil {
			t.Fatalf("CheckModel(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"-x", "--model", "a b", "a\nb", "a\tb", strings.Repeat("m", 201)} {
		if err := CheckModel(bad); err == nil {
			t.Fatalf("CheckModel(%q) = nil, want an error", bad)
		}
	}
}

func TestSubmitModelAdmission(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedAgents: []string{"claude", "plain", "exec"}, AllowedRunners: []string{"local"}, AllowExec: true,
		}},
		Agents: map[string]config.AgentConfig{
			"claude": {Type: agent.TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"}},
			"plain":  {Type: agent.TypeCLIAgent, Command: "plain-tool", Args: []string{"{{prompt}}"}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	for name, req := range map[string]JobRequest{
		"exec agent":         {ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Model: "opus"},
		"no model_args":      {ProjectKey: "self", Agent: "plain", Runner: "local", Prompt: "x", Model: "m"},
		"flag-like model":    {ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "x", Model: "--evil"},
		"whitespace in name": {ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "x", Model: "a b"},
	} {
		if _, err := s.Submit(req); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// TestSubmitModelRecordedOnJob: the model lands in request_json and on JobResult.Model
// (live snapshot AND the record read back), and a job without one carries none.
func TestSubmitModelRecordedOnJob(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newResumeRunnableService(t, root, "claude")
	with := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30, Model: "opus"})
	if with.Model != "opus" || templateRequest(t, with).Model != "opus" {
		t.Fatalf("model not recorded: result=%q request=%q", with.Model, templateRequest(t, with).Model)
	}
	if !strings.Contains(with.RenderedCommand, `"--model","opus","p"`) {
		t.Fatalf("rendered command %q does not carry --model opus", with.RenderedCommand)
	}
	without := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30})
	if without.Model != "" || strings.Contains(without.RequestJSON, `"model"`) || strings.Contains(without.RenderedCommand, "--model") {
		t.Fatalf("job without --model changed: model=%q request=%s cmd=%q", without.Model, without.RequestJSON, without.RenderedCommand)
	}
}

// TestResumeInheritsAndOverridesModel: a continuation keeps the source job's model; an
// explicit one replaces it; a source without one stays without (argv unchanged).
func TestResumeInheritsAndOverridesModel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newResumeRunnableService(t, root, "claude")
	src := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30, Model: "opus"})
	sid := src.SessionID

	inherited, err := s.ResumeJob(src.ID, "next", "", "c")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(inherited.ID); s.Wait(inherited.ID) })
	want := []string{"claude", "--resume", sid, "-p", "--model", "opus", "next"}
	if got := resumeArgv(t, inherited.RequestJSON); !equalArgs(got, want) || inherited.Model != "opus" {
		t.Fatalf("inherited resume argv = %#v (model %q), want %#v with model opus", got, inherited.Model, want)
	}

	over, err := s.ResumeJobWith(src.ID, "next", "", "c", ResumeOptions{Model: "sonnet"})
	if err != nil {
		t.Fatalf("ResumeJobWith: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(over.ID); s.Wait(over.ID) })
	want = []string{"claude", "--resume", sid, "-p", "--model", "sonnet", "next"}
	if got := resumeArgv(t, over.RequestJSON); !equalArgs(got, want) || over.Model != "sonnet" {
		t.Fatalf("override resume argv = %#v (model %q), want %#v", got, over.Model, want)
	}

	if _, err := s.ResumeJobWith(src.ID, "next", "", "c", ResumeOptions{Model: "--bad"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("bad override err = %v, want ErrInvalidRequest", err)
	}

	plain := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30})
	r, err := s.ResumeJob(plain.ID, "next", "", "c")
	if err != nil {
		t.Fatalf("ResumeJob plain: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(r.ID); s.Wait(r.ID) })
	if got := resumeArgv(t, r.RequestJSON); !equalArgs(got, []string{"claude", "--resume", plain.SessionID, "-p", "next"}) || r.Model != "" {
		t.Fatalf("model-less resume argv changed: %#v model=%q", got, r.Model)
	}
}

func TestTemplateFrontmatterModel(t *testing.T) {
	s, root := newTemplateService(t)
	// The fake agent has no built-in model_args, so give it one.
	ac := s.config().Agents["fake"]
	ac.ModelArgs = []string{"--model", "{{model}}"}
	s.config().Agents["fake"] = ac
	writeTemplate(t, root, "m", "---\nagent: fake\nmodel: tpl-model\n---\nhello\n")
	res := submitAndWait(t, s, JobRequest{ProjectKey: "self", Template: "m", Cwd: ".", TimeoutSec: 30})
	if res.Model != "tpl-model" || templateRequest(t, res).Model != "tpl-model" {
		t.Fatalf("template model not applied: %q", res.Model)
	}
	// An explicit request model wins over the task book's.
	res = submitAndWait(t, s, JobRequest{ProjectKey: "self", Template: "m", Cwd: ".", TimeoutSec: 30, Model: "mine"})
	if res.Model != "mine" {
		t.Fatalf("explicit model lost to the template: %q", res.Model)
	}
}

// ACP: the model is picked over the protocol before the first prompt.
func TestACPModelSelection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, mode, model string
		wantDone          bool
		wantLog, wantErr  string
	}{
		{"config option", "config", acptest.ModelB, true, "session/set_config_option model=" + acptest.ModelB, ""},
		{"models block", "models", acptest.ModelB, true, "session/set_model model=" + acptest.ModelB, ""},
		{"not offered", "config", "nope", false, "", `model "nope" not offered`},
		{"agent exposes nothing", "", "any", false, "", "does not expose model selection"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newACPService(t, t.TempDir(), acptest.Options{ModelMode: c.mode})
			res := submitAndWait(t, s, JobRequest{
				ProjectKey: "self", Agent: "acpbot", Runner: "local", Prompt: acpTestPrompt,
				Cwd: ".", TimeoutSec: 30, Model: c.model,
			})
			if res.Model != c.model {
				t.Fatalf("job model = %q, want %q", res.Model, c.model)
			}
			if c.wantDone {
				if res.Status != StatusDone {
					t.Fatalf("status = %s (err=%s), want done", res.Status, res.Error)
				}
				if log := readJobLog(t, res, "stderr.log"); !strings.Contains(log, c.wantLog) {
					t.Fatalf("stderr.log lacks %q:\n%s", c.wantLog, log)
				}
				return
			}
			if res.Status != StatusFailed || !strings.Contains(res.Error, c.wantErr) {
				t.Fatalf("status = %s err = %q, want failed containing %q", res.Status, res.Error, c.wantErr)
			}
		})
	}
	// No model: neither method is ever called.
	s := newACPService(t, t.TempDir(), acptest.Options{ModelMode: "config"})
	res := acpSubmit(t, s, 30)
	if log := readJobLog(t, res, "stderr.log"); res.Status != StatusDone || strings.Contains(log, "set_config_option") || strings.Contains(log, "set_model") {
		t.Fatalf("job without a model touched model selection: status=%s\n%s", res.Status, log)
	}
}

// TestTodoDispatchCarriesModel: a todo's model rides into the dispatched job (request,
// result and rendered argv); a todo without one dispatches exactly as before.
func TestTodoDispatchCarriesModel(t *testing.T) {
	t.Parallel()
	s := newDispatchService(t, t.TempDir())
	ac := s.config().Agents["omp"]
	ac.ModelArgs = []string{"--model", "{{model}}"}
	s.config().Agents["omp"] = ac

	for _, model := range []string{"opus", ""} {
		id := "todo-" + model + "x"
		seedDispatchPlan(t, s,
			jobstore.Plan{PlanID: "plan-" + id, Title: "p", Status: jobstore.PlanOpen, ProjectKey: "self", CreatedAt: 1, UpdatedAt: 1},
			jobstore.PlanTodo{TodoID: id, Title: "step", Assignee: "omp", Model: model, CreatedAt: 1, UpdatedAt: 1})
		setTodoStatus(t, s, id, jobstore.TodoReady)
		d, err := s.MaybeDispatchTodo(id, "alice")
		if err != nil || d.Job == nil {
			t.Fatalf("dispatch %q: job=%v err=%v reason=%q", model, d.Job, err, d.Reason)
		}
		final, _ := s.Wait(d.Job.ID)
		if final.Model != model {
			t.Fatalf("dispatched job model = %q, want %q", final.Model, model)
		}
		if has := strings.Contains(final.RenderedCommand, `"--model","opus"`); has != (model != "") {
			t.Fatalf("model %q: rendered command %q", model, final.RenderedCommand)
		}
	}
}
