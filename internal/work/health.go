package work

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Lane health (N3 design 2026-10-09 §3.2): how a piece of parallel work is doing,
// computed from facts the store already has. Only the non-ok values are shown.
const (
	HealthOK      = "ok"
	HealthAtRisk  = "at_risk"
	HealthStalled = "stalled"
	HealthBlocked = "blocked"
)

// healthRank orders the values worst first (blocked > stalled > at_risk > ok).
var healthRank = map[string]int{HealthBlocked: 0, HealthStalled: 1, HealthAtRisk: 2, HealthOK: 3}

// HealthRank is the sort key of a health value (lower = needs attention sooner).
func HealthRank(h string) int {
	if r, ok := healthRank[h]; ok {
		return r
	}
	return len(healthRank)
}

// milestonesInView is how many recent milestones an ItemView carries.
const milestonesInView = 5

// failureClassBudget mirrors job.FailureClassBudget (work must not import job).
const failureClassBudget = "budget"

// HealthInput is everything ComputeHealth looks at.
type HealthInput struct {
	// InProgress says the item / plan is meant to be moving right now (work status
	// active, plan open): only then can it stall. Waiting / parked work never stalls.
	InProgress bool
	// Blocker is the unresolved blocker text ("" = none).
	Blocker string
	// BlockedPlan names a linked plan whose status is blocked ("" = none).
	BlockedPlan string
	// LastActivity is the newest journal line / linked-job activity (unix s); a
	// session heartbeat does not count.
	LastActivity int64
	// AgentRunning: an agent is working on it right now (never stalled then).
	AgentRunning bool
	// LatestJob is the most recent linked job (nil = none).
	LatestJob *jobstore.JobRecord
}

// ComputeHealth applies §3.2: blocked (a blocked plan, or an unresolved blocker) >
// stalled (in progress and quiet for stallAfter) > at_risk (the latest linked job
// failed or hit its budget) > ok. reason is a short human sentence ("" for ok).
func ComputeHealth(in HealthInput, now int64, stallAfter time.Duration) (health, reason string) {
	if in.BlockedPlan != "" {
		return HealthBlocked, "plan " + in.BlockedPlan + " 阻塞"
	}
	if b := strings.TrimSpace(in.Blocker); b != "" {
		return HealthBlocked, "阻塞：" + clipRunes(strings.Join(strings.Fields(b), " "), 60)
	}
	if in.InProgress && !in.AgentRunning && stallAfter > 0 && in.LastActivity > 0 {
		if quiet := time.Duration(now-in.LastActivity) * time.Second; quiet >= stallAfter {
			return HealthStalled, quietText(quiet) + "没有活动"
		}
	}
	if j := in.LatestJob; j != nil {
		switch {
		case j.FailureClass == failureClassBudget:
			return HealthAtRisk, "最近的 job " + shortID(j.ID) + " 预算熔断"
		case j.Status == "failed" || j.Status == "timeout":
			return HealthAtRisk, "最近的 job " + shortID(j.ID) + " 失败"
		}
	}
	return HealthOK, ""
}

func quietText(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%d 小时", int(d/time.Hour))
	}
	return fmt.Sprintf("%d 分钟", int(d/time.Minute))
}

// jobActive reports whether a job status means an agent is (or is about to be) at work.
func jobActive(st string) bool {
	switch st {
	case "queued", "running", "pending_interaction", "recovering", "waiting_dir":
		return true
	}
	return false
}

// JobActivityAt is a job's newest timestamp (updated / ended / started).
func JobActivityAt(j jobstore.JobRecord) int64 {
	return max(j.UpdatedAt, j.EndedAt, j.StartedAt)
}

// linkedJobsLimit caps the linked jobs a view carries (newest submitted first): the
// health and the lanes only look at the latest job, the live ones and the newest
// activity, and a session that watched hundreds of jobs must not make every Works list
// read them all. linkedPlanJobs is how many of each linked plan's newest jobs count.
const (
	linkedJobsLimit = 50
	linkedPlanJobs  = 10
)

// linkedJobs returns the item's linked jobs, newest submitted first: what its current
// sessions watch, explicit job links, and the most recent jobs of its linked plans —
// one batched query (jobstore.WorkItemLinkedJobs), not a lookup per session / job.
func (s *Service) linkedJobs(id string) []jobstore.JobRecord {
	out, err := s.store.WorkItemLinkedJobs(id, linkedJobsLimit, linkedPlanJobs)
	if err != nil {
		return nil
	}
	return out
}

