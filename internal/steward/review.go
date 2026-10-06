package steward

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// Review triggers.
const (
	TriggerDaily  = "daily"
	TriggerManual = "manual"
	TriggerEvent  = "event"
)

// ReviewOpts selects what to review.
type ReviewOpts struct {
	// Trigger is daily | manual | event ("" = manual).
	Trigger string
	// Force runs the review even when no work item changed since the last one.
	Force bool
}

// ReviewResult says what a review run did.
type ReviewResult struct {
	ReviewID int64 `json:"review_id"`
	// Skipped is true when nothing needed reviewing: no session was started.
	Skipped bool     `json:"skipped"`
	Reason  string   `json:"reason,omitempty"`
	JobID   string   `json:"job_id,omitempty"`
	Items   []string `json:"items,omitempty"`
	Events  int      `json:"events"`
}

// reviewItems picks the work items one review handles: the unfinished ones that changed
// since the cutoff plus the due ones (daily / manual), or the ones the pending events point
// at (event); most urgent first, capped at max. The second result is how many more there
// were.
func (s *Service) reviewItems(trigger string, cutoff int64, events []jobstore.StewardEvent, max int) ([]work.ItemView, int, error) {
	items, err := s.work.List(work.WorkListOptsOpen())
	if err != nil {
		return nil, 0, err
	}
	evItems := map[string]bool{}
	for _, e := range events {
		if id := eventItemID(e); id != "" {
			evItems[id] = true
		}
	}
	var picked []work.ItemView
	for _, it := range items {
		switch trigger {
		case TriggerEvent:
			if evItems[it.ID] {
				picked = append(picked, it)
			}
		default:
			// The stored activity time: the session heartbeats the card shows are not "the
			// item changed" (those are what the session events cover).
			if it.WorkItem.LastActivityAt > cutoff || it.Due || evItems[it.ID] {
				picked = append(picked, it)
			}
		}
	}
	sortForPrime(picked)
	more := 0
	if max > 0 && len(picked) > max {
		more = len(picked) - max
		picked = picked[:max]
	}
	return picked, more, nil
}

// RunReview runs one review: it records the run, tells the (started on demand) steward
// session which work items to look at, and finishes the record in the background when the
// turn ends. Nothing happens — and no session starts — when nothing changed.
func (s *Service) RunReview(ctx context.Context, o ReviewOpts) (ReviewResult, error) {
	if o.Trigger == "" {
		o.Trigger = TriggerManual
	}
	c, err := s.ready()
	if err != nil {
		return ReviewResult{}, err
	}
	s.reviewMu.Lock()
	if s.reviewOn {
		s.reviewMu.Unlock()
		return ReviewResult{}, ErrBusy
	}
	s.reviewOn = true
	s.reviewMu.Unlock()
	release := func() {
		s.reviewMu.Lock()
		s.reviewOn = false
		s.reviewMu.Unlock()
	}

	now := s.nowFn()
	day := now.Format("2006-01-02")
	events, _ := s.store.PendingStewardEvents(200)
	items, more, err := s.reviewItems(o.Trigger, s.kvInt(kvReviewCutoff), events, c.MaxReviewItems())
	if err != nil {
		release()
		return ReviewResult{}, err
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if len(items) == 0 && !o.Force {
		release()
		id, berr := s.store.BeginStewardReview(day, o.Trigger, nil)
		if berr == nil {
			_ = s.store.FinishStewardReview(id, jobstore.StewardReviewSkipped, "", "", "没有变化的工作项")
		}
		return ReviewResult{ReviewID: id, Skipped: true, Reason: "没有变化的工作项"}, nil
	}
	notes, _ := s.NotesStatus()
	prompt := reviewPrompt(o.Trigger, day, items, more, events, notes.NeedSlim, now)

	rid, err := s.store.BeginStewardReview(day, o.Trigger, ids)
	if err != nil {
		release()
		return ReviewResult{}, err
	}
	turnBefore := 0
	s.askMu.Lock()
	s.mu.Lock()
	jobID, started, err := s.ensureSession(prompt)
	if err == nil && !started {
		if info, ok := s.host.Job(jobID); ok {
			turnBefore = info.TurnNo
		}
	}
	s.mu.Unlock()
	if err == nil && !started {
		err = s.say(ctx, jobID, prompt)
	}
	s.askMu.Unlock()
	if err != nil {
		_ = s.store.FinishStewardReview(rid, jobstore.StewardReviewFailed, "", jobID, err.Error())
		release()
		return ReviewResult{ReviewID: rid}, err
	}
	var evIDs []int64
	for _, e := range events {
		evIDs = append(evIDs, e.ID)
	}
	_ = s.store.MarkStewardEventsHandled(evIDs, now.Unix())

	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		defer release()
		ok := s.waitTurnDone(jobID, turnBefore)
		end := s.nowFn()
		if ok {
			if o.Trigger != TriggerEvent {
				s.setKV(kvReviewCutoff, strconv.FormatInt(end.Unix(), 10))
			}
			_ = s.store.FinishStewardReview(rid, jobstore.StewardReviewDone, "", jobID, "")
		} else {
			_ = s.store.FinishStewardReview(rid, jobstore.StewardReviewFailed, "", jobID, "管家会话没有按时完成这一轮")
		}
		slog.Info("steward.review_done", "event", "steward.review_done", "review", rid, "trigger", o.Trigger, "ok", ok, "items", len(ids))
	}()
	return ReviewResult{ReviewID: rid, JobID: jobID, Items: ids, Events: len(events)}, nil
}

