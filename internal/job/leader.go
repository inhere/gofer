package job

// leader.go is MCP-05 阶段 B (design §二.B): the "leader round".
//
// A member job that belongs to a plan (`PlanID != ""`) reaching a FINISHED state
// writes a durable PENDING wake (jobstore.plan_leader_wakes). The sweeper
// (SweepDueLeaderWakes, driven by a serve loop) turns a due wake into a LEADER JOB: a
// normal job — usage, timeout, AUTO-03 takeover all apply — that runs the configured
// leader agent, is bound to the same plan and carries `leader` + `leader_of:<member>`
// tags. Its prompt is the decision brief (plan goal + checklist + the member's report
// tail + what it may do next), and its tools are the whitelisted MCP subset.
//
// The gates, in order, and why each exists:
//
//	opt-in        supervisor.leader.enabled — nil/absent means the whole feature is off
//	scope         the plan scope (the only one implemented)
//	member        a plan-attached job, NOT a leader job, NOT carrying the `leader` tag
//	state         done / failed / needs_review — the three endings a human would react to
//	paused        a paused plan is a 一键叫停: nothing is even armed (`plan.leader_skipped`)
//	budget        max_rounds_per_scope, counted from the rows that actually started a
//	              leader job; a spent budget escalates to a human instead
//
// Two more rules keep it from running away: a human's comment anywhere under the plan
// cancels the pending round (the wake delay is exactly that window), and the leader's
// own terminal state never arms another round. The leader can never accept or reject —
// 人工验收 stays a human's call (GATE-01 §3), enforced at the review routes.

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Leader-round event types (MCP-05 阶段 B, design §二.B.5). They are recorded on the
// PLAN's scope (PlanEventScope), like plan.blocked: there is no job to hang them on.
// Only plan.leader_exhausted is in the notification default set — a leader that hands
// the plan back to a human is the "someone must act" signal the others are not.
const (
	// EventPlanLeaderWoken: a leader job was started for a finished member job.
	EventPlanLeaderWoken = "plan.leader_woken"
	// EventPlanLeaderSkipped: a round was armed but not started (reason: paused |
	// submit_failed | unknown_plan | budget).
	EventPlanLeaderSkipped = "plan.leader_skipped"
	// EventPlanLeaderCancelled: a human spoke inside the wake window (by: the author).
	EventPlanLeaderCancelled = "plan.leader_cancelled"
	// EventPlanLeaderExhausted: the plan's round budget is spent — the chain goes back
	// to a human (design §二.B.3).
	EventPlanLeaderExhausted = "plan.leader_exhausted"
)

// The provenance a leader job carries: the two tags the design names (`leader`,
// `leader_of:<member job>`), the round it belongs to, and the submission channel.
// `leaderTag` is also the SWITCH the wake rules read: any job carrying it is treated as
// a leader run, whoever started it.
const (
	leaderTag       = "leader"
	leaderOfTag     = "leader_of:"
	leaderRoundTag  = "leader_round:"
	leaderChannel   = "leader"
	leaderCallerID  = "gofer"
	leaderSkipPause = "paused"
)

// maybeWakeLeader arms a leader round for a job that just reached a FINISHED state
// (MCP-05 阶段 B). It is called from the two places a plan-attached job ends — the
// terminal tail of finish() and its needs_review branch — and is best-effort
// throughout: a job must never fail (or fail to park) because a leader could not be
// woken, so every error is logged and the job's own state stays authoritative.
func (s *Service) maybeWakeLeader(snap JobResult) {
	cfg := s.config()
	leader := cfg.LeaderConfig()
	if leader == nil || !leader.Enabled || !leader.ScopeEnabled(config.LeaderPlanScope) {
		return
	}
	if !leader.MemberDoneWakes() {
		return
	}
	if snap.PlanID == "" || snap.LeaderOfPlan != "" || hasJobTag(snap.Tags, leaderTag) {
		return
	}
	switch snap.Status {
	case StatusDone, StatusFailed, StatusNeedsReview:
	default:
		return
	}
	armed, err := s.meta.LeaderWakeArmedForMember(snap.ID)
	if err != nil {
		slog.Warn("leader wake: check armed", "job_id", snap.ID, "err", err)
		return
	}
	if armed {
		return
	}
	plan, ok, err := s.meta.GetPlan(snap.PlanID)
	if err != nil || !ok {
		return
	}
	scope := PlanEventScope(plan.PlanID)
	if plan.Paused {
		// The one-click stop: a plan held by a human does not summon a leader.
		s.RecordScopedEvent(scope, EventPlanLeaderSkipped, plan.ProjectKey, map[string]any{
			"plan_id": plan.PlanID, "job": snap.ID, "reason": leaderSkipPause,
		})
		return
	}
	spent, err := s.meta.LeaderRoundsSpent(plan.PlanID)
	if err != nil {
		slog.Warn("leader wake: count rounds", "plan_id", plan.PlanID, "err", err)
		return
	}
	if spent >= leader.MaxRounds() {
		s.exhaustLeaderRounds(plan, snap, spent)
		return
	}
	now := s.nowFn()
	w := jobstore.LeaderWake{
		ID:           "lw-" + RandomSuffix(),
		PlanID:       plan.PlanID,
		MemberJobID:  snap.ID,
		MemberStatus: snap.Status,
		DueAt:        now.Add(leader.WakeDelay()).Unix(),
		State:        jobstore.LeaderWakePending,
		Round:        spent + 1,
		CreatedAt:    now.Unix(),
	}
	if err := s.meta.InsertLeaderWake(w); err != nil {
		slog.Warn("leader wake: insert", "plan_id", plan.PlanID, "job_id", snap.ID, "err", err)
	}
}

