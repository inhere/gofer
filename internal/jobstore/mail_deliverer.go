package jobstore

import "strings"

// The server deliverer's claim holder helpers (plan M5). They are the claimed_by-checked
// counterparts of ConfirmMail / the lease expiry, so a deliverer only ever finishes or gives
// back the letters IT claimed — never one a hook claimed after the deliverer's lease ran out.

// MailDelivererClaimer is the claimed_by stamp of the server deliverer's claims.
const MailDelivererClaimer = "deliverer"

// ConfirmMailClaim finishes a delivery path's claim: delivering → delivered, only while the
// letter is still held by claimedBy. ok=false means the lease ran out and somebody else has
// it (or the letter moved on) — the at-least-once path then may deliver it again.
func (s *Store) ConfirmMailClaim(id int64, claimedBy string) (Mail, bool, error) {
	return s.moveMailWhere(id, MailDelivered, ", delivered_at=?, claim_until=0, last_error=''",
		[]any{s.unixNow()}, " AND state=? AND claimed_by=?", []any{MailDelivering, strings.TrimSpace(claimedBy)})
}

// ReleaseMailClaim hands a letter back to pending right away (delivering → pending) because
// the path that claimed it could not deliver it now (the recipient is not online): waiting for
// the lease to run out would only delay the next attempt. reason is kept as last_error.
func (s *Store) ReleaseMailClaim(id int64, claimedBy, reason string) (Mail, bool, error) {
	return s.moveMailWhere(id, MailPending, ", claim_until=0, last_error=?",
		[]any{strings.TrimSpace(reason)}, " AND state=? AND claimed_by=?", []any{MailDelivering, strings.TrimSpace(claimedBy)})
}
