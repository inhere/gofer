package job

// todo_continue.go decides whether a plan item continues the agent session of the item
// it names in continue_from (design 2026-10-17-plan-todo-continue-session), and keeps
// the bookkeeping around that decision: the predecessor's continuation slot, the
// continue_note that says why a dispatch started a fresh session instead, and the
// events.
//
// The decision is made at dispatch time, on top of the ORDINARY request (normalTodoRequest):
// every unmet condition is a fallback to that request, never an error. When all hold, the
// continuation request is assembled on that same base (assembleContinuation): only the
// session fields come from the predecessor's job.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/delivery"
	"github.com/inhere/gofer/internal/jobstore"
)

const (
	// continueChainMaxHops bounds the resumed_from walk that counts a chain: it only
	// guards against a cycle in damaged data. It must stay well above the largest limit
	// (config.MaxContinueMaxChain) because automatic continuations and rework add hops
	// that are not counted.
	continueChainMaxHops = 256

	// continueBridgeFormat is the line a continuation prompt opens with: the session
	// already knows the previous item, the task book that follows is the new one.
	continueBridgeFormat = "本任务接着上一项（%s）在同一个会话里继续。上一项已完成；下面是新的任务说明，以它为准。"

	// Reasons of the claim step (condition 7), distinguished on purpose: a human fixing a
	// plan needs to know whether another item took the session or this one already did.
	continueClaimedByOther = "已被其他项续接"
	continueClaimedBySelf  = "已由本项占用"
)

// continueFallbackNote is what continue_note stores for a reason.
func continueFallbackNote(reason string) string {
	return "未接续：" + reason + "，已开新会话"
}

// continueSubmitError reports whether a refused submit of a continuation request is of
// the continuation class: the session or the way to continue it is not usable, which an
// ordinary request does not need. Anything else (a project that refuses the agent, a bad
// field) would refuse the ordinary request the same way and stays a dispatch error.
func continueSubmitError(err error) bool {
	return errors.Is(err, ErrNoSession) || errors.Is(err, ErrCrossRunner) ||
		errors.Is(err, ErrResumeUnsupported) || errors.Is(err, ErrJobNotTerminal)
}

// recordContinueFallback stores the reason a todo was dispatched without continuing its
// predecessor's session and emits job.continue_fallback on the plan's scope (no job
// exists yet to hang it on).
//
// baseNote is what basePredecessorBranch said about the new worktree ("" = nothing): it
// follows the reason in the note, and the branch rides the event.
func (s *Service) recordContinueFallback(todo jobstore.PlanTodo, projectKey, reason, baseNote, baseBranch string) {
	if err := s.meta.SetTodoContinueNote(todo.TodoID, continueFallbackNote(reason)+baseNote); err != nil {
		slog.Warn("store todo continue note", "todo_id", todo.TodoID, "err", err)
	}
	detail := map[string]any{"todo_id": todo.TodoID, "reason": reason}
	if baseBranch != "" {
		detail["worktree_base"] = baseBranch
	}
	s.RecordScopedEvent(PlanEventScope(todo.PlanID), EventJobContinueFallback, projectKey, detail)
}

// basePredecessorBranch is called when todo starts a fresh session although it names a
// predecessor (continue_from): when that predecessor's job left a worktree branch that
// is not merged yet and the ordinary request will run in a new worktree, the new
// worktree starts at that branch (WorktreeBase), so the item sees the commits before
// it. It returns the sentence for continue_note and the branch; "" when nothing changed
// (no predecessor job or branch, merged, another cwd or runner, no worktree wanted).
func (s *Service) basePredecessorBranch(todo jobstore.PlanTodo, req *JobRequest) (note, branch string) {
	if todo.ContinueFrom == "" || req.WorktreeBase != "" || !config.IsLocalRunnerName(req.Runner) {
		return "", ""
	}
	pred, ok, err := s.meta.GetTodo(todo.ContinueFrom)
	if err != nil || !ok || pred.PlanID != todo.PlanID {
		return "", ""
	}
	predJob, found, err := s.continuationPredecessorJob(pred.TodoID)
	if err != nil || !found || predJob.WorktreePath == "" || predJob.WorktreeBranch == "" ||
		predJob.WorkerID != "" || !config.IsLocalRunnerName(predJob.Runner) {
		return "", ""
	}
	// The branch lives in the repository the predecessor worked in.
	if todo.Cwd != "" && cleanCwd(todo.Cwd) != cleanCwd(s.resumeCwd(predJob)) {
		return "", ""
	}
	// Will the item run in a worktree at all? Its task book may ask for one.
	cfg := s.config()
	eff := *req
	if eff.Template != "" {
		tpl, rerr := s.renderTemplate(cfg, &eff)
		if rerr != nil {
			return "", ""
		}
		applyTemplateMeta(&eff, tpl)
	}
	if !worktreeRequested(cfg, &eff) {
		return "", ""
	}
	if st, werr := s.WorktreeStatus(predJob.ID); werr != nil || !st.Exists || st.Merged {
		return "", ""
	}
	req.WorktreeBase = predJob.WorktreeBranch
	return fmt.Sprintf("；新 worktree 以前序分支 %s 为基", predJob.WorktreeBranch), predJob.WorktreeBranch
}

