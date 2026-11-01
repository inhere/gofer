package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

const (
	goodAnswer = `{"schema":"daily-memory-synthesis.v1","summary":"the project shipped the feature","highlights":[{"text":"feature shipped","source_refs":["work:%s"]}],"open_questions":[]}`
	secretTok  = "sk-abcdefghijklmnop1234"
)

type rig struct {
	t       *testing.T
	cfg     *config.Config
	jobs    *job.Service
	store   *jobstore.Store
	eng     *Engine
	capture string
	item    jobstore.WorkItem
}

// newRig wires the real job service with a "codex" cli-agent whose command is the test
// helper: it writes the prompt it received to a file and prints `answer` (the stand-in
// for a model answering the one-shot call).
func newRig(t *testing.T, answer string, mutate func(*config.Config)) *rig {
	t.Helper()
	root := t.TempDir()
	capture := filepath.Join(root, "prompt.txt")
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Server:  config.ServerConfig{Language: string(i18n.EN)},
		Projects: map[string]config.ProjectConfig{
			"proj": {HostPath: root, AllowedAgents: []string{"codex"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"prompt-capture", capture, answer, "{{prompt}}"}},
		},
		Work:   config.WorkConfig{SummarizerAgent: "codex"},
		Memory: config.MemoryConfig{Synthesis: config.SynthesisConfig{Enabled: true, Timeout: "1m"}},
	}
	if mutate != nil {
		mutate(cfg)
	}
	meta, err := jobstore.Open(filepath.Join(root, "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	agents := agent.NewRegistry(cfg)
	jobs := job.NewService(cfg, project.NewRegistry(cfg, ""), agents,
		map[string]runner.Runner{localrunner.Name: localrunner.New()}, meta, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
		defer cancel()
		if err := jobs.Drain(ctx); err != nil {
			t.Errorf("teardown drain: %v", err)
		}
	})
	item, err := meta.CreateWorkItem(jobstore.WorkItemInput{Title: "ship the feature token=" + secretTok, ProjectKey: "proj", Status: jobstore.WorkParked, By: "human:t"})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, cfg: cfg, jobs: jobs, store: meta, capture: capture, item: item}
	r.eng = New(Deps{Store: meta, Config: func() *config.Config { return cfg }, OneShot: JobOneShot{Jobs: jobs, Agents: agents}})
	return r
}

func (r *rig) answer() string { return strings.ReplaceAll(goodAnswer, "%s", r.item.ID) }

func (r *rig) run(req Request) (memorysynthesis.Outcome, error) {
	r.t.Helper()
	if req.Project == "" {
		req.Project = "proj"
	}
	return r.eng.Run(context.Background(), req)
}

func (r *rig) runs() []jobstore.MemorySynthesisRun {
	r.t.Helper()
	rows, err := r.store.ListMemorySynthesisRuns(jobstore.MemorySynthesisRunFilter{})
	if err != nil {
		r.t.Fatal(err)
	}
	return rows
}

func (r *rig) prompt() string {
	b, err := os.ReadFile(r.capture)
	if err != nil {
		return ""
	}
	return string(b)
}

