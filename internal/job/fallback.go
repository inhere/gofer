package job

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// fallbackChainMaxHops bounds the walk up a continuation chain when a transfer has to
// recover the request the WORK was submitted with (see fallbackBase). A resume chain
// is short by construction (an automatic continuation is attempted once per job), so
// the bound only exists to make a corrupted lineage terminate.
const fallbackChainMaxHops = 8

// resolveFallbackCandidates resolves the ordered, ADMITTED candidate list for a job
// (SUP-01 P3). The list come from the request (`--fallback`), else the project's
// agent_fallbacks, else the agent's own fallback_agents; every candidate is then held
// against the project's allowed_agents — a candidate the project does not admit is
// SKIPPED with a warning (the alternative is a transfer that fails admission at the
// worst possible moment), and an empty result simply means "no transfer".
//
// Duplicates and the agent itself are dropped for the same reason: they can only
// repeat work already under way. The result is frozen into jobs.fallback_json at
// submit, so this runs ONCE per chain.
func resolveFallbackCandidates(cfg *config.Config, projectKey, agentKey string, explicit []string) []string {
	if len(explicit) == 0 {
		explicit = cfg.AgentFallbacksFor(projectKey, agentKey)
	}
	var out []string
	seen := map[string]bool{agentKey: true}
	for _, cand := range explicit {
		switch {
		case seen[cand]:
			slog.Warn("fallback candidate skipped (already in the chain)", "project", projectKey, "agent", agentKey, "candidate", cand)
		case agent.CheckAllowed(cfg, projectKey, cand) != nil:
			slog.Warn("fallback candidate skipped (not allowed in project)", "project", projectKey, "agent", agentKey, "candidate", cand)
		default:
			seen[cand] = true
			out = append(out, cand)
		}
	}
	return out
}

// failureDecision is the terminal-time verdict for a FAILED job (SUP-01 P3): how it
// is classified, and whether it is taken over — by the same agent (AutoResume) or by
// the next candidate (Fallback). Deciding it in ONE place keeps the event choice, the
// persisted failure_class and the submitted takeover in agreement.
type failureDecision struct {
	Class      string
	Hit        string
	AutoResume bool
	Fallback   string
}

// failureDecision reads a failed job's outcome: the pattern match (transient or not),
// whether its own continuation is still available, and whether the chain has a
// candidate left. It is PURE apart from reading the agents registry — it submits
// nothing, so finish can pick the event before the failure becomes observable.
func (s *Service) failureDecision(snap JobResult) failureDecision {
	hit, transient := s.transientHit(snap)
	if !transient {
		return failureDecision{Class: FailureClassOther}
	}
	dec := failureDecision{Class: FailureClassTransient, Hit: hit}
	// A job that IS a continuation carrier (a resubmission of an earlier failure) and
	// dies transiently has already shown that this agent cannot finish the work: the
	// continuation is not attempted again even with budget left, the chain moves on.
	sameAgentAlreadyRetried := snap.ResumedFrom != "" && snap.AutoResumeAttempt > 0
	if !sameAgentAlreadyRetried && s.autoResumeEligible(snap) {
		dec.AutoResume = true
	}
	next := snap.Fallback.next()
	if next == "" || noFallbackRequested(snap) || !s.config().AgentFallbackOnFailure() {
		return dec
	}
	if dec.AutoResume {
		return dec // the same agent gets its own continuation first
	}
	dec.Fallback = next
	return dec
}

// noFallbackRequested reports whether the job's persisted request turned the transfer
// off (`job run --no-fallback`). The flag lives in request_json — it is a property of
// the submission, not a column — so the failure decision reads it back from the same
// blob every other lineage field comes from.
func noFallbackRequested(snap JobResult) bool {
	if snap.RequestJSON == "" {
		return false
	}
	var req struct {
		NoFallback bool `json:"no_fallback"`
	}
	_ = json.Unmarshal([]byte(snap.RequestJSON), &req)
	return req.NoFallback
}

// fallbackBase returns the job whose REQUEST a transfer is built from (SUP-01 P3) and
// its snapshot. It is normally the failed job itself; the exception is a continuation
// carrier, whose request is an exec argv (agent=exec, no prompt) that says nothing
// about the work — there the ORIGINAL job's request is the one that names the agent
// and the prompt, so the chain walks ResumedFrom up to that job. A chain that never
// reaches a prompt-carrying request falls back to the failed job's own.
func (s *Service) fallbackBase(snap JobResult) JobResult {
	base := snap
	for hop := 0; hop < fallbackChainMaxHops && base.ResumedFrom != ""; hop++ {
		prev, ok := s.Get(base.ResumedFrom)
		if !ok {
			break
		}
		base = prev
		if !isExecCarrier(base) {
			break
		}
	}
	if isExecCarrier(base) {
		return snap
	}
	return base
}

// isExecCarrier reports whether a job's persisted request is an exec argv whose prompt
// is baked into the command — the shape a `resume` submits. Such a request carries no
// prompt of its own (see resumeJob), which is what makes it useless as the base of a
// transfer.
func isExecCarrier(src JobResult) bool {
	var req JobRequest
	if src.RequestJSON == "" || json.Unmarshal([]byte(src.RequestJSON), &req) != nil {
		return false
	}
	return req.Agent == agent.ExecAgentKey || len(req.Cmd) > 0
}

