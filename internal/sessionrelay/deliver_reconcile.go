package sessionrelay

// deliver_reconcile.go is the ASYNCHRONOUS half of the delivery ladder (gofer-qzv5):
// an internal injection / delivery job may outlive the wait window the HTTP caller can
// afford (injectWaitSec), and — during an update waiting for idle — it used to be
// parked for minutes. What gofer knows about the message then is one thing only: the
// job it was handed to. This file keeps that link and resolves it:
//
//   - the job was still RUNNING when gofer gave up waiting → the message is 「送达中」;
//   - the job ends → the outcome is written back onto the outbox record;
//   - the job had NOT STARTED → the host cancels it (nothing can arrive late) and the
//     message is 「未送达」 with the reason, which is safe to retry.
//
// The relay owns the mapping from a job outcome to a message state, because that is
// where the delivery ladder's semantics live (a deliver_command that exited non-zero
// may still have handed the text over — see deliveryNeverStarted); the host owns the
// jobs (InjectProbe).

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// maxPendingDeliveries bounds the in-memory link table. Only a delivery that outlived
// the wait window is registered, and every entry is dropped when its job ends, so this
// is far above what a running server holds at once; the bound only keeps a pathological
// failure (a job that never reports a terminal state) from growing the table for ever.
const maxPendingDeliveries = 256

// pendingDelivery links an injection job that outlived the wait window to the session
// whose outbox its outcome belongs on.
type pendingDelivery struct {
	JobID     string
	SessionID string
}

