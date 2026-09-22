package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

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
	jobRunOpts.todo = ""
	jobRunOpts.agentArgs = nil
	jobRunOpts.interactive, jobRunOpts.cols, jobRunOpts.rows = false, 0, 0
	jobRunOpts.worktree, jobRunOpts.worktreeBase = false, ""

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

// TestJobRunWorktreeFlags verifies WT-01 --worktree / --worktree-base bind onto
// jobRunOpts AND survive the mapping into the JobRequest (a flag that parses but is
// never copied into the request would silently run the job in the shared checkout).
func TestJobRunWorktreeFlags(t *testing.T) {
	jobRunOpts.worktree, jobRunOpts.worktreeBase = false, ""
	var got job.JobRequest
	app := NewApp("test")
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, nil)
		if err != nil {
			return err
		}
		got = req
		return nil
	}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--worktree", "--worktree-base", "v1.2.0", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if !got.Worktree {
		t.Fatal("--worktree was not mapped onto JobRequest.Worktree")
	}
	if got.WorktreeBase != "v1.2.0" {
		t.Fatalf("JobRequest.WorktreeBase = %q, want v1.2.0", got.WorktreeBase)
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

// TestJobRunTodoFlag: `job run --todo <id>` binds the flag onto the submitted
// request (SUP-01 C: the linkage the hub resolves into a plan and a checklist
// update), and leaving it out submits a plain job.
func TestJobRunTodoFlag(t *testing.T) {
	jobRunOpts = jobRunFlags{}

	app := NewApp("test")
	var got job.JobRequest
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		req, err := buildJobRunRequest(c, nil)
		if err != nil {
			return err
		}
		got = req
		return nil
	}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--todo", "todo-cli", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got.TodoID != "todo-cli" {
		t.Fatalf("JobRequest.TodoID = %q, want todo-cli", got.TodoID)
	}

	jobRunOpts = jobRunFlags{} // gcli does not clear an unset flag: start clean
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got.TodoID != "" {
		t.Fatalf("JobRequest.TodoID = %q, want empty without --todo", got.TodoID)
	}
}

func TestJobRunInteractiveFlagsBuildRequest(t *testing.T) {
	jobRunOpts = jobRunFlags{}

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
	jobRunOpts = jobRunFlags{}

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
	if runner != "server" {
		t.Fatalf("runner default=%q want server", runner)
	}
	if cwd != "." {
		t.Fatalf("cwd default=%q want .", cwd)
	}
}