// waitTurnDone waits until the session finished the turn after `turnBefore` (it is between
// turns again), or the session is gone / the wait times out.
func (s *Service) waitTurnDone(jobID string, turnBefore int) bool {
	deadline := time.Now().Add(s.reviewTimeout)
	last := turnBefore
	for time.Now().Before(deadline) {
		info, ok := s.host.Job(jobID)
		if !ok {
			return last > turnBefore
		}
		last = info.TurnNo
		if info.AwaitingInput && info.TurnNo > turnBefore {
			return true
		}
		if !info.Live {
			return info.TurnNo > turnBefore
		}
		time.Sleep(s.pollEvery)
	}
	return false
}

// SetReviewSummary stores the steward's point of view on the running (or today's finished)
// review: what the daily digest appends as 管家点评.
func (s *Service) SetReviewSummary(text string) (jobstore.StewardReview, error) {
	return s.store.SetStewardReviewSummary(s.nowFn().Format("2006-01-02"), text)
}

func reviewPrompt(trigger, day string, items []work.ItemView, more int, events []jobstore.StewardEvent, slim bool, now time.Time) string {
	var b strings.Builder
	label := map[string]string{TriggerDaily: "每日巡检", TriggerManual: "手动巡检", TriggerEvent: "事件整理"}[trigger]
	fmt.Fprintf(&b, "## %s（%s）\n\n", label, day)
	if len(items) == 0 {
		b.WriteString("没有标记为有变化的工作项（强制巡检）：只做笔记和到期检查。\n")
	} else {
		fmt.Fprintf(&b, "只处理下面这 %d 个工作项（自上次巡检后有变化、已到期或有事件）", len(items))
		if more > 0 {
			fmt.Fprintf(&b, "；另有 %d 个同样有变化但超出本次上限，留给下次", more)
		}
		b.WriteString("：\n")
		for _, it := range items {
			b.WriteString(itemLine(it, now) + "\n")
		}
	}
	if len(events) > 0 {
		b.WriteString("\n待处理的事件：\n")
		for i, e := range events {
			if i >= 30 {
				fmt.Fprintf(&b, "- …另有 %d 条\n", len(events)-i)
				break
			}
			fmt.Fprintf(&b, "- [%s] %s\n", e.Kind, runesCap(e.Detail, 120))
		}
	}
	b.WriteString("\n你要做的（只整理，不替用户决定）：\n")
	b.WriteString("1. 对每个工作项用 gofer_work_get 看目标 / 阻塞 / 下一步 / 日志和会话；信息缺失或过期的，用 gofer_work_summarize 触发整理，或用 gofer_work_request_report 请运行中的会话汇报（异步：之后用 gofer_work_requests 看结果，不要等）；需要时用 gofer_session_tail 读会话记录尾部。\n")
	b.WriteString("2. 检查到期 / 即将到期的提醒和搁置：必要时用 gofer_work_remind 设提醒，用 gofer_work_note 记一笔。\n")
	b.WriteString("   关联了 issue 的项可用 gofer_issue_get 核对 issue 当前状态（只读）；等的资源已到、会话在线时，用 gofer_session_ask 带话让它继续（离线会报错，别重试）。\n")
	b.WriteString("3. 发现同一件事被拆成多个工作项，用 gofer_work_merge_suggest 记合并建议（只记建议，由用户确认）。\n")
	if slim {
		b.WriteString("4. **管家笔记已超过 8KB，请先精简**：gofer_steward_notes get 读全文，保留长期有效的偏好和约定，重写成更短的版本，用 set（带 version）写回（旧版本会保留）。\n")
	} else {
		b.WriteString("4. 有新的长期有效的偏好或约定，用 gofer_steward_notes 更新笔记（set 要带 version）。\n")
	}
	b.WriteString("5. 最后调用 gofer_steward_notes，action 填 review_summary，text 写一段 300 字以内的点评（重点、风险、这些事的共同点；不要复述数字），它会附在今天的每日摘要里。\n")
	b.WriteString("\n不要标完成 / 放弃，不要合并，不要提交 job。做完后只回复一行总结。")
	return b.String()
}