// notePendingDelivery registers the link (see pendingDelivery). The oldest entry is
// dropped once maxPendingDeliveries entries are held: an unresolvable delivery costs the
// write-back of one message, never the process.
func (s *Service) notePendingDelivery(sid, jobID string) {
	if strings.TrimSpace(sid) == "" || strings.TrimSpace(jobID) == "" {
		return
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pendingDeliveries = append(s.pendingDeliveries, pendingDelivery{JobID: jobID, SessionID: sid})
	if len(s.pendingDeliveries) > maxPendingDeliveries {
		s.pendingDeliveries = s.pendingDeliveries[len(s.pendingDeliveries)-maxPendingDeliveries:]
	}
}

// takePendingDelivery removes and returns the session of one job's pending delivery.
func (s *Service) takePendingDelivery(jobID string) (string, bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for i, p := range s.pendingDeliveries {
		if p.JobID != jobID {
			continue
		}
		s.pendingDeliveries = append(s.pendingDeliveries[:i], s.pendingDeliveries[i+1:]...)
		return p.SessionID, true
	}
	return "", false
}

// pendingDelivery is the answer of a delivery whose job had begun but had not finished
// when the wait window elapsed: the text is on its way, so the caller records 「送达中」
// and learns how it ended from ReconcileDeliveries. The link is registered here, which
// is what lets the host's terminal hook name the session to write back to.
func (s *Service) pendingDelivery(sid, jobID, path string) DeliverResult {
	s.notePendingDelivery(sid, jobID)
	return DeliverResult{Path: path, JobID: jobID, Delivering: true}
}

// notStarted wraps "the job had NOT started when gofer gave up waiting" as an
// undeliverable error. The text is not in the session and cannot arrive later — the host
// cancelled that job — so this reason is one a retry is safe against.
func notStarted(res InjectResult) error {
	detail := strings.TrimSpace(res.StatusDetail)
	if detail == "" {
		detail = strings.TrimSpace(res.Status)
	}
	if detail == "" {
		detail = "未开始"
	}
	return undeliverable(NotDeliveredPrefix+res.Status, fmt.Errorf(
		"job %s 未启动（%s），已取消：未送达", res.JobID, detail))
}

// ReconcileDeliveryJob resolves ONE finished injection job (the assembly's terminal
// hook, which knows the job id but not the session). It reports whether a pending
// delivery was waiting on that job; an ordinary job — or a delivery already resolved —
// is simply not its business.
func (s *Service) ReconcileDeliveryJob(jobID string) (bool, error) {
	sid, ok := s.takePendingDelivery(jobID)
	if !ok {
		return false, nil
	}
	if err := s.ReconcileDeliveries(sid); err != nil {
		// Put the link back: the outcome is not written back yet, and a later pass (the
		// outbox read path runs the same reconcile) must be able to find the message.
		s.notePendingDelivery(sid, jobID)
		return true, err
	}
	return true, nil
}

// ReconcileDeliveries writes back every outbox record of this session that is still
// marked 「送达中」: the message asked a job to carry it, and the job's own answer now
// decides whether it arrived. Records whose job is still running are left alone.
//
// It is called both when the job ends and on the outbox READ path, which is what makes
// the state self-healing: a write-back lost with a server restart — the delivery job dies
// with the old process — is resolved by the next read instead of leaving a message
// 「送达中」 for ever.
func (s *Service) ReconcileDeliveries(sid string) error {
	if s.injectProbe == nil || strings.TrimSpace(sid) == "" {
		return nil
	}
	list, err := s.store.ReadSessionOutbox(sid)
	if err != nil {
		return err
	}
	var errs []error // at most one per message
	for _, m := range list {
		if m.Status != jobstore.SessionMessageDelivering || strings.TrimSpace(m.JobID) == "" {
			continue
		}
		state, perr := s.injectProbe.InjectJob(m.JobID)
		if perr != nil {
			slog.Warn("sessionrelay: probe delivery job", "session_id", sid, "job_id", m.JobID, "err", perr)
			continue
		}
		if state.Known && !state.Done {
			continue // still carrying the message; the terminal event comes later
		}
		next := s.deliveryOutcome(m, state)
		if next.Status == m.Status {
			continue
		}
		next.UpdatedAt = s.nowFn().Unix()
		if aerr := s.store.AppendSessionOutbox(next); aerr != nil {
			errs = append(errs, fmt.Errorf("message %s: %w", m.ID, aerr))
			continue
		}
		slog.Info("sessionrelay: delivery written back", "event", "sessionrelay.delivery_settled",
			"session_id", sid, "message_id", m.ID, "job_id", m.JobID,
			"status", next.Status, "job_status", state.Status)
	}
	return errors.Join(errs...)
}

// deliveryOutcome is what a 「送达中」 record becomes once its job's fate is known. The
// ladder's rules apply unchanged: exit 0 means the text arrived, and path C's
// offline-match (a code that means "no such live process") is still read as "not
// running" rather than as a failure. Everything else is `failed` — a delivery that was
// ATTEMPTED and whose outcome is unknown, which is deliberately NOT offered for retry.
func (s *Service) deliveryOutcome(m jobstore.SessionMessage, state InjectState) jobstore.SessionMessage {
	next := m
	switch {
	case m.Channel == messengerChannel:
		// The one-shot messenger's success is its receipt, not just exit 0 (gofer-kx53).
		if state.Known && state.Done && state.Status == deliveryDoneStatus && state.ExitCode == 0 &&
			messengerSaidSent(state.Output) {
			next.Status, next.Error = jobstore.SessionMessageDelivered, ""
		} else {
			next.Status, next.Error = jobstore.SessionMessageFailed, deliveryFailureReason(m, state)
		}
	case state.Known && state.Done && state.Status == deliveryDoneStatus && state.ExitCode == 0:
		next.Status, next.Error = jobstore.SessionMessageDelivered, ""
	case state.Known && state.Done && state.ExitCode == deliverExitNotRunning:
		next.Status, next.Error = jobstore.SessionMessageUndelivered, deliveryNotRunningReason(m)
	case state.Known && state.Done && m.Channel == PathCommand && s.deliverPlan != nil &&
		s.offlineMatch(m, state.Output):
		next.Status, next.Error = jobstore.SessionMessageUndelivered, deliveryNotRunningReason(m)
	default:
		next.Status, next.Error = jobstore.SessionMessageFailed, deliveryFailureReason(m, state)
	}
	return next
}

// offlineMatch answers whether a deliver_command's non-zero exit is its own "no such
// live process" signal (agent config offline_match), which the synchronous path reads as
// "not running" instead of a failure — the plan is re-rendered here, because the outbox
// record only keeps the text (and the speaker, for the prefix).
func (s *Service) offlineMatch(m jobstore.SessionMessage, output string) bool {
	a, ok, err := s.store.GetAgentSession(m.SessionID)
	if err != nil || !ok {
		return false
	}
	plan := s.deliverPlan(a.Agent, a.SessionID, injectText(m.Text, m.Operator))
	return plan.OfflineMatch != nil && plan.OfflineMatch.MatchString(output)
}

// deliveryNotRunningReason is the 「未送达」 reason of a job that reported the session's
// process is gone: nothing was handed over, so resending is safe.
func deliveryNotRunningReason(m jobstore.SessionMessage) string {
	return ReasonNotRunning + ": job " + m.JobID + " 报告会话进程已不在运行（未送达，可重试）"
}

// deliveryFailureReason explains a failed delivery with the JOB's state first: the old
// text ("deliver_failed:exit code -1: job X") said nothing about why nothing arrived —
// the job had not even started in the case that motivated this (gofer-qzv5).
func deliveryFailureReason(m jobstore.SessionMessage, state InjectState) string {
	if !state.Known {
		return fmt.Sprintf("job %s 已不在（服务重启或已清理），无法确认是否送达", m.JobID)
	}
	reason := fmt.Sprintf("job %s 状态 %s exit=%d", m.JobID, statusText(state.Status), state.ExitCode)
	if detail := strings.TrimSpace(state.Output); detail != "" {
		return reason + "：" + truncateRunes(detail, 200)
	}
	return reason + "（无输出）"
}

// statusText is a job status for a human reading a reason line.
func statusText(status string) string {
	if strings.TrimSpace(status) == "" {
		return "unknown"
	}
	return status
}

// deliveryDoneStatus is the job status of a job that ran to completion (the job layer's
// StatusDone; the relay avoids importing it for two states).
const deliveryDoneStatus = "done"

// deliveryNeverStarted reports whether a delivery failure means the text did NOT reach
// the session, so the message can be offered for retry (jobstore
// SessionMessageUndelivered). It is the set of failures of a delivery that never got an
// attempt in: no execution machine, no pane, the session is over, the agent cannot be
// continued, the pane check refused, the internal job never started.
//
// A FAILURE of an attempt is NOT in this set: a deliver_command that exited non-zero may
// still have handed the text to the live process, and a retry would then deliver it
// twice — which is exactly why the ladder stopped at it in the first place.
func deliveryNeverStarted(reason string) bool {
	if strings.HasPrefix(reason, NotDeliveredPrefix) {
		return true
	}
	switch reason {
	case ReasonNoRunner, ReasonNoTmux, ReasonEnded, ReasonNoResumeTemplate,
		ReasonInteractiveNotAllowed, ReasonCwdOutsideProject,
		InjectFailedPrefix + injectPaneMissing:
		return true
	}
	if strings.HasPrefix(reason, HandedOffPrefix) {
		return true
	}
	return strings.HasPrefix(reason, InjectFailedPrefix+injectPaneBusyPrefix)
}
