package sessionrelay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Session nudges (N2 §E, SESS-12). A person sets a timer on a terminal session; when it
// is due the text goes out through the SAME delivery ladder as the web "send a message to
// the session" box (SendMessage: relay turn -> messenger -> deliver command / tmux). The
// sweeper is driven by the server (httpapi.Server.StartNudgeSweeper) the way the schedule
// and wakeup sweepers are; this file owns the rules, the store owns the rows.

// NudgeMaxFailures is how many CONSECUTIVE failed deliveries pause a nudge: a session that
// cannot be reached must not be hammered every interval, and the person is told.
const NudgeMaxFailures = 3

// nudgeOperator is the speaker label of a nudge in the session outbox and the messenger
// prompt; the nudge id is appended so a delivered message can be traced back.
const nudgeOperator = "gofer-nudge"

// nudgeSendTimeout bounds one delivery attempt (the messenger path polls up to
// messengerTimeout itself; this is the outer guard).
const nudgeSendTimeout = 3 * time.Minute

// ErrNudgeNotFound is returned for an unknown nudge id.
var ErrNudgeNotFound = errors.New("sessionrelay: unknown session nudge")

// NudgeNotifier is the optional notification seam for a nudge that paused itself after
// repeated delivery failures. The Notifier the service holds implements it when it can
// (job.Service does); otherwise the pause is only logged and visible in the nudge row.
type NudgeNotifier interface {
	NotifySessionNudgePaused(sessionID, projectKey, title, nudgeID, reason string)
}

// NudgeInput is the creation request of one nudge.
type NudgeInput struct {
	Kind     string // jobstore.NudgeEvery | jobstore.NudgeStalled
	Interval time.Duration
	Text     string
	UntilAt  int64 // unix seconds; 0 = no end
	By       string
}

func nudgeErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jobstore.ErrNudgeInvalid):
		return fmt.Errorf("%w: %s", ErrInvalidInput, strings.TrimPrefix(err.Error(), jobstore.ErrNudgeInvalid.Error()+": "))
	case errors.Is(err, jobstore.ErrNudgeNotFound):
		return ErrNudgeNotFound
	}
	return err
}

// CreateNudge arms a nudge on a live session. An ended or handed-off session is refused:
// there is nobody the text could reach and the nudge would end at its first sweep.
func (s *Service) CreateNudge(sid string, in NudgeInput) (jobstore.SessionNudge, error) {
	a, err := s.Session(sid)
	if err != nil {
		return jobstore.SessionNudge{}, err
	}
	switch a.State {
	case jobstore.SessionEnded:
		return jobstore.SessionNudge{}, fmt.Errorf("%w: session ended", ErrInvalidInput)
	case jobstore.SessionHandedOff:
		return jobstore.SessionNudge{}, fmt.Errorf("%w: session is handed off to a takeover job", ErrInvalidInput)
	}
	n, err := s.store.CreateSessionNudge(jobstore.SessionNudge{
		SessionID: sid, Kind: in.Kind, IntervalSec: int64(in.Interval / time.Second),
		Text: in.Text, UntilAt: in.UntilAt, CreatedBy: in.By,
	})
	return n, nudgeErr(err)
}

// Nudges lists a session's nudges (ended ones only with includeEnded).
func (s *Service) Nudges(sid string, includeEnded bool) ([]jobstore.SessionNudge, error) {
	if _, err := s.Session(sid); err != nil {
		return nil, err
	}
	return s.store.ListSessionNudges(sid, includeEnded)
}

// Nudge reads one nudge.
func (s *Service) Nudge(id string) (jobstore.SessionNudge, error) {
	n, ok, err := s.store.GetSessionNudge(id)
	if err != nil {
		return jobstore.SessionNudge{}, err
	}
	if !ok {
		return jobstore.SessionNudge{}, ErrNudgeNotFound
	}
	return n, nil
}

// PauseNudge / ResumeNudge flip a nudge by hand; both are idempotent-safe (a nudge in the
// wrong state answers ErrInvalidInput so the caller sees why nothing changed).
func (s *Service) PauseNudge(id string) (jobstore.SessionNudge, error) {
	n, err := s.Nudge(id)
	if err != nil {
		return n, err
	}
	if n.State != jobstore.NudgeActive {
		return n, fmt.Errorf("%w: nudge is %s, only an active nudge can be paused", ErrInvalidInput, n.State)
	}
	if _, err := s.store.PauseSessionNudge(id, "paused by hand"); err != nil {
		return n, err
	}
	return s.Nudge(id)
}

// ResumeNudge re-arms a paused nudge (failure count cleared, timers restart now). A
// nudge whose session already ended cannot come back.
func (s *Service) ResumeNudge(id string) (jobstore.SessionNudge, error) {
	n, err := s.Nudge(id)
	if err != nil {
		return n, err
	}
	if n.State != jobstore.NudgePaused {
		return n, fmt.Errorf("%w: nudge is %s, only a paused nudge can be resumed", ErrInvalidInput, n.State)
	}
	if a, ok, _ := s.store.GetAgentSession(n.SessionID); !ok || a.State == jobstore.SessionEnded || a.State == jobstore.SessionHandedOff {
		return n, fmt.Errorf("%w: session is no longer live", ErrInvalidInput)
	}
	if _, err := s.store.ResumeSessionNudge(id); err != nil {
		return n, err
	}
	return s.Nudge(id)
}

