package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work/transcript"
)

// W2b: the work-side rules the steward is held to. The credential layer decides WHICH
// routes the steward may call; these functions decide what a permitted call may do, so the
// same rules apply however the steward reaches them (REST today, anything else later).

// ErrStewardFinalStatus is returned when the steward asks to end a work item: done and
// dropped are a person's call.
var ErrStewardFinalStatus = errors.New("the steward may not mark a work item done or dropped; that is a person's decision")

// StewardUpdateResult is what a steward update did.
type StewardUpdateResult struct {
	Item jobstore.WorkItem
	// Notes are the parts of the request that were deliberately not applied (e.g. a status
	// asked for while the person's own status stands).
	Notes []string
}

// StewardUpdate applies the steward's patch: descriptive and scheduling fields freely,
// the status only to a non-final one and never over a status a person set (then it is
// reported back in Notes and the rest of the patch still applies). A status the steward
// does set is owned by `report`, not `human`, so the session-derived mapping and a person
// can still override it.
func (s *Service) StewardUpdate(id string, p jobstore.WorkItemPatch, expectedRev int64, by string) (StewardUpdateResult, error) {
	var res StewardUpdateResult
	p.StatusSource = nil
	if p.Status != nil {
		st := strings.TrimSpace(*p.Status)
		switch {
		case !jobstore.ValidWorkStatus(st):
			return res, fmt.Errorf("%w: invalid status %q", jobstore.ErrWorkInvalid, st)
		case jobstore.WorkStatusFinal(st):
			return res, ErrStewardFinalStatus
		}
		cur, ok, err := s.store.GetWorkItem(id)
		if err != nil {
			return res, err
		}
		if !ok {
			return res, jobstore.ErrWorkItemNotFound
		}
		switch {
		case st == cur.Status:
			p.Status = nil
		case cur.StatusSource == jobstore.WorkSourceHuman:
			res.Notes = append(res.Notes, "状态：想标为 "+st+"（已被人手动设置的状态优先，未采纳）")
			p.Status = nil
		default:
			src := jobstore.WorkSourceReport
			p.Status, p.StatusSource = &st, &src
		}
	}
	w, _, err := s.store.UpdateWorkItem(id, p, expectedRev, by)
	if err != nil {
		return res, err
	}
	res.Item = w
	return res, nil
}

// ---------------------------------------------------------------- merge suggestions

// ErrMergeSuggestionResolved is returned when a suggestion was already decided.
var ErrMergeSuggestionResolved = errors.New("merge suggestion was already decided")

// SuggestMerge records the steward's "source belongs in target" for a person to confirm.
// It merges nothing.
func (s *Service) SuggestMerge(targetID, sourceID, reason, by string) (jobstore.WorkMergeSuggestion, bool, error) {
	return s.store.AddWorkMergeSuggestion(targetID, sourceID, reason, by)
}

// MergeSuggestions lists pending suggestions.
func (s *Service) MergeSuggestions() ([]jobstore.WorkMergeSuggestion, error) {
	return s.store.ListWorkMergeSuggestions(jobstore.MergeSuggestPending)
}

// AcceptMergeSuggestion is the person's yes: it performs the merge and closes the
// suggestion.
func (s *Service) AcceptMergeSuggestion(id int64, by string) (jobstore.WorkItem, error) {
	sg, err := s.store.GetWorkMergeSuggestion(id)
	if err != nil {
		return jobstore.WorkItem{}, err
	}
	if sg.State != jobstore.MergeSuggestPending {
		return jobstore.WorkItem{}, ErrMergeSuggestionResolved
	}
	w, err := s.store.MergeWorkItems(sg.TargetID, []string{sg.SourceID}, by)
	if err != nil {
		return jobstore.WorkItem{}, err
	}
	if err := s.store.ResolveWorkMergeSuggestion(id, jobstore.MergeSuggestAccepted); err != nil {
		return w, err
	}
	s.SyncItem(w.ID)
	return w, nil
}

// DismissMergeSuggestion is the person's no.
func (s *Service) DismissMergeSuggestion(id int64) error {
	sg, err := s.store.GetWorkMergeSuggestion(id)
	if err != nil {
		return err
	}
	if sg.State != jobstore.MergeSuggestPending {
		return ErrMergeSuggestionResolved
	}
	return s.store.ResolveWorkMergeSuggestion(id, jobstore.MergeSuggestDismissed)
}

// ---------------------------------------------------------------- session tail

// ErrSessionNotFound is returned by SessionTail for an unknown session id.
var ErrSessionNotFound = errors.New("session not found")

// DefaultSessionTailBytes / MaxSessionTailBytes bound what the steward's read-only session
// tail returns (the same read the summarizer uses, so a worker's v17 transcript_tail frame
// enforces the size limit on its side too).
const (
	DefaultSessionTailBytes = 64 * 1024
	MaxSessionTailBytes     = 256 * 1024
	// tailTextMaxRunes is the rendered text cap handed to the steward.
	tailTextMaxRunes = 12000
)

// SessionTailResult is the read-only transcript tail of one session.
type SessionTailResult struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	// Source is "transcript" (the registered transcript's tail) or "fallback" (the
	// session's last message / progress line when the transcript cannot be read).
	Source string `json:"source"`
	Text   string `json:"text"`
}

// SessionTail returns the tail of a session's transcript rendered as plain text, or — when
// the transcript is not readable (none registered, an old worker, the file is gone) — the
// session's last message and progress line. maxBytes <= 0 means the default; it is clamped
// to MaxSessionTailBytes. It never writes anything.
func (s *Service) SessionTail(ctx context.Context, sid string, maxBytes int64) (SessionTailResult, error) {
	a, ok, err := s.store.GetAgentSession(strings.TrimSpace(sid))
	if err != nil {
		return SessionTailResult{}, err
	}
	if !ok {
		return SessionTailResult{}, ErrSessionNotFound
	}
	if maxBytes <= 0 {
		maxBytes = DefaultSessionTailBytes
	}
	maxBytes = min(maxBytes, MaxSessionTailBytes)
	out := SessionTailResult{SessionID: a.SessionID, Agent: a.Agent, Source: "fallback"}
	if s.transcripts != nil && strings.TrimSpace(a.Transcript) != "" {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		raw, rerr := s.transcripts.ReadTail(rctx, a, maxBytes)
		cancel()
		if rerr == nil {
			turns := transcript.Parse(transcript.DialectFor(a.Agent), raw)
			if t := transcript.Format(turns, transcript.FormatOpts{}); strings.TrimSpace(t) != "" {
				out.Source, out.Text = "transcript", clipRunes(t, tailTextMaxRunes)
				return out, nil
			}
		}
	}
	var b strings.Builder
	if m := strings.TrimSpace(a.LastMessage); m != "" {
		b.WriteString("会话最后一条消息：\n" + clipRunes(m, 3000) + "\n\n")
	}
	if p := strings.TrimSpace(a.ProgressText); p != "" {
		b.WriteString("会话进度行：" + clipRunes(p, 500) + "\n")
	}
	out.Text = strings.TrimSpace(b.String())
	return out, nil
}

// StatusLabel is the Chinese label of a work-item status (the steward prime and the digest
// use the same words the board shows).
func StatusLabel(st string) string { return statusLabel(st) }
