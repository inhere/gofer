package job

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/wsproto"
)

// claudeMsg is one `assistant` line of a claude stream-json run: a content block of the
// message `id`, whose usage carries in+out+cache_read = in+out+cache tokens.
func claudeMsg(id string, in, out, cache int) string {
	b, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"id": id, "role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": "step " + id}},
			"usage":   map[string]any{"input_tokens": in, "output_tokens": out, "cache_read_input_tokens": cache},
		},
	})
	return string(b)
}

const claudeResultRow = `{"type":"result","subtype":"success","is_error":false,"result":"all done","num_turns":3}`

// newClaudeBudgetService builds a service whose "claude" agent replays the given stream
// (and stays alive for `linger` afterwards, so a job that is NOT killed would run on).
func newClaudeBudgetService(t *testing.T, lines []string, linger string, mutate func(*config.Config)) *Service {
	t.Helper()
	root := t.TempDir()
	sample := filepath.Join(root, "claude-sample.ndjson")
	if err := os.WriteFile(sample, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	args := []string{"cat-file", sample}
	if linger != "" {
		args = []string{"cat-file-sleep", sample, linger}
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"claude", "exec"}, AllowedRunners: []string{"local"}, AllowExec: true},
		},
		Agents: map[string]config.AgentConfig{
			// Keyed "claude" so the built-in claude projector applies; the command only
			// replays the fixture.
			"claude": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: args, OutputFormat: config.OutputFormatNDJSON},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return newServiceFromCfg(t, root, cfg)
}

func budgetEvent(t *testing.T, s *Service, jobID string) (map[string]any, bool) {
	t.Helper()
	events, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	for _, ev := range events {
		if ev.Type == EventJobBudgetExceeded {
			var d map[string]any
			_ = json.Unmarshal([]byte(ev.Detail), &d)
			return d, true
		}
	}
	return nil, false
}

// TestBudgetMaxTokensKillsClaudeJob: the claude stream is metered as it arrives (message
// id de-duplicated), the job is killed the moment the sum crosses max_tokens — long before
// the process would have ended — and fails with failure_class=budget.
func TestBudgetMaxTokensKillsClaudeJob(t *testing.T) {
	t.Parallel()
	s := newClaudeBudgetService(t, []string{
		claudeMsg("msg_1", 300, 100, 100), // 500
		claudeMsg("msg_1", 300, 100, 100), // same message again: not counted twice
		claudeMsg("msg_2", 400, 200, 0),   // +600 = 1100 > 1000
		claudeResultRow,
	}, "60s", nil)

	start := time.Now()
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 120,
		Budget: &Budget{MaxTokens: 1000},
	})
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("job took %s: the process tree was not killed at the limit", elapsed)
	}
	if final.Status != StatusFailed || final.FailureClass != FailureClassBudget {
		t.Fatalf("status=%s class=%q err=%q, want failed/budget", final.Status, final.FailureClass, final.Error)
	}
	if want := "budget exceeded: max_tokens 1000 (used 1100)"; final.Error != want {
		t.Fatalf("error = %q, want %q", final.Error, want)
	}
	if final.Budget == nil || final.Budget.MaxTokens != 1000 {
		t.Fatalf("result budget = %+v, want max_tokens 1000", final.Budget)
	}
	if final.Usage == nil || final.Usage.Turns != 2 || final.Usage.TotalTokens != 1100 {
		t.Fatalf("usage = %+v, want 2 turns / 1100 tokens", final.Usage)
	}
	detail, ok := budgetEvent(t, s, final.ID)
	if !ok {
		t.Fatal("no job.budget_exceeded event recorded")
	}
	if detail["limit"] != "max_tokens" || detail["max"] != float64(1000) || detail["used"] != float64(1100) {
		t.Fatalf("event detail = %v", detail)
	}
	// Not transient: nothing took it over.
	if final.AutoResumedBy != "" || final.FellBackTo != "" {
		t.Fatalf("budget failure was taken over: resumed_by=%q fell_back_to=%q", final.AutoResumedBy, final.FellBackTo)
	}
}

