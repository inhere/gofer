package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// parseRun runs the gcli arg pipeline (NormalizeArgs -> app.Run) up to the point
// where `job run` binds its flags. It returns the bound jobRunOpts snapshot, the
// resolved prompt (--prompt flag) and the captured raw cmd (remainArgs, i.e. the
// tokens after `--` that gcli leaves for the Func handler), so tests can assert
// the JobRequest mapping without a live server.
func parseRun(t *testing.T, in []string) (project, agent, runner, cwd, prompt, plan string, cmd []string) {
	t.Helper()
	// Reset shared state so tests don't leak into each other.
	jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner = "", "", ""
	jobRunOpts.cwd, jobRunOpts.prompt, jobRunOpts.plan = "", "", ""
	jobRunOpts.agentArgs = nil
	jobRunOpts.interactive, jobRunOpts.cols, jobRunOpts.rows = false, 0, 0

	app := NewApp("test")
	// Replace job run's Func with a capturing one so we never hit the network.
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		// Mirror runJobRun: prompt from the --prompt flag; cmd from the arrayed
		// `cmd` arg (the post-`--` tokens gcli binds natively).
		prompt = jobRunOpts.prompt
		plan = jobRunOpts.plan
		if a := c.Arg("cmd"); a != nil {
			cmd = a.Strings()
		}
		return nil
	}

	// app.Run returns the process exit code (0 on success); flag-binding happens
	// inside, so a non-zero code here would signal a parse failure. gcli handles
	// `--` natively and binds the post-`--` tokens to the arrayed `cmd` arg.
	if code := app.Run(in); code != 0 {
		t.Fatalf("app.Run exit code=%d for args %v", code, in)
	}
	return jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner, jobRunOpts.cwd, prompt, plan, cmd
}

func TestJobRunRawCmdMapping(t *testing.T) {
	p, a, _, _, prompt, _, cmd := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "version"})
	if p != "self" || a != "exec" {
		t.Fatalf("flags not bound: project=%q agent=%q", p, a)
	}
	if prompt != "" {
		t.Fatalf("prompt should be empty for raw cmd, got %q", prompt)
	}
	if !reflect.DeepEqual(cmd, []string{"go", "version"}) {
		t.Fatalf("remainArgs=%v want [go version]", cmd)
	}
}

func TestJobRunRawCmdWithFlagsInside(t *testing.T) {
	// Flags after `--` belong to the raw command, not to job run.
	_, _, _, _, _, _, cmd := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "test", "-run", "X"})
	if !reflect.DeepEqual(cmd, []string{"go", "test", "-run", "X"}) {
		t.Fatalf("remainArgs=%v want [go test -run X]", cmd)
	}
}

func TestJobRunPromptFlag(t *testing.T) {
	// prompt is supplied via the --prompt flag (cli-agents); no positional arg.
	_, _, _, _, prompt, _, cmd := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "claude", "--prompt", "summarize the repo"})
	if prompt != "summarize the repo" {
		t.Fatalf("prompt=%q want 'summarize the repo'", prompt)
	}
	if len(cmd) != 0 {
		t.Fatalf("remainArgs should be empty, got %v", cmd)
	}
}

// TestJobRunRoleFlags verifies the E35 --role / --system-prompt flags bind onto
// jobRunOpts (so runJobRun threads them into the JobRequest).
func TestJobRunRoleFlags(t *testing.T) {
	jobRunOpts.role, jobRunOpts.systemPrompt = "", ""
	app := NewApp("test")
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(_ *gcli.Command, _ []string) error { return nil }
	if code := app.Run([]string{"job", "run", "-p", "self", "--role", "reviewer", "--system-prompt", "be strict"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if jobRunOpts.role != "reviewer" {
		t.Fatalf("--role not bound: %q", jobRunOpts.role)
	}
	if jobRunOpts.systemPrompt != "be strict" {
		t.Fatalf("--system-prompt not bound: %q", jobRunOpts.systemPrompt)
	}
}

func TestJobRunPlanFlagBuildsRequest(t *testing.T) {
	_, _, _, _, _, plan, _ := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "exec", "--plan", "plan-cli", "--", "go", "version"})
	if plan != "plan-cli" {
		t.Fatalf("--plan not bound: %q", plan)
	}

	app := NewApp("test")
	var got string
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, nil)
		if err != nil {
			return err
		}
		got = req.PlanID
		return nil
	}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--plan", "plan-cli", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got != "plan-cli" {
		t.Fatalf("JobRequest.PlanID = %q, want plan-cli", got)
	}
}