// SweepDueLeaderWakes starts the leader job of every armed round that has come due,
// and reports how many it started. It is the ONLY place a leader job is submitted
// (mirroring SweepDueRetries): the row is what makes a pending round survive a restart,
// and the claim is what keeps two sweeps (or a sweep and a reload) from starting the
// same round twice.
//
// A round that cannot start is never silent: the reason goes to the plan's event stream
// and the round is released (transient: a submit failure) or cancelled (terminal: the
// plan vanished, or its budget was spent in the meantime).
func (s *Service) SweepDueLeaderWakes(now int64) (int, error) {
	due, err := s.meta.DueLeaderWakes(now)
	if err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}
	cfg := s.config()
	leader := cfg.LeaderConfig()
	if leader == nil || !leader.Enabled || !leader.ScopeEnabled(config.LeaderPlanScope) || !leader.MemberDoneWakes() {
		return 0, nil
	}
	started := 0
	for _, w := range due {
		if ok := s.fireLeaderWake(cfg, leader, w); ok {
			started++
		}
	}
	return started, nil
}

// fireLeaderWake starts ONE armed round and reports whether a leader job was started.
func (s *Service) fireLeaderWake(cfg *config.Config, leader *config.LeaderConfig, w jobstore.LeaderWake) bool {
	scope := PlanEventScope(w.PlanID)
	plan, ok, err := s.meta.GetPlan(w.PlanID)
	if err != nil || !ok {
		s.meta.CancelLeaderWake(w.ID, "gofer:no_plan", s.nowFn().Unix())
		return false
	}
	if plan.Paused {
		// Paused between arming and firing: drop the round rather than hold it. When the
		// human resumes, the chain moves on; a member that finishes then arms a fresh one.
		if cancelled, cerr := s.meta.CancelLeaderWake(w.ID, "gofer:"+leaderSkipPause, s.nowFn().Unix()); cerr == nil && cancelled {
			s.RecordScopedEvent(scope, EventPlanLeaderSkipped, plan.ProjectKey, map[string]any{
				"plan_id": plan.PlanID, "job": w.MemberJobID, "reason": leaderSkipPause,
			})
		}
		return false
	}
	// Re-check the budget HERE (not only when the round was armed): two member jobs can
	// arm in the same window, and the cap must hold however they interleave.
	spent, err := s.meta.LeaderRoundsSpent(plan.PlanID)
	if err != nil {
		slog.Warn("leader sweep: count rounds", "plan_id", plan.PlanID, "err", err)
		return false
	}
	if spent >= leader.MaxRounds() {
		if cancelled, cerr := s.meta.CancelLeaderWake(w.ID, "gofer:budget", s.nowFn().Unix()); cerr == nil && cancelled {
			member, _, _ := s.meta.GetJob(w.MemberJobID)
			s.exhaustLeaderRounds(plan, JobResult{ID: w.MemberJobID, PlanID: plan.PlanID, Status: member.Status}, spent)
		}
		return false
	}
	claimed, err := s.meta.ClaimLeaderWake(w.ID)
	if err != nil || !claimed {
		return false
	}
	// The ROUND NUMBER is decided here, not when the round was armed: two members can
	// finish inside the same wake window, and each leader job must be told which round it
	// is (and be counted as one).
	w.Round = spent + 1
	req, err := s.leaderJobRequest(cfg, leader, plan, w)
	if err == nil {
		var res JobResult
		if res, err = s.Submit(req); err == nil {
			if _, merr := s.meta.SetLeaderWakeFired(w.ID, res.ID, w.Round); merr != nil {
				slog.Warn("leader sweep: mark fired", "wake_id", w.ID, "job_id", res.ID, "err", merr)
			}
			s.RecordScopedEvent(scope, EventPlanLeaderWoken, plan.ProjectKey, map[string]any{
				"plan_id": plan.PlanID, "round": w.Round, "job": w.MemberJobID,
				"member_status": w.MemberStatus, "leader_job": res.ID, "agent": res.Agent,
			})
			return true
		}
	}
	// The round cannot start — a misconfigured leader agent/role (the project's
	// allowlist rejects it), or a plan that lost its project. The round is CLOSED
	// rather than retried: the reason is on the plan's stream for a human to fix, and
	// the next member terminal arms a fresh round. Silently retrying every tick would
	// only repeat the same refusal forever.
	s.logLeaderSkipped(scope, plan, w, "submit_failed", err.Error())
	if _, cerr := s.meta.CancelLeaderWake(w.ID, "gofer:submit_failed", s.nowFn().Unix()); cerr != nil {
		slog.Warn("leader sweep: cancel failed round", "wake_id", w.ID, "err", cerr)
	}
	return false
}