// TestBudgetDedupesClaudeMessages: a message repeated per content block counts once, so a
// budget the REAL total stays under does not trip.
func TestBudgetDedupesClaudeMessages(t *testing.T) {
	t.Parallel()
	s := newClaudeBudgetService(t, []string{
		claudeMsg("msg_1", 300, 100, 100),
		claudeMsg("msg_1", 300, 100, 100),
		claudeMsg("msg_1", 300, 100, 100),
		claudeResultRow,
	}, "", nil)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60,
		Budget: &Budget{MaxTokens: 700, MaxTurns: 1},
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done: duplicates must not double-count", final.Status, final.Error)
	}
	if final.Usage == nil || final.Usage.Turns != 1 {
		t.Fatalf("usage = %+v, want 1 turn recorded next to the ceiling", final.Usage)
	}
	if _, ok := budgetEvent(t, s, final.ID); ok {
		t.Fatal("budget event recorded for a job within its budget")
	}
}

// TestBudgetMaxTurnsCountsModelRequests: max_turns=2 lets two assistant messages finish and
// trips on the third.
func TestBudgetMaxTurnsCountsModelRequests(t *testing.T) {
	t.Parallel()
	s := newClaudeBudgetService(t, []string{
		claudeMsg("m1", 1, 1, 0), claudeMsg("m2", 1, 1, 0), claudeMsg("m3", 1, 1, 0), claudeResultRow,
	}, "60s", nil)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 120,
		Budget: &Budget{MaxTurns: 2},
	})
	if final.Status != StatusFailed || final.FailureClass != FailureClassBudget {
		t.Fatalf("status=%s class=%q err=%q", final.Status, final.FailureClass, final.Error)
	}
	if want := "budget exceeded: max_turns 2 (used 3 model requests)"; final.Error != want {
		t.Fatalf("error = %q, want %q", final.Error, want)
	}
}

// TestBudgetUnsetLeavesJobUntouched: no budget anywhere = the old behaviour, byte for byte
// (no budget on the result, no turns stamped, no event).
func TestBudgetUnsetLeavesJobUntouched(t *testing.T) {
	t.Parallel()
	s := newClaudeBudgetService(t, []string{claudeMsg("m1", 9000, 9000, 9000), claudeMsg("m2", 9000, 9000, 9000), claudeResultRow}, "", nil)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60,
	})
	if final.Status != StatusDone || final.FailureClass != "" {
		t.Fatalf("status=%s class=%q err=%q, want a plain done", final.Status, final.FailureClass, final.Error)
	}
	if final.Budget != nil {
		t.Fatalf("budget = %+v, want none", final.Budget)
	}
	if final.Usage != nil && final.Usage.Turns != 0 {
		t.Fatalf("usage.turns = %d, want 0 without a budget", final.Usage.Turns)
	}
	if strings.Contains(final.RequestJSON, "budget") {
		t.Fatalf("request_json grew a budget key: %s", final.RequestJSON)
	}
}

// TestBudgetCostFromClaudeResultRow: claude reports cost only on the result row, so a
// cost ceiling is judged when it arrives (after the fact) and still fails the job.
func TestBudgetCostFromClaudeResultRow(t *testing.T) {
	t.Parallel()
	s := newClaudeBudgetService(t, []string{
		claudeMsg("m1", 10, 10, 0),
		`{"type":"result","subtype":"success","result":"x","total_cost_usd":1.5,"usage":{"input_tokens":10,"output_tokens":10}}`,
	}, "", nil)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60,
		Budget: &Budget{MaxCostUSD: 1},
	})
	if final.Status != StatusFailed || final.FailureClass != FailureClassBudget {
		t.Fatalf("status=%s class=%q err=%q", final.Status, final.FailureClass, final.Error)
	}
	if want := "budget exceeded: max_cost_usd $1.0000 (used $1.5000)"; final.Error != want {
		t.Fatalf("error = %q, want %q", final.Error, want)
	}
}

