package work

import (
	"log/slog"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/mail"
)

// The capture box's 「交给会话」 hands a captured item over as a LETTER (M6, design 2026-10-16
// §3「分派动作」): the letter carries the item's id (mail.capture_item_id), and what happens to
// the letter is written back into the item's journal — 已送达 once it is delivered, and the
// head of the answer once the recipient replies. The item's text goes INTO the letter, but the
// letter's body never comes back into the item (设计「敏感信息」: 条目只存信件 id 与摘要).
//
// The writeback hangs on the store's letter transitions (jobstore.SetMailStateHook) instead of
// on a call from internal/mail: every delivery path — the hook injection, the server deliverer,
// a read acknowledgement, a reply — performs its own jobstore write, so ONE observer here covers
// them all, and the mailbox never has to learn that a capture box exists (work → mail/jobstore,
// never the reverse, G022).

// mailFeedbackBy is the journal author of a writeback line.
const mailFeedbackBy = "mail"

// watchMail installs the letter transition observer. It is called once, from New.
func (s *Service) watchMail() {
	if s == nil || s.store == nil {
		return
	}
	s.store.SetMailStateHook(func(_, next jobstore.Mail) { s.noteMailState(next) })
}

// noteMailState writes one letter's outcome into the capture-box item it came from. It runs from
// the mailbox's write, so it is best effort like NoteJobOutcome: an error is logged, never
// returned (the letter is stored either way). A letter no item dispatched (CaptureItemID empty)
// is not its business, and neither is any state the item's log does not care about (pending,
// delivering, held, cancelled…).
func (s *Service) noteMailState(next jobstore.Mail) {
	if s == nil || s.store == nil || strings.TrimSpace(next.CaptureItemID) == "" {
		return
	}
	lang := s.itemLang(next.CaptureItemID)
	var line string
	switch next.State {
	case jobstore.MailDelivered:
		line = i18n.T(lang, "work.journal.mail_delivered", "id", next.ID, "target", mailTargetLabel(lang, next))
	case jobstore.MailAnswered:
		line = i18n.T(lang, "work.journal.mail_answered", "id", next.ID, "summary", s.replySummary(lang, next))
	default:
		return
	}
	if _, err := s.store.AppendWorkJournal(next.CaptureItemID, jobstore.WorkJournalNote, line, mailFeedbackBy); err != nil {
		slog.Warn("work.mail_feedback_failed", "event", "work.mail_feedback_failed",
			"item", next.CaptureItemID, "mail", next.ID, "state", next.State, "err", err)
	}
}

// replySummary is the head of the answer that made a letter answered. The answer is the letter
// stored with reply_to = m.ID (mail.Service.Reply writes it right before marking the original
// answered, so it is there); a reply that cannot be read leaves the line without a summary
// instead of inventing one.
func (s *Service) replySummary(lang i18n.Lang, m jobstore.Mail) string {
	rows, err := s.store.ListMail(jobstore.ListMailOpts{ReplyTo: m.ID, OldestFirst: true, Limit: 1})
	if err != nil {
		slog.Warn("work.mail_reply_read_failed", "event", "work.mail_reply_read_failed", "mail", m.ID, "err", err)
		return i18n.T(lang, "work.journal.mail_reply_unreadable")
	}
	if len(rows) == 0 {
		return i18n.T(lang, "work.journal.mail_reply_missing")
	}
	return mail.Summarize(strings.Join(strings.Fields(rows[0].Body), " "), mail.MailSummaryRunes)
}

// mailTargetLabel names a letter's recipient for the item's journal: 「会话 sess-a」 / 「job
// job-7」, and the raw address when the kind is not one of them (a caller letter, or one whose
// address resolved to nobody — an item's letter always has one, but the log must not lie).
func mailTargetLabel(lang i18n.Lang, m jobstore.Mail) string {
	switch m.ToKind {
	case jobstore.MailKindSession:
		return i18n.T(lang, "work.journal.mail_target_session", "id", m.ToID)
	case jobstore.MailKindJob:
		return i18n.T(lang, "work.journal.mail_target_job", "id", m.ToID)
	}
	return m.ToAddress
}
