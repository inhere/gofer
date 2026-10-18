package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/util"
)

// gofer 信箱 (design 2026-10-16-session-topics-mailbox-and-inbox-design.md 「二、gofer 信箱」):
// one row per LETTER passed between a session, a caller and a job. A letter is not a web
// message: it has an author (from_kind / from_id / from_caller), the raw address it was
// sent to (to_address: "session:<sid>" / "job:<id>" / "topic:<kw>" / "path:<p>" / "caller")
// and the recipient that address resolved to, it lives in a conversation thread and it
// moves through its own delivery state machine.
//
// This file is the storage layer only: it validates the shape of a row and owns the state
// machine; address resolution, the send limits and the capture box live in internal/mail,
// the delivery ladder in sessionrelay / the hook. Nothing else in the package reads or
// writes these rows (the retired presence `messages` table is unrelated and stays unread).

// MailKindSession / MailKindCaller / MailKindJob are both the from_kind and the to_kind of
// a letter (设计「发件出处与信任级别」): a session of the same caller, a user-class caller
// (web console / CLI / an agent with no session identity) or a job credential.
const (
	MailKindSession = "session"
	MailKindCaller  = "caller"
	MailKindJob     = "job"
)

// MailValidKind reports whether k is one of the three letter kinds.
func MailValidKind(k string) bool {
	switch k {
	case MailKindSession, MailKindCaller, MailKindJob:
		return true
	}
	return false
}

// Letter states (设计「状态机」):
//
//	pending ─claim─▶ delivering ─confirm─▶ delivered ─answer─▶ answered
//	   │                 │ release (lease expired)
//	   │                 └──────▶ pending
//	   └─ hold / expire / cancel ─▶ held │ expired │ cancelled
//	held ─readdress─▶ pending (a person assigns it again)
//	delivering ─fail─▶ failed (送达失败且不再重试)
//
// held is where a letter goes when there is nothing to deliver it to: an ambiguous or
// empty address, a thread that hit its automatic-round-trip ceiling, a letter past its
// delivery deadline, a pty job. A held letter is recorded in the capture box.
const (
	MailPending    = "pending"
	MailDelivering = "delivering"
	MailDelivered  = "delivered"
	MailAnswered   = "answered"
	MailHeld       = "held"
	MailExpired    = "expired"
	MailFailed     = "failed"
	MailCancelled  = "cancelled"
)

// MailValidState reports whether s is one of the letter states.
func MailValidState(s string) bool {
	_, ok := mailTransitions[s]
	return ok
}

// mailTransitions is THE state machine: every legal move, from-state → to-states. Every
// mutation derives its allowed from-states from it (mailIncoming), so a new state can only
// be reached by adding it here, and an illegal transition can never be written by accident.
var mailTransitions = map[string][]string{
	MailPending: {MailDelivering, MailDelivered, MailHeld, MailExpired, MailCancelled},
	// A letter leaves delivering either forward (confirmed / failed), back to pending when
	// its claim's lease expired, or to the side (held / expired / cancelled).
	MailDelivering: {MailDelivered, MailPending, MailHeld, MailExpired, MailFailed, MailCancelled},
	MailDelivered:  {MailAnswered},
	MailAnswered:   {},
	// A held letter goes back to pending when a person (or its author) assigns it again
	// (readdress) — the delivery path then re-claims it as usual.
	MailHeld: {MailPending, MailExpired, MailCancelled},
	// An expired letter is the same dead end one step further: it can still be handed to
	// the capture box (held) or withdrawn.
	MailExpired:   {MailHeld, MailCancelled},
	MailFailed:    {},
	MailCancelled: {},
}

// mailIncoming returns the states from which `to` is a legal next state — the reverse of
// mailTransitions, sorted so the generated WHERE clause is deterministic.
func mailIncoming(to string) []string {
	var from []string
	for state, next := range mailTransitions {
		for _, n := range next {
			if n == to {
				from = append(from, state)
				break
			}
		}
	}
	sort.Strings(from)
	return from
}

