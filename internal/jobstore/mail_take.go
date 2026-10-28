package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// The hook's way to take letters for ITS session (设计「投递语义」: hook 路径先确认、再注入，
// 至多一次). A take is three store calls: ClaimSessionMail (pending → delivering, lease),
// ConfirmSessionMail (delivering → delivered, only for the claim's holder) and, when the
// hook gives the letters up (the turn it wanted to close was already answered), the
// ReleaseSessionMail that hands them back. A hook injects only letters whose confirmation
// the server acknowledged; a letter whose hook died between claim and confirm returns to
// pending when the lease runs out.

// MailHookClaimer is the claimed_by stamp of the hook delivery paths of one session.
func MailHookClaimer(sid string) string { return "hook:" + strings.TrimSpace(sid) }

// TakeMailOpts bounds one take. Limit <= 0 and MaxRunes <= 0 mean no bound on that axis;
// UrgentOnly restricts the take to urgent letters (the PreToolUse path).
type TakeMailOpts struct {
	Limit      int
	MaxRunes   int
	UrgentOnly bool
	TTL        time.Duration
}

// TakenMail is a claimed letter. Truncated reports that Body was cut to the rune budget (only
// the FIRST letter of a take can be cut: a letter that alone exceeds the budget would
// otherwise never fit any take and block everything behind it).
type TakenMail struct {
	Mail
	Truncated bool
}

// ClaimSessionMail claims, in ONE write-locked section, the letters of session sid that a
// hook may inject now: urgent first, then oldest first, up to opts.Limit letters whose bodies
// together fit opts.MaxRunes. Claims whose lease already ran out are returned to pending
// first, so a hook that died mid-delivery never strands a letter. remaining counts the
// eligible letters left pending afterwards. Letters are never claimed beyond the budget:
// a letter that does not fit stops the take (the order stays strict).
func (s *Store) ClaimSessionMail(sid string, opts TakeMailOpts) (taken []TakenMail, remaining int, err error) {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return nil, 0, fmt.Errorf("%w: session id required", ErrMailInvalid)
	}
	claimer := MailHookClaimer(sid)
	now := s.unixNow()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	defer func() {
		for _, t := range taken {
			s.emit(Change{Kind: ChangeMail, ID: mailIDString(t.ID)})
		}
	}()
	if paused, err := s.mailDeliveryPausedLocked(sid); err != nil {
		return nil, 0, err
	} else if paused {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM mail WHERE to_kind=? AND to_id=? AND state=?`, MailKindSession, sid, MailPending).Scan(&n); err != nil {
			return nil, 0, err
		}
		return nil, n, nil
	}
	if _, err := s.db.Exec(`UPDATE mail SET state=?, claim_until=0 WHERE state=? AND claim_until>0 AND claim_until<=? AND to_kind=? AND to_id=?`,
		MailPending, MailDelivering, now, MailKindSession, sid); err != nil {
		return nil, 0, fmt.Errorf("jobstore: release expired mail claims of %s: %w", sid, err)
	}
	q := selectMailCols + ` WHERE to_kind=? AND to_id=? AND state=?`
	if opts.UrgentOnly {
		q += ` AND COALESCE(urgent,0)=1`
	}
	q += ` ORDER BY COALESCE(urgent,0) DESC, id ASC`
	rows, err := s.db.Query(q, MailKindSession, sid, MailPending)
	if err != nil {
		return nil, 0, fmt.Errorf("jobstore: list pending mail of %s: %w", sid, err)
	}
	var pending []Mail
	for rows.Next() {
		m, err := scanMail(rows)
		if err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("jobstore: list pending mail of %s: %w", sid, err)
		}
		pending = append(pending, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()

	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultMailClaimTTL
	}
	used := 0
	for i, m := range pending {
		if opts.Limit > 0 && len(taken) >= opts.Limit {
			break
		}
		body, cut := m.Body, false
		n := utf8.RuneCountInString(body)
		if opts.MaxRunes > 0 {
			if len(taken) == 0 && n > opts.MaxRunes {
				body, n, cut = string([]rune(body)[:opts.MaxRunes]), opts.MaxRunes, true
			} else if used+n > opts.MaxRunes {
				break
			}
		}
		ok, err := s.moveMailLocked(m.ID, MailDelivering,
			", claimed_by=?, claim_until=?, attempts=attempts+1",
			[]any{claimer, now + int64(ttl/time.Second)}, " AND state=?", []any{MailPending})
		if err != nil {
			return taken, len(pending) - i, err
		}
		if !ok {
			continue
		}
		after, found, err := s.mailLocked(m.ID)
		if err != nil || !found {
			return taken, len(pending) - i, err
		}
		after.Body = body
		taken = append(taken, TakenMail{Mail: after, Truncated: cut})
		used += n
	}
	return taken, len(pending) - len(taken), nil
}

func (s *Store) mailDeliveryPausedLocked(sid string) (bool, error) {
	var paused int
	err := s.db.QueryRow(`SELECT COALESCE(mail_paused,0) FROM agent_sessions WHERE session_id=?`, sid).Scan(&paused)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("jobstore: read mail pause for %s: %w", sid, err)
	}
	return paused != 0, nil
}

// only for letters this session's hook claimed. The confirmed letters are returned; an id
// that is not (or no longer) held by the hook's claim is skipped, so the hook injects exactly
// the returned ones.
func (s *Store) ConfirmSessionMail(sid string, ids []int64) ([]Mail, error) {
	sid = strings.TrimSpace(sid)
	claimer := MailHookClaimer(sid)
	now := s.unixNow()
	out := make([]Mail, 0, len(ids))
	for _, id := range ids {
		m, ok, err := s.moveMailWhere(id, MailDelivered, ", delivered_at=?, claim_until=0, last_error=''",
			[]any{now}, " AND state=? AND claimed_by=? AND to_kind=? AND to_id=?",
			[]any{MailDelivering, claimer, MailKindSession, sid})
		if err != nil {
			return out, err
		}
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// ReleaseSessionMail hands claimed letters back to pending (the hook gave them up). Only the
// hook's own claims move.
func (s *Store) ReleaseSessionMail(sid string, ids []int64) error {
	sid = strings.TrimSpace(sid)
	claimer := MailHookClaimer(sid)
	for _, id := range ids {
		if _, _, err := s.moveMailWhere(id, MailPending, ", claimed_by='', claim_until=0", nil,
			" AND state=? AND claimed_by=? AND to_kind=? AND to_id=?",
			[]any{MailDelivering, claimer, MailKindSession, sid}); err != nil {
			return err
		}
	}
	return nil
}