// fillHealth adds the WORK-06 / N3 read fields to a view: the latest milestones, the
// linked jobs and plans, and the health. Errors degrade to "no data", never fail the read.
// A finished item is always ok and nothing reads its plans / jobs (closed items are the
// bulk of an all-items list).
func (s *Service) fillHealth(v *ItemView, now int64) {
	v.Milestones = []jobstore.WorkJournalEntry{}
	if ms, err := s.store.ListWorkJournalLevel(v.ID, milestonesInView, 0, jobstore.WorkLevelMilestone); err == nil {
		v.Milestones = ms
	}
	if jobstore.WorkStatusFinal(v.Status) {
		v.Health = HealthOK
		return
	}
	blockedPlan := ""
	hasPlan := false
	for _, l := range v.Links {
		if l.Kind != jobstore.WorkLinkPlan {
			continue
		}
		hasPlan = true
		if p, ok, err := s.store.GetPlan(l.Ref); err == nil && ok {
			v.Plans = append(v.Plans, p)
			if p.Status == jobstore.PlanBlocked && blockedPlan == "" {
				blockedPlan = l.Ref
				if t := strings.TrimSpace(p.Title); t != "" {
					blockedPlan = "「" + clipRunes(t, 40) + "」"
				}
			}
		}
	}
	if hasPlan || len(v.SessionIDs) > 0 || hasJobLink(v.Links) {
		v.LinkedJobs = s.linkedJobs(v.ID)
	}
	// Stall looks at the journal and the linked jobs only (design §3.2): a terminal
	// session that merely heartbeats (last_seen_at) is not progress. The item's own
	// activity column is touched only by writes that also journal.
	in := HealthInput{
		InProgress:   v.Status == jobstore.WorkActive,
		Blocker:      v.BlockerText,
		BlockedPlan:  blockedPlan,
		LastActivity: v.WorkItem.LastActivityAt,
	}
	if in.InProgress {
		if at, err := s.store.LatestWorkJournalAt(v.ID); err == nil && at > in.LastActivity {
			in.LastActivity = at
		}
	}
	for _, b := range v.Sessions {
		if b.Role == jobstore.WorkSessionCurrent && (b.State == jobstore.SessionRunning || b.State == jobstore.SessionHandedOff ||
			(b.Kind == KindJob && jobActive(b.State))) {
			in.AgentRunning = true
		}
	}
	for i := range v.LinkedJobs {
		j := v.LinkedJobs[i]
		if at := JobActivityAt(j); at > in.LastActivity {
			in.LastActivity = at
		}
		if jobActive(j.Status) {
			in.AgentRunning = true
		}
	}
	if len(v.LinkedJobs) > 0 {
		in.LatestJob = &v.LinkedJobs[0]
	}
	v.Health, v.HealthReason = ComputeHealth(in, now, s.cfg().StallAfterDuration())
}

func hasJobLink(links []jobstore.WorkLink) bool {
	for _, l := range links {
		if l.Kind == jobstore.WorkLinkJob {
			return true
		}
	}
	return false
}

// StallAfter is the effective work.stall_after.
func (s *Service) StallAfter() time.Duration { return s.cfg().StallAfterDuration() }

// ---------------------------------------------------------------- job outcomes

// JobOutcome is what the work journal needs to know about a finished linked job (the
// entry layer adapts job.JobResult — work must not import job).
type JobOutcome struct {
	ID           string
	Agent        string
	Title        string
	Status       string
	PlanID       string
	FailureClass string
	Error        string
	Commits      int
	// Reviewed says the status is a person's verdict on a needs_review job.
	Reviewed bool
}

// OutcomeLine renders a job outcome as one journal line and its level (WORK-06 §3.3: a
// success with commits, a failure and a budget stop are milestones; the rest — a
// success without commits, a cancel — are details; a review verdict is a milestone).
func OutcomeLine(o JobOutcome) (text, level string) {
	who := "job " + shortID(o.ID)
	if a := strings.TrimSpace(o.Agent); a != "" {
		who += "（" + a + "）"
	}
	if t := strings.TrimSpace(o.Title); t != "" {
		who += "「" + clipRunes(t, 40) + "」"
	}
	level = jobstore.WorkLevelDetail
	switch {
	case o.FailureClass == failureClassBudget:
		text, level = who+" 预算熔断", jobstore.WorkLevelMilestone
	case o.Status == "failed" || o.Status == "timeout":
		text, level = who+" 失败", jobstore.WorkLevelMilestone
		if e := strings.TrimSpace(o.Error); e != "" {
			text += "：" + clipRunes(strings.Join(strings.Fields(e), " "), 120)
		}
	case o.Status == "rejected":
		text, level = who+" 验收未通过", jobstore.WorkLevelMilestone
	case o.Status == "done" && o.Reviewed:
		text, level = who+" 验收通过", jobstore.WorkLevelMilestone
	case o.Status == "done" || o.Status == statusNeedsReview:
		text = who + " 完成"
		if o.Status == statusNeedsReview {
			text += "，待验收"
		}
		if o.Commits > 0 {
			text += fmt.Sprintf("，%d 个提交", o.Commits)
			level = jobstore.WorkLevelMilestone
		}
	case o.Status == "cancelled":
		text = who + " 已取消"
	default:
		text = who + " 结束（" + o.Status + "）"
	}
	return text, level
}

// NoteJobOutcome journals a finished job on every open work item it belongs to (a job /
// plan link, a session that watches it, or the job as an attached session). Best-effort:
// errors are logged, never returned (it runs from the job's terminal hook).
func (s *Service) NoteJobOutcome(o JobOutcome) {
	if s == nil || s.store == nil || strings.TrimSpace(o.ID) == "" {
		return
	}
	ids, err := s.store.OpenWorkItemsFor(jobstore.WorkRefs{JobID: o.ID, PlanID: o.PlanID})
	if err != nil {
		slog.Warn("work.job_outcome_failed", "event", "work.job_outcome_failed", "job", o.ID, "err", err)
		return
	}
	text, level := OutcomeLine(o)
	for _, id := range ids {
		if _, err := s.store.AppendWorkJournalLevel(id, jobstore.WorkJournalNote, text, "job:"+o.ID, level); err != nil {
			slog.Warn("work.journal_failed", "event", "work.journal_failed", "id", id, "err", err)
		}
	}
}
