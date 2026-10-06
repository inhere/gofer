package work

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// SessionSayer delivers a message into a resident ACP / pty session JOB (job.Service
// implements it via an adapter: `job say`).
type SessionSayer interface {
	SaySession(jobID, text string) error
}

// SetSessionSayer injects the job-session delivery seam (nil = job sessions cannot be
// addressed).
func (s *Service) SetSessionSayer(x SessionSayer) { s.sayer = x }

// Errors of AskSession; the entry layers map them to 404 / 409 / 403 / 400.
var (
	// ErrAskNotFound: the id is neither a registered terminal session nor a job.
	ErrAskNotFound = errors.New("session not found")
	// ErrAskOffline: the session exists but cannot receive a message right now. The text
	// is NOT queued — the caller is told and decides.
	ErrAskOffline = errors.New("session is not online")
	// ErrAskDenied: the caller may not speak to this session.
	ErrAskDenied = errors.New("not allowed to speak to this session")
	// ErrAskEmpty: nothing to say.
	ErrAskEmpty = errors.New("text is required")
	// ErrAskUnsupported: no delivery path is wired for this kind of session.
	ErrAskUnsupported = errors.New("no delivery path for this session")
)

// AskInput is one "带话": a short message ("the resource arrived, carry on") for a
// running session.
type AskInput struct {
	// SessionID is a terminal relay session id OR an ACP / pty session job id.
	SessionID string
	Text      string
	// By is the speaker label (steward(<agent>) / human:<caller>).
	By string
	// WorkID, when set, names the work item the message is logged on; otherwise every
	// open item that has the session as a current one gets the log line.
	WorkID string
	// Prefix is put in front of the text delivered to the session (the steward tells the
	// session who is talking); the journal records the bare text.
	Prefix string
	// Allow vets the target; nil allows all. job is true for a job session.
	Allow func(sid string, job bool) bool
}

// AskResult says where the message went.
type AskResult struct {
	SessionID string `json:"session_id"`
	// Kind is "session" (terminal relay session) or "job" (ACP / pty session job).
	Kind string `json:"kind"`
	// Channel is the path that carried it ("turn" / "messenger" / "job_say").
	Channel string `json:"channel"`
	// WorkItems are the items the delivery was logged on.
	WorkItems []string `json:"work_items"`
}

// AskSession delivers a message to a running session over the existing "传话" channels:
// a terminal session through the relay (answering an open turn, else the one-shot
// messenger), an ACP / pty session job through `job say`. A session that is not online is
// an explicit ErrAskOffline — nothing is queued or silently dropped. A delivered message
// is logged on the work item(s) of that session.
func (s *Service) AskSession(ctx context.Context, in AskInput) (AskResult, error) {
	sid, text := strings.TrimSpace(in.SessionID), strings.TrimSpace(in.Text)
	if sid == "" {
		return AskResult{}, fmt.Errorf("%w: session id is required", ErrAskNotFound)
	}
	if text == "" {
		return AskResult{}, ErrAskEmpty
	}
	by := strings.TrimSpace(in.By)
	if by == "" {
		by = "system"
	}
	deliver := text
	if p := strings.TrimSpace(in.Prefix); p != "" {
		deliver = p + " " + text
	}
	res := AskResult{SessionID: sid}
	if a, found, err := s.store.GetAgentSession(sid); err != nil {
		return res, err
	} else if found {
		res.Kind = "session"
		if in.Allow != nil && !in.Allow(sid, false) {
			return res, ErrAskDenied
		}
		if !sessionRunning(a.State) {
			return res, fmt.Errorf("%w: session %s is %s", ErrAskOffline, shortID(sid), a.State)
		}
		if s.messenger == nil {
			return res, fmt.Errorf("%w: terminal session messaging is not configured", ErrAskUnsupported)
		}
		ch, err := s.messenger.SendRequest(ctx, sid, deliver, by)
		if err != nil {
			return res, fmt.Errorf("%w: delivery failed: %v", ErrAskOffline, err)
		}
		res.Channel = ch
	} else if rec, isJob, err := s.store.GetJob(sid); err != nil {
		return res, err
	} else if isJob {
		res.Kind = "job"
		if in.Allow != nil && !in.Allow(sid, true) {
			return res, ErrAskDenied
		}
		if jobStatusEnded(rec.Status) {
			return res, fmt.Errorf("%w: job session %s is %s", ErrAskOffline, shortID(sid), rec.Status)
		}
		if s.sayer == nil {
			return res, fmt.Errorf("%w: job sessions are not wired", ErrAskUnsupported)
		}
		if err := s.sayer.SaySession(sid, deliver); err != nil {
			return res, fmt.Errorf("%w: %v", ErrAskOffline, err)
		}
		res.Channel = "job_say"
	} else {
		return res, fmt.Errorf("%w: %s", ErrAskNotFound, sid)
	}

	res.WorkItems = s.logAsk(in.WorkID, sid, text, by)
	return res, nil
}

// logAsk writes the "已向会话带话" journal line on the named item, else on the open
// items that hold the session as a current one; it returns the item ids.
func (s *Service) logAsk(workID, sid, text, by string) []string {
	ids := []string{}
	if w := strings.TrimSpace(workID); w != "" {
		ids = append(ids, w)
	} else if items, err := s.store.ListWorkItems(jobstore.WorkListOpts{SessionID: sid, Limit: 20}); err == nil {
		for _, w := range items {
			ids = append(ids, w.ID)
		}
	}
	line := fmt.Sprintf("已向会话 %s 带话：%s", shortID(sid), clipRunes(text, 200))
	logged := ids[:0]
	for _, id := range ids {
		if _, err := s.store.AppendWorkJournal(id, jobstore.WorkJournalNote, line, by); err != nil {
			slog.Warn("work.journal_failed", "event", "work.journal_failed", "id", id, "err", err)
			continue
		}
		logged = append(logged, id)
	}
	return logged
}