func TestJobRunRunnerAliasesNormalizeRequest(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{input: "", want: "local"},
		{input: "server", want: "local"},
		{input: "local", want: "local"},
		{input: "remote-w1", want: "remote-w1"},
	}
	for _, tc := range cases {
		if got := normalizeJobRunner(tc.input); got != tc.want {
			t.Errorf("normalizeJobRunner(%q)=%q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestJobRunRunnerAliasBuildsServerLocalRequest(t *testing.T) {
	for _, input := range []string{"server", "local"} {
		jobRunOpts = jobRunFlags{}
		app := NewApp("test")
		var got string
		runCmd := app.GetCommand("job").GetCommand("run")
		runCmd.Func = func(c *gcli.Command, _ []string) error {
			req, err := buildJobRunRequest(c, nil)
			if err != nil {
				return err
			}
			got = req.Runner
			return nil
		}
		if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--runner", input}); code != 0 {
			t.Fatalf("app.Run exit code=%d for runner %q", code, input)
		}
		if got != "local" {
			t.Fatalf("runner %q built request runner=%q, want local", input, got)
		}
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

// TestJobRunPrintsClampWarning: when the server truncates --timeout to the project
// ceiling, `job run` must SAY SO on stderr (bd h-aii-s9ck) instead of leaving the
// caller believing the job got the budget it asked for.
func TestJobRunPrintsClampWarning(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	jobRunOpts.timeout, jobRunOpts.wait, jobRunOpts.sync = 0, false, false
	t.Cleanup(func() { jobRunOpts.timeout, jobRunOpts.wait, jobRunOpts.sync = 0, false, false })

	var gotReq job.JobRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		// What a server with a 3600s ceiling returns for --timeout 5400.
		_ = json.NewEncoder(w).Encode(job.JobResult{
			ID: "job-1", Status: job.StatusQueued,
			TimeoutSec: 3600, RequestedTimeoutSec: 5400, TimeoutClamped: true,
		})
	}))
	defer ts.Close()

	var code int
	errOut := captureStderr(t, func() {
		code = NewApp("test").Run([]string{
			"job", "run", "-p", "self", "-a", "exec", "--timeout", "5400",
			"--server", ts.URL, "--", "go", "version",
		})
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotReq.TimeoutSec != 5400 {
		t.Fatalf("client must send the REQUESTED timeout (the server clamps), got %d", gotReq.TimeoutSec)
	}
	want := "warning: --timeout 5400s exceeds the project ceiling (3600s); the job will run with 3600s"
	if !strings.Contains(errOut, want) {
		t.Fatalf("stderr missing the clamp warning\nwant substring: %s\ngot: %q", want, errOut)
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

// TestJobShowPrintsRecoveringSince: RECOV-01. A job held `recovering` (its worker's
// connection dropped; the hub is holding it for the reconnect window) must show WHEN
// it entered that state — otherwise `job show` gives no hint that the job is waiting
// on a worker instead of running. The line is omitted for a job that never recovered.
func TestJobShowPrintsRecoveringSince(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })

	const since = int64(1_700_000_000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// `job show` also reads the job's wakeups (JOB-09); an empty list is the
		// normal answer here and prints no line.
		if r.URL.Path == "/v1/jobs/job-rec/wakeups" {
			_, _ = w.Write([]byte(`{"wakeups":[]}`))
			return
		}
		// ...and its retry chain (AUTO-03), which prints no line when nothing is
		// waiting either.
		if r.URL.Path == "/v1/jobs/job-rec/retries" {
			_, _ = w.Write([]byte(`{"job_id":"job-rec","retries":[]}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/jobs/job-rec" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{
			ID: "job-rec", ProjectKey: "self", Status: job.StatusRecovering,
			WorkerID: "w1", RecoveringSince: since,
		})
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	app := NewApp("test")
	errOut := captureOutput(t, func() {
		if code := app.Run([]string{"job", "show", "job-rec", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if strings.Contains(errOut, "requires an <id>") {
		t.Fatalf("job show rejected the id argument:\n%s", errOut)
	}
	if !strings.Contains(errOut, "status:     "+job.StatusRecovering) {
		t.Fatalf("job show must report the recovering status:\n%s", errOut)
	}
	want := "recovering_since: " + formatStarted(since)
	if !strings.Contains(errOut, want) {
		t.Fatalf("job show output missing %q:\n%s", want, errOut)
	}
}

// TestJobShowOmitsRecoveringSinceWhenUnset: the field is 0 for a job that is not (or
// no longer) recovering, and then the line must not appear at all.
func TestJobShowOmitsRecoveringSinceWhenUnset(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-run", ProjectKey: "self", Status: job.StatusRunning})
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{"job", "show", "job-run", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if strings.Contains(out, "recovering_since") {
		t.Fatalf("a running job must not show recovering_since:\n%s", out)
	}
}

// TestFmtServerTimeUsesServerOffset is the h-aii-tnua regression: a stamp is
// rendered in the SERVER's zone with its UTC offset, not in whatever zone the CLI
// process happens to run in. The offset is pinned through the resolution seam, so
// the assertion is independent of the test host's own timezone.
func TestFmtServerTimeUsesServerOffset(t *testing.T) {
	t.Cleanup(func() { setServerTZ(0, false) })

	// +08:00 (the deployment's zone): 2026-09-22 20:13:20 +08:00.
	setServerTZ(8*3600, true)
	got := fmtServerTime(1789997000)
	if !strings.Contains(got, "+08:00") {
		t.Fatalf("fmtServerTime = %q, want a +08:00 suffix", got)
	}
	if want := time.Unix(1789997000, 0).In(time.FixedZone("", 8*3600)).Format("2006-01-02 15:04:05"); !strings.HasPrefix(got, want) {
		t.Fatalf("fmtServerTime = %q, want it to start with %q", got, want)
	}

	// A negative offset renders as -05:30 (not "+-5:30").
	setServerTZ(-5*3600-1800, true)
	if got := fmtServerTime(1789997000); !strings.Contains(got, "-05:30") {
		t.Fatalf("fmtServerTime = %q, want a -05:30 suffix", got)
	}

	// No offset available (offline / older server): fall back to local AND say so.
	setServerTZ(0, false)
	got = fmtServerTime(1789997000)
	if !strings.HasSuffix(got, " (local)") {
		t.Fatalf("fmtServerTime fallback = %q, want a (local) marker", got)
	}
	// 0 / unset stays "-" (the CLI's "never happened" convention).
	if got := fmtServerTime(0); got != "-" {
		t.Fatalf("fmtServerTime(0) = %q, want -", got)
	}

	// formatStarted / probeTime / formatScheduleTime all funnel through it.
	setServerTZ(8*3600, true)
	if got := formatStarted(1789997000); !strings.Contains(got, "+08:00") {
		t.Fatalf("formatStarted = %q, want the server offset", got)
	}
	if got := probeTime(1789997000); !strings.Contains(got, "+08:00") {
		t.Fatalf("probeTime = %q, want the server offset", got)
	}
	if got := formatScheduleTime(1789997000); !strings.Contains(got, "+08:00") {
		t.Fatalf("formatScheduleTime = %q, want the server offset", got)
	}
	// A run within a day collapses to the clock (still the server's clock); a
	// further one keeps the full stamp WITH the offset.
	near := time.Now().Unix() + 3600
	if got, want := formatScheduleListTime(near), time.Unix(near, 0).In(time.FixedZone("", 8*3600)).Format("15:04:05"); got != want {
		t.Fatalf("formatScheduleListTime(near) = %q, want the server clock %q", got, want)
	}
	far := time.Now().Unix() + 30*24*3600
	if got := formatScheduleListTime(far); !strings.HasSuffix(got, "+08:00") {
		t.Fatalf("formatScheduleListTime(far) = %q, want the server offset", got)
	}
}
