package commands

import (
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// TestJobRunStallFlags: AUTO-05 的停滞窗口在 CLI 上是三态——什么都不给 = nil（server 按
// request > agent > server 解析）、--stall-timeout N = 显式窗口、--no-stall = 显式关。
// gcli 分不清"没给"与"给了 0"，所以"关"由独立的布尔开关表达（与 --verify/--no-verify 同理）；
// 两者同时给是自相矛盾，必须拒绝而不是让后者悄悄赢。
func TestJobRunStallFlags(t *testing.T) {
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

	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if buildErr != nil {
		t.Fatalf("no flag must build a request: %v", buildErr)
	}
	if got.StallTimeoutSec != nil {
		t.Fatalf("StallTimeoutSec = %v, want nil (unresolved) when no flag was given", *got.StallTimeoutSec)
	}

	jobRunOpts = jobRunFlags{} // gcli does not clear an unset flag: start clean
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--stall-timeout", "120", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got.StallTimeoutSec == nil || *got.StallTimeoutSec != 120 {
		t.Fatalf("--stall-timeout did not reach the request: %v", got.StallTimeoutSec)
	}

	jobRunOpts = jobRunFlags{}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--no-stall", "--", "go", "version"}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if got.StallTimeoutSec == nil || *got.StallTimeoutSec != 0 {
		t.Fatalf("--no-stall must reach the request as an explicit 0, got %v", got.StallTimeoutSec)
	}

	jobRunOpts = jobRunFlags{}
	if code := app.Run([]string{"job", "run", "-p", "self", "-a", "exec", "--no-stall", "--stall-timeout", "60", "--", "go", "version"}); code == 0 {
		t.Fatalf("--no-stall with --stall-timeout must be refused, got exit code 0 (req=%+v)", got)
	}
}
