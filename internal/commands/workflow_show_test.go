package commands

import (
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
)

// TestWorkflowShowChainColumns pins gofer-plkx: the chain table carries each row's STARTED
// time and, under --verbose, its working directory (plus the ref a ref: step checked out),
// so a human can tell WHERE and WHEN a step / fan actually ran.
func TestWorkflowShowChainColumns(t *testing.T) {
	code := 0
	step := client.WorkflowStep{
		StepIndex: 1, FanIndex: 1, FanName: "linux-race", Name: "checks",
		Runner: "local", Status: "done", ExitCode: &code, DurationSec: 42,
		StartedAt: 1_700_000_000, Cwd: "/tmp/ref-checkout", Ref: "abcdef1234567890abcdef",
	}
	out := captureOutput(t, func() {
		printStepChain(gcli.NewCommand("show", "", nil), []client.WorkflowStep{step})
		printStepDirs(gcli.NewCommand("show", "", nil), []client.WorkflowStep{step})
	})
	for _, want := range []string{
		"STARTED",           // the new column header
		"linux-race",        // the fan name still names the row
		"42s",               // duration unchanged
		"workdirs:",         // --verbose block
		"/tmp/ref-checkout", // the directory it ran in
		"ref abcdef123456",  // the checked-out commit, shortened
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("wf show output missing %q:\n%s", want, out)
		}
	}
}

// TestWorkflowListColumns keeps `wf list` honest about its STARTED column: a running
// workflow has no duration yet, a finished one shows created_at→updated_at.
func TestWorkflowListColumns(t *testing.T) {
	if got := workflowDurationCol(client.Workflow{Status: "running", CreatedAt: 100, UpdatedAt: 200}); got != "-" {
		t.Fatalf("running workflow duration = %q, want -", got)
	}
	if got := workflowDurationCol(client.Workflow{Status: "done", CreatedAt: 100, UpdatedAt: 165}); got != "1m5s" {
		t.Fatalf("finished workflow duration = %q, want 1m5s", got)
	}
	if got := startedCol(0); got != "-" {
		t.Fatalf("startedCol(0) = %q, want -", got)
	}
	if got := startedCol(1_700_000_000); got == "-" || got == "" {
		t.Fatalf("startedCol(unix) = %q, want a formatted time", got)
	}
}