// DefaultMailClaimTTL is the 占用租期 (设计「默认数值」): how long a delivery path may hold a
// letter before another path may claim it again. It has to cover one hook call and one
// `suga send` / `job say`, and it is deliberately short: a path that dies mid-delivery
// only delays the letter by one lease.
const DefaultMailClaimTTL = 60 * time.Second

// DefaultMailDeliverBy is the 送达期限: a letter nobody delivered within this window goes to
// the capture box for a person to assign (24h — an unreachable recipient overnight is
// most likely not coming back).
const DefaultMailDeliverBy = 24 * time.Hour

// Errors of the mail storage layer. ErrMailState is wrapped by every refused transition, so
// a caller can tell "illegal move / lost the claim race" (false, nil) from a broken store.
var (
	ErrMailInvalid = errors.New("jobstore: invalid mail")
	ErrMailState   = errors.New("jobstore: illegal mail state transition")
)

// Mail is one stored letter. Times are unix SECONDS (0 = never). ThreadID is the
// conversation thread (a new letter with no thread given starts its own); ReplyTo is the
// letter it answers (0 = a root letter). JobID is the job this letter is about when either
// side is a job. CaptureItemID is the capture-box work item a held letter was recorded as.
type Mail struct {
	ID            int64
	ThreadID      string
	ReplyTo       int64
	FromKind      string
	FromID        string
	FromCaller    string
	ToAddress     string
	ToKind        string
	ToID          string
	ToCaller      string
	JobID         string
	Urgent        bool
	Body          string
	State         string
	ClaimedBy     string
	ClaimUntil    int64
	Attempts      int
	LastError     string
	CreatedAt     int64
	DeliveredAt   int64
	ReadAt        int64
	DeliverBy     int64
	CaptureItemID string
}

// MailInput is one letter to store. ThreadID empty starts a fresh thread. DeliverBy 0 takes
// DefaultMailDeliverBy from CreatedAt. ToKind "" is a letter whose address resolved to nobody:
// its recipient is only its raw address, and the mailbox parks it as held.
type MailInput struct {
	ThreadID   string
	ReplyTo    int64
	FromKind   string
	FromID     string
	FromCaller string
	ToAddress  string
	ToKind     string
	ToID       string
	ToCaller   string
	JobID      string
	Urgent     bool
	Body       string
	DeliverBy  int64
}

const selectMailCols = `SELECT id, thread_id, COALESCE(reply_to,0), from_kind, COALESCE(from_id,''),
  COALESCE(from_caller,''), COALESCE(to_address,''), COALESCE(to_kind,''), COALESCE(to_id,''),
  COALESCE(to_caller,''), COALESCE(job_id,''), COALESCE(urgent,0), COALESCE(body,''), state,
  COALESCE(claimed_by,''), COALESCE(claim_until,0), COALESCE(attempts,0), COALESCE(last_error,''),
  created_at, COALESCE(delivered_at,0), COALESCE(read_at,0), COALESCE(deliver_by,0),
  COALESCE(capture_item_id,'')
  FROM mail`

func scanMail(sc rowScanner) (Mail, error) {
	var m Mail
	var urgent int
	err := sc.Scan(&m.ID, &m.ThreadID, &m.ReplyTo, &m.FromKind, &m.FromID, &m.FromCaller,
		&m.ToAddress, &m.ToKind, &m.ToID, &m.ToCaller, &m.JobID, &urgent, &m.Body, &m.State,
		&m.ClaimedBy, &m.ClaimUntil, &m.Attempts, &m.LastError,
		&m.CreatedAt, &m.DeliveredAt, &m.ReadAt, &m.DeliverBy, &m.CaptureItemID)
	m.Urgent = urgent != 0
	return m, err
}

