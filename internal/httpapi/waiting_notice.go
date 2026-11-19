package httpapi

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/inbox"
	"github.com/inhere/gofer/internal/today"
	"github.com/inhere/gofer/internal/waitnotice"
	"github.com/inhere/gofer/internal/workbench"
)

// The 「等我」 notices (internal/waitnotice) of this server: what waits, who is told how, and
// the scanner that ties them to the waiting_notices record. The scan rides the session-nudge
// sweeper (StartNudgeSweeper); the logic is in waitnotice, this file only adapts what the
// server holds.

// waitNoticeHolder builds the scanner once, on first use.
type waitNoticeHolder struct {
	once sync.Once
	scan *waitnotice.Scanner
}

// waitScanner returns the notice scanner; nil when this server has no metadata store.
func (s *Server) waitScanner() *waitnotice.Scanner {
	s.waitNotices.once.Do(func() {
		if s.jobs == nil || s.jobs.Meta() == nil || s.today == nil {
			return
		}
		s.waitNotices.scan = &waitnotice.Scanner{
			Store:  s.jobs.Meta(),
			Source: waitingNoticeSource{s},
			Sender: waitingNoticeSender{s},
			ReminderAfter: func() time.Duration {
				cfg := s.serverCfg()
				if cfg == nil {
					return time.Duration(0)
				}
				return time.Duration(cfg.Notification.EffectiveReminderAfterMin()) * time.Minute
			},
		}
	})
	return s.waitNotices.scan
}

// sweepWaitingNotices is one pass of the scan; a failure is logged and the next pass retries.
func (s *Server) sweepWaitingNotices() {
	sc := s.waitScanner()
	if sc == nil {
		return
	}
	if err := sc.Sweep(); err != nil {
		slog.Warn("waiting notice sweep failed", "event", "waitnotice.sweep_failed", "err", err)
	}
}

// waitingNoticeSource is waitnotice.Source over the one 「等我」 judgement. Only the subjects
// whose notification this scan replaced are told about here: a terminal session turn that
// asks for a reply, and a managed ACP session job that waits for a reply. The other kinds of
// the list (approvals, reviews, failures ...) keep the notifications their own events send.
type waitingNoticeSource struct{ s *Server }

func (w waitingNoticeSource) Waiting() ([]waitnotice.Subject, error) {
	subjects, err := w.s.waitingSubjectsFor("")
	if err != nil {
		return nil, err
	}
	var sids, jids []string
	var keep []inbox.Subject
	for _, sub := range subjects {
		switch {
		case sub.Type == inbox.SubjectSession && sub.Card.Kind == today.KindRelay:
			sids = append(sids, sub.Ref)
		case sub.Type == inbox.SubjectJob && sub.Card.Kind == today.KindACPReply:
			jids = append(jids, sub.Ref)
		default:
			continue
		}
		keep = append(keep, sub)
	}
	if len(keep) == 0 {
		return nil, nil
	}
	meta := w.s.jobs.Meta()
	sessions, err := meta.GetAgentSessionsByIDs(sids)
	if err != nil {
		return nil, err
	}
	jobs, err := meta.GetJobsByIDs(jids)
	if err != nil {
		return nil, err
	}
	seats, err := meta.SeatsByRefs(append(append([]string(nil), sids...), jids...))
	if err != nil {
		return nil, err
	}
	out := make([]waitnotice.Subject, 0, len(keep))
	for _, sub := range keep {
		n := waitnotice.Subject{Key: sub.Key, Category: string(sub.Category)}
		if sub.Type == inbox.SubjectSession {
			a, ok := sessions[sub.Ref]
			if !ok {
				continue
			}
			n.CallerID, n.ProjectKey, n.ThreadID = a.CallerID, a.ProjectKey, "r:"+a.SessionID
			n.Title = strings.TrimSpace(a.Title)
			if n.Title == "" {
				n.Title = a.Agent + " " + shortRef(a.SessionID)
			}
		} else {
			rec, ok := jobs[sub.Ref]
			if !ok {
				continue
			}
			n.CallerID, n.ProjectKey, n.ThreadID = rec.CallerID, rec.ProjectKey, workbench.JobThreadID(rec.ID, rec.SessionID)
			n.Title = sub.Card.Title
		}
		if x, ok := seats[sub.Ref]; ok && strings.TrimSpace(x.Name) != "" {
			n.Title = x.Name
		}
		out = append(out, n)
	}
	return out, nil
}

func shortRef(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// waitingNoticeSender is waitnotice.Sender over the three channels: desktop and webhook
// (and event hooks) through the job service, Web Push through the push service.
type waitingNoticeSender struct{ s *Server }

func (w waitingNoticeSender) Send(n waitnotice.Notice) {
	w.s.jobs.NotifyWaiting(n)
	if w.s.push != nil {
		w.s.push.NotifyWaiting(n)
	}
}
