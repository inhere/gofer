package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/wsproto"
)

// newAgentBudgetResumeService has one agent with an agent_budget mapping ("claude", so
// its built-in resume template applies) and one without ("plain").
func newAgentBudgetResumeService(t *testing.T, root string, mutate func(*config.Config)) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedAgents: []string{"claude", "plain", "exec"}, AllowedRunners: []string{"local"}, AllowExec: true,
		}},
		Agents: map[string]config.AgentConfig{
			"claude": {
				Type: agent.TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"},
				AgentBudget: &config.AgentBudgetMap{
					TokensArgs: []string{"--budget-tokens", "{{budget_tokens}}"},
					CostEnv:    "AGENT_BUDGET_COST",
				},
			},
			"plain": {Type: agent.TypeCLIAgent, Command: "plain-tool", Args: []string{"{{prompt}}"}},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return newServiceFromCfg(t, root, cfg)
}

// TestAgentBudgetRejectedWithoutMapping: an explicit agent budget on an agent that
// declares no mapping (or only one of the two dimensions) is a 400, never ignored.
func TestAgentBudgetRejectedWithoutMapping(t *testing.T) {
	t.Parallel()
	s := newAgentBudgetResumeService(t, t.TempDir(), nil)
	for name, req := range map[string]JobRequest{
		"no mapping": {ProjectKey: "self", Agent: "plain", Runner: "local", Prompt: "x", Cwd: ".", Budget: &Budget{AgentTokens: 1000}},
		"exec":       {ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".", Budget: &Budget{AgentCostUSD: 1}},
	} {
		_, err := s.Submit(req)
		if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "does not support budgets") {
			t.Fatalf("%s: err = %v, want ErrInvalidRequest naming the missing budget support", name, err)
		}
	}
	// A mapped agent takes both dimensions here (args for tokens, env for cost).
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "x", Cwd: ".", TimeoutSec: 30, Budget: &Budget{AgentTokens: 5, AgentCostUSD: 1}}); err != nil {
		t.Fatalf("mapped agent refused: %v", err)
	}
	if err := (&Budget{AgentTokens: -1}).Validate(); err == nil {
		t.Fatal("negative agent_tokens validated")
	}
}

// TestAgentBudgetRenderedIntoArgv: the mapping lands in the job's argv, the job echoes
// the budget, and a job without one renders the argv it always did.
func TestAgentBudgetRenderedIntoArgv(t *testing.T) {
	t.Parallel()
	s := newAgentBudgetResumeService(t, t.TempDir(), nil)
	with := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30, Budget: &Budget{AgentTokens: 50000}})
	if !strings.Contains(with.RenderedCommand, `"--budget-tokens","50000"`) {
		t.Fatalf("rendered command %q lacks the budget flag", with.RenderedCommand)
	}
	if with.Budget == nil || with.Budget.AgentTokens != 50000 || with.Budget.HasHard() {
		t.Fatalf("job budget = %+v, want only agent_tokens 50000", with.Budget)
	}
	without := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30})
	if strings.Contains(without.RenderedCommand, "budget") || without.Budget != nil {
		t.Fatalf("job without a budget changed: %q %+v", without.RenderedCommand, without.Budget)
	}
}

// TestAgentBudgetLayeredDefaultSkippedWhereUnsupported: a project-wide default that
// carries an agent budget never breaks a job of an agent that cannot take it.
func TestAgentBudgetLayeredDefaultSkippedWhereUnsupported(t *testing.T) {
	t.Parallel()
	s := newAgentBudgetResumeService(t, t.TempDir(), func(cfg *config.Config) {
		p := cfg.Projects["self"]
		p.Budget = &config.Budget{AgentTokens: 777}
		cfg.Projects["self"] = p
	})
	plain := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "plain", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30})
	if plain.Budget != nil {
		t.Fatalf("unsupported agent got the default budget: %+v", plain.Budget)
	}
	mapped := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30})
	if mapped.Budget == nil || mapped.Budget.AgentTokens != 777 || !strings.Contains(mapped.RenderedCommand, `"--budget-tokens","777"`) {
		t.Fatalf("mapped agent: budget=%+v cmd=%q, want the default applied", mapped.Budget, mapped.RenderedCommand)
	}
}