// InsertMail stores one letter in `pending` (or — for a caller letter, which has no
// session and no job to deliver to — the caller records it in the capture box; the caller
// of this method does that with HoldMail). The row's invariants are checked here (kinds
// known, a session/job recipient named, a body to deliver); whether the two ends may talk
// to each other is the entry layer's judgement, not the store's.
func (s *Store) InsertMail(in MailInput) (Mail, error) {
	in.FromKind = strings.TrimSpace(in.FromKind)
	in.ToKind = strings.TrimSpace(in.ToKind)
	if err := validateMailInput(in); err != nil {
		return Mail{}, err
	}
	now := s.unixNow()
	deliverBy := in.DeliverBy
	if deliverBy <= 0 {
		deliverBy = now + int64(DefaultMailDeliverBy/time.Second)
	}
	defer s.emit(Change{Kind: ChangeMail})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`INSERT INTO mail(thread_id, reply_to, from_kind, from_id, from_caller,
  to_address, to_kind, to_id, to_caller, job_id, urgent, body, state, attempts, created_at, deliver_by)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?)`,
		strings.TrimSpace(in.ThreadID), in.ReplyTo, in.FromKind, strings.TrimSpace(in.FromID),
		strings.TrimSpace(in.FromCaller), strings.TrimSpace(in.ToAddress), in.ToKind,
		strings.TrimSpace(in.ToID), strings.TrimSpace(in.ToCaller), strings.TrimSpace(in.JobID),
		boolInt(in.Urgent), in.Body, MailPending, now, deliverBy)
	if err != nil {
		return Mail{}, fmt.Errorf("jobstore: insert mail: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Mail{}, fmt.Errorf("jobstore: insert mail id: %w", err)
	}
	// A letter sent with no thread of its own OPENS one: the thread id is derived from the
	// row's own id, so it can only be assigned after the insert (and never collides).
	if strings.TrimSpace(in.ThreadID) == "" {
		if _, err := s.db.Exec(`UPDATE mail SET thread_id=? WHERE id=?`, mailThreadID(id), id); err != nil {
			return Mail{}, fmt.Errorf("jobstore: open mail thread %d: %w", id, err)
		}
	}
	m, ok, err := s.mailLocked(id)
	if err != nil {
		return Mail{}, err
	}
	if !ok {
		return Mail{}, fmt.Errorf("jobstore: inserted mail %d vanished", id)
	}
	return m, nil
}

// mailThreadID is the thread id a root letter opens ("m12" for letter 12).
func mailThreadID(id int64) string { return "m" + strconv.FormatInt(id, 10) }

func validateMailInput(in MailInput) error {
	if !MailValidKind(in.FromKind) {
		return fmt.Errorf("%w: unknown from_kind %q", ErrMailInvalid, in.FromKind)
	}
	switch in.FromKind {
	case MailKindSession, MailKindJob:
		if strings.TrimSpace(in.FromID) == "" {
			return fmt.Errorf("%w: from_id required for a %s sender", ErrMailInvalid, in.FromKind)
		}
	}
	switch in.ToKind {
	case "":
		// An address that resolved to nobody: the letter is on its way to the capture box and
		// its raw address (ToAddress) is the only recipient information there is.
	case MailKindSession, MailKindJob:
		if strings.TrimSpace(in.ToID) == "" {
			return fmt.Errorf("%w: to_id required for a %s recipient", ErrMailInvalid, in.ToKind)
		}
	case MailKindCaller:
		// to_caller may be empty: a server without tokens has no caller identity, and
		// 「the default caller's capture box」 is still a meaningful recipient.
	default:
		return fmt.Errorf("%w: unknown to_kind %q", ErrMailInvalid, in.ToKind)
	}
	if strings.TrimSpace(in.Body) == "" {
		return fmt.Errorf("%w: body required", ErrMailInvalid)
	}
	if in.ReplyTo < 0 || in.DeliverBy < 0 {
		return fmt.Errorf("%w: negative reply_to / deliver_by", ErrMailInvalid)
	}
	return nil
}