// RemoveNudge deletes a nudge.
func (s *Service) RemoveNudge(id string) error {
	ok, err := s.store.DeleteSessionNudge(id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNudgeNotFound
	}
	return nil
}

// lastProgressAt is the newest sign that the session is doing something: a hook beat
// (prompt / Stop / sub-agent / notification all stamp last_seen_at), a progress line, or
// token usage growth (AddSessionUsage stamps usage_at). Whatever is newest wins.
func lastProgressAt(a jobstore.AgentSession) int64 {
	t := a.LastSeenAt
	if a.ProgressAt > t {
		t = a.ProgressAt
	}
	if a.UsageAt > t {
		t = a.UsageAt
	}
	if a.StartedAt > t {
		t = a.StartedAt
	}
	return t
}

// hasOpenWork reports whether the session is attached to a work item that is not
// finished (done / dropped) and not parked on purpose.
func (s *Service) hasOpenWork(sid string) bool {
	items, err := s.store.ListWorkItems(jobstore.WorkListOpts{SessionID: sid, Limit: 50})
	if err != nil {
		return false
	}
	for _, it := range items {
		if it.Status != jobstore.WorkParked && !jobstore.WorkStatusFinal(it.Status) {
			return true
		}
	}
	return false
}

// nudgeStalled is the stall rule: the session is `running` (mid-turn), or `idle` while its
// work item is still open (the agent stopped but the work is not done), and the newest
// progress signal — also the last time this nudge fired, so one stall is nudged once per
// interval rather than every sweep — is older than the threshold.
func (s *Service) nudgeStalled(a jobstore.AgentSession, n jobstore.SessionNudge, now int64) bool {
	switch a.State {
	case jobstore.SessionRunning:
	case jobstore.SessionIdle:
		if !s.hasOpenWork(a.SessionID) {
			return false
		}
	default:
		return false
	}
	ref := lastProgressAt(a)
	if n.LastFiredAt > ref {
		ref = n.LastFiredAt
	}
	if n.CreatedAt > ref {
		ref = n.CreatedAt
	}
	return now-ref >= n.IntervalSec
}

// SweepNudges runs one pass over the active nudges at now and returns how many were
// delivered. Ended / handed-off sessions and expired nudges are closed first; an offline
// session is skipped (not a failure: it is not reachable and will beat when it is back).
func (s *Service) SweepNudges(ctx context.Context, now int64) int {
	list, err := s.store.ListActiveNudges()
	if err != nil {
		slog.Warn("sessionrelay: list nudges failed", "err", err)
		return 0
	}
	sent := 0
	for _, n := range list {
		if ctx.Err() != nil {
			return sent
		}
		a, ok, err := s.store.GetAgentSession(n.SessionID)
		if err != nil {
			continue
		}
		switch {
		case !ok || a.State == jobstore.SessionEnded:
			_, _ = s.store.EndSessionNudge(n.ID, jobstore.NudgeEndedSessionEnd)
			continue
		case a.State == jobstore.SessionHandedOff:
			_, _ = s.store.EndSessionNudge(n.ID, jobstore.NudgeEndedHandedOff)
			continue
		case n.UntilAt > 0 && n.UntilAt <= now:
			_, _ = s.store.EndSessionNudge(n.ID, jobstore.NudgeEndedUntil)
			continue
		case a.State == jobstore.SessionOffline:
			continue
		}
		due := false
		if n.Kind == jobstore.NudgeEvery {
			due = n.NextRunAt <= now
		} else {
			due = s.nudgeStalled(a, n, now)
		}
		if !due {
			continue
		}
		if s.fireNudge(ctx, a, n, now) {
			sent++
		}
	}
	return sent
}

func (s *Service) fireNudge(ctx context.Context, a jobstore.AgentSession, n jobstore.SessionNudge, now int64) bool {
	sctx, cancel := context.WithTimeout(ctx, nudgeSendTimeout)
	defer cancel()
	_, serr := s.SendMessage(sctx, a.SessionID, n.Text, nudgeOperator+":"+n.ID)
	errText := ""
	if serr != nil {
		errText = serr.Error()
	}
	upd, paused, err := s.store.RecordNudgeAttempt(n.ID, now, serr == nil, errText, now+n.IntervalSec, NudgeMaxFailures)
	if err != nil {
		slog.Warn("sessionrelay: record nudge attempt failed", "nudge", n.ID, "err", err)
		return serr == nil
	}
	if serr != nil {
		slog.Warn("sessionrelay: nudge delivery failed", "nudge", n.ID, "session", a.SessionID, "fails", upd.FailCount, "err", serr)
	}
	if paused {
		if nn, ok := s.notifier.(NudgeNotifier); ok {
			nn.NotifySessionNudgePaused(a.SessionID, a.ProjectKey, a.Title, n.ID,
				fmt.Sprintf("%d 次连续送达失败：%s", upd.FailCount, upd.LastError))
		}
	}
	return serr == nil
}
