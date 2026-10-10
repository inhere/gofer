package work

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Messenger delivers a request text into a running terminal session (httpapi adapts
// sessionrelay.SendMessage: a session waiting for a reply gets it through the relay,
// anything else through the one-shot messenger). The service never knows how.
type Messenger interface {
	// SendRequest returns the channel that carried the text ("turn" / "messenger" / ...).
	SendRequest(ctx context.Context, sid, text, operator string) (channel string, err error)
}

// SetMessenger injects the delivery seam (nil = requests cannot be delivered and are
// turned into a tidy-up straight away).
func (s *Service) SetMessenger(m Messenger) { s.messenger = m }

// ErrNoSession is returned when a request has no current session to ask.
var ErrNoSession = errors.New("work item has no current session to ask")

// RequestOpts selects what to ask and whom.
type RequestOpts struct {
	// Kind is report (default) or handoff.
	Kind string
	// SessionID restricts the ask to one of the item's current sessions.
	SessionID string
	// By is the speaker label of whoever asked ("" = system).
	By string
	// Allow vets each target session (the owner check); nil allows all.
	Allow func(sid string) bool
}

// RequestOutcome is what happened to the ask for one session.
type RequestOutcome struct {
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id,omitempty"`
	// Kind is the kind of request actually recorded: a session that is not running is
	// turned into a "summarize" one.
	Kind    string `json:"kind,omitempty"`
	State   string `json:"state,omitempty"`
	Sent    bool   `json:"sent"`
	Channel string `json:"channel,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// sessionRunning reports whether a message can still reach the session's process.
func sessionRunning(state string) bool {
	switch state {
	case jobstore.SessionRunning, jobstore.SessionIdle, jobstore.SessionWaitingReply, jobstore.SessionNeedsAttention:
		return true
	}
	return false
}

func requestText(kind, rid, itemID, title, status string) string {
	if kind == jobstore.WorkRequestHandoff {
		return fmt.Sprintf("[gofer 工作项交接请求 %s] 工作项 %s「%s」被标为%s，请写一段交接：做到哪了、卡在哪、回来后第一步。运行 "+
			"`gofer work report %s --request %s --summary \"<做到哪了>\" --blocker \"<卡在哪>\" --next \"<回来第一步>\"`"+
			"（不需要的字段可省略）。", rid, itemID, title, statusLabel(status), itemID, rid)
	}
	return fmt.Sprintf("[gofer 工作项汇报请求 %s] 请汇报工作项 %s「%s」当前进展：运行 "+
		"`gofer work report %s --request %s --goal \"<目标>\" --status <active|needs_me|waiting_resource|needs_onsite|review|parked> "+
		"--blocker \"<卡在哪>\" --next \"<下一步>\" --summary \"<做到哪了>\"`（不需要的字段可省略；若已不再受阻请用 --status active）。",
		rid, itemID, title, itemID, rid)
}

func shortID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}

func (s *Service) journalSystem(id, text string) {
	if _, err := s.store.AppendWorkJournal(id, jobstore.WorkJournalNote, text, "system"); err != nil {
		slog.Warn("work.journal_failed", "event", "work.journal_failed", "id", id, "err", err)
	}
}

// RequestSessions asks the item's current sessions to report (or write a hand-over)
// through the request ledger. A session that is running gets the text with the request
// id (it answers with `gofer work report --request <id>`); one that is not running — or
// that the text cannot be delivered to — is turned into a tidy-up (kind summarize)
// instead, so the item still gets fresher content.
func (s *Service) RequestSessions(ctx context.Context, itemID string, o RequestOpts) ([]RequestOutcome, error) {
	kind := o.Kind
	if kind == "" {
		kind = jobstore.WorkRequestReport
	}
	if kind != jobstore.WorkRequestReport && kind != jobstore.WorkRequestHandoff {
		return nil, fmt.Errorf("%w: kind must be report or handoff", jobstore.ErrWorkInvalid)
	}
	w, ok, err := s.store.GetWorkItem(itemID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, jobstore.ErrWorkItemNotFound
	}
	rows, err := s.store.ListWorkItemSessions(w.ID)
	if err != nil {
		return nil, err
	}
	by := strings.TrimSpace(o.By)
	if by == "" {
		by = "system"
	}
	var out []RequestOutcome
	for _, r := range rows {
		if r.Role != jobstore.WorkSessionCurrent || (o.SessionID != "" && o.SessionID != r.SessionID) {
			continue
		}
		oc := RequestOutcome{SessionID: r.SessionID}
		a, found, gerr := s.store.GetAgentSession(r.SessionID)
		switch {
		case gerr != nil:
			oc.Reason = gerr.Error()
		case !found && s.isJobSession(r.SessionID):
			oc.Reason = "ACP / 终端 job 会话不能被请求汇报，请在它的 job 页面里对话"
		case !found:
			oc.Reason = "会话记录已不存在"
		case o.Allow != nil && !o.Allow(r.SessionID):
			oc.Reason = "无权向该会话传话"
		case !sessionRunning(a.State) || s.messenger == nil:
			why := "会话未在运行（" + a.State + "），改为整理"
			if s.messenger == nil {
				why = "未配置会话传话通道，改为整理"
			}
			oc = s.fallbackToSummarize(w, r.SessionID, by, "", why)
		default:
			oc = s.deliverRequest(ctx, w, a, kind, by)
		}
		out = append(out, oc)
	}
	if len(out) == 0 {
		return nil, ErrNoSession
	}
	return out, nil
}