// GetMail reads one letter. ok is false when there is no such id.
func (s *Store) GetMail(id int64) (Mail, bool, error) {
	return s.mailLocked(id)
}

func (s *Store) mailLocked(id int64) (Mail, bool, error) {
	if id <= 0 {
		return Mail{}, false, nil
	}
	row := s.db.QueryRow(selectMailCols+` WHERE id=?`, id)
	m, err := scanMail(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Mail{}, false, nil
	}
	if err != nil {
		return Mail{}, false, fmt.Errorf("jobstore: get mail %d: %w", id, err)
	}
	return m, true, nil
}

// ClaimMail takes a letter for one delivery path: the CAS from pending to delivering that
// stamps the holder and its lease (设计「投递语义」). ok is false when the letter was not
// pending (already claimed, or never deliverable), which is how two paths — a hook call and
// the deliverer — settle who sends it. Every claim increments attempts.
func (s *Store) ClaimMail(id int64, claimedBy string, ttl time.Duration) (Mail, bool, error) {
	if ttl <= 0 {
		ttl = DefaultMailClaimTTL
	}
	now := s.unixNow()
	defer s.emit(Change{Kind: ChangeMail, ID: mailIDString(id)})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ok, err := s.moveMailLocked(id, MailDelivering,
		", claimed_by=?, claim_until=?, attempts=attempts+1",
		[]any{strings.TrimSpace(claimedBy), now + int64(ttl/time.Second)}, "", nil)
	if err != nil || !ok {
		return Mail{}, false, err
	}
	return s.mailAfterMoveLocked(id)
}

// ConfirmMail finishes a delivery path's claim: delivering → delivered. ok is false when the
// letter is not (or no longer) held by a delivery path — a letter that was never claimed is
// delivered by being READ (MarkMailRead), not by a path confirming it, so the confirmation
// additionally requires the letter to be in the delivering state.
func (s *Store) ConfirmMail(id int64) (Mail, bool, error) {
	return s.confirmMail(id, s.unixNow())
}

// confirmMail is the claimed-only delivery write behind ConfirmMail.
func (s *Store) confirmMail(id, now int64) (Mail, bool, error) {
	return s.moveMailWhere(id, MailDelivered, ", delivered_at=?, claim_until=0, last_error=''",
		[]any{now}, " AND state=?", []any{MailDelivering})
}

