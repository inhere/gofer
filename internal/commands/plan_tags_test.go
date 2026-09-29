package commands

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestPlanTagsCreateUpdateFilter(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	server := newPlanTestServer(t)

	run := func(args ...string) string {
		t.Helper()
		planListOpts.tags = nil
		var code int
		out := captureOutput(t, func() { code = NewApp("test").Run(args) })
		if code != 0 {
			t.Fatalf("command %v exit code=%d output=%s", args, code, out)
		}
		return out
	}
	run("plan", "create", "--server", server, "--plan-id", "plan-cli-tags", "--title", "tagged", "--tags", "alpha,beta")
	out := run("plan", "list", "--server", server, "--tag", "alpha", "--tag", "beta")
	if !strings.Contains(out, "plan-cli-tags") {
		t.Fatalf("tag intersection output=%s", out)
	}
	run("plan", "set", "--server", server, "plan-cli-tags", "--tags", "gamma,beta", "--untag", "beta")
	out = run("plan", "list", "--server", server, "--tag", "gamma")
	if !strings.Contains(out, "plan-cli-tags") {
		t.Fatalf("updated tag output=%s", out)
	}
	out = run("plan", "list", "--server", server, "--tag", "beta")
	if strings.Contains(out, "plan-cli-tags") {
		t.Fatalf("removed tag still matched: %s", out)
	}
}