// TestBudgetLayeredDefaults: agent < project < request, per dimension, recorded on the
// result as the decided value.
func TestBudgetLayeredDefaults(t *testing.T) {
	t.Parallel()
	s := newClaudeBudgetService(t, []string{claudeMsg("m1", 1, 1, 0), claudeResultRow}, "", func(cfg *config.Config) {
		ac := cfg.Agents["claude"]
		ac.Budget = &config.Budget{MaxTokens: 100_000, MaxCostUSD: 5, MaxTurns: 50}
		cfg.Agents["claude"] = ac
		p := cfg.Projects["self"]
		p.Budget = &config.Budget{MaxTokens: 80_000, MaxTurns: 40}
		cfg.Projects["self"] = p
	})
	base := JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60}

	got := submitAndWait(t, s, base)
	if got.Budget == nil || *got.Budget != (Budget{MaxTokens: 80_000, MaxCostUSD: 5, MaxTurns: 40}) {
		t.Fatalf("defaults only: budget = %+v, want project over agent per dimension", got.Budget)
	}
	req := base
	req.Budget = &Budget{MaxTurns: 7}
	got = submitAndWait(t, s, req)
	if got.Budget == nil || *got.Budget != (Budget{MaxTokens: 80_000, MaxCostUSD: 5, MaxTurns: 7}) {
		t.Fatalf("request override: budget = %+v, want the request's turns over the defaults", got.Budget)
	}
}

// TestBudgetRefusedWhereItCannotBeEnforced: an explicit budget on an agent that streams no
// readable usage is a 400 (never a silently unlimited job), while a project-wide DEFAULT
// budget leaves such a job running untouched.
func TestBudgetRefusedWhereItCannotBeEnforced(t *testing.T) {
	t.Parallel()
	s := newClaudeBudgetService(t, []string{claudeResultRow}, "", func(cfg *config.Config) {
		p := cfg.Projects["self"]
		p.Budget = &config.Budget{MaxTokens: 1000}
		cfg.Projects["self"] = p
	})
	_, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
		Budget: &Budget{MaxTokens: 5},
	})
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "budget cannot be enforced") {
		t.Fatalf("explicit budget on exec: err = %v, want ErrInvalidRequest naming the budget", err)
	}
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"echo", "hi"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone || final.Budget != nil {
		t.Fatalf("exec job under a project default: status=%s budget=%+v, want done/none", final.Status, final.Budget)
	}
	_, err = s.Submit(JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "x", Cwd: ".", TimeoutSec: 30,
		Budget: &Budget{MaxTokens: -1},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("negative budget: err = %v, want ErrInvalidRequest", err)
	}
}

// TestBudgetACPUsageUpdateKillsJob: an acp agent's usage_update is metered live; the job is
// cancelled at the crossing instead of waiting out the agent's delay.
func TestBudgetACPUsageUpdateKillsJob(t *testing.T) {
	t.Parallel()
	s := newACPService(t, t.TempDir(), acptest.Options{
		UsageUpdate: []string{`{"used":900}`, `{"used":2500,"cost":{"total":0.2}}`},
		Delay:       60 * time.Second,
	})
	start := time.Now()
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 120,
		Budget: &Budget{MaxTokens: 2000},
	})
	if time.Since(start) > 30*time.Second {
		t.Fatal("the acp job was not cancelled at the limit")
	}
	if final.Status != StatusFailed || final.FailureClass != FailureClassBudget {
		t.Fatalf("status=%s class=%q err=%q", final.Status, final.FailureClass, final.Error)
	}
	if want := "budget exceeded: max_tokens 2000 (used 2500)"; final.Error != want {
		t.Fatalf("error = %q, want %q", final.Error, want)
	}
}