// predecessorEndPoint is where the predecessor's work ended: the head of its worktree
// branch when it had a worktree, else the newest commit it made, else the commit it
// started from (nothing committed). The result is a full commit id, or "" when the
// point cannot be told (no repository, nothing recorded) — the continuation then
// measures from its own start, as any job does.
func (s *Service) predecessorEndPoint(predJob JobResult) string {
	dir, ref := predJob.Cwd, ""
	if predJob.WorktreePath != "" {
		dir, ref = predJob.WorktreePath, predJob.WorktreeHeadSHA
	} else if len(predJob.Commits) > 0 {
		ref = predJob.Commits[0].SHA
	} else {
		ref = predJob.BaseSHA
	}
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	if ref == "" && predJob.WorktreePath != "" {
		ref = "HEAD" // the end capture did not record it: the worktree still holds it
	}
	if ref == "" {
		return ""
	}
	sha, err := gitOut(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || !baselineSHARe.MatchString(sha) {
		return ""
	}
	return sha
}

// releaseTodoContinuation gives back the slot a continuation attempt of todo took.
func (s *Service) releaseTodoContinuation(todo jobstore.PlanTodo) {
	if todo.ContinueFrom == "" {
		return
	}
	if err := s.meta.ReleaseTodoContinuation(todo.ContinueFrom, todo.TodoID); err != nil {
		slog.Warn("release todo continuation", "todo_id", todo.TodoID, "pred", todo.ContinueFrom, "err", err)
	}
}

// continueMaxChain is the chain length limit of a project; 0 means unlimited.
func (s *Service) continueMaxChain(projectKey string) int {
	return s.config().EffectiveContinueMaxChain(projectKey)
}

// todoContinuationRequest evaluates the ten conditions under which todo continues the
// session of the item named in its continue_from, in this order, and stops at the first
// one that does not hold (its description is the returned reason):
//
//  1. continue_from names a todo of the same plan that still exists and is done;
//  2. that todo has a job that ended done with a captured session_id;
//  3. the assignee is not exec and belongs to the session family of the agent that
//     owns that session;
//  4. the runner is the same and is the local one;
//  5. the todo sets no cwd, or the cwd of the predecessor's job;
//  6. a worktree of the predecessor still exists and does not need auto delivery;
//  7. the predecessor's continuation slot is claimed for this todo;
//  8. the chain of continuations has not reached its limit;
//  9. the configuration can continue it: the agent resumes, and the model / budget the
//     todo asks for are available on that agent;
//  10. the todo's read-only setting equals the predecessor's.
//
// On success the slot stays claimed and the request is returned; the caller releases the
// slot if the submit does not stand. On any failure the slot is NOT held.
func (s *Service) todoContinuationRequest(plan jobstore.Plan, todo jobstore.PlanTodo, normalReq JobRequest) (JobRequest, string) {
	cfg := s.config()

	// 1
	pred, ok, err := s.meta.GetTodo(todo.ContinueFrom)
	if err != nil {
		return JobRequest{}, "读取前序项失败：" + err.Error()
	}
	if !ok || pred.PlanID != todo.PlanID {
		return JobRequest{}, "前序项不存在"
	}
	if pred.Status != jobstore.TodoDone {
		return JobRequest{}, fmt.Sprintf("前序项状态为 %s，未完成", pred.Status)
	}

	// 2
	predJob, found, err := s.continuationPredecessorJob(pred.TodoID)
	if err != nil {
		return JobRequest{}, "读取前序 job 失败：" + err.Error()
	}
	if !found {
		return JobRequest{}, "前序项没有已完成且带会话的 job"
	}

	// 3
	if todo.Assignee == agent.ExecAgentKey {
		return JobRequest{}, "exec 不能续接会话"
	}
	if _, err := s.checkResume(predJob.ID, "", ResumeOptions{Agent: todo.Assignee}); err != nil {
		return JobRequest{}, "前序会话不可续接：" + err.Error()
	}

	// 4
	runner := config.NormalizeRunnerName(normalReq.Runner)
	if !config.IsLocalRunnerName(runner) || config.NormalizeRunnerName(predJob.Runner) != runner || predJob.WorkerID != "" {
		return JobRequest{}, fmt.Sprintf("runner 不同或非本机（本项 %q，前序 %q）", normalReq.Runner, predJob.Runner)
	}

	// 5
	if todo.Cwd != "" && cleanCwd(todo.Cwd) != cleanCwd(s.resumeCwd(predJob)) {
		return JobRequest{}, fmt.Sprintf("本项 cwd %q 与前序 job 的 %q 不同", todo.Cwd, s.resumeCwd(predJob))
	}

	// 6
	if predJob.WorktreePath != "" {
		if st, err := os.Stat(predJob.WorktreePath); err != nil || !st.IsDir() {
			return JobRequest{}, "前序 job 的 worktree 已不存在"
		}
		if EffectiveDeliveryMode(cfg, requestOfJob(predJob)) != delivery.ModeNone {
			return JobRequest{}, "前序 job 需要自动交付"
		}
	}

	// 7
	claimed, err := s.meta.ClaimTodoContinuation(pred.TodoID, todo.TodoID)
	if err != nil {
		return JobRequest{}, "占用续接位失败：" + err.Error()
	}
	if !claimed {
		if fresh, ok, gerr := s.meta.GetTodo(pred.TodoID); gerr == nil && ok && fresh.ContinuedBy == todo.TodoID {
			return JobRequest{}, continueClaimedBySelf
		}
		return JobRequest{}, continueClaimedByOther
	}
	// Every condition below can still fail: give the slot back when one does.
	cp, reason := s.continuationRemainder(cfg, todo, normalReq, predJob)
	if reason == "" {
		req, why := s.assembleContinuation(cfg, pred, predJob, cp)
		if why == "" {
			return req, ""
		}
		reason = why
	}
	s.releaseTodoContinuation(todo)
	return JobRequest{}, reason
}

// continuationPlan is what conditions 8 to 10 settled and the assembly builds on: the
// request as Submit would see it (the task book rendered into the prompt, its defaults
// folded in) and the validated resume of the predecessor's session.
type continuationPlan struct {
	eff JobRequest
	chk resumeCheck
}

// continuationRemainder evaluates conditions 8 to 10, which come after the slot is
// claimed. It returns the reason one fails, or "" with the plan for the assembly.
func (s *Service) continuationRemainder(cfg *config.Config, todo jobstore.PlanTodo, normalReq JobRequest, predJob JobResult) (continuationPlan, string) {
	// 8
	if limit := s.continueMaxChain(normalReq.ProjectKey); limit > 0 {
		if n := s.continuationChainLength(predJob); n >= limit {
			return continuationPlan{}, fmt.Sprintf("已连续接续 %d 次", n)
		}
	}

	// 9 and 10 look at the request as Submit would see it: with the task book's
	// defaults (model, budget, read-only) folded in.
	eff := normalReq
	if eff.Template != "" {
		tpl, err := s.renderTemplate(cfg, &eff)
		if err != nil {
			return continuationPlan{}, "任务书渲染失败：" + err.Error()
		}
		applyTemplateMeta(&eff, tpl)
	}
	chk, err := s.checkResume(predJob.ID, "", ResumeOptions{Agent: todo.Assignee, Model: eff.Model, Budget: eff.Budget})
	if err != nil {
		return continuationPlan{}, "配置不支持续接：" + err.Error()
	}
	if !resumable(chk.Agent) {
		return continuationPlan{}, fmt.Sprintf("agent %q 没有可用的会话续接方式", chk.TargetAgent)
	}

	// 10
	if eff.ReadOnly != predJob.ReadOnly {
		return continuationPlan{}, fmt.Sprintf("只读设置不一致（本项 %t，前序 %t）", eff.ReadOnly, predJob.ReadOnly)
	}
	return continuationPlan{eff: eff, chk: chk}, ""
}

// assembleContinuation builds the request that continues predJob's session for a plan
// item. The base is the ORDINARY request of the item (cp.eff: caller, channel, plan and
// todo linkage, notification, acceptance, scope, verify, review, timeout, budget, tags,
// read-only, the task book's defaults); from the predecessor's job it takes only the
// session fields — session id, lineage, cwd, the worktree to keep, the runner and its
// resolved MCP. Hold, the frozen fallback plan, delivery, lock paths, secrets, env files,
// the " (resumed)" title suffix and ReviewFixed are NOT inherited: they are simply never
// copied.
//
// The prompt is rendered completely here and submitted as already decided
// (RulesResolved, SkillsResolved): the bridging line, the item's task book (or the
// default prompt) and the item's own acceptance, delivery contract and time-limit line.
// Rules, tracker-prime, the tools section and the skills list are not repeated — the
// session has them. A non-empty reason means the request could not be built.
func (s *Service) assembleContinuation(cfg *config.Config, pred jobstore.PlanTodo, predJob JobResult, cp continuationPlan) (JobRequest, string) {
	req, chk := cp.eff, cp.chk

	// The task book is rendered into the prompt already; Submit must not render it again.
	dropTemplate(&req)
	// The session's worktree is kept, never a new one (a task book may ask for one).
	req.Worktree, req.WorktreeBase = false, ""

	// Session, lineage and place: from the predecessor's job.
	req.SessionID = predJob.SessionID
	req.ResumedFrom, req.SourceJobID = predJob.ID, predJob.ID
	req.Cwd = s.resumeCwd(predJob)
	req.InheritWorktree = predJob.WorktreePath != ""
	// This item's own changes are measured from where the previous one ended; the
	// worktree's branch base (WorktreeBaseSHA) is not touched.
	req.BaselineSHA = s.predecessorEndPoint(predJob)
	req.Runner = predJob.Runner
	req.MCP = predJob.MCP
	// gofer-81b4: the item continues the predecessor's session, so it is approved the way
	// that session was (a resident 收件会话's own gate, not the project policy).
	req.SessionApproval = requestOfJob(predJob).SessionApproval
	// The item's model, else the predecessor's (checkResume settled which).
	req.Model = chk.Model

	// The time limit of THIS job, fixed before the prompt quotes it. The carrier of a cli
	// agent is an exec job, which would default to the short exec limit: the limit is
	// resolved for the agent that really does the work and passed explicitly.
	probe := req
	probe.Agent = chk.TargetAgent
	timeout, _, limit := effectiveTimeout(cfg, &probe, s.nowFn())
	if timeout > 0 {
		req.TimeoutSec = int(timeout / time.Second)
	}

	req.Prompt = strings.TrimSpace(fmt.Sprintf(continueBridgeFormat, pred.Title)) + "\n\n" + strings.TrimSpace(req.Prompt)
	// The sections are decided for the agent that really runs the work, over the prompt
	// they follow.
	probe = req
	probe.Agent = chk.TargetAgent
	sections, knowledge := buildPromptSections(cfg, &probe, chk.TargetAgent, limit)
	if sections != "" {
		req.Prompt = appendPromptSection(req.Prompt, sections)
	}
	req.KnowledgeCapture = knowledge
	req.RulesResolved, req.SkillsResolved = true, true

	var err error
	if chk.Agent.Type == agent.TypeACPAgent {
		// One turn, then the job ends: session/load is filled from ResumedFrom.
		req, err = s.acpResumeRequest(req, predJob, chk.Agent, chk.TargetAgent, req.Prompt, false, 0, true)
	} else {
		prompt := req.Prompt
		req.Prompt = ""
		req, err = s.cliCarrierRequest(req, predJob, chk.Agent, chk.TargetAgent, prompt, nil, false, false)
	}
	if err != nil {
		return JobRequest{}, "续接请求组装失败：" + err.Error()
	}
	return req, ""
}

// continuationPredecessorJob picks the job a successor continues: the MOST RECENT job
// of the predecessor todo that ended done and captured a session_id. Retries and
// rework leave several jobs on one todo, the newest of which may have failed or been
// rejected, so the list is read until a match is found or the history is exhausted —
// a fixed page could miss it behind many failures.
func (s *Service) continuationPredecessorJob(predTodoID string) (JobResult, bool, error) {
	limit := 50
	for {
		recs, err := s.meta.ListJobsByTodo(predTodoID, limit)
		if err != nil {
			return JobResult{}, false, err
		}
		for _, rec := range recs {
			if rec.Status != StatusDone || rec.SessionID == "" {
				continue
			}
			if snap, ok := s.Get(rec.ID); ok {
				return snap, true, nil
			}
		}
		if len(recs) < limit {
			return JobResult{}, false, nil
		}
		limit *= 4
	}
}

// continuationChainLength is how many times the session of job was already handed on
// from one plan item to another: the TodoID changes along its resumed_from chain
// (automatic continuations and rework keep the TodoID and do not count).
func (s *Service) continuationChainLength(job JobResult) int {
	n := 0
	cur := job
	for hop := 0; hop < continueChainMaxHops && cur.ResumedFrom != ""; hop++ {
		prev, ok := s.Get(cur.ResumedFrom)
		if !ok {
			break
		}
		if prev.TodoID != cur.TodoID {
			n++
		}
		cur = prev
	}
	return n
}

// cleanCwd compares two relative cwd spellings: "" and "." are the project root.
func cleanCwd(p string) string {
	p = strings.TrimSpace(filepath.ToSlash(p))
	if p == "" {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(p))
}