// TestAgentBudgetResumeCarries: a continuation keeps the source job's agent budget (in
// the carrier argv, like the model) and an explicit one overrides it; an explicit one
// the target agent cannot take is refused.
func TestAgentBudgetResumeCarries(t *testing.T) {
	t.Parallel()
	s := newAgentBudgetResumeService(t, t.TempDir(), nil)
	src := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30, Budget: &Budget{AgentTokens: 50000}})
	sid := src.SessionID

	inherited, err := s.ResumeJob(src.ID, "next", "", "c")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(inherited.ID); s.Wait(inherited.ID) })
	want := []string{"claude", "--resume", sid, "-p", "next", "--budget-tokens", "50000"}
	if got := resumeArgv(t, inherited.RequestJSON); !equalArgs(got, want) {
		t.Fatalf("inherited resume argv = %#v, want %#v", got, want)
	}
	if inherited.Budget == nil || inherited.Budget.AgentTokens != 50000 {
		t.Fatalf("inherited budget = %+v", inherited.Budget)
	}

	over, err := s.ResumeJobWith(src.ID, "next", "", "c", ResumeOptions{Budget: &Budget{AgentTokens: 7000, AgentCostUSD: 2}})
	if err != nil {
		t.Fatalf("ResumeJobWith: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(over.ID); s.Wait(over.ID) })
	want = []string{"claude", "--resume", sid, "-p", "next", "--budget-tokens", "7000"}
	if got := resumeArgv(t, over.RequestJSON); !equalArgs(got, want) {
		t.Fatalf("overridden resume argv = %#v, want %#v", got, want)
	}
	if over.Budget == nil || over.Budget.AgentTokens != 7000 || over.Budget.AgentCostUSD != 2 {
		t.Fatalf("overridden budget = %+v", over.Budget)
	}
	if !strings.Contains(over.RequestJSON, `"AGENT_BUDGET_COST":"2"`) {
		t.Fatalf("the cost dimension (env mapping) did not reach the carrier env: %s", over.RequestJSON)
	}

	if _, err := s.ResumeJobWith(src.ID, "next", "", "c", ResumeOptions{Agent: "plain", Budget: &Budget{AgentTokens: 1}}); err == nil {
		t.Fatal("explicit agent budget on an unmapped resume target accepted")
	}
}

const budgetExhaustedMeta = `{"used":1100,"_meta":{"budget_scope":"turn","budget_limit":{"tokens":1000},"budget_used":{"tokens":1100},"budget_remaining":{"tokens":0},"budget_exceeded":true,"budget_reason":"max_tokens"}}`

// TestAgentBudgetACPExhaustedEndReason: an ACP usage_update whose _meta says the budget
// is exceeded marks the job's end reason "budget" (wrap_up), keeps the snapshot with the
// usage, records the event, and does NOT kill or fail the job.
func TestAgentBudgetACPExhaustedEndReason(t *testing.T) {
	t.Parallel()
	s := newACPServiceAgent(t, t.TempDir(), acptest.Options{UsageUpdate: []string{budgetExhaustedMeta}}, nil, func(ac *config.AgentConfig) {
		ac.AgentBudget = &config.AgentBudgetMap{TokensEnv: "SUGA_BUDGET_TOKENS"}
	})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 60,
		Budget: &Budget{AgentTokens: 1000},
	})
	if final.Status != StatusDone || final.FailureClass != "" {
		t.Fatalf("status=%s class=%q err=%q, want a normal done", final.Status, final.FailureClass, final.Error)
	}
	if final.WrapUp != ndjsonfilter.WrapUpBudget {
		t.Fatalf("wrap_up = %q, want %q", final.WrapUp, ndjsonfilter.WrapUpBudget)
	}
	ab := final.Usage.AgentBudget
	if ab == nil || !ab.Exceeded || ab.LimitTokens != 1000 || ab.UsedTokens != 1100 || ab.Scope != "turn" || ab.Reason != "max_tokens" {
		t.Fatalf("usage.agent_budget = %+v", ab)
	}
	events, err := s.ListJobEvents(final.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Type == EventJobAgentBudgetExhausted {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s events = %d, want 1", EventJobAgentBudgetExhausted, n)
	}
}

