package mail

import (
	"log/slog"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The mailbox's log events: one line per step of a letter's life, so the question 「这封信何时
// 发出、发给谁、现在为什么还没送达、何时经什么路径送达」 is answered from the server log alone
// (the deliverer used to log only a real wake, leaving a letter that waited three minutes —
// or a session that was never reached — with no trace at all).
//
// Event names follow the 域.动作 convention and the fields are what an operator filters on:
//
//	mail.sent            a letter was stored (who → whom, urgent, thread)
//	mail.delivered       a delivery path landed it (path, latency, the job that carried it)
//	mail.claim_released  a path handed its claim back (reason): the letter waits again
//	mail.held            parked for a person, with the reason nobody could take it
//	mail.bounced         a 退信 written back to the sender
//	mail.wake_waiting    a session with pending letters is not woken, and why
//
// SR1301: ids, kinds, states, reasons and ages only. A letter's body, a prompt, a reply and
// any credential never reach the log — the body is the one thing the mailbox must not copy
// anywhere (设计「敏感信息」).
const (
	EventSent          = "mail.sent"
	EventDelivered     = "mail.delivered"
	EventClaimReleased = "mail.claim_released"
	EventHeld          = "mail.held"
	EventBounced       = "mail.bounced"
	EventWakeWaiting   = "mail.wake_waiting"
)

// Delivery paths the server-side deliverer logs (the hook's own take logs "hook" from the
// store's ConfirmSessionMail, the only place that write happens).
const (
	// PathSugaSend is `suga send` into a suga session's live process.
	PathSugaSend = "suga_send"
	// PathJobSay is `job say` into an ACP session job awaiting input.
	PathJobSay = "job_say"
	// PathResidentSay is `job say` into the resident session of an `agent:` address.
	PathResidentSay = "resident_say"
)

// logSent reports one stored letter (Send / Reply / readdress).
func logSent(m jobstore.Mail) {
	slog.Info(EventSent,
		"mail_id", m.ID, "from_kind", m.FromKind, "from_id", m.FromID,
		"to_kind", m.ToKind, "to_id", m.ToID, "to_address", m.ToAddress,
		"urgent", m.Urgent, "thread", m.ThreadID, "state", m.State)
}

// logDelivered reports one letter a delivery path landed. extra carries the path's own fields
// (session, job_id) and must never be a body.
func logDelivered(m jobstore.Mail, path string, now time.Time, extra ...any) {
	attrs := []any{
		"mail_id", m.ID, "path", path, "to_kind", m.ToKind, "to_id", m.ToID,
		"latency_sec", mailLatencySec(m, now),
	}
	slog.Info(EventDelivered, append(attrs, extra...)...)
}

// logClaimReleased reports a letter handed back to pending: it waits for the next path, and
// the reason says whether the recipient was unreachable or the path failed.
func logClaimReleased(id int64, reason string) {
	slog.Info(EventClaimReleased, "mail_id", id, "reason", logReason(reason))
}

// logHeld reports a letter parked in the capture box, with the reason.
func logHeld(m jobstore.Mail, reason string) {
	slog.Info(EventHeld,
		"mail_id", m.ID, "reason", reason, "from_kind", m.FromKind, "from_id", m.FromID,
		"to_kind", m.ToKind, "to_id", m.ToID, "to_address", m.ToAddress)
}

// maxLogReasonRunes bounds a delivery failure written to the log: a deliver_command's failure
// carries the subprocess' stderr, which can be a multi-line blob of arbitrary length. The value
// STORED on the letter (last_error) keeps the full text; only the log line is compressed.
const maxLogReasonRunes = 300

// logReason makes a failure reason safe for one log line: every run of whitespace (newlines,
// tabs included) becomes a single space, and the result is cut to maxLogReasonRunes on a rune
// boundary with an ellipsis (SR1301: a reason may quote a command's stderr, never a body).
func logReason(reason string) string {
	return Summarize(strings.Join(strings.Fields(reason), " "), maxLogReasonRunes)
}

// mailLatencySec is how long a letter waited between being stored and being delivered.
func mailLatencySec(m jobstore.Mail, now time.Time) int64 {
	if m.CreatedAt <= 0 {
		return 0
	}
	if age := now.Unix() - m.CreatedAt; age > 0 {
		return age
	}
	return 0
}