func TestJobRunInteractiveFlagsBuildRequest(t *testing.T) {
	jobRunOpts = struct {
		project      string
		agent        string
		runner       string
		cwd          string
		prompt       string
		timeout      int
		title        string
		wait         bool
		sync         bool
		waitTimeout  int
		file         string
		workerID     string
		workerLabels string
		tags         string
		plan         string
		channel      string
		role         string
		systemPrompt string
		agentArgs    gcli.Strings
		interactive  bool
		cols         int
		rows         int
	}{}

	app := NewApp("test")
	var gotInteractive bool
	var gotCols, gotRows int
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, nil)
		if err != nil {
			return err
		}
		gotInteractive = req.Interactive
		gotCols = req.Cols
		gotRows = req.Rows
		return nil
	}

	code := app.Run([]string{
		"job", "run", "-p", "self", "-a", "term-agent", "--runner", "worker",
		"--interactive", "--cols", "120", "--rows", "32", "--prompt", "hello",
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if !gotInteractive || gotCols != 120 || gotRows != 32 {
		t.Fatalf("interactive request = (%v,%d,%d), want (true,120,32)", gotInteractive, gotCols, gotRows)
	}
}

// TestJobRunInteractiveIgnoresSync verifies guardInteractiveSync (tools-l8p): an
// --interactive --sync combo must not block the submit response, so the command
// forces Sync back to false before buildJobRunRequest runs.
func TestJobRunInteractiveIgnoresSync(t *testing.T) {
	jobRunOpts = struct {
		project      string
		agent        string
		runner       string
		cwd          string
		prompt       string
		timeout      int
		title        string
		wait         bool
		sync         bool
		waitTimeout  int
		file         string
		workerID     string
		workerLabels string
		tags         string
		plan         string
		channel      string
		role         string
		systemPrompt string
		agentArgs    gcli.Strings
		interactive  bool
		cols         int
		rows         int
	}{}

	app := NewApp("test")
	var gotSync bool
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		guardInteractiveSync(c)
		req, err := buildJobRunRequest(c, nil)
		if err != nil {
			return err
		}
		gotSync = req.Sync
		return nil
	}

	code := app.Run([]string{
		"job", "run", "-p", "self", "-a", "term-agent", "--interactive", "--sync", "--prompt", "hello",
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotSync {
		t.Fatalf("req.Sync = true, want false (--sync must be ignored for --interactive)")
	}
	if !jobRunOpts.interactive {
		t.Fatalf("--interactive not bound")
	}
}

func TestJobRunAgentArgFlagsBuildRequest(t *testing.T) {
	jobRunOpts.agentArgs = nil
	app := NewApp("test")
	var got []string
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, nil)
		if err != nil {
			return err
		}
		got = req.AgentArgs
		return nil
	}

	code := app.Run([]string{
		"job", "run", "-p", "self", "-a", "codex", "--prompt", "hi",
		"--agent-arg", "--x", "--agent-arg", "1",
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if !reflect.DeepEqual(got, []string{"--x", "1"}) {
		t.Fatalf("agent_args = %#v, want [--x 1]", got)
	}
}

func TestJobRunDefaults(t *testing.T) {
	_, _, runner, cwd, _, _, _ := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "exec", "--", "ls"})
	if runner != "local" {
		t.Fatalf("runner default=%q want local", runner)
	}
	if cwd != "." {
		t.Fatalf("cwd default=%q want .", cwd)
	}
}

// TestJobResumeRegistered verifies the `job resume` sub-command is in the group's
// Subs (GetCommand only resolves after the tree is bound via app.Run, so inspect
// Subs directly — mirrors TestAgentCmdSubsRegistered).
func TestJobResumeRegistered(t *testing.T) {
	found := false
	for _, sub := range NewJobCmd().Subs {
		if sub.Name == "resume" {
			found = true
		}
	}
	if !found {
		t.Fatal("job group missing `resume` sub-command")
	}
}

