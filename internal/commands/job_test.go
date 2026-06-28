package commands

import (
	"reflect"
	"testing"

	"github.com/gookit/gcli/v3"
)

// parseRun runs the gcli arg pipeline (NormalizeArgs -> app.Run) up to the point
// where `job run` binds its flags. It returns the bound jobRunOpts snapshot, the
// resolved prompt (--prompt flag) and the captured raw cmd (remainArgs, i.e. the
// tokens after `--` that gcli leaves for the Func handler), so tests can assert
// the JobRequest mapping without a live server.
func parseRun(t *testing.T, in []string) (project, agent, runner, cwd, prompt string, cmd []string) {
	t.Helper()
	// Reset shared state so tests don't leak into each other.
	jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner = "", "", ""
	jobRunOpts.cwd, jobRunOpts.prompt = "", ""

	app := NewApp("test")
	// Replace job run's Func with a capturing one so we never hit the network.
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		// Mirror runJobRun: prompt from the --prompt flag; cmd from the arrayed
		// `cmd` arg (the post-`--` tokens gcli binds natively).
		prompt = jobRunOpts.prompt
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
	return jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner, jobRunOpts.cwd, prompt, cmd
}

func TestJobRunRawCmdMapping(t *testing.T) {
	p, a, _, _, prompt, cmd := parseRun(t,
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
	_, _, _, _, _, cmd := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "test", "-run", "X"})
	if !reflect.DeepEqual(cmd, []string{"go", "test", "-run", "X"}) {
		t.Fatalf("remainArgs=%v want [go test -run X]", cmd)
	}
}

func TestJobRunPromptFlag(t *testing.T) {
	// prompt is supplied via the --prompt flag (cli-agents); no positional arg.
	_, _, _, _, prompt, cmd := parseRun(t,
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

func TestJobRunDefaults(t *testing.T) {
	_, _, runner, cwd, _, _ := parseRun(t,
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