// MarkMailRead records that the recipient itself read the letter: reading IS delivery
// (设计「投递语义」), so a pending / delivering letter becomes delivered here, and an already
// delivered one only gets read_at stamped (the move is a no-op, not an illegal transition).
func (s *Store) MarkMailRead(id int64) (Mail, bool, error) {
	now := s.unixNow()
	states := append(mailIncoming(MailDelivered), MailDelivered)
	defer s.emit(Change{Kind: ChangeMail, ID: mailIDString(id)})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE mail SET state=?, read_at=?,
  delivered_at=CASE WHEN COALESCE(delivered_at,0)>0 THEN delivered_at ELSE ? END, claim_until=0
  WHERE id=? AND state IN (`+mailPlaceholders(len(states))+`)`,
		append([]any{MailDelivered, now, now, id}, mailAnySlice(states)...)...)
	if err != nil {
		return Mail{}, false, fmt.Errorf("jobstore: mark mail %d read: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Mail{}, false, err
	}
	if n == 0 {
		return Mail{}, false, nil
	}
	return s.mailAfterMoveLocked(id)
}

// MarkMailAnswered records the recipient's reply: delivered → answered. ok is false when the
// letter was never delivered (an unanswered delivery cannot be answered).
func (s *Store) MarkMailAnswered(id int64) (Mail, bool, error) {
	return s.moveMail(id, MailAnswered, "", nil)
}

// HoldMail parks a letter for a person: (pending|delivering|expired) → held, with the reason
// the delivery could not happen (an ambiguous address and its candidates, a full thread, a
// pty job, a passed deadline). A caller that can record the letter in the capture box does
// so afterwards with SetMailCaptureItem.
func (s *Store) HoldMail(id int64, reason string) (Mail, bool, error) {
	return s.moveMail(id, MailHeld, ", claim_until=0, last_error=?", []any{strings.TrimSpace(reason)})
}

// CancelMail withdraws a letter its author no longer wants delivered.
func (s *Store) CancelMail(id int64, reason string) (Mail, bool, error) {
	return s.moveMail(id, MailCancelled, ", claim_until=0, last_error=?", []any{strings.TrimSpace(reason)})
}

// FailMail gives up on a letter: delivering → failed, with why. This is the terminal state
// of a delivery path that must not be retried (a malformed recipient, a rejected payload).
func (s *Store) FailMail(id int64, reason string) (Mail, bool, error) {
	return s.moveMail(id, MailFailed, ", claim_until=0, last_error=?", []any{strings.TrimSpace(reason)})
}

// ExpireMail marks one letter expired — the deadline state for a caller with no capture box
// to hand it to. The bulk sweep (ExpireOverdue) parks overdue letters as held instead: that
// is what the design promises a person ("超过送达期限仍未送达，转为待指派进收集箱").
func (s *Store) ExpireMail(id int64, reason string) (Mail, bool, error) {
	return s.moveMail(id, MailExpired, ", claim_until=0, last_error=?", []any{strings.TrimSpace(reason)})
}

// moveMail runs the write half of one transition and then reads the row back.
func (s *Store) moveMail(id int64, to, setCols string, setArgs []any) (Mail, bool, error) {
	return s.moveMailWhere(id, to, setCols, setArgs, "", nil)
}

// moveMailWhere is moveMail with one extra condition on the move — how an operation narrows
// the transition table to what it actually means (ConfirmMail's 「confirm MY claim」).
func (s *Store) moveMailWhere(id int64, to, setCols string, setArgs []any, whereCols string, whereArgs []any) (Mail, bool, error) {
	defer s.emit(Change{Kind: ChangeMail, ID: mailIDString(id)})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ok, err := s.moveMailLocked(id, to, setCols, setArgs, whereCols, whereArgs)
	if err != nil || !ok {
		return Mail{}, false, err
	}
	return s.mailAfterMoveLocked(id)
}

func (s *Store) mailAfterMoveLocked(id int64) (Mail, bool, error) {
	m, ok, err := s.mailLocked(id)
	if err != nil || !ok {
		return Mail{}, ok, err
	}
	return m, true, nil
}

// moveMailLocked is the single transition writer: `to` may only be reached from the states
// mailTransitions allows (the generated IN list), ANDed with the caller's extra condition
// (an expired lease, say). ok=false means the letter was in none of those states — an
// illegal transition or a lost race, never an error.
func (s *Store) moveMailLocked(id int64, to, setCols string, setArgs []any, whereCols string, whereArgs []any) (bool, error) {
	from := mailIncoming(to)
	if len(from) == 0 {
		return false, fmt.Errorf("%w: %s is unreachable", ErrMailState, to)
	}
	q := `UPDATE mail SET state=?` + setCols + ` WHERE id=?` + whereCols +
		` AND state IN (` + mailPlaceholders(len(from)) + `)`
	args := make([]any, 0, util.CapSum(len(setArgs), len(whereArgs), len(from), 3))
	args = append(args, to)
	args = append(args, setArgs...)
	args = append(args, id)
	args = append(args, whereArgs...)
	args = append(args, mailAnySlice(from)...)
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return false, fmt.Errorf("jobstore: move mail %d to %s: %w", id, to, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: move mail %d to %s: %w", id, to, err)
	}
	return n > 0, nil
}

// ReleaseExpiredClaims returns letters whose delivery path died mid-flight to pending
// (设计「投递语义」: 租期内未确认则回到 pending), so the next sweep or hook call can send
// them. A claim without a lease (claim_until 0) is never released — it is being delivered
// right now, not abandoned.
func (s *Store) ReleaseExpiredClaims(now int64) (int64, error) {
	from := mailIncoming(MailPending)
	defer s.emit(Change{Kind: ChangeMail})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	q := `UPDATE mail SET state=?, claim_until=0 WHERE state=? AND claim_until>0 AND claim_until<=? AND state IN (` +
		mailPlaceholders(len(from)) + `)`
	args := append([]any{MailPending, MailDelivering, now}, mailAnySlice(from)...)
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: release expired mail claims: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: release expired mail claims: %w", err)
	}
	return n, nil
}

// ExpireOverdue parks every letter past its delivery deadline in the capture box's state:
// pending|delivering → held. The returned letters are the ones the caller must record
// (SetMailCaptureItem), so the sweep and the capture item are written by the same caller.
func (s *Store) ExpireOverdue(now int64) ([]Mail, error) {
	from := mailIncoming(MailHeld)
	defer s.emit(Change{Kind: ChangeMail})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.db.Query(`SELECT id FROM mail WHERE state IN (`+mailPlaceholders(len(from))+
		`) AND deliver_by>0 AND deliver_by<=? ORDER BY id`, append(mailAnySlice(from), now)...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: scan overdue mail: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("jobstore: scan overdue mail: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("jobstore: scan overdue mail: %w", err)
	}
	rows.Close()
	out := make([]Mail, 0, len(ids))
	for _, id := range ids {
		ok, err := s.moveMailLocked(id, MailHeld, ", claim_until=0, last_error=?",
			[]any{mailOverdueReason}, "", nil)
		if err != nil {
			return out, err
		}
		if !ok {
			continue // it moved on between the scan and the write (a reply, a cancel)
		}
		m, exists, err := s.mailLocked(id)
		if err != nil {
			return out, err
		}
		if exists {
			out = append(out, m)
		}
	}
	return out, nil
}

// mailOverdueReason explains the hold a passed deadline produced.
const mailOverdueReason = "delivery deadline passed"

// SetMailCaptureItem records which capture-box item holds this letter (the other half of the
// reference is the mail id inside the item's text). A held letter without an item id is one
// nobody has parked yet.
func (s *Store) SetMailCaptureItem(id int64, itemID string) (bool, error) {
	defer s.emit(Change{Kind: ChangeMail, ID: mailIDString(id)})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE mail SET capture_item_id=? WHERE id=?`, strings.TrimSpace(itemID), id)
	if err != nil {
		return false, fmt.Errorf("jobstore: set mail %d capture item: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: set mail %d capture item: %w", id, err)
	}
	return n > 0, nil
}

