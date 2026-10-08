package job

import (
	"fmt"
	"log/slog"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/wsproto"
)

// CheckBudget validates a requested budget (N2 §B): a limit is a finite number >= 0.
func CheckBudget(b *Budget) error {
	if err := b.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return nil
}

// budgetSupport reports whether a job of this agent can be metered at all (N2 §B):
// the budget is only as good as the accounting the agent streams. A job that cannot be
// metered must not silently run unlimited when the caller asked for a ceiling, so an
// explicit budget on such an agent is refused at admission (and a layered default is
// simply not applied, so a project-wide budget never breaks its exec jobs).
//
//   - acp-agent: usage_update + prompt turns.
//   - cli-agent with structured (ndjson) output whose projector knows its usage rows
//     (claude / omp) or that names an ndjson_usage_path.
//   - codex: the `tokens used` tail on stderr — printed once, when the run ends, so its
//     verdict comes after the fact (the job is still failed as over budget).
//
// Everything else — exec, an interactive pty session, a text cli-agent — reports no
// accounting we can read.
func budgetSupport(cfg *config.Config, agentKey string, interactive bool) (ok bool, why string) {
	if interactive {
		return false, "interactive (pty) jobs stream no usage accounting"
	}
	ac, found := agent.ResolveAgent(cfg, agentKey)
	if !found {
		return false, fmt.Sprintf("unknown agent %q", agentKey)
	}
	switch ac.Type {
	case agent.TypeExec:
		return false, fmt.Sprintf("exec agent %q reports no usage accounting", agentKey)
	case agent.TypeACPAgent:
		return true, ""
	}
	if ac.NDJSONOutput() {
		switch agent.NDJSONProjectorFor(agentKey, ac) {
		case ndjsonfilter.ProjectorClaude, ndjsonfilter.ProjectorOMP:
			return true, ""
		}
		if ac.NDJSONUsagePath != "" {
			return true, ""
		}
	}
	if isCodexAgent(agentKey, ac.Command) {
		return true, ""
	}
	return false, fmt.Sprintf("agent %q reports no readable usage (needs output_format: ndjson with a known projector or ndjson_usage_path, or codex)", agentKey)
}

// checkBudgetAdmission is the validate-time gate for an EXPLICIT budget: well-formed and
// servable by the agent that will run it. Remote jobs are judged by the executing
// machine (its own validate runs this same gate), like the other capability checks.
func checkBudgetAdmission(cfg *config.Config, req JobRequest, gateAgent string) error {
	if req.Budget.IsZero() {
		return nil
	}
	if err := CheckBudget(req.Budget); err != nil {
		return err
	}
	if ok, why := budgetSupport(cfg, gateAgent, req.Interactive); !ok {
		return fmt.Errorf("%w: budget cannot be enforced: %s", ErrInvalidRequest, why)
	}
	return nil
}

// resolveBudget fills the request's budget from the layered defaults (agent < project <
// request, per dimension) and normalises "no limit" to nil, so request_json, the wire
// frame and the result all carry one decided value. Defaults only apply where the agent
// can be metered (see budgetSupport); a dispatched job (BudgetFixed) arrives decided
// and is left alone.
func resolveBudget(cfg *config.Config, req *JobRequest, remote bool) {
	if req.BudgetFixed {
		req.Budget = req.Budget.Normalize()
		return
	}
	agentKey := gateAgentOf(*req)
	merged := cfg.EffectiveBudget(req.ProjectKey, agentKey, req.Budget)
	if merged != nil && !remote {
		if ok, why := budgetSupport(cfg, agentKey, req.Interactive); !ok {
			// An explicit budget was refused in validate; only defaults get here.
			slog.Debug("job.budget_default_skipped", "agent", agentKey, "reason", why)
			merged = req.Budget.Normalize()
		}
	}
	req.Budget = merged
}

// checkBudgetWorker refuses a budgeted job for a worker whose protocol cannot carry it
// (G032: a worker below wsproto.BudgetMinProtocolVersion would ignore the field and run
// the job unlimited while the row claims a ceiling). An offline / unknown-version worker
// passes: the dispatch-time check in the worker runner is the backstop.
func (s *Service) checkBudgetWorker(cfg *config.Config, req JobRequest) error {
	if req.Budget.IsZero() || !isWorkerRunner(cfg, req.Runner) || s.workers == nil {
		return nil
	}
	workerID := req.WorkerID
	if workerID == "" {
		workerID = cfg.Runners[req.Runner].WorkerID
	}
	if workerID == "" {
		return nil
	}
	if caps, online := s.workers.Candidate(workerID); online && caps.ProtocolKnown && !wsproto.SupportsBudget(caps.ProtocolVersion) {
		return fmt.Errorf("%w: worker %s speaks protocol v%d, a job budget needs v%d; upgrade the worker or drop the budget",
			ErrInvalidRequest, workerID, caps.ProtocolVersion, wsproto.BudgetMinProtocolVersion)
	}
	return nil
}

// newBudgetMeter builds the meter of a job that executes on THIS machine, wired to the
// kill path: the first crossing records the verdict on the entry and cancels the job's
// context (the same path a user cancel / the stall watchdog takes, so the runner
// terminates the whole process tree). nil when the job has no budget or runs remotely
// (the worker meters its own copy).
func (s *Service) newBudgetMeter(entry *jobEntry, jobID string, budget *Budget) *runner.BudgetMeter {
	return runner.NewBudgetMeter(budget, func(b runner.BudgetBreach) {
		entry.budgetErr.Store(&b)
		slog.Warn("job.budget_exceeded", "job_id", jobID, "limit", b.Limit, "max", b.Max, "used", b.Actual)
		entry.mu.Lock()
		cancel := entry.cancel
		entry.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
}

// takeBudget returns the failure the meter recorded, if a limit was crossed.
func (entry *jobEntry) takeBudget() error {
	if p := entry.budgetErr.Load(); p != nil {
		return *p
	}
	return nil
}

// recordBudgetUsage stamps the meter's turn count (and, when the capture produced no
// usage at all, the tokens / cost it saw) on the job's result, so `job show` can print
// "spent" next to the ceiling even for a source that reports no final tally.
func recordBudgetUsage(entry *jobEntry, m *runner.BudgetMeter) {
	if m == nil {
		return
	}
	tokens, cost, turns := m.Spent()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	u := entry.result.Usage
	if u == nil {
		if tokens == 0 && cost == 0 && turns == 0 {
			return
		}
		u = &Usage{TotalTokens: tokens, CostUSD: cost, Source: "budget:meter"}
		entry.result.Usage = u
	}
	u.Turns = int64(turns)
}

// budgetFailureDetail is the job.budget_exceeded event detail for a budget failure
// error text (recoverable even when the failure came back from a worker as text only).
func budgetFailureDetail(jobID, errText string) (map[string]any, bool) {
	b, ok := runner.ParseBudgetBreach(errText)
	if !ok {
		return nil, false
	}
	detail := map[string]any{"job_id": jobID, "limit": b.Limit, "max": b.Max, "used": b.Actual, "error": errText}
	return detail, true
}