// An explicit run goes through the real wiring end to end: a hidden read-only one-shot job
// of the configured cli-agent answers, the answer is validated and stored on the run record.
func TestRunEndToEndThroughRealJobService(t *testing.T) {
	r := newRig(t, "", nil)
	// The answer is only known after the work item exists: rebuild the agent argv.
	ac := r.cfg.Agents["codex"]
	ac.Args = []string{"prompt-capture", r.capture, r.answer(), "{{prompt}}"}
	r.cfg.Agents["codex"] = ac

	out, err := r.run(Request{CreatedBy: "human:t"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != memorysynthesis.OutcomeSucceeded {
		t.Fatalf("out=%+v", out)
	}
	run := out.Run
	if !strings.Contains(run.ResultJSON, "the project shipped the feature") || run.Agent != "codex" || run.CreatedBy != "human:t" {
		t.Fatalf("run=%+v", run)
	}
	// What the model saw: the envelope with the work item, never the secret.
	p := r.prompt()
	if !strings.Contains(p, "work:"+r.item.ID) || strings.Contains(p, secretTok) || !strings.Contains(p, "[redacted]") {
		t.Fatalf("prompt=%s", p)
	}
	// The one-shot job is internal: the synthesis job does not appear in the ordinary list.
	jobs, err := r.store.ListJobs(jobstore.ListQuery{ExcludeTags: jobstore.InternalJobTags})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("internal job leaked into the ordinary list: %v err=%v", jobs, err)
	}
	all, _ := r.store.ListJobs(jobstore.ListQuery{})
	if len(all) != 1 || all[0].Status != job.StatusDone {
		t.Fatalf("all=%+v", all)
	}

	// Same input: reused, no new model call (the capture file is not rewritten).
	if err := os.Remove(r.capture); err != nil {
		t.Fatal(err)
	}
	again, err := r.run(Request{})
	if err != nil || again.State != memorysynthesis.OutcomeReused || again.Run.ID != run.ID {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	if r.prompt() != "" {
		t.Fatal("a reused run must not call the model")
	}
	// --force runs it again and retires the old record.
	forced, err := r.run(Request{Force: true})
	if err != nil || forced.State != memorysynthesis.OutcomeSucceeded || forced.Run.ID == run.ID || r.prompt() == "" {
		t.Fatalf("forced=%+v err=%v", forced, err)
	}
	old, _, _ := r.store.GetMemorySynthesisRun(run.ID)
	if old.Status != jobstore.MemorySynthesisSkipped {
		t.Fatalf("old=%+v", old)
	}
}

func TestRunRecordsUnparsableAnswerAsFailed(t *testing.T) {
	r := newRig(t, "I refuse to answer in JSON", nil)
	out, err := r.run(Request{})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != memorysynthesis.OutcomeFailed || out.Run.ResultJSON != "" || !strings.Contains(out.Run.Error, "invalid output") {
		t.Fatalf("out=%+v", out)
	}
	stored := r.runs()
	if len(stored) != 1 || stored[0].Status != jobstore.MemorySynthesisFailed {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestRunRefusedBeforeAnythingHappens(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		r := newRig(t, "x", func(c *config.Config) { c.Memory.Synthesis.Enabled = false })
		_, err := r.run(Request{})
		if !errors.Is(err, ErrDisabled) || !strings.Contains(err.Error(), "memory.synthesis.enabled") {
			t.Fatalf("err=%v", err)
		}
		if all, _ := r.store.ListJobs(jobstore.ListQuery{}); len(all) != 0 || len(r.runs()) != 0 || r.prompt() != "" {
			t.Fatal("a disabled synthesis must not create jobs, runs or prompts")
		}
	})
	t.Run("unknown project", func(t *testing.T) {
		r := newRig(t, "x", nil)
		if _, err := r.run(Request{Project: "nope"}); !errors.Is(err, ErrProject) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("project not listed", func(t *testing.T) {
		r := newRig(t, "x", func(c *config.Config) { c.Memory.Synthesis.Projects = []string{"other"} })
		if _, err := r.run(Request{}); !errors.Is(err, ErrProject) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("bad day", func(t *testing.T) {
		r := newRig(t, "x", nil)
		if _, err := r.run(Request{Day: "28/10/2026"}); !errors.Is(err, ErrDay) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("agent without a no-tools argv", func(t *testing.T) {
		r := newRig(t, "x", func(c *config.Config) {
			c.Agents["plain"] = config.AgentConfig{Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"prompt-capture", "x", "x", "{{prompt}}"}}
			c.Memory.Synthesis.Agent = "plain"
		})
		_, err := r.run(Request{})
		if !errors.Is(err, ErrAgent) || !strings.Contains(err.Error(), "no-tools") {
			t.Fatalf("err=%v", err)
		}
		if len(r.runs()) != 0 {
			t.Fatal("an unusable agent must not leave a run record")
		}
	})
	t.Run("no material", func(t *testing.T) {
		r := newRig(t, "x", nil)
		out, err := r.run(Request{Day: "2001-01-02"})
		if err != nil || out.State != memorysynthesis.OutcomeEmpty || len(r.runs()) != 0 {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	})
}

// The prompt follows the project language through the real wiring.
func TestRunPromptUsesProjectLanguage(t *testing.T) {
	r := newRig(t, "{}", func(c *config.Config) { c.Server.Language = string(i18n.ZhCN) })
	if _, err := r.run(Request{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.prompt(), "项目动态摘要员") {
		t.Fatalf("prompt=%s", r.prompt())
	}
}

func TestModelOf(t *testing.T) {
	for in, want := range map[string]string{"--model haiku": "haiku", "--model=opus": "opus", "-m x": "x", "--tools": ""} {
		if got := modelOf(strings.Fields(in)); got != want {
			t.Errorf("modelOf(%q)=%q want %q", in, got, want)
		}
	}
}