// TestBudgetCodexStderrTallyFailsAfterTheFact: codex prints its token tally last, so the
// verdict arrives once the run is over — the job is still failed as over budget.
func TestBudgetCodexStderrTallyFailsAfterTheFact(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"codex"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{
			// codex prints its tally on stderr as the last thing; stderr-exit stands in.
			"codex": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"stderr-exit", "0", "tokens used\n19,802", "{{prompt}}"}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60,
		Budget: &Budget{MaxTokens: 10_000},
	})
	if final.Status != StatusFailed || final.FailureClass != FailureClassBudget {
		t.Fatalf("status=%s class=%q err=%q usage=%+v", final.Status, final.FailureClass, final.Error, final.Usage)
	}
	if want := "budget exceeded: max_tokens 10000 (used 19802)"; final.Error != want {
		t.Fatalf("error = %q, want %q", final.Error, want)
	}
}

// TestBudgetNotRetried: a retry policy must not re-spend a budget the job already blew.
func TestBudgetNotRetried(t *testing.T) {
	t.Parallel()
	s := newClaudeBudgetService(t, []string{claudeMsg("m1", 5000, 5000, 0), claudeResultRow}, "60s", nil)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 120,
		Budget: &Budget{MaxTokens: 100},
		Retry:  &RetryPolicy{MaxAttempts: 3},
	})
	if final.FailureClass != FailureClassBudget {
		t.Fatalf("class = %q (err=%q), want budget", final.FailureClass, final.Error)
	}
	rows, err := s.meta.ListRetriesByJob(final.ID)
	if err != nil {
		t.Fatalf("ListRetriesByJob: %v", err)
	}
	if len(rows) > 0 {
		t.Fatalf("a budget failure scheduled %d retries", len(rows))
	}
}

// TestBudgetResumeInheritsAndOverrides: a continuation keeps the source job's budget; an
// explicit one overrides per dimension.
func TestBudgetResumeInheritsAndOverrides(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"codex", "exec"}, AllowedRunners: []string{"local"}, AllowExec: true},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Type: agent.TypeCLIAgent, Command: "codex", Args: []string{"-p", "{{prompt}}"}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local", Prompt: "long", Cwd: ".", TimeoutSec: 600,
		SessionID: "sess-budget", Budget: &Budget{MaxTokens: 5000, MaxTurns: 9},
	})
	if src.Budget == nil || src.Budget.MaxTokens != 5000 {
		t.Fatalf("setup: source budget = %+v", src.Budget)
	}

	inherited, err := s.ResumeJob(src.ID, "continue", "", "c1")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(inherited.ID); s.Wait(inherited.ID) })
	if inherited.Budget == nil || *inherited.Budget != (Budget{MaxTokens: 5000, MaxTurns: 9}) {
		t.Fatalf("inherited budget = %+v, want the source's", inherited.Budget)
	}

	over, err := s.ResumeJobWith(src.ID, "continue", "", "c1", ResumeOptions{Budget: &Budget{MaxTokens: 9000, MaxCostUSD: 2}})
	if err != nil {
		t.Fatalf("ResumeJobWith: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(over.ID); s.Wait(over.ID) })
	if over.Budget == nil || *over.Budget != (Budget{MaxTokens: 9000, MaxCostUSD: 2, MaxTurns: 9}) {
		t.Fatalf("overridden budget = %+v, want tokens/cost replaced and turns kept", over.Budget)
	}
}

// TestBudgetWorkerProtocolGate: a budgeted job for a worker below protocol v20 is a 400
// (never silently unlimited); v20 carries it on the Forward; a job without a budget is
// never refused.
func TestBudgetWorkerProtocolGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		proto   int
		budget  *Budget
		wantErr bool
	}{
		{"old worker + budget", wsproto.BudgetMinProtocolVersion - 1, &Budget{MaxTokens: 100}, true},
		{"new worker + budget", wsproto.BudgetMinProtocolVersion, &Budget{MaxTokens: 100}, false},
		{"old worker, no budget", wsproto.BudgetMinProtocolVersion - 1, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubWorkerRunner{}
			sel := fakeSelector{cands: []WorkerCandidate{selfCaps(WorkerCandidate{WorkerID: "w1", ProtocolVersion: tc.proto, ProtocolKnown: true})}}
			s := newWorkerTestServiceSel(t, t.TempDir(), stub, map[string]config.WorkerAuthConfig{"w1": {Token: "t"}}, sel)
			res, err := s.Submit(JobRequest{
				ProjectKey: "self", Agent: "exec", Runner: "remote-w1", Cmd: []string{"echo", "x"}, Cwd: ".", TimeoutSec: 30,
				Budget: tc.budget,
			})
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "budget") {
					t.Fatalf("err = %v, want ErrInvalidRequest naming the budget", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			s.Wait(res.ID)
			if stub.gotForward == nil {
				t.Fatal("runner never saw the forward")
			}
			if (tc.budget == nil) != (stub.gotForward.Budget == nil) {
				t.Fatalf("forward budget = %+v, want carried iff requested", stub.gotForward.Budget)
			}
			if tc.budget != nil && *stub.gotForward.Budget != *tc.budget {
				t.Fatalf("forward budget = %+v, want %+v", stub.gotForward.Budget, tc.budget)
			}
		})
	}
}

