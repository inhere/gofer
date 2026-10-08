package commands

import (
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/job"
)

// TestJobRunBudgetFlags: --max-tokens / --max-cost / --max-turns reach JobRequest.Budget;
// without them the request carries none (so request_json is unchanged).
func TestJobRunBudgetFlags(t *testing.T) {
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
		args := append([]string{"job", "run", "-p", "self", "-a", "claude", "--prompt", "x"}, extra...)
		if code := app.Run(args); code != 0 {
			t.Fatalf("exit %d", code)
		}
	}
	run()
	if gotErr != nil || got.Budget != nil {
		t.Fatalf("no flags: budget = %+v err=%v, want none", got.Budget, gotErr)
	}
	run("--max-tokens", "50k", "--max-cost", "2.5", "--max-turns", "20")
	if gotErr != nil || got.Budget == nil || *got.Budget != (job.Budget{MaxTokens: 50_000, MaxCostUSD: 2.5, MaxTurns: 20}) {
		t.Fatalf("budget = %+v err=%v", got.Budget, gotErr)
	}
	run("--max-cost", "nope")
	if gotErr == nil || !strings.Contains(gotErr.Error(), "--max-cost") {
		t.Fatalf("a bad --max-cost must be refused, err=%v", gotErr)
	}
}

func TestParseTokenCount(t *testing.T) {
	for in, want := range map[string]int64{"1000": 1000, "50k": 50_000, "1.5M": 1_500_000, "2_000": 2000} {
		if got, err := parseTokenCount(in); err != nil || got != want {
			t.Errorf("parseTokenCount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "-5", "abc", "k"} {
		if _, err := parseTokenCount(in); err == nil {
			t.Errorf("parseTokenCount(%q) accepted", in)
		}
	}
}

func TestFormatBudgetAndSpent(t *testing.T) {
	b := &job.Budget{MaxTokens: 50_000, MaxCostUSD: 2, MaxTurns: 20}
	if got := formatBudget(b); got != "50k tokens / $2 / 20 turns" {
		t.Fatalf("formatBudget = %q", got)
	}
	u := &job.Usage{TotalTokens: 12_300, CostUSD: 0.12, Turns: 5}
	if got := formatBudgetSpent(b, u); got != "12.3k tokens / $0.1200 / 5 turns" {
		t.Fatalf("formatBudgetSpent = %q", got)
	}
	if formatBudget(nil) != "" || formatBudgetSpent(nil, u) != "" {
		t.Fatal("no budget must render nothing")
	}
}
