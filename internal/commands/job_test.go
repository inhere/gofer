package commands

import (
	"reflect"
	"testing"

	"github.com/gookit/gcli/v3"
)

// parseRun runs the gcli arg pipeline (SplitRawArgs -> SetRawCmd ->
// NormalizeArgs -> app.Run) up to the point where `job run` binds its flags and
// positional, stopping before the network call. It returns the bound jobRunOpts
// snapshot, the resolved prompt and the captured rawCmd so tests can assert the
// JobRequest mapping without a live server.
func parseRun(t *testing.T, in []string) (project, agent, runner, cwd, prompt string, cmd []string) {
	t.Helper()
	// Reset shared state so tests don't leak into each other.
	jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner = "", "", ""
	jobRunOpts.cwd, jobRunOpts.prompt = "", ""
	SetRawCmd(nil)

	app := NewApp("test")
	// Replace job run's Func with a capturing one so we never hit the network.
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, args []string) error {
		// Mirror runJobRun's prompt resolution (flag > positional > args[0]).
		prompt = jobRunOpts.prompt
		if prompt == "" {
			if a := c.Arg("prompt"); a != nil {
				prompt = a.String()
			}
			if prompt == "" && len(args) > 0 {
				prompt = args[0]
			}
		}
		return nil
	}

	head, raw := SplitRawArgs(in)
	SetRawCmd(raw)
	// app.Run returns the process exit code (0 on success); flag-binding happens
	// inside, so a non-zero code here would signal a parse failure.
	if code := app.Run(NormalizeArgs(app, head)); code != 0 {
		t.Fatalf("app.Run exit code=%d for args %v", code, in)
	}
	return jobRunOpts.project, jobRunOpts.agent, jobRunOpts.runner, jobRunOpts.cwd, prompt, rawCmd
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
		t.Fatalf("rawCmd=%v want [go version]", cmd)
	}
}

func TestJobRunRawCmdWithFlagsInside(t *testing.T) {
	// Flags after `--` belong to the raw command, not to job run.
	_, _, _, _, _, cmd := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "test", "-run", "X"})
	if !reflect.DeepEqual(cmd, []string{"go", "test", "-run", "X"}) {
		t.Fatalf("rawCmd=%v want [go test -run X]", cmd)
	}
}

func TestJobRunPositionalPrompt(t *testing.T) {
	_, _, _, _, prompt, cmd := parseRun(t,
		[]string{"job", "run", "-p", "self", "-a", "claude", "summarize the repo"})
	if prompt != "summarize the repo" {
		t.Fatalf("prompt=%q want 'summarize the repo'", prompt)
	}
	if len(cmd) != 0 {
		t.Fatalf("rawCmd should be empty, got %v", cmd)
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

func TestSplitRawArgs(t *testing.T) {
	head, raw := SplitRawArgs([]string{"job", "run", "-p", "self", "--", "go", "version"})
	if !reflect.DeepEqual(head, []string{"job", "run", "-p", "self"}) {
		t.Fatalf("head=%v", head)
	}
	if !reflect.DeepEqual(raw, []string{"go", "version"}) {
		t.Fatalf("raw=%v", raw)
	}

	head, raw = SplitRawArgs([]string{"job", "show", "abc"})
	if raw != nil {
		t.Fatalf("raw should be nil without --, got %v", raw)
	}
	if !reflect.DeepEqual(head, []string{"job", "show", "abc"}) {
		t.Fatalf("head=%v", head)
	}
}
