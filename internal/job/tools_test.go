package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
)

// declaredTools is the CFG-16 declaration the tests below give the executing machine.
var declaredTools = []config.Tool{
	{Name: "ffmpeg", Desc: "transcoder"},
	{Name: "sim", Bin: "/opt/sim", Desc: "simulator"},
}

func withTools(c *config.Config) { c.Tools = declaredTools }

// TestToolsSectionFollowsRulesAndSkills: a job this machine runs gets the declared
// tools section AFTER the injected rules and the skills list and BEFORE the caller's
// text — rules are the frame, skills and tools the content. request_json keeps the
// caller's own prompt, so a rerun never stacks a second section.
func TestToolsSectionFollowsRulesAndSkills(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, _ := newSkillService(t, root, withTools, "house-rules")
	rulesFile := filepath.Join(root, "work", filepath.FromSlash(projectRulesFile))
	if err := os.MkdirAll(filepath.Dir(rulesFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesFile, []byte("keep it small\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const body = "TOOLS-PROMPT-BODY"
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: body, TimeoutSec: 30,
		Skills: []string{"house-rules"},
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	prompt := executedPromptOf(t, final)
	iRules := strings.Index(prompt, rulesPromptEnd)
	iSkills := strings.Index(prompt, skillsPromptHeader)
	iTools := strings.Index(prompt, toolsPromptHeader)
	iBody := strings.Index(prompt, body)
	if iRules < 0 || iSkills < 0 || iTools < 0 || iBody < 0 || !(iRules < iSkills && iSkills < iTools && iTools < iBody) {
		t.Fatalf("want rules < skills < tools < body, got %d/%d/%d/%d:\n%s", iRules, iSkills, iTools, iBody, prompt)
	}
	for _, line := range []string{"- `ffmpeg` — transcoder", "- `sim`（`/opt/sim`）— simulator"} {
		if !strings.Contains(prompt, line) {
			t.Fatalf("tools section misses %q:\n%s", line, prompt)
		}
	}
	if strings.Contains(prompt, "worker `") {
		t.Fatalf("a server-local job must not name a worker:\n%s", prompt)
	}
	if got := promptOf(t, final); strings.Contains(got, toolsPromptHeader) {
		t.Fatalf("request_json must keep the caller's text, got %q", got)
	}
}

// TestToolsSectionOnWorkerReentry: the worker's own Submit (RulesResolved and
// SkillsResolved set, CredentialExternal, GOFER_WORKER_ID in env) DOES render its
// tools — the "already resolved" markers are not the skip signal — and names the
// worker it runs on.
func TestToolsSectionOnWorkerReentry(t *testing.T) {
	t.Parallel()
	s, _ := newSkillService(t, t.TempDir(), withTools)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "local",
		Cwd: ".", Prompt: "worker body", TimeoutSec: 30,
		RulesResolved: true, SkillsResolved: true, CredentialExternal: true,
		Env: map[string]string{EnvWorkerID: "w-test"},
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	prompt := executedPromptOf(t, final)
	if !strings.HasPrefix(prompt, toolsPromptHeader) || !strings.Contains(prompt, "本任务运行在 worker `w-test` 上") {
		t.Fatalf("worker re-entry prompt should open with the tools section naming the worker:\n%s", prompt)
	}
	if !strings.HasSuffix(prompt, "worker body") {
		t.Fatalf("caller text must follow the section:\n%s", prompt)
	}
}

// TestToolsSectionHubForwardsWithout: a hub forwarding to a worker renders nothing
// (the worker renders its own), and the caller's --no-rules travels on the Forward so
// the worker can skip its section too.
func TestToolsSectionHubForwardsWithout(t *testing.T) {
	t.Parallel()
	stub := &stubPeerRunner{}
	s, _ := newSkillServiceRunners(t, t.TempDir(), func(c *config.Config) {
		withTools(c)
		c.Server.Workers = map[string]config.WorkerAuthConfig{"w1": {Token: "tok-w1"}}
		c.Runners = map[string]config.RunnerConfig{"remote-w1": {Type: "worker", WorkerID: "w1"}}
		p := c.Projects["self"]
		p.AllowedRunners = []string{"local", "remote-w1"}
		c.Projects["self"] = p
	}, map[string]runner.Runner{"remote-w1": stub})

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "ok", Runner: "remote-w1",
		Cwd: ".", Prompt: "hub body", TimeoutSec: 30, NoRules: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("job status = %s (err=%s)", final.Status, final.Error)
	}
	if strings.Contains(stub.got.Prompt, toolsPromptHeader) {
		t.Fatalf("the hub must not render its tools into a forwarded prompt:\n%s", stub.got.Prompt)
	}
	if !stub.got.NoRules {
		t.Fatal("Forward.NoRules = false, want the caller's --no-rules carried to the executor")
	}
}

// TestToolsPromptSectionSkips covers every documented skip and the worker-id trust
// rule: only a dispatched job (CredentialExternal) is believed about its worker.
func TestToolsPromptSectionSkips(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Tools:  declaredTools,
		Agents: map[string]config.AgentConfig{"ok": {Type: agent.TypeCLIAgent, Command: "ok"}},
	}
	base := JobRequest{Agent: "ok"}
	if got := toolsPromptSection(cfg, &base); !strings.HasPrefix(got, toolsPromptHeader) {
		t.Fatalf("plain agent job: section = %q", got)
	}
	cases := map[string]func(r *JobRequest){
		"exec agent":         func(r *JobRequest) { r.Agent = agent.ExecAgentKey },
		"resume":             func(r *JobRequest) { r.ResumedFrom = "src" },
		"resume carrier":     func(r *JobRequest) { r.ResumeSourceAgent = "ok" },
		"no_rules":           func(r *JobRequest) { r.NoRules = true },
		"unresolvable agent": func(r *JobRequest) { r.Agent = "ghost" },
	}
	for name, mut := range cases {
		req := base
		mut(&req)
		if got := toolsPromptSection(cfg, &req); got != "" {
			t.Errorf("%s: section = %q, want none", name, got)
		}
	}
	if got := toolsPromptSection(&config.Config{Agents: cfg.Agents}, &base); got != "" {
		t.Errorf("no declaration: section = %q, want none", got)
	}

	spoofed := JobRequest{Agent: "ok", Env: map[string]string{EnvWorkerID: "fake"}}
	if got := toolsPromptSection(cfg, &spoofed); strings.Contains(got, "fake") {
		t.Errorf("a local job's own env must not name a worker: %q", got)
	}
	dispatched := JobRequest{Agent: "ok", CredentialExternal: true, Env: map[string]string{EnvWorkerID: "w9"}}
	if got := toolsPromptSection(cfg, &dispatched); !strings.Contains(got, "worker `w9`") {
		t.Errorf("dispatched job: section = %q, want the worker id", got)
	}
}