// leaderJobRequest builds the leader job a round starts: the configured agent (or
// role), the same plan, the decision-brief prompt, and the provenance tags. The
// runner is the built-in local one: the round is driven by THIS server's store and
// config, and its marker (LeaderOfPlan) is server-side — it must not travel to another
// machine as a request property.
func (s *Service) leaderJobRequest(cfg *config.Config, leader *config.LeaderConfig, plan jobstore.Plan, w jobstore.LeaderWake) (JobRequest, error) {
	name := strings.TrimSpace(leader.Agent)
	if name == "" {
		return JobRequest{}, fmt.Errorf("supervisor.leader.agent is not configured")
	}
	projectKey := plan.ProjectKey
	if projectKey == "" {
		return JobRequest{}, fmt.Errorf("plan %s has no project", plan.PlanID)
	}
	member, _ := s.Get(w.MemberJobID)
	req := JobRequest{
		ProjectKey: projectKey,
		Runner:     builtinLocalRunner,
		Title:      "leader 回合 " + strconv.Itoa(w.Round) + "：" + leaderTitle(plan),
		Prompt:     s.leaderPrompt(plan, member, w),
		PlanID:     plan.PlanID,
		Tags: []string{
			leaderTag,
			leaderOfTag + w.MemberJobID,
			leaderRoundTag + strconv.Itoa(w.Round),
		},
		Channel:  leaderChannel,
		CallerID: leaderCallerID,
		// The marker: server-set, persisted on the row, and what makes this job's
		// comments dispatchable while barring it from reviewing.
		LeaderOfPlan: plan.PlanID,
	}
	// The leader is named by agent key or by role — resolveCommentMention is the same
	// declare-wins resolution the comment dispatcher uses (an agent key wins over a role
	// of the same name), so `supervisor.leader.agent` cannot mean two different things
	// in two places.
	kind, _, ok := resolveCommentMention(cfg, name)
	if !ok {
		return JobRequest{}, fmt.Errorf("supervisor.leader.agent %q is neither a configured agent nor a role", name)
	}
	if kind == "role" {
		req.Role = name
	} else {
		req.Agent = name
	}
	return req, nil
}

