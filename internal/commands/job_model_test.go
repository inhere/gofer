package commands

import (
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// TestJobRunModelFlag: --model reaches JobRequest.Model (trimmed); without it the
// request carries none, so the argv a cli-agent renders is untouched.
func TestJobRunModelFlag(t *testing.T) {
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	app := NewApp("test")
	var got job.JobRequest
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) (err error) {
		got, err = buildJobRunRequest(c, nil)
		return err
	}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "claude", "--prompt", "x"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got.Model != "" {
		t.Fatalf("Model = %q without --model", got.Model)
	}
	jobRunOpts = jobRunFlags{}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "claude", "--prompt", "x", "--model", " opus "}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got.Model != "opus" {
		t.Fatalf("Model = %q, want opus", got.Model)
	}
}

func TestJobResumeModelFlagBound(t *testing.T) {
	jobResumeOpts.model = ""
	app := NewApp("test")
	resumeCmd := app.GetCommand("job").GetCommand("resume")
	resumeCmd.Func = func(*gcli.Command, []string) error { return nil }
	if code := app.Run([]string{"job", "resume", "job-1", "--prompt", "p", "--model", "sonnet"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if jobResumeOpts.model != "sonnet" {
		t.Fatalf("--model = %q", jobResumeOpts.model)
	}
}

// TestJobRunFromSessionFlag (gofer-f4z8): --from-session reaches JobRequest.FromSession
// (trimmed); without it the request carries none.
func TestJobRunFromSessionFlag(t *testing.T) {
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	app := NewApp("test")
	var got job.JobRequest
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) (err error) {
		got, err = buildJobRunRequest(c, nil)
		return err
	}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "suag", "--prompt", "x"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got.FromSession != "" {
		t.Fatalf("FromSession = %q without --from-session", got.FromSession)
	}
	jobRunOpts = jobRunFlags{}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "suag", "--prompt", "x", "--from-session", " s-old "}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got.FromSession != "s-old" {
		t.Fatalf("FromSession = %q, want s-old", got.FromSession)
	}
}
