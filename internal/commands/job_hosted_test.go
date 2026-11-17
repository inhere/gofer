package commands

import (
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// TestJobRunHostedFlag: `job run --hosted` is the caller's "this session never ends
// for being idle" switch (hosted resident session). The flag itself only rides to the
// request: whether it is legal without --session or next to an idle / max limit is
// Submit's call, so the CLI must not add a second, drifting rule.
func TestJobRunHostedFlag(t *testing.T) {
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	app := NewApp("test")
	var got job.JobRequest
	var buildErr error
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		got, buildErr = buildJobRunRequest(c, nil)
		return buildErr
	}

	jobRunOpts = jobRunFlags{}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "acpbot", "--prompt", "hi", "--session"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got.Hosted {
		t.Fatalf("Hosted = true without --hosted")
	}

	jobRunOpts = jobRunFlags{}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "acpbot", "--prompt", "hi", "--session", "--hosted"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if buildErr != nil {
		t.Fatalf("build with --hosted: %v", buildErr)
	}
	if !got.Hosted {
		t.Fatal("--hosted did not reach the request")
	}
	if !got.Session {
		t.Fatal("--hosted dropped --session")
	}

	// `--notify-session` accepts a session JOB id as well as a session id (hosted
	// resident session design §4): the CLI passes the target through untouched.
	jobRunOpts = jobRunFlags{}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--notify-session", "job-20261117-101010-abcdef12", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got.NotifySession != "job-20261117-101010-abcdef12" || !got.Notify {
		t.Fatalf("notify = %v / %q, want the job id as the target", got.Notify, got.NotifySession)
	}
}