// fallBack submits the takeover job for a failed one (SUP-01 P3): the frozen chain's
// next candidate runs the SAME work — its own request, continued in the same cwd
// (inside the source's worktree, which is not re-created), with a fresh session and a
// prompt that says what happened. On success the source row records the link
// (fell_back_to) and the event job.fell_back; the caller records job.terminal instead
// when this returns false.
func (s *Service) fallBack(snap JobResult, hit, nextAgent string) bool {
	base := s.fallbackBase(snap)
	var req JobRequest
	if base.RequestJSON == "" || json.Unmarshal([]byte(base.RequestJSON), &req) != nil {
		return false
	}
	// CallerID / SourceJobID / RequestedAgent are not part of the client-facing JSON
	// (tag "-"), so the lineage is restored from the snapshot (mirroring
	// maybeRetryJob). RequestedAgent falls back to the base request's agent: the first
	// hop is the job the caller actually submitted.
	req.CallerID = base.CallerID
	req.SourceJobID = base.SourceJobID
	requestedAgent := base.RequestedAgent
	if requestedAgent == "" {
		requestedAgent = base.Agent
	}
	req.RequestedAgent = requestedAgent
	// The checklist item follows the chain, and a worker-local row only DISPLAYS the
	// hub's todo (TodoForeign) — exactly as the continuation base does.
	req.TodoForeign = base.TodoForeign
	// The SHAPE of the work decides whether a prompt exists to prefix — read before
	// the agent is replaced (an exec request's argv is the command, there is no prompt).
	execShaped := base.Agent == agent.ExecAgentKey || len(req.Cmd) > 0
	req.Agent = nextAgent
	req.FellBackFrom = snap.ID
	// The plan is the one the DECISION read (the failing job's frozen list), carried
	// one link deeper; len(Candidates) is the chain limit, so this is what makes the
	// resubmission terminate.
	if snap.Fallback != nil {
		req.Fallback = &FallbackState{Candidates: snap.Fallback.Candidates, Depth: snap.Fallback.Depth + 1}
	} else {
		req.Fallback = nil
	}
	// The work continues where it stopped: inside the source's worktree when it had
	// one (never a second worktree), else in the same relative cwd.
	req.Cwd = s.resumeCwd(snap)
	req.Worktree = false
	req.WorktreeBase = ""
	// A fresh session: the new agent must not inherit the failed agent's — or its own
	// previous link's — conversation, and the prompt below tells it what it needs.
	req.SessionID = ""
	req.ResumedFrom = ""
	req.AutoResumeAttempt = 0
	req.RequestID = ""
	req.Sync = false // a takeover is never synchronous: the caller already returned
	req.Title = fallbackTitle(req.Title, nextAgent)
	// An exec-shaped request keeps its argv verbatim (there is no prompt to prefix and
	// the command IS the work); a prompt-shaped one gets the handover note.
	if !execShaped {
		req.Prompt = fallbackPrompt(base.Agent, hit, req.Prompt)
	}

	res, err := s.Submit(req)
	if err != nil {
		slog.Warn("fallback submit failed", "job_id", snap.ID, "agent", nextAgent, "err", err)
		return false
	}
	snap.FellBackTo = res.ID
	_ = s.persist(snap)
	s.recordEvent(snap.ID, EventJobFellBack, map[string]any{
		"to_job": res.ID,
		"agent":  nextAgent,
		"reason": hit,
	})
	return true
}

// fallbackPrompt prefixes the original prompt with the handover note: the new agent
// must know that the work was started by another one and interrupted by a provider
// error, and that it should look at what is already committed instead of starting
// over. The matched error text is quoted (bounded to the same 120 chars the event
// carries) so the new agent can recognise the failure mode.
func fallbackPrompt(fromAgent, hit, prompt string) string {
	return fmt.Sprintf("上一次由 %s 执行，因供应商错误（%s）中断；先 git status / git log 看进度，只做剩余部分，不要重做已提交的工作，然后按原要求汇报。\n\n%s",
		fromAgent, strings.TrimSpace(hit), prompt)
}

// fallbackTitle marks a takeover in the title ("<title> (→omp)") so a plan or board
// shows the chain rather than two identically named rows. An empty title stays empty
// (Submit derives one from the prompt).
func fallbackTitle(title, toAgent string) string {
	if strings.TrimSpace(title) == "" {
		return title
	}
	return title + " (→" + toAgent + ")"
}

// substituteDegradedAgent replaces a degraded agent BEFORE dispatch (SUP-01 P3,
// server.agent_fallback.pre_dispatch): it returns the first candidate that is not
// degraded (a healthy one, or an unknown one — never having failed is not a reason to
// move work away) together with the chain depth that candidate occupies, or ("", 0)
// when there is nothing to substitute — no candidates, a job that already IS a
// takeover (`requestedAgent` set), or every candidate degraded (swapping one degraded
// agent for another buys nothing).
func (s *Service) substituteDegradedAgent(plan *FallbackState, requestedAgent string) (string, int) {
	if plan == nil || requestedAgent != "" {
		return "", 0
	}
	cfg := s.config()
	hc := cfg.EffectiveAgentHealth()
	since := s.nowFn().Unix() - int64(hc.WindowSec)
	for i, cand := range plan.Candidates {
		h, err := s.meta.AgentHealth(cand, since)
		if err != nil {
			// Best-effort: health is an optimisation, so a failed read means "no
			// evidence" (unknown) rather than a failed submit.
			slog.Warn("agent health read failed", "agent", cand, "err", err)
			h = jobstore.AgentHealth{Agent: cand}
		}
		if agent.HealthState(h, hc) != agent.HealthDegraded {
			return cand, i + 1
		}
	}
	return "", 0
}
