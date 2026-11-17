package commands

import (
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// TestJobRunPermissionTierFlag: --permission-tier reaches JobRequest.PermissionTier; without
// it the request carries none (so request_json is unchanged).
func TestJobRunPermissionTierFlag(t *testing.T) {
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	app := NewApp("test")
	var got job.JobRequest
	var gotErr error
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		got, gotErr = buildJobRunRequest(c, nil)
		return nil
	}
	run := func(extra ...string) {
		jobRunOpts = jobRunFlags{}
		args := append([]string{"job", "run", "-p", "self", "-a", "claude-acp", "--prompt", "x"}, extra...)
		if code := app.Run(args); code != 0 {
			t.Fatalf("exit %d", code)
		}
	}
	run()
	if gotErr != nil || got.PermissionTier != "" {
		t.Fatalf("no flag: tier = %q err=%v, want none", got.PermissionTier, gotErr)
	}
	run("--permission-tier", "ask")
	if gotErr != nil || got.PermissionTier != "ask" {
		t.Fatalf("tier = %q err=%v, want ask", got.PermissionTier, gotErr)
	}
}