// deliverRequest records and sends one request to a running session.
func (s *Service) deliverRequest(ctx context.Context, w jobstore.WorkItem, a jobstore.AgentSession, kind, by string) RequestOutcome {
	oc := RequestOutcome{SessionID: a.SessionID, Kind: kind}
	timeout := s.cfg().RequestTimeout()
	req, err := s.store.CreateWorkRequest(jobstore.WorkRequestInput{
		WorkItemID: w.ID, SessionID: a.SessionID, Kind: kind, By: by, Deadline: s.nowFn().Add(timeout).Unix(),
	})
	if err != nil {
		oc.Reason = err.Error()
		return oc
	}
	oc.RequestID, oc.State = req.ID, req.State
	text := requestText(kind, req.ID, w.ID, w.Title, w.Status)
	channel, serr := s.messenger.SendRequest(ctx, a.SessionID, text, by)
	if serr != nil {
		_, _, _ = s.store.MarkWorkRequest(req.ID, jobstore.WorkRequestFailed, "", serr.Error())
		s.journalSystem(w.ID, fmt.Sprintf("请求 %s 未能送达会话 %s：%s，改为整理", req.ID, shortID(a.SessionID), serr.Error()))
		fb := s.fallbackToSummarize(w, a.SessionID, by, req.ID, "送达失败："+serr.Error())
		fb.Reason = "送达失败（" + serr.Error() + "），已改为整理"
		return fb
	}
	if _, _, err := s.store.MarkWorkRequest(req.ID, jobstore.WorkRequestSent, channel, "", jobstore.WorkRequestPending); err != nil {
		slog.Warn("work.request_mark_failed", "event", "work.request_mark_failed", "id", req.ID, "err", err)
	}
	what := "汇报"
	if kind == jobstore.WorkRequestHandoff {
		what = "写交接"
	}
	if _, err := s.store.AppendWorkJournalLevel(w.ID, jobstore.WorkJournalNote,
		fmt.Sprintf("已请会话 %s %s（请求 %s，%d 分钟内回复）", shortID(a.SessionID), what, req.ID, int(timeout/time.Minute)), by, jobstore.WorkLevelDetail); err != nil {
		slog.Warn("work.journal_failed", "event", "work.journal_failed", "id", w.ID, "err", err)
	}
	oc.State, oc.Sent, oc.Channel = jobstore.WorkRequestSent, true, channel
	return oc
}

// fallbackToSummarize records a summarize request (child of parent when set) and starts
// the tidy-up in the background.
func (s *Service) fallbackToSummarize(w jobstore.WorkItem, sid, by, parent, reason string) RequestOutcome {
	oc := RequestOutcome{SessionID: sid, Kind: jobstore.WorkRequestSummarize, Reason: reason}
	req, err := s.store.CreateWorkRequest(jobstore.WorkRequestInput{
		WorkItemID: w.ID, SessionID: sid, Kind: jobstore.WorkRequestSummarize, By: by, ParentID: parent,
		Deadline: s.nowFn().Add(s.cfg().RequestTimeout()).Unix(),
	})
	if err != nil {
		oc.Reason = err.Error()
		return oc
	}
	oc.RequestID, oc.State = req.ID, req.State
	s.StartSummarize(w.ID, SummarizeOpts{Cause: CauseRequest, RequestID: req.ID, SessionID: sid, By: by})
	return oc
}

