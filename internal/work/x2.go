package work

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/notify"
)

// X2 additions: plan decisions drive "needs me", completion write-back suggestions and
// the work.needs_me notification.

// planDecisionWait reports an open (not yet answered) plan decision the item is waiting
// on: a plan linked to the item, or the plan a linked job belongs to. "" = none. Relay
// turns (a terminal session's own question) are not plan decisions — the session state
// already maps those.
func (s *Service) planDecisionWait(id string, jobs map[string]JobState) string {
	seen := map[string]bool{}
	var plans []string
	add := func(p string) {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			plans = append(plans, p)
		}
	}
	if links, err := s.store.ListWorkLinks(id); err == nil {
		for _, l := range links {
			if l.Kind == jobstore.WorkLinkPlan {
				add(l.Ref)
			}
		}
	}
	for _, j := range jobs {
		add(j.PlanID)
	}
	for _, p := range plans {
		ds, err := s.store.ListDecisions(jobstore.DecisionOpen, p)
		if err != nil {
			continue
		}
		for _, d := range ds {
			if d.Kind == jobstore.DecisionKindRelay {
				continue
			}
			return "关联 plan " + p + " 有待应答的 decision：" + d.Title
		}
	}
	return ""
}

// ---------------------------------------------------------------- completion write-back

// completionBy is the suggestion author of the completion write-back.
const completionBy = "system(完成回写)"

// checkLinkedCompletion looks at the todos / issues linked to an open item. When every
// one of them is finished (todo done, issue closed) — and at least one exists — it does
// NOT change the item's status (a person's call) but leaves a pending `status_hint`
// suggestion plus a journal line: 「待验收」 when only todos are involved, 「完成」 once a
// linked issue was closed too. A suggestion the person dismissed is not made again; one
// that is already pending with the same value is left alone (no journal spam).
func (s *Service) checkLinkedCompletion(id string) {
	w, ok, err := s.store.GetWorkItem(id)
	if err != nil || !ok || jobstore.WorkStatusFinal(w.Status) || w.MergedInto != "" {
		return
	}
	links, err := s.store.ListWorkLinks(id)
	if err != nil {
		return
	}
	var done []string
	hasIssue := false
	for _, l := range links {
		switch l.Kind {
		case jobstore.WorkLinkTodo:
			t, found, err := s.store.GetTodo(l.Ref)
			if err != nil || !found {
				continue
			}
			if t.Status != jobstore.TodoDone {
				return // something linked is still open
			}
			done = append(done, "todo "+l.Ref+" 已完成")
		case jobstore.WorkLinkIssue:
			closed, known := s.issueClosed(l.Ref)
			if !known {
				continue
			}
			if !closed {
				return
			}
			hasIssue = true
			done = append(done, "issue "+l.Ref+" 已关闭")
		}
	}
	if len(done) == 0 {
		return
	}
	want, label := jobstore.WorkReview, "待验收"
	if hasIssue {
		want, label = jobstore.WorkDone, "完成"
	}
	if w.Status == want {
		return
	}
	if cur, found, _ := s.store.GetWorkSuggestion(id, jobstore.SuggestStatusHint); found && cur.Value == want {
		return
	}
	stored, err := s.store.UpsertWorkSuggestion(jobstore.WorkSuggestion{
		WorkItemID: id, Field: jobstore.SuggestStatusHint, Value: want, Confidence: 1, By: completionBy,
	})
	if err != nil || !stored {
		return
	}
	if _, err := s.store.AppendWorkJournal(id, jobstore.WorkJournalNote,
		strings.Join(done, "；")+"，建议把状态改为「"+label+"」（在工作项详情的建议区采纳或忽略，状态未自动改变）", "system"); err != nil {
		slog.Warn("work.journal_failed", "event", "work.journal_failed", "id", id, "err", err)
	}
}

// issueClosed reads one linked issue from the server's tracker mirror. known=false when
// the mirror does not hold it (or holds the same id in several repos that disagree).
func (s *Service) issueClosed(issueID string) (closed, known bool) {
	rows, err := s.store.FindTrackerIssues(issueID)
	if err != nil || len(rows) == 0 {
		return false, false
	}
	allClosed, any := true, false
	for _, r := range rows {
		var is struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(r.Body, &is) != nil {
			continue
		}
		any = true
		if is.Status != "closed" {
			allClosed = false
		}
	}
	return allClosed && any, any
}

// ---------------------------------------------------------------- work.needs_me

// notifyEnteredNeedsMe announces an item that just moved INTO needs_me by a write (a
// person, a report or the steward); the auto-sync path calls notifyNeedsMe directly.
func (s *Service) notifyEnteredNeedsMe(prevStatus string, w jobstore.WorkItem, reason string) {
	if prevStatus != jobstore.WorkNeedsMe && w.Status == jobstore.WorkNeedsMe {
		s.notifyNeedsMe(w, reason)
	}
}

func needsMeKVKey(id string) string { return "needs_me_notified:" + id }

// notifyNeedsMe sends one work.needs_me notification: only when the switch is on
// (default off), a notifier is wired, and the same item has not been announced within
// the throttle window (default 30 minutes). Any error is logged, never returned.
func (s *Service) notifyNeedsMe(w jobstore.WorkItem, reason string) {
	if s == nil || s.notifier == nil || !s.cfg().NeedsMeNotifyOn() {
		return
	}
	now := s.nowFn()
	key := needsMeKVKey(w.ID)
	if v, err := s.store.GetWorkKV(key); err == nil && v != "" {
		if last, perr := strconv.ParseInt(v, 10, 64); perr == nil && now.Unix()-last < int64(s.cfg().NeedsMeThrottle()/time.Second) {
			return
		}
	}
	if err := s.store.SetWorkKV(key, strconv.FormatInt(now.Unix(), 10)); err != nil {
		slog.Warn("work.needs_me_mark_failed", "event", "work.needs_me_mark_failed", "id", w.ID, "err", err)
	}
	var b strings.Builder
	b.WriteString(reason)
	if w.Goal != "" {
		b.WriteString("\n目标：" + w.Goal)
	}
	if w.BlockerText != "" {
		b.WriteString("\n阻塞：" + w.BlockerText)
	}
	if w.NextStep != "" {
		b.WriteString("\n下一步：" + w.NextStep)
	}
	s.notifier.NotifyWork(notify.EventWorkNeedsMe, w.ProjectKey, fmt.Sprintf("等我处理 · %s", w.Title), b.String(),
		s.notifier.WebURL("/work?id="+w.ID), "打开工作项")
}
