package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
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
	// N2 §B: a budget stop is the ceiling doing its job — neither a provider flake nor a
	// bug. It is recognised from the error text (the one thing a remote worker's result
	// carries) and takes nothing over: continuing or handing the work to another agent
	// would simply spend the same money again.
	if _, ok := runner.ParseBudgetBreach(snap.Error); ok {
		return failureDecision{Class: FailureClassBudget}
	}
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
// its snapshot — the whole lineage, as every caller but the transfer itself needs it
// (see fallbackBaseWithin).
func (s *Service) fallbackBase(snap JobResult) JobResult {
	return s.fallbackBaseWithin(snap, false)
}

// fallbackBaseWithin returns the job whose REQUEST a transfer is built from (SUP-01 P3)
// and its snapshot. It is normally the failed job itself; the exception is a continuation
// carrier, whose request is an exec argv (agent=exec, no prompt) that says nothing
// about the work — there the ORIGINAL job's request is the one that names the agent
// and the prompt, so the chain walks ResumedFrom up to that job. A chain that never
// reaches a prompt-carrying request falls back to the failed job's own.
//
// stopAtTodo makes the walk stop at the TODO BOUNDARY: a job whose TodoID differs from
// snap's belongs to ANOTHER plan item, so the walk stops before it and the transfer
// never replays the previous item's request. Only the transfer asks for that (design
// 「失败转移（fallback）」); checkResume keeps the full walk, because the agent that OWNS
// a session must stay recognisable however many items were done on it.
func (s *Service) fallbackBaseWithin(snap JobResult, stopAtTodo bool) JobResult {
	base := snap
	for hop := 0; hop < fallbackChainMaxHops && base.ResumedFrom != ""; hop++ {
		prev, ok := s.Get(base.ResumedFrom)
		if !ok {
			break
		}
		if stopAtTodo && prev.TodoID != snap.TodoID {
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

// fallbackSourceFor resolves the request a transfer of snap is built from, and what the
// takeover needs to know about it:
//
//   - a plan-todo CONTINUATION (snap continues a different item, see
//     todoContinuationTodo) is REBUILT from that item through rebuildTodoRequest. Its
//     own request is a continuation of the previous item's session — an exec argv for a
//     cli agent, a loaded session for an acp agent — so replaying it would hand the
//     candidate the wrong task. With no seam wired (nil, until the plan dispatch owns
//     the ordinary assembly) the replay below stands, stopped at the boundary.
//   - everything else replays the request fallbackBaseWithin points at.
func (s *Service) fallbackSourceFor(snap JobResult) (fallbackSource, bool) {
	if todoID, continuation := s.todoContinuationTodo(snap); continuation && s.rebuildTodoRequest != nil {
		req, err := s.rebuildTodoRequest(todoID)
		if err != nil {
			slog.Warn("fallback: rebuild the todo request", "job_id", snap.ID, "todo_id", todoID, "err", err)
			return fallbackSource{}, false
		}
		return fallbackSource{Req: req, Snapshot: snap, Rebuilt: true}, true
	}
	base := s.fallbackBaseWithin(snap, true)
	var req JobRequest
	if base.RequestJSON == "" || json.Unmarshal([]byte(base.RequestJSON), &req) != nil {
		return fallbackSource{}, false
	}
	// The base request's prompt is already rendered (SUP-01 P5): a template must not
	// run a second time over it.
	dropTemplate(&req)
	return fallbackSource{
		Req:      req,
		Snapshot: base,
		// The SHAPE of the work decides whether a prompt exists to prefix — read before
		// the agent is replaced (an exec request's argv is the command, there is no prompt).
		ExecShaped: base.Agent == agent.ExecAgentKey || len(req.Cmd) > 0,
	}, true
}

// todoContinuationTodo reports the plan item snap CONTINUES — the item it was dispatched
// for, when the job it was continued from belonged to a DIFFERENT one. That difference is
// how a todo continuation is recognised without a job field of its own (design §数据). A
// job with no item (a plain job, or a continuation detached from its plan) is not one.
func (s *Service) todoContinuationTodo(snap JobResult) (string, bool) {
	if snap.ResumedFrom == "" || snap.TodoID == "" {
		return "", false
	}
	prev, ok := s.Get(snap.ResumedFrom)
	if !ok || prev.TodoID == snap.TodoID {
		return "", false
	}
	return snap.TodoID, true
}

// fallbackSource is the request a transfer of a failed job is built from, plus the two
// things that decide how the takeover is submitted: where its non-wire lineage comes
// from, and whether the request IS an argv rather than a prompt.
type fallbackSource struct {
	// Req is the request the candidate agent re-runs.
	Req JobRequest
	// Snapshot is the job the request came from. CallerID / SourceJobID /
	// RequestedAgent are json:"-" (they never ride request_json), so the lineage is
	// restored from here.
	Snapshot JobResult
	// ExecShaped says the request's argv IS the work: there is no prompt to prefix with
	// the handover note.
	ExecShaped bool
	// Rebuilt says the request was assembled from the checklist item instead of being
	// replayed from an earlier run.
	Rebuilt bool
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
//
// The request it re-runs is the failed WORK's own, never the previous plan item's: the
// lineage walk stops at the todo boundary, and a todo continuation is rebuilt from its
// item (see fallbackSourceFor).
func (s *Service) fallBack(snap JobResult, hit, nextAgent string) bool {
	src, ok := s.fallbackSourceFor(snap)
	if !ok {
		return false
	}
	req := src.Req
	// CallerID / SourceJobID / RequestedAgent are not part of the client-facing JSON
	// (tag "-"), so the lineage is restored from the snapshot (mirroring
	// maybeRetryJob). RequestedAgent falls back to the base request's agent: the first
	// hop is the job the caller actually submitted.
	req.CallerID = src.Snapshot.CallerID
	// The takeover re-runs the failed work, so it stays bounded by whoever caused that
	// work — not by the gofer process that decided to fail over.
	req.OriginCaller = src.Snapshot.OriginCaller
	req.SourceJobID = src.Snapshot.SourceJobID
	requestedAgent := src.Snapshot.RequestedAgent
	if requestedAgent == "" {
		requestedAgent = src.Snapshot.Agent
		if src.Rebuilt {
			// A REBUILT request comes from the item, so its snapshot is the continuation
			// carrier itself — whose agent is gofer's own "exec" argv runner and says
			// nothing about the work. The agent that carrier re-drove is the one the
			// takeover takes over from.
			requestedAgent = firstNonEmptyString(src.Snapshot.ResumeAgent, requestedAgent)
		}
	}
	req.RequestedAgent = requestedAgent
	if !src.Rebuilt {
		// The checklist item follows the chain, and a worker-local row only DISPLAYS the
		// hub's todo (TodoForeign) — exactly as the continuation base does. A rebuilt
		// request already carries the item it was assembled for.
		req.TodoForeign = src.Snapshot.TodoForeign
	}
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
	if !src.ExecShaped {
		req.Prompt = fallbackPrompt(src.Snapshot.Agent, hit, req.Prompt)
	}

	// gofer-3db4: a failover replays the failed run's request on another agent — the
	// end-notification must not be the reason the rescue is refused.
	req.NotifyDegradable = true
	res, err := s.Submit(req)
	if err != nil {
		// PERMANENT: the ceiling of whoever caused the failed work refuses the takeover, so
		// the chain ends here — the failure stays terminal (the caller's late job.terminal)
		// and the source job records WHY no candidate took it over (design §准入).
		if errors.Is(err, ErrCeilingDenied) {
			s.recordCeilingDenied(snap.ID, "fallback", err)
		}
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