// maybeAutoHandoff asks the running sessions for a hand-over when a person has just
// parked the item or marked it needs_onsite (design §14.3). Sessions that are not
// running are tidied up instead. It never blocks the caller.
func (s *Service) maybeAutoHandoff(id, prevStatus, newStatus, by string) {
	if prevStatus == newStatus || (newStatus != jobstore.WorkParked && newStatus != jobstore.WorkNeedsOnsite) {
		return
	}
	if !s.cfg().AutoHandoffOn() {
		return
	}
	s.spawn(func() {
		ctx, cancel := context.WithTimeout(s.BackgroundContext(), 2*time.Minute)
		defer cancel()
		if _, err := s.RequestSessions(ctx, id, RequestOpts{Kind: jobstore.WorkRequestHandoff, By: by}); err != nil && !errors.Is(err, ErrNoSession) {
			slog.Warn("work.auto_handoff_failed", "event", "work.auto_handoff_failed", "id", id, "err", err)
		}
	})
}

// answerRequest settles the request a report names. It must belong to the item; a late
// answer (the request already expired) still counts — the session did answer.
func (s *Service) checkRequest(itemID, rid string) error {
	if rid == "" {
		return nil
	}
	r, ok, err := s.store.GetWorkRequest(rid)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: unknown request %q", jobstore.ErrWorkInvalid, rid)
	}
	if r.WorkItemID != itemID {
		return fmt.Errorf("%w: request %s belongs to another work item", jobstore.ErrWorkInvalid, rid)
	}
	return nil
}

func (s *Service) answerRequest(itemID, rid string) {
	if rid == "" {
		return
	}
	r, changed, err := s.store.MarkWorkRequest(rid, jobstore.WorkRequestAnswered, "", "",
		jobstore.WorkRequestPending, jobstore.WorkRequestSent, jobstore.WorkRequestExpired)
	if err != nil || !changed {
		return
	}
	s.journalSystem(itemID, fmt.Sprintf("会话已回复请求 %s（%s）", r.ID, r.Kind))
}

// advanceRequests moves overdue requests along: an unanswered report / hand-over
// expires and is replaced by a tidy-up; a tidy-up that never finished (the server
// restarted under it, or it hung) fails. It reads the ledger, so a restart simply
// carries on from the table.
func (s *Service) advanceRequests(now time.Time) {
	overdue, err := s.store.ListOverdueWorkRequests(now.Unix())
	if err != nil {
		slog.Warn("work.request_scan_failed", "event", "work.request_scan_failed", "err", err)
		return
	}
	for _, r := range overdue {
		if r.Kind == jobstore.WorkRequestSummarize {
			if s.summarizing(r.ID) {
				continue
			}
			if _, ch, _ := s.store.MarkWorkRequest(r.ID, jobstore.WorkRequestFailed, "", "整理超时或被中断",
				jobstore.WorkRequestPending); ch {
				s.journalSystem(r.WorkItemID, fmt.Sprintf("整理请求 %s 超时未完成", r.ID))
			}
			continue
		}
		if _, ch, _ := s.store.MarkWorkRequest(r.ID, jobstore.WorkRequestExpired, "", "会话未回应",
			jobstore.WorkRequestPending, jobstore.WorkRequestSent); !ch {
			continue
		}
		w, ok, err := s.store.GetWorkItem(r.WorkItemID)
		if err != nil || !ok {
			continue
		}
		s.journalSystem(r.WorkItemID, fmt.Sprintf("会话 %s 未回应请求 %s，已改为整理", shortID(r.SessionID), r.ID))
		s.fallbackToSummarize(w, r.SessionID, r.By, r.ID, "请求超时")
	}
}

// ListRequests returns an item's ledger rows, newest first ("" = all items).
func (s *Service) ListRequests(itemID string, activeOnly bool, limit int) ([]jobstore.WorkRequest, error) {
	if itemID != "" {
		if _, ok, err := s.store.GetWorkItem(itemID); err != nil {
			return nil, err
		} else if !ok {
			return nil, jobstore.ErrWorkItemNotFound
		}
	}
	return s.store.ListWorkRequests(itemID, activeOnly, limit)
}

// isJobSession reports whether id is a job id (an ACP persistent / pty session attached
// by job id) rather than a terminal relay session.
func (s *Service) isJobSession(id string) bool {
	_, ok, err := s.store.GetJob(id)
	return err == nil && ok
}