// leaderPrompt renders the decision brief a leader receives (design §二.B: plan goal +
// checklist + the finished job's report + the action list). It is built at FIRE time,
// not when the round was armed, so it describes the plan as it stands NOW — the delay
// is a window a human (or the chain itself) may have changed things in.
func (s *Service) leaderPrompt(plan jobstore.Plan, member JobResult, w jobstore.LeaderWake) string {
	var b strings.Builder
	fmt.Fprintf(&b, "你是 plan %s 的 leader（第 %d 轮）。一个成员 job 刚刚结束，请你读完它的汇报，决定下一步："+
		"派活、推进待办、建 wakeup，或者升级给人。\n\n", plan.PlanID, w.Round)

	b.WriteString("## Plan\n")
	if t := strings.TrimSpace(plan.Title); t != "" {
		fmt.Fprintf(&b, "标题：%s\n", t)
	}
	fmt.Fprintf(&b, "状态：%s", plan.Status)
	if plan.Paused {
		b.WriteString("（已暂停）")
	}
	if plan.BlockedTodo != "" {
		fmt.Fprintf(&b, "（阻塞在 %s）", plan.BlockedTodo)
	}
	b.WriteString("\n")
	if d := strings.TrimSpace(plan.Description); d != "" {
		fmt.Fprintf(&b, "目标：%s\n", d)
	}

	b.WriteString("\n## 待办链现状\n")
	b.WriteString(s.leaderTodoTable(plan.PlanID))

	b.WriteString("\n## 刚结束的成员 job\n")
	if member.ID == "" {
		fmt.Fprintf(&b, "（job %s 已不在库里，只知状态 %s）\n", w.MemberJobID, w.MemberStatus)
	} else {
		b.WriteString(s.commentJobContext(member))
	}

	b.WriteString("\n## 你可以做的（只有这些 MCP 工具）\n")
	b.WriteString("- gofer_list_comments：先读 plan / todo / job 的评论区，看看人和成员都说过什么。\n")
	b.WriteString("- gofer_comment：在评论区说话；正文里写 `@<agent 或 role>` 会**真的派活**（你是本 plan 的 leader，你的 @提及生效）。\n")
	b.WriteString("- gofer_get_plan：读 plan 与待办现状。\n")
	b.WriteString("- gofer_update_todo：把某个待办置 `ready`（已指派即派活）或 `skipped`；**只接受 ready|skipped**，其他状态会被拒绝。\n")
	b.WriteString("- gofer_wakeup_create：给某个 job 建定时/事件 wakeup。\n")
	b.WriteString("- gofer_ask_human：拿不准就升级给人（会阻塞到有人回答或超时）。\n")
	b.WriteString("\n## 你不能做的\n")
	b.WriteString("- **不能** accept/reject：验收永远由人做；也不能把待办直接标 done。\n")
	b.WriteString("- 不能改配置、不能 push、不能替人做最终决定。\n")
	b.WriteString("\n## 规则\n")
	fmt.Fprintf(&b, "- 这是第 %d 轮；轮次用尽后 gofer 会停止唤醒并升级给人。\n", w.Round)
	b.WriteString("- 人一旦在评论区说话，本轮唤醒即被取消，由人接管；不要和人的决定抢时间。\n")
	b.WriteString("- 不要自己实现活：写清楚任务，用 @提及 或把待办置 ready 派给成员。\n")
	return b.String()
}

