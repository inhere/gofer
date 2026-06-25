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