// TestBudgetFailureTextIsClassifiedWithoutLocalMeter: a remote worker reports the kill as
// text only; the host still files it as failure_class=budget and records the event.
func TestBudgetFailureTextIsClassifiedWithoutLocalMeter(t *testing.T) {
	t.Parallel()
	stub := &budgetFailingRunner{}
	sel := fakeSelector{cands: []WorkerCandidate{selfCaps(WorkerCandidate{WorkerID: "w1", ProtocolVersion: wsproto.CurrentProtocolVersion, ProtocolKnown: true})}}
	s := newWorkerTestServiceSel(t, t.TempDir(), stub, map[string]config.WorkerAuthConfig{"w1": {Token: "t"}}, sel)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "remote-w1", Cmd: []string{"echo", "x"}, Cwd: ".", TimeoutSec: 30,
		Budget: &Budget{MaxTurns: 3},
	})
	if final.Status != StatusFailed || final.FailureClass != FailureClassBudget {
		t.Fatalf("status=%s class=%q err=%q", final.Status, final.FailureClass, final.Error)
	}
	detail, ok := budgetEvent(t, s, final.ID)
	if !ok || detail["limit"] != "max_turns" || detail["used"] != float64(4) {
		t.Fatalf("event = %v (ok=%v), want max_turns / used 4", detail, ok)
	}
}

type budgetFailingRunner struct{}

func (budgetFailingRunner) Name() string { return "remote-w1" }
func (budgetFailingRunner) Run(_ context.Context, _ runner.Request) runner.Result {
	return runner.Result{ExitCode: -1, Err: runner.BudgetBreach{Limit: runner.BudgetLimitTurns, Max: 3, Actual: 4}}
}

// TestBudgetTaskBookFrontmatterFillsUnsetDimensions: the task book's `budget` is the
// default below the request's own values and above the agent / project defaults.
func TestBudgetTaskBookFrontmatterFillsUnsetDimensions(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"codex"}, AllowedRunners: []string{"local"}, Budget: &config.Budget{MaxTokens: 1_000_000, MaxTurns: 99}},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"printf", "{{prompt}}"}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	writeTemplate(t, root, "capped", "---\nagent: codex\nbudget:\n  max_tokens: 50000\n  max_cost_usd: 2.5\n---\nhello\n")
	res := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Runner: "local", Template: "capped", Cwd: ".", TimeoutSec: 30,
		Budget: &Budget{MaxCostUSD: 9},
	})
	if res.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", res.Status, res.Error)
	}
	if res.Budget == nil || *res.Budget != (Budget{MaxTokens: 50000, MaxCostUSD: 9, MaxTurns: 99}) {
		t.Fatalf("budget = %+v, want request cost over template tokens over project turns", res.Budget)
	}
}