// leaderTodoTable renders a plan's checklist as the leader's status table: one line per
// item with its status, id, assignee, dependencies and the tail of its note (the note
// is where a member's outcome line lands). It is bounded like the comment context, so a
// 200-item plan does not become the prompt.
func (s *Service) leaderTodoTable(planID string) string {
	todos, err := s.meta.ListTodosByPlan(planID)
	if err != nil {
		return "（待办读取失败）\n"
	}
	if len(todos) == 0 {
		return "（这个 plan 还没有待办）\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 项：\n", len(todos))
	for i, t := range todos {
		if i == commentPlanContextTodos {
			fmt.Fprintf(&b, "  … 还有 %d 项\n", len(todos)-i)
			break
		}
		fmt.Fprintf(&b, "  - [%s] %s %s", t.Status, t.TodoID, strings.TrimSpace(t.Title))
		if t.Assignee != "" {
			fmt.Fprintf(&b, "（指派：%s", t.Assignee)
			if len(t.After) > 0 {
				fmt.Fprintf(&b, "，依赖：%s", strings.Join(t.After, ","))
			}
			b.WriteString("）")
		} else if len(t.After) > 0 {
			fmt.Fprintf(&b, "（依赖：%s）", strings.Join(t.After, ","))
		}
		if note := lastNoteLine(t.Note); note != "" {
			fmt.Fprintf(&b, " — %s", note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// leaderTitle is the plan's title for the leader job's own title (a plan without one
// still yields a readable job title).
func leaderTitle(p jobstore.Plan) string {
	if t := strings.TrimSpace(p.Title); t != "" {
		return t
	}
	return p.PlanID
}

// lastNoteLine returns the last non-empty line of a todo note, capped — the checklist's
// own log tail, which is what the leader reads to see how the previous item went.
func lastNoteLine(note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return ""
	}
	lines := strings.Split(note, "\n")
	return firstRunes(strings.TrimSpace(lines[len(lines)-1]), 160)
}

// exhaustLeaderRounds is the "budget spent" hand-back (design §二.B.3): the plan's round
// budget is gone, so gofer stops waking leaders and gives the chain to a human — the
// plan.leader_exhausted event (in the notification default set, like plan.blocked) plus
// an OPEN decision, which is the same 待人处理 item gofer_ask_human raises and the web
// plan page surfaces.
func (s *Service) exhaustLeaderRounds(plan jobstore.Plan, member JobResult, spent int) {
	s.RecordScopedEvent(PlanEventScope(plan.PlanID), EventPlanLeaderExhausted, plan.ProjectKey, map[string]any{
		"plan_id": plan.PlanID, "job": member.ID, "rounds": spent,
	})
	d := jobstore.PlanDecision{
		PlanID: plan.PlanID,
		Title:  "leader 轮次已用尽：" + leaderTitle(plan),
		Question: fmt.Sprintf("plan %s 的 leader 已用满轮次，最近一次成员 job 是 %s（%s）。请接手：继续推进这个 plan，或把它停下。",
			plan.PlanID, member.ID, member.Status),
		State: jobstore.DecisionOpen,
	}
	if err := s.meta.InsertDecision(&d); err != nil {
		slog.Warn("leader exhausted: raise decision", "plan_id", plan.PlanID, "err", err)
	}
}

// logLeaderSkipped records an armed round that did not start, with the reason.
func (s *Service) logLeaderSkipped(scope string, plan jobstore.Plan, w jobstore.LeaderWake, reason, detail string) {
	fields := map[string]any{
		"plan_id": plan.PlanID, "job": w.MemberJobID, "reason": reason,
	}
	if detail != "" {
		fields["error"] = detail
	}
	s.RecordScopedEvent(scope, EventPlanLeaderSkipped, plan.ProjectKey, fields)
}

// CancelLeaderWakesForPlan stops every round of a plan that has not started a leader
// job yet and reports how many it stopped (MCP-05 阶段 B: a human speaking in the
// thread takes the round over). It is called from the comment path with the author as
// `by`; the plan's event stream records who intervened.
func (s *Service) CancelLeaderWakesForPlan(planID, by string) int {
	if planID == "" {
		return 0
	}
	n, err := s.meta.CancelLeaderWakesForPlan(planID, by, s.nowFn().Unix())
	if err != nil {
		slog.Warn("cancel leader wakes", "plan_id", planID, "err", err)
		return 0
	}
	if n == 0 {
		return 0
	}
	projectKey := ""
	if p, ok, err := s.meta.GetPlan(planID); err == nil && ok {
		projectKey = p.ProjectKey
	}
	s.RecordScopedEvent(PlanEventScope(planID), EventPlanLeaderCancelled, projectKey, map[string]any{
		"plan_id": planID, "by": by, "cancelled": n,
	})
	return int(n)
}

// IsLeaderJob reports whether jobID is a plan's leader job (MCP-05 阶段 B): the row's
// server-set marker is the authority — an unknown id is simply not one. The HTTP review
// gate and the MCP tool surface read it; the job's own agent cannot clear it.
func (s *Service) IsLeaderJob(jobID string) bool {
	if jobID == "" {
		return false
	}
	rec, ok, err := s.meta.GetJob(jobID)
	if err != nil || !ok {
		return false
	}
	return rec.LeaderOfPlan != ""
}

// LeaderStatus is what a plan page shows about its leader rounds: whether the round is
// on, how many rounds are spent against the cap, and the last leader job started.
type LeaderStatus struct {
	Enabled   bool
	Round     int
	MaxRounds int
	LastJobID string
}

// LeaderStatus reads a plan's round state: the configured cap, the rounds already spent
// (the rows that started a leader job) and the most recent one's job id. It never
// fails: a store error reads as "no rounds", which is what a plan without any looks like.
func (s *Service) LeaderStatus(planID string) LeaderStatus {
	leader := s.config().LeaderConfig()
	st := LeaderStatus{
		Enabled:   leader != nil && leader.Enabled && leader.ScopeEnabled(config.LeaderPlanScope),
		MaxRounds: leader.MaxRounds(),
	}
	if !st.Enabled || planID == "" {
		return st
	}
	if spent, err := s.meta.LeaderRoundsSpent(planID); err == nil {
		st.Round = spent
	}
	if id, err := s.meta.LatestLeaderJob(planID); err == nil {
		st.LastJobID = id
	}
	return st
}

// hasJobTag reports whether tags carries one exact tag (the same membership test the
// tag filters use; kept local so the leader rules never guess at partial matches).
func hasJobTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
