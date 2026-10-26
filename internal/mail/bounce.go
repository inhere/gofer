package mail

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// The 退信 (gofer-0n6r): a letter the sender believed was accepted, which the mailbox could not
// deliver after all. Whatever the reason — the recipient job ended, a one-off job can never take
// a letter, a pty terminal job, a passed delivery deadline — the sender is told in a letter of
// gofer's own (from_kind gofer, body opening with [gofer · 退信]): which letter it was, why it was
// not delivered, and where it is now. The bounce goes through the same mailbox as the original,
// so a session sender receives it exactly like any other letter (injection at its next turn, a
// wake when it is idle).
//
// A bounce is never bounced: gofer's own notice is skipped, and so are the copies of a broadcast
// (one undelivered copy of a 20-recipient notice would otherwise return twenty letters). Neither
// is a hold that happens INSIDE a send: the sender is holding the answer of that call already
// (held=true with the reason and the candidates).

// Bounce returns letter m to its sender as a gofer notice. reason says why it could not be
// delivered, where tells the sender where it is now. ok is false when nothing was written: a
// gofer notice of its own, a broadcast copy, or a letter whose sender has no id at all.
func (s *Service) Bounce(m jobstore.Mail, reason, where string) (jobstore.Mail, bool, error) {
	if m.ID <= 0 || m.FromKind == jobstore.MailKindGofer || m.BroadcastID > 0 {
		return jobstore.Mail{}, false, nil
	}
	jobID := ""
	if m.FromKind == jobstore.MailKindJob {
		jobID = m.FromID // job redaction and the secret scan find the notice of a job's letter
	}
	in := jobstore.MailInput{
		FromKind:  jobstore.MailKindGofer,
		ToAddress: AddressOf(m.FromKind, m.FromID, m.FromCaller),
		ToKind:    m.FromKind, ToID: m.FromID, ToCaller: m.FromCaller,
		JobID: jobID, Urgent: m.Urgent,
		Body: BounceTextLang(s.textLanguage(), m, reason, where), DeliverBy: s.deliverBy(),
	}
	b, err := s.store.InsertMail(in)
	if err != nil {
		return jobstore.Mail{}, false, err
	}
	if m.FromKind == jobstore.MailKindSession {
		return b, true, nil
	}
	// A job / caller sender has no inbox: the notice is recorded (the sender's listing shows it)
	// but there is no path to push it through, so it is parked instead of waiting for a delivery
	// that will never happen.
	held, ok, err := s.store.HoldMail(b.ID, noInboxReason)
	if err != nil || !ok {
		return b, true, err
	}
	return held, true, nil
}

// noInboxReason explains a bounce parked because its addressee cannot take letters at all.
const noInboxReason = "发件方是 job / 调用方，没有可注入的信箱：退信只在此记录"

// overdueReasonText is the bounce reason of a letter that passed its delivery deadline.
const overdueReasonText = "超过送达期限仍没有送到"

// bounceExcerptRunes is how much of the returned letter's body the notice quotes back.
const bounceExcerptRunes = MailSummaryRunes

// BounceText renders a returned letter in the default language.
func BounceText(m jobstore.Mail, reason, where string) string {
	return BounceTextLang(i18n.Default, m, reason, where)
}

// BounceTextLang renders a returned letter in lang.
func BounceTextLang(lang i18n.Lang, m jobstore.Mail, reason, where string) string {
	var b strings.Builder
	b.WriteString(msgprefix.BounceOpen)
	fmt.Fprintf(&b, " %s", i18n.T(lang, "mail.bounce.letter", "id", m.ID))
	if m.ToAddress != "" {
		b.WriteString(i18n.T(lang, "mail.bounce.for", "address", m.ToAddress))
	}
	if m.Urgent {
		b.WriteString(i18n.T(lang, "mail.bounce.urgent"))
	}
	b.WriteString(i18n.T(lang, "mail.bounce.not_delivered"))
	if reason = strings.TrimSpace(reason); reason != "" {
		b.WriteString("：" + bounceReason(reason))
	}
	b.WriteString("。")
	if where = strings.TrimSpace(where); where != "" {
		b.WriteString("\n" + i18n.T(lang, "mail.bounce.where") + where)
	}
	if head := Summarize(strings.TrimSpace(m.Body), bounceExcerptRunes); head != "" {
		b.WriteString("\n" + i18n.T(lang, "mail.bounce.excerpt") + strings.Join(strings.Fields(head), " "))
	}
	return b.String()
}

// captureWhere says the returned letter is waiting in the capture box (item id when it has one).
func captureWhere(itemID string) string {
	if itemID == "" {
		return "已转入收集箱待指派"
	}
	return "已转入收集箱待指派（条目 " + itemID + "）"
}

// bounceReason renders a hold reason for its sender: the store keeps the machine's wording, a
// person reads a sentence.
func bounceReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case PtyHoldReason:
		return "收件方是 pty 终端作业，收不到信件"
	case noInboxReason:
		return noInboxReason
	}
	return strings.TrimSpace(reason)
}
