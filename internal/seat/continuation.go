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

// ContinuationText is what a NEW session sitting in the seat is told: what the seat is, its
// charter, the most recent work report (or plan handoff) of the seat's project, and that the
// earlier conversation is not in this session's context. It is one function so that every
// path that puts a session into a seat (T4's SessionStart injection, an ACP restart) says the
// same thing. The text is cut to a budget on a rune boundary.
func (s *Service) ContinuationText(x jobstore.Seat) (string, error) {
	lang := s.lang()
	var b strings.Builder
	b.WriteString(i18n.T(lang, "seat.continuation.intro", "name", x.Name))
	if p := strings.TrimSpace(x.Purpose); p != "" {
		b.WriteString("\n" + i18n.T(lang, "seat.continuation.purpose", "purpose", p))
	}
	if c := strings.TrimSpace(x.Charter); c != "" {
		b.WriteString("\n\n" + i18n.T(lang, "seat.continuation.charter") + "\n" + c)
	}
	label, body, err := s.recentWork(x)
	if err != nil {
		return "", err
	}
	if body != "" {
		b.WriteString("\n\n" + i18n.T(lang, label) + "\n" + cutRunes(body, reportBudget))
	}
	b.WriteString("\n\n" + i18n.T(lang, "seat.continuation.fresh"))
	return cutRunes(b.String(), continuationBudget), nil
}

// charterReminder is the short opening of a session that loaded its old conversation.
func (s *Service) charterReminder(x jobstore.Seat) string {
	lang := s.lang()
	out := i18n.T(lang, "seat.continuation.restarted", "name", x.Name)
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
