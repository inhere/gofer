package seat

import (
	"strings"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

const (
	// continuationBudget bounds the whole injected text (bytes); reportBudget bounds the
	// recent-report excerpt inside it.
	continuationBudget = 4000
	reportBudget       = 1500
)

// ContinuationText is what a session sitting in the seat is told when nothing else carries the
// seat block: what the seat is, its charter, and the most recent work report (or plan handoff) of
// the seat's project. It is one function so that every path that puts a session into a seat (T4's
// SessionStart injection, an ACP restart) says the same thing. The text is cut to a budget on a
// rune boundary.
func (s *Service) ContinuationText(x jobstore.Seat) (string, error) {
	return s.continuationText(x, continuationOpts{lang: s.lang(), charter: true, fresh: true})
}

// continuationOpts is how ContinuationText varies by caller.
type continuationOpts struct {
	// lang is the language of the session the text is written for.
	lang i18n.Lang
	// charter adds the charter body. A caller that renders the seat block itself (the
	// SessionStart hook, through Section) must leave it off: the charter is rendered there
	// once already, and a second copy reads as two different charters.
	charter bool
	// fresh says the session is brand new (source=startup): only then is the session told that
	// the earlier conversation is not in its context. A resumed or compacted session keeps the
	// old conversation, and claiming otherwise is simply false.
	fresh bool
}

// continuationText is ContinuationText under explicit options (the session registration knows the
// language and the start source of the session it answers).
func (s *Service) continuationText(x jobstore.Seat, o continuationOpts) (string, error) {
	lang := o.lang
	var b strings.Builder
	b.WriteString(i18n.T(lang, "seat.continuation.intro", "name", x.Name))
	if p := strings.TrimSpace(x.Purpose); p != "" {
		b.WriteString("\n" + i18n.T(lang, "seat.continuation.purpose", "purpose", p))
	}
	if c := strings.TrimSpace(x.Charter); o.charter && c != "" {
		b.WriteString("\n\n" + i18n.T(lang, "seat.continuation.charter") + "\n" + c)
	}
	label, body, err := s.recentWork(x)
	if err != nil {
		return "", err
	}
	if body != "" {
		b.WriteString("\n\n" + i18n.T(lang, label) + "\n" + cutRunes(body, reportBudget))
	}
	if o.fresh {
		b.WriteString("\n\n" + i18n.T(lang, "seat.continuation.fresh"))
	} else {
		b.WriteString("\n\n" + i18n.T(lang, "seat.continuation.resumed"))
	}
	return cutRunes(b.String(), continuationBudget), nil
}

// TakeContinuationNotice hands the continuation text to the session that has just taken its
// seat and marks it delivered for that ref in the same step: ok is false when the session sits
// in no seat or was already told (its second SessionStart). Only a write path (the session
// registration) may call it; the read views leave the notice owed.
//
// source is the SessionStart source the agent reported (startup|resume|clear|compact, "" when it
// reports none): only a startup session is told that the earlier conversation is missing.
func (s *Service) TakeContinuationNotice(ref, source string, lang i18n.Lang) (text string, ok bool, err error) {
	x, sits, err := s.SeatOfRef(ref)
	if err != nil || !sits {
		return "", false, err
	}
	taken, err := s.store.TakeSeatNotice(x.ID, ref)
	if err != nil || !taken {
		return "", false, err
	}
	if lang == "" {
		lang = s.lang()
	}
	// The charter is left to the seat block the hook renders above this notice (Section); an
	// unknown source claims nothing about the context, so it gets the neutral wording.
	fresh := strings.EqualFold(strings.TrimSpace(source), SourceStartup)
	t, err := s.continuationText(x, continuationOpts{lang: lang, fresh: fresh})
	if err != nil {
		return "", false, err
	}
	return t, true, nil
}

// SourceStartup is the SessionStart source of a brand new conversation (the other sources are
// resume / clear / compact; an agent that reports none sends nothing).
const SourceStartup = "startup"

// charterReminder is the short opening of a session that loaded its old conversation.
func (s *Service) charterReminder(x jobstore.Seat) string {
	return s.charterBlock(x, "seat.continuation.restarted")
}

// charterBlock renders a session's seat block with introKey as its first line (the seat's name is
// the {name} placeholder) followed by the charter body. Every path that puts a running session
// into a seat says the same thing this way, differing only in how it opens.
func (s *Service) charterBlock(x jobstore.Seat, introKey string) string {
	lang := s.lang()
	out := i18n.T(lang, introKey, "name", x.Name)
	if c := strings.TrimSpace(x.Charter); c != "" {
		out += "\n\n" + i18n.T(lang, "seat.continuation.charter") + "\n" + c
	}
	return cutRunes(out, continuationBudget)
}

// recentWork finds the newest `work report` of the seat's project, else the latest plan
// handoff of one of its plans. label is the i18n key of the heading; body "" = nothing found.
func (s *Service) recentWork(x jobstore.Seat) (label, body string, err error) {
	items, err := s.store.ListWorkItems(jobstore.WorkListOpts{Project: x.ProjectKey, Limit: 5})
	if err != nil {
		return "", "", err
	}
	for _, it := range items {
		entries, err := s.store.ListWorkJournal(it.ID, 20, 0)
		if err != nil {
			return "", "", err
		}
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Kind == jobstore.WorkJournalReport && strings.TrimSpace(entries[i].Text) != "" {
				return "seat.continuation.report", it.Title + "\n" + strings.TrimSpace(entries[i].Text), nil
			}
		}
	}
	plans, err := s.store.ListPlans(jobstore.PlanFilter{ProjectKey: x.ProjectKey, Limit: 5})
	if err != nil {
		return "", "", err
	}
	for _, p := range plans {
		h, ok, err := s.store.GetPlanHandoff(p.PlanID, 0)
		if err != nil {
			return "", "", err
		}
		if ok && strings.TrimSpace(h.Body) != "" {
			return "seat.continuation.handoff", p.Title + "\n" + strings.TrimSpace(h.Body), nil
		}
	}
	return "", "", nil
}

// cutRunes cuts s to at most max bytes without splitting a rune, marking the cut.
func cutRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
