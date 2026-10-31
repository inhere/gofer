package brief

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/msgprefix"
)

// The post-compact re-injection (设计 2026-10-22 D1/D2): after the repository-level
// prime, a session whose context was just compacted (or resumed) is told what IT was
// working on. Every item is one line with the command that picks it up — the section
// is a reminder of state, not a copy of the issue / plan / letter.
//
// The types below are the renderer's input; `gofer repo prime` fills them from
// GET /v1/sessions/{sid}/focus plus the local tracker (the claim inference).

// RefocusClaim is an issue the session has claimed. Inferred marks a claim that the
// tracker does not record against this session: it looks like the session's work
// (in_progress, touched while the session was running) but prime can only say
// 「可能」.
type RefocusClaim struct {
	ID       string
	Title    string
	Status   string
	Inferred bool
}

// RefocusPlan is an open / blocked plan the session supervises.
type RefocusPlan struct {
	PlanID   string
	Title    string
	Status   string
	Progress int
	Total    int
	Next     string
	Blocked  string
}

// RefocusJob is a job the session was waiting on.
type RefocusJob struct {
	JobID  string
	Title  string
	Status string
}

// RefocusMail is a letter the session has not answered yet.
type RefocusMail struct {
	ID      int64
	From    string
	Subject string
}

// Refocus is everything the re-injection section may hold.
type Refocus struct {
	Topic  string
	Claims []RefocusClaim
	Plans  []RefocusPlan
	Jobs   []RefocusJob
	Mail   []RefocusMail
}

// Empty reports whether there is nothing to remind the session of (no section then:
// a compact must not add an empty header to the prime).
func (r Refocus) Empty() bool {
	return strings.TrimSpace(r.Topic) == "" && len(r.Claims) == 0 && len(r.Plans) == 0 &&
		len(r.Jobs) == 0 && len(r.Mail) == 0
}

// RefocusHeadIn is the sentence after the prefix in lang: why the section appeared.
func RefocusHeadIn(lang i18n.Lang, resumed bool) string {
	if resumed {
		return briefText(lang, "brief.refocus.head_resumed")
	}
	return briefText(lang, "brief.refocus.head_compacted")
}

// RenderRefocusIn renders the section in lang, or "" when there is nothing to say, it
// does not fit the budget, or the caller asked for no section at all.
//
// budget is the number of BYTES the whole prime has left (the same budget
// tracker.PrimeMaxBytes gives the other sections): items are appended while they fit,
// so a long topic still leaves room for the first claim instead of dropping the
// whole section.
func RenderRefocusIn(lang i18n.Lang, r Refocus, resumed bool, budget int) string {
	if r.Empty() || budget <= 0 {
		return ""
	}
	var b strings.Builder
	head := msgprefix.FormatIn(lang, msgprefix.Hint, "", RefocusHeadIn(lang, resumed))
	if len(head)+1 > budget {
		return ""
	}
	b.WriteString(head)
	b.WriteString("\n")
	add := func(line string) bool {
		line += "\n"
		if b.Len()+len(line) > budget {
			return false
		}
		b.WriteString(line)
		return true
	}
	if topic := strings.TrimSpace(r.Topic); topic != "" {
		if !add(briefText(lang, "brief.refocus.topic", "topic", topic)) {
			return ""
		}
	}
	status := func(s string) string {
		if s == "" {
			return ""
		}
		return briefText(lang, "brief.refocus.status", "status", s)
	}
	if len(r.Claims) > 0 {
		if !add(briefText(lang, "brief.refocus.claims")) {
			return b.String()
		}
		for _, c := range r.Claims {
			line := "- " + c.ID + " " + c.Title + status(c.Status)
			if c.Inferred {
				line = "- " + briefText(lang, "brief.refocus.inferred") + " " + c.ID + " " + c.Title + status(c.Status)
			}
			line += " → gofer issue brief " + c.ID
			if !add(line) {
				return b.String()
			}
		}
	}
	if len(r.Plans) > 0 {
		if !add(briefText(lang, "brief.refocus.plans")) {
			return b.String()
		}
		for _, p := range r.Plans {
			line := "- " + p.PlanID
			if p.Title != "" {
				line += " " + p.Title
			}
			if detail := planProgress(lang, p); detail != "" {
				line += briefText(lang, "brief.refocus.detail", "detail", detail)
			}
			line += " → gofer plan brief " + p.PlanID
			if !add(line) {
				return b.String()
			}
		}
	}
	if len(r.Jobs) > 0 {
		if !add(briefText(lang, "brief.refocus.jobs")) {
			return b.String()
		}
		for _, j := range r.Jobs {
			line := "- " + j.JobID
			if j.Title != "" {
				line += " " + j.Title
			}
			line += status(j.Status)
			line += " → gofer job show " + j.JobID
			if !add(line) {
				return b.String()
			}
		}
	}
	if len(r.Mail) > 0 {
		if !add(briefText(lang, "brief.refocus.mail")) {
			return b.String()
		}
		for _, m := range r.Mail {
			line := "- " + briefText(lang, "brief.refocus.mail_from", "from", m.From)
			if m.Subject != "" {
				line += briefText(lang, "brief.refocus.mail_subject", "subject", m.Subject)
			}
			line += fmt.Sprintf(" → gofer mail show %d", m.ID)
			if !add(line) {
				return b.String()
			}
		}
	}
	return b.String()
}

// planProgress spells a plan's state: progress, the next item, the blocker.
func planProgress(lang i18n.Lang, p RefocusPlan) string {
	parts := make([]string, 0, 3)
	if p.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", p.Progress, p.Total))
	}
	if p.Status == "blocked" && p.Blocked != "" {
		parts = append(parts, briefText(lang, "brief.refocus.blocked", "value", p.Blocked))
	} else if p.Next != "" {
		parts = append(parts, briefText(lang, "brief.refocus.next", "value", p.Next))
	}
	return strings.Join(parts, briefText(lang, "brief.refocus.join"))
}