// TestAgentBudgetACPWithinLimitIsNotAnEndReason: a snapshot that is not exceeded is
// stored but sets no end reason and records no event.
func TestAgentBudgetACPWithinLimitIsNotAnEndReason(t *testing.T) {
	t.Parallel()
	s := newACPServiceAgent(t, t.TempDir(), acptest.Options{UsageUpdate: []string{
		`{"used":100,"_meta":{"budget_scope":"turn","budget_limit":{"tokens":1000},"budget_used":{"tokens":100},"budget_remaining":{"tokens":900},"budget_exceeded":false}}`,
	}}, nil, func(ac *config.AgentConfig) {
		ac.AgentBudget = &config.AgentBudgetMap{TokensEnv: "SUGA_BUDGET_TOKENS"}
	})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 60,
		Budget: &Budget{AgentTokens: 1000},
	})
	if final.Status != StatusDone || final.WrapUp != "" {
		t.Fatalf("status=%s wrap_up=%q, want done with no end reason", final.Status, final.WrapUp)
	}
	if ab := final.Usage.AgentBudget; ab == nil || ab.Exceeded || ab.UsedTokens != 100 || ab.RemainingTokens != 900 {
		t.Fatalf("usage.agent_budget = %+v", ab)
	}
}

// TestAgentBudgetStreamJSONExhausted: the cli path reads the `budget` object of the
// result row; a wrap_up of max_tokens the agent reported for the same reason is
// replaced by the budget end reason.
func TestAgentBudgetStreamJSONExhausted(t *testing.T) {
	t.Parallel()
	s := newClaudeBudgetService(t, []string{
		claudeMsg("m1", 10, 10, 0),
		`{"type":"result","subtype":"success","result":"wrapped up","wrap_up":"max_tokens","budget":{"scope":"turn","exceeded":true,"reason":"max_tokens","tokens":{"limit":1000,"used":1100,"remaining":0}},"estimated":true}`,
	}, "", func(cfg *config.Config) {
		ac := cfg.Agents["claude"]
		ac.AgentBudget = &config.AgentBudgetMap{TokensArgs: []string{"--budget-tokens", "{{budget_tokens}}"}}
		cfg.Agents["claude"] = ac
	})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60,
		Budget: &Budget{AgentTokens: 1000},
	})
	if final.Status != StatusDone {
		t.Fatalf("status=%s err=%q", final.Status, final.Error)
	}
	if final.WrapUp != ndjsonfilter.WrapUpBudget {
		t.Fatalf("wrap_up = %q, want %q", final.WrapUp, ndjsonfilter.WrapUpBudget)
	}
	if ab := final.Usage.AgentBudget; ab == nil || !ab.Exceeded || ab.UsedTokens != 1100 || !ab.Estimated {
		t.Fatalf("usage.agent_budget = %+v", ab)
	}
}

// TestAgentBudgetWorkerProtocolGate: an agent budget for a worker below the protocol
// that carries it is a 400 (the worker would run the agent without the soft budget);
// at v24 it rides the Forward; a hard-only budget is judged by the older v20 floor.
func TestAgentBudgetWorkerProtocolGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		proto   int
		budget  *Budget
		wantErr bool
	}{
		{"old worker + agent budget", wsproto.AgentBudgetMinProtocolVersion - 1, &Budget{AgentTokens: 100}, true},
		{"new worker + agent budget", wsproto.AgentBudgetMinProtocolVersion, &Budget{AgentTokens: 100}, false},
		{"old worker + hard budget only", wsproto.AgentBudgetMinProtocolVersion - 1, &Budget{MaxTokens: 100}, false},
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
				if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "agent budget") {
					t.Fatalf("err = %v, want ErrInvalidRequest naming the agent budget", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			s.Wait(res.ID)
			if stub.gotForward == nil || stub.gotForward.Budget == nil || *stub.gotForward.Budget != *tc.budget {
				t.Fatalf("forward = %+v, want the budget carried", stub.gotForward)
			}
		})
	}
}
