package jobstore

import (
	"log/slog"
)

// The two letter transitions the store itself has to report, because no upper layer sees
// them: the hook's confirmed take (mail.delivered with path "hook" — the ONLY place that
// write happens, both HTTP handlers and the relay's Stop delivery go through it) and the
// delivery deadline's expiry of a broadcast copy (ExpireOverdue returns only the letters a
// person has to record, so the expired copies are invisible from above).
//
// SR1301: ids, kinds, states and reasons only — never a letter's body. Field names match the
// mailbox's own events (internal/mail/log.go).

// MailPathHook is the delivery path label of the hook's own take (claim → confirm).
const MailPathHook = "hook"

// logHookDelivered reports one letter the session's hook confirmed and is about to inject.
func logHookDelivered(m Mail, now int64) {
	latency := int64(0)
	if m.CreatedAt > 0 && now > m.CreatedAt {
		latency = now - m.CreatedAt
	}
	slog.Info("mail.delivered", "mail_id", m.ID, "path", MailPathHook,
		"to_kind", m.ToKind, "to_id", m.ToID, "latency_sec", latency)
}

// logMailExpired reports one letter the delivery deadline dropped.
func logMailExpired(m Mail, reason string) {
	slog.Info("mail.expired", "mail_id", m.ID, "reason", reason, "state", m.State,
		"from_kind", m.FromKind, "to_kind", m.ToKind, "to_id", m.ToID, "broadcast_id", m.BroadcastID)
}