// MailListMaxLimit caps ListMail's page size (the caller's Limit is clamped to it).
const MailListMaxLimit = 500

// ListMailOpts filters a letter listing. Every given filter is ANDed; an empty one is not
// applied. Since/Until bound created_at (inclusive / exclusive; 0 = unbounded).
type ListMailOpts struct {
	ToKind     string
	ToID       string
	ToCaller   string
	FromKind   string
	FromID     string
	FromCaller string
	ThreadID   string
	States     []string
	Since      int64
	Until      int64
	// OldestFirst orders by id ascending (a thread read from its first letter) instead of
	// the default newest-first listing.
	OldestFirst bool
	Limit       int
	Offset      int
}

// ListMail returns the letters matching opts, newest first (or oldest first). Limit <= 0
// takes DefaultListLimit; a Limit above MailListMaxLimit is clamped, like every other listing.
func (s *Store) ListMail(opts ListMailOpts) ([]Mail, error) {
	for _, st := range opts.States {
		if !MailValidState(strings.TrimSpace(st)) {
			return nil, fmt.Errorf("%w: unknown state %q", ErrMailInvalid, st)
		}
	}
	where := make([]string, 0, 8)
	args := make([]any, 0, 12)
	add := func(col, val string) {
		if v := strings.TrimSpace(val); v != "" {
			where = append(where, col+"=?")
			args = append(args, v)
		}
	}
	add("to_kind", opts.ToKind)
	add("to_id", opts.ToID)
	add("to_caller", opts.ToCaller)
	add("from_kind", opts.FromKind)
	add("from_id", opts.FromID)
	add("from_caller", opts.FromCaller)
	add("thread_id", opts.ThreadID)
	if len(opts.States) > 0 {
		ph := make([]string, 0, len(opts.States))
		for _, st := range opts.States {
			ph = append(ph, "?")
			args = append(args, strings.TrimSpace(st))
		}
		where = append(where, "state IN ("+strings.Join(ph, ",")+")")
	}
	if opts.Since > 0 {
		where = append(where, "created_at>=?")
		args = append(args, opts.Since)
	}
	if opts.Until > 0 {
		where = append(where, "created_at<?")
		args = append(args, opts.Until)
	}
	q := selectMailCols
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if opts.OldestFirst {
		q += " ORDER BY id"
	} else {
		q += " ORDER BY id DESC"
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MailListMaxLimit {
		limit = MailListMaxLimit
	}
	q += " LIMIT ?"
	args = append(args, limit)
	if opts.Offset > 0 {
		q += " OFFSET ?"
		args = append(args, opts.Offset)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list mail: %w", err)
	}
	defer rows.Close()
	out := make([]Mail, 0, limit)
	for rows.Next() {
		m, err := scanMail(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: list mail: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PendingCount is how many letters a recipient still has to read: everything not yet
// delivered (pending or delivering — a letter in flight is unread too). It is the number
// the session heartbeat reports as mail_pending.
func (s *Store) PendingCount(toKind, toID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM mail WHERE to_kind=? AND to_id=? AND state IN (?,?)`,
		strings.TrimSpace(toKind), strings.TrimSpace(toID), MailPending, MailDelivering).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("jobstore: count pending mail: %w", err)
	}
	return n, nil
}

// CountMailFrom counts the letters one author sent at or after since — the rate window of
// internal/mail's send limit. A caller author is identified by caller only (its id is
// empty), a session / job author by its id.
func (s *Store) CountMailFrom(fromKind, fromID, fromCaller string, since int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM mail
  WHERE from_kind=? AND COALESCE(from_id,'')=? AND COALESCE(from_caller,'')=? AND created_at>=?`,
		strings.TrimSpace(fromKind), strings.TrimSpace(fromID), strings.TrimSpace(fromCaller), since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("jobstore: count mail from sender: %w", err)
	}
	return n, nil
}

// CountThreadMail counts the letters of one conversation thread — the automatic-round-trip
// ceiling of internal/mail ("同一对话串 20 封后，后续信件转待指派").
func (s *Store) CountThreadMail(threadID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM mail WHERE thread_id=?`, strings.TrimSpace(threadID)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("jobstore: count thread mail: %w", err)
	}
	return n, nil
}

// PruneMail deletes every letter created before `before` (保存期, 设计「默认数值」90 天) and
// reports how many rows went. The delivery deadline (24h) is far shorter than the retention,
// so nothing undelivered has to survive it; a letter a person is still holding in the
// capture box is referenced by id from the item, and that item's own retention is separate.
func (s *Store) PruneMail(before int64) (int64, error) {
	defer s.emit(Change{Kind: ChangeMail})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM mail WHERE created_at<?`, before)
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune mail: %w", err)
	}
	return res.RowsAffected()
}

func mailIDString(id int64) string { return strconv.FormatInt(id, 10) }

func mailPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func mailAnySlice(vals []string) []any {
	out := make([]any, 0, len(vals))
	for _, v := range vals {
		out = append(out, v)
	}
	return out
}
