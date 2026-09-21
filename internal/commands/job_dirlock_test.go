package commands

import (
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// TestJobRunDirLockFlags: `job run --exclusive-dir` / `--shared-dir` are the caller's
// three-state override of the same-directory lock rule (JOB-11): leaving both off is
// nil (the server applies the default rule), and each flag pins the decision
// explicitly. The pair is contradictory and must be refused instead of letting the
// last one win.
func TestJobRunDirLockFlags(t *testing.T) {
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

	// Default: neither flag → the request leaves the decision to the server.
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if buildErr != nil {
		t.Fatalf("no flag must build a request: %v", buildErr)
	}
	if got.ExclusiveDir != nil {
		t.Fatalf("ExclusiveDir = %v, want nil (unresolved) when no flag was given", *got.ExclusiveDir)
	}

	jobRunOpts = jobRunFlags{} // gcli does not clear an unset flag: start clean
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--exclusive-dir", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got.ExclusiveDir == nil || !*got.ExclusiveDir {
		t.Fatalf("--exclusive-dir did not reach the request: %v", got.ExclusiveDir)
	}

	jobRunOpts = jobRunFlags{}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--shared-dir", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got.ExclusiveDir == nil || *got.ExclusiveDir {
		t.Fatalf("--shared-dir did not reach the request: %v", got.ExclusiveDir)
	}

	jobRunOpts = jobRunFlags{}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--exclusive-dir", "--shared-dir", "--", "go", "version"}); code == 0 {
		t.Fatalf("--exclusive-dir with --shared-dir must be refused, got exit code 0 (req=%+v)", got)
	}
}
