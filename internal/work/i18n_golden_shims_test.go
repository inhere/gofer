package work

import (
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// The shims give the golden scenario ONE way to call the text producers.

func shimOutcomeLine(lang i18n.Lang, o JobOutcome) (string, string) { return OutcomeLineIn(lang, o) }

func shimComputeHealth(lang i18n.Lang, in HealthInput, now int64, stall time.Duration) (string, string) {
	return ComputeHealthIn(lang, in, now, stall)
}

func shimMailTarget(lang i18n.Lang, m jobstore.Mail) string { return mailTargetLabel(lang, m) }

func shimDerived(lang i18n.Lang, sessions []jobstore.AgentSession, jobs map[string]JobState, planWait string) (string, string, bool) {
	return derivedStatus(lang, sessions, jobs, planWait)
}

func shimSuggestionLabel(lang i18n.Lang, field string) string { return suggestionLabel(lang, field) }

func shimTodoNote(lang i18n.Lang, w jobstore.WorkItem) string { return todoNote(lang, w) }

func shimStatusLabel(lang i18n.Lang, st string) string { return statusLabelIn(lang, st) }

func shimDigest(lang i18n.Lang, svc *Service, now time.Time) (Digest, error) {
	return svc.BuildDigestScopedIn(lang, now, nil)
}