// TestJobResumeFlagsBound drives the gcli pipeline up to `job resume`'s Func with
// a capturing replacement, asserting --prompt / --runner / the <id> arg bind
// (without hitting the network).
func TestJobResumeFlagsBound(t *testing.T) {
	jobResumeOpts.prompt, jobResumeOpts.runner = "", ""

	app := NewApp("test")
	var gotID string
	resumeCmd := app.GetCommand("job").GetCommand("resume")
	resumeCmd.Func = func(c *gcli.Command, _ []string) error {
		gotID = argID(c)
		return nil
	}

	if code := app.Run([]string{"job", "resume", "job-123", "--prompt", "what number", "--runner", "local"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotID != "job-123" {
		t.Fatalf("id arg = %q, want job-123", gotID)
	}
	if jobResumeOpts.prompt != "what number" {
		t.Fatalf("--prompt = %q, want 'what number'", jobResumeOpts.prompt)
	}
	if jobResumeOpts.runner != "local" {
		t.Fatalf("--runner = %q, want local", jobResumeOpts.runner)
	}
}

func TestJobRerunCallsRebuildEndpoint(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	jobRerunOpts.watch = false

	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/jobs/job-1/request" || r.URL.Path == "/v1/jobs" {
			t.Fatalf("job rerun must not use legacy request/submit path: %s %s", r.Method, r.URL.Path)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs/job-1/rebuild" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		called = true
		var ov job.RebuildOverrides
		if err := json.NewDecoder(r.Body).Decode(&ov); err != nil {
			t.Fatalf("decode rebuild body: %v", err)
		}
		if ov.ProjectKey != nil || len(ov.EnvSet) != 0 || len(ov.EnvUnset) != 0 {
			t.Fatalf("rerun should send empty overrides, got %+v", ov)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{
			ID: "job-new", Status: job.StatusQueued, SourceJobID: "job-1",
		})
	}))
	defer ts.Close()

	app := NewApp("test")
	if code := app.Run([]string{"job", "rerun", "--server", ts.URL, "job-1"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if !called {
		t.Fatal("rebuild endpoint was not called")
	}
}

// isolateConfigEnv chdir's into a fresh empty temp dir (no ./.gofer[.local].yaml)
// and redirects GOFER_CONFIG_DIR to another empty temp dir (no ~/.config/gofer
// picked up), and clears GOFER_CONFIG, so config.Load resolves no config file
// unless the test passes an explicit path. Mirrors the isolation pattern used by
// config_test.go's TestInitServerGlobalPath.
func isolateConfigEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv(config.EnvConfigPath, "")
	t.Setenv(config.EnvConfigDir, t.TempDir())
}

// TestNewClientNoConfigNoServerFails covers example-project-3a4: when no config
// file is found AND no --server/-s (nor GOFER_SERVER_ADDR, which gcli would have
// already interpolated into serverFlag) is given, newClient must fail fast with a
// clear reason instead of silently falling back to config.DefaultAddr and later
// failing with a confusing connection error.
func TestNewClientNoConfigNoServerFails(t *testing.T) {
	isolateConfigEnv(t)

	_, err := newClient("", "", "")
	if err == nil {
		t.Fatal("want error when config missing and no --server given, got nil")
	}
	if !strings.Contains(err.Error(), "未找到配置文件") {
		t.Fatalf("error message should explain the missing config/server cause, got: %v", err)
	}
}

// TestNewClientExplicitServerBypassesConfigCheck verifies an explicit --server
// still works even with no config file present (must not be caught by the
// fail-fast check added for example-project-3a4).
func TestNewClientExplicitServerBypassesConfigCheck(t *testing.T) {
	isolateConfigEnv(t)

	cli, err := newClient("", "127.0.0.1:8765", "")
	if err != nil {
		t.Fatalf("newClient with explicit --server should succeed, got: %v", err)
	}
	if cli == nil {
		t.Fatal("want non-nil client")
	}
}

// TestNewClientConfigPresentNoServerFlagStillWorks verifies the fail-fast check
// does not misfire when a real config file is found (path != ""), even though
// --server is empty and server.addr falls back to config.DefaultAddr.
func TestNewClientConfigPresentNoServerFlagStillWorks(t *testing.T) {
	isolateConfigEnv(t)
	cfgPath := writeRawConfig(t, "server:\n  addr: 127.0.0.1:9999\n")

	cli, err := newClient(cfgPath, "", "")
	if err != nil {
		t.Fatalf("newClient with a resolved config should succeed, got: %v", err)
	}
	if cli == nil {
		t.Fatal("want non-nil client")
	}
}
