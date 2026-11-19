package jobstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The seat roster (design 2026-11-18 resident-sessions §4): a seat is one long-lived working
// position of a caller in a project; sessions come and go, the seat stays. Three tables hold it:
// seats (the position and the session currently sitting in it), seat_bindings (every change of
// who sits there) and seat_charter_history (every change of purpose / charter).
//
// Rows are facts only. Whether the sitting session is alive, waiting or unread is derived at
// read time by internal/seat from the session / job tables; ordering and pinning live in the
// workbench layout, never here.

// Seat kinds: a terminal agent CLI session, or an ACP session job.
const (
	SeatKindTerminal = "terminal"
	SeatKindACP      = "acp"
)

// ValidSeatKind reports whether k is a seat kind.
func ValidSeatKind(k string) bool { return k == SeatKindTerminal || k == SeatKindACP }

// Who last wrote a seat's charter / purpose (Seat.CharterBy): SeatByUser or "session:<sid>".
const (
	SeatByUser          = "user"
	SeatBySessionPrefix = "session:"
)

// Seat binding actions recorded in seat_bindings.
const (
	SeatActionBind           = "bind"
	SeatActionRebind         = "rebind"
	SeatActionUnbind         = "unbind"
	SeatActionSessionDeleted = "session_deleted"
	SeatActionArchive        = "archive"
)

var (
	// ErrSeatNotFound: no seat has that id.
	ErrSeatNotFound = errors.New("seat not found")
	// ErrSeatInvalid wraps every malformed seat input.
	ErrSeatInvalid = errors.New("invalid seat")
	// ErrSeatNameTaken: an active seat of the same caller already has that name in the project.
	ErrSeatNameTaken = errors.New("seat name already in use")
	// ErrSeatOccupied: the seat already has another session sitting in it and the bind did not
	// ask to replace it.
	ErrSeatOccupied = errors.New("seat is occupied")
	// ErrSeatRefTaken: the session (or job) already sits in another seat.
	ErrSeatRefTaken = errors.New("session already sits in another seat")
	// ErrSeatArchived: the seat is archived and cannot be bound or edited.
	ErrSeatArchived = errors.New("seat is archived")
)

// Seat is one roster row. Timestamps are unix seconds.
type Seat struct {
	ID         string
	CallerID   string
	ProjectKey string
	Name       string
	// RelCwd is the project-relative directory of the seat ("" / "." = the project root).
	RelCwd  string
	Purpose string
	// Charter is the standing instruction text, or a repository-relative path to it.
	Charter string
	// CharterBy is SeatByUser or "session:<sid>": who last changed Purpose / Charter.
	CharterBy string
	Kind      string
	Agent     string
	// CurrentRef is the terminal session id (kind terminal) or the ACP job id (kind acp)
	// sitting in the seat; "" = vacant.
	CurrentRef string
	// NoticeRef is the ref that already received the seat's continuation notice: a session that
	// takes the seat (bind, ACP restart) is told once what the seat is, and not again. "" = the
	// current session still owes it.
	NoticeRef string
	// ArchivedAt is 0 while the seat is active.
	ArchivedAt int64
	CreatedAt  int64
	UpdatedAt  int64
}

// SeatBinding is one history row of who sat in a seat.
type SeatBinding struct {
	ID      int64
	SeatID  string
	Action  string
	FromRef string
	ToRef   string
	By      string
	At      int64
}

// SeatCharterRevision is one history row of a seat's purpose / charter.
type SeatCharterRevision struct {
	ID      int64
	SeatID  string
	Purpose string
	Charter string
	By      string
	At      int64
}

// SeatFilter filters ListSeats. An empty field does not filter.
type SeatFilter struct {
	CallerID        string
	ProjectKey      string
	IncludeArchived bool
}

// SeatPatch is a partial update; a nil field is left alone. By is who writes (SeatByUser or
// "session:<sid>"): a change of Purpose or Charter records it in CharterBy and appends a
// charter history row.
type SeatPatch struct {
	Name    *string
	RelCwd  *string
	Purpose *string
	Charter *string
	Agent   *string
	By      string
}

func newSeatID() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("seat-%010x", time.Now().UnixNano()&0xffffffffff)
	}
	return "seat-" + hex.EncodeToString(b[:])
}

const selectSeatCols = `SELECT id, caller_id, project_key, name, rel_cwd, purpose, charter, charter_by, kind, agent,
  current_ref, notice_ref, COALESCE(archived_at,0), created_at, updated_at FROM seats`

func scanSeat(sc rowScanner) (Seat, error) {
	var x Seat
	err := sc.Scan(&x.ID, &x.CallerID, &x.ProjectKey, &x.Name, &x.RelCwd, &x.Purpose, &x.Charter, &x.CharterBy,
		&x.Kind, &x.Agent, &x.CurrentRef, &x.NoticeRef, &x.ArchivedAt, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

// migrateSeats adds the seat columns introduced after the table's first schema (the table
// itself comes from the base schema, so it always exists here). Additive only; idempotent on
// every Open.
func (s *Store) migrateSeats() error {
	cols, err := s.tableColumns("seats")
	if err != nil {
		return err
	}
	if _, ok := cols["notice_ref"]; ok {
		return nil
	}
	if _, err := s.db.Exec(`ALTER TABLE seats ADD COLUMN notice_ref TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("jobstore: migrate add seats.notice_ref: %w", err)
	}
	return nil
}

func isSeatUniqueErr(err error, col string) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE") && strings.Contains(err.Error(), col)
}

// CreateSeat inserts a seat (always vacant: binding is a separate step). The name must be
// unique among the caller's active seats of the project; an archived seat frees its name.
func (s *Store) CreateSeat(in Seat) (Seat, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return Seat{}, fmt.Errorf("%w: name required", ErrSeatInvalid)
	}
	if strings.TrimSpace(in.ProjectKey) == "" {
		return Seat{}, fmt.Errorf("%w: project required", ErrSeatInvalid)
	}
	if in.Kind == "" {
		in.Kind = SeatKindTerminal
	}
	if !ValidSeatKind(in.Kind) {
		return Seat{}, fmt.Errorf("%w: kind must be terminal or acp", ErrSeatInvalid)
	}
	if in.CharterBy == "" {
		in.CharterBy = SeatByUser
	}
	now := s.unixNow()
	in.ID, in.CurrentRef, in.ArchivedAt, in.CreatedAt, in.UpdatedAt = newSeatID(), "", 0, now, now
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Seat{}, fmt.Errorf("jobstore: begin create seat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO seats (id, caller_id, project_key, name, rel_cwd, purpose, charter, charter_by,
  kind, agent, current_ref, archived_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,'',NULL,?,?)`,
		in.ID, in.CallerID, in.ProjectKey, in.Name, in.RelCwd, in.Purpose, in.Charter, in.CharterBy,
		in.Kind, in.Agent, in.CreatedAt, in.UpdatedAt); err != nil {
		if isSeatUniqueErr(err, "seats.name") {
			return Seat{}, fmt.Errorf("%w: %q in project %q", ErrSeatNameTaken, in.Name, in.ProjectKey)
		}
		return Seat{}, fmt.Errorf("jobstore: insert seat: %w", err)
	}
	if in.Purpose != "" || in.Charter != "" {
		if _, err := tx.Exec(`INSERT INTO seat_charter_history (seat_id, purpose, charter, by, at) VALUES (?,?,?,?,?)`,
			in.ID, in.Purpose, in.Charter, in.CharterBy, now); err != nil {
			return Seat{}, fmt.Errorf("jobstore: record seat charter: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Seat{}, fmt.Errorf("jobstore: commit create seat: %w", err)
	}
	return in, nil
}

// GetSeat reads one seat. ok is false when the id is unknown.
func (s *Store) GetSeat(id string) (Seat, bool, error) {
	x, err := scanSeat(s.db.QueryRow(selectSeatCols+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Seat{}, false, nil
	}
	if err != nil {
		return Seat{}, false, fmt.Errorf("jobstore: get seat %q: %w", id, err)
	}
	return x, true, nil
}

// ListSeats lists seats, oldest first (a stable base order: the display order is the
// workbench layout's business).
func (s *Store) ListSeats(f SeatFilter) ([]Seat, error) {
	q, args := selectSeatCols, []any{}
	var where []string
	if f.CallerID != "" {
		where, args = append(where, "caller_id = ?"), append(args, f.CallerID)
	}
	if f.ProjectKey != "" {
		where, args = append(where, "project_key = ?"), append(args, f.ProjectKey)
	}
	if !f.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.Query(q+" ORDER BY created_at ASC, id ASC", args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list seats: %w", err)
	}
	defer rows.Close()
	var out []Seat
	for rows.Next() {
		x, err := scanSeat(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: list seats: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SeatsByRefs maps each ref that sits in a seat to that seat (one query for the whole set).
func (s *Store) SeatsByRefs(refs []string) (map[string]Seat, error) {
	out := map[string]Seat{}
	if len(refs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(selectSeatCols+` WHERE current_ref IN (`+mailPlaceholders(len(refs))+`)`, mailAnySlice(refs)...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: seats by refs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		x, err := scanSeat(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: seats by refs: %w", err)
		}
		out[x.CurrentRef] = x
	}
	return out, rows.Err()
}

// UpdateSeat applies a patch. A change of Purpose or Charter stamps CharterBy with patch.By
// and appends a history row; the other fields do not.
func (s *Store) UpdateSeat(id string, p SeatPatch) (Seat, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Seat{}, fmt.Errorf("jobstore: begin update seat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := scanSeat(tx.QueryRow(selectSeatCols+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Seat{}, ErrSeatNotFound
	}
	if err != nil {
		return Seat{}, fmt.Errorf("jobstore: read seat %q: %w", id, err)
	}
	if cur.ArchivedAt != 0 {
		return Seat{}, ErrSeatArchived
	}
	next, by := cur, p.By
	if by == "" {
		by = SeatByUser
	}
	if p.Name != nil {
		next.Name = strings.TrimSpace(*p.Name)
		if next.Name == "" {
			return Seat{}, fmt.Errorf("%w: name required", ErrSeatInvalid)
		}
	}
	if p.RelCwd != nil {
		next.RelCwd = strings.TrimSpace(*p.RelCwd)
	}
	if p.Agent != nil {
		next.Agent = strings.TrimSpace(*p.Agent)
	}
	if p.Purpose != nil {
		next.Purpose = *p.Purpose
	}
	if p.Charter != nil {
		next.Charter = *p.Charter
	}
	charterChanged := next.Purpose != cur.Purpose || next.Charter != cur.Charter
	if charterChanged {
		next.CharterBy = by
	}
	now := s.unixNow()
	if _, err := tx.Exec(`UPDATE seats SET name=?, rel_cwd=?, purpose=?, charter=?, charter_by=?, agent=?, updated_at=? WHERE id=?`,
		next.Name, next.RelCwd, next.Purpose, next.Charter, next.CharterBy, next.Agent, now, cur.ID); err != nil {
		if isSeatUniqueErr(err, "seats.name") {
			return Seat{}, fmt.Errorf("%w: %q in project %q", ErrSeatNameTaken, next.Name, next.ProjectKey)
		}
		return Seat{}, fmt.Errorf("jobstore: update seat %q: %w", id, err)
	}
	if charterChanged {
		if _, err := tx.Exec(`INSERT INTO seat_charter_history (seat_id, purpose, charter, by, at) VALUES (?,?,?,?,?)`,
			cur.ID, next.Purpose, next.Charter, by, now); err != nil {
			return Seat{}, fmt.Errorf("jobstore: record seat charter: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Seat{}, fmt.Errorf("jobstore: commit update seat: %w", err)
	}
	next.UpdatedAt = now
	return next, nil
}

// BindSeat makes ref (a terminal session id or an ACP job id) the session sitting in the seat,
// and writes the history row, in one transaction. A seat that already has a DIFFERENT ref is
// ErrSeatOccupied unless replace is set, so two sessions binding the same vacant seat at once
// end deterministically: the first wins, the second gets ErrSeatOccupied. A ref that sits in
// another seat is ErrSeatRefTaken. Binding the ref that already sits there changes nothing.
func (s *Store) BindSeat(id, ref string, replace bool, by string) (Seat, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Seat{}, fmt.Errorf("%w: session reference required", ErrSeatInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Seat{}, fmt.Errorf("jobstore: begin bind seat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := scanSeat(tx.QueryRow(selectSeatCols+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Seat{}, ErrSeatNotFound
	}
	if err != nil {
		return Seat{}, fmt.Errorf("jobstore: read seat %q: %w", id, err)
	}
	if cur.ArchivedAt != 0 {
		return Seat{}, ErrSeatArchived
	}
	if cur.CurrentRef == ref {
		return cur, nil
	}
	if cur.CurrentRef != "" && !replace {
		return Seat{}, fmt.Errorf("%w: %s is held by %s", ErrSeatOccupied, cur.ID, cur.CurrentRef)
	}
	var other string
	switch err := tx.QueryRow(`SELECT id FROM seats WHERE current_ref = ? AND id <> ?`, ref, cur.ID).Scan(&other); {
	case err == nil:
		return Seat{}, fmt.Errorf("%w: %s sits in %s", ErrSeatRefTaken, ref, other)
	case !errors.Is(err, sql.ErrNoRows):
		return Seat{}, fmt.Errorf("jobstore: check seat ref: %w", err)
	}
	now := s.unixNow()
	if _, err := tx.Exec(`UPDATE seats SET current_ref=?, notice_ref='', updated_at=? WHERE id=?`, ref, now, cur.ID); err != nil {
		if isSeatUniqueErr(err, "seats.current_ref") {
			return Seat{}, fmt.Errorf("%w: %s", ErrSeatRefTaken, ref)
		}
		return Seat{}, fmt.Errorf("jobstore: bind seat %q: %w", id, err)
	}
	action := SeatActionBind
	if cur.CurrentRef != "" {
		action = SeatActionRebind
	}
	if _, err := tx.Exec(`INSERT INTO seat_bindings (seat_id, action, from_ref, to_ref, by, at) VALUES (?,?,?,?,?,?)`,
		cur.ID, action, cur.CurrentRef, ref, by, now); err != nil {
		return Seat{}, fmt.Errorf("jobstore: record seat binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Seat{}, fmt.Errorf("jobstore: commit bind seat: %w", err)
	}
	cur.CurrentRef, cur.UpdatedAt = ref, now
	cur.NoticeRef = ""
	return cur, nil
}

// TakeSeatNotice claims the right to deliver the seat's continuation notice to ref: it is true
// exactly once per session sitting in the seat (the first claim stamps notice_ref, every later
// claim is false). A read never calls it, so a session view cannot consume the notice.
func (s *Store) TakeSeatNotice(id, ref string) (bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("jobstore: begin seat notice: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var current, noticed string
	err = tx.QueryRow(`SELECT current_ref, notice_ref FROM seats WHERE id = ?`, strings.TrimSpace(id)).Scan(&current, &noticed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrSeatNotFound
	}
	if err != nil {
		return false, fmt.Errorf("jobstore: read seat %q: %w", id, err)
	}
	if current != ref || noticed == ref {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE seats SET notice_ref=? WHERE id=?`, ref, strings.TrimSpace(id)); err != nil {
		return false, fmt.Errorf("jobstore: mark seat notice %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("jobstore: commit seat notice: %w", err)
	}
	return true, nil
}

// UnbindSeat empties the seat and records who sat there. A seat with nobody in it is left
// alone (no history row).
func (s *Store) UnbindSeat(id, by string) (Seat, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Seat{}, fmt.Errorf("jobstore: begin unbind seat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := scanSeat(tx.QueryRow(selectSeatCols+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Seat{}, ErrSeatNotFound
	}
	if err != nil {
		return Seat{}, fmt.Errorf("jobstore: read seat %q: %w", id, err)
	}
	if cur.CurrentRef == "" {
		return cur, nil
	}
	now := s.unixNow()
	if err := s.vacateSeatTx(tx, cur, SeatActionUnbind, by, now); err != nil {
		return Seat{}, err
	}
	if err := tx.Commit(); err != nil {
		return Seat{}, fmt.Errorf("jobstore: commit unbind seat: %w", err)
	}
	cur.CurrentRef, cur.UpdatedAt = "", now
	return cur, nil
}

func (s *Store) vacateSeatTx(tx *sql.Tx, cur Seat, action, by string, now int64) error {
	if _, err := tx.Exec(`UPDATE seats SET current_ref='', updated_at=? WHERE id=?`, now, cur.ID); err != nil {
		return fmt.Errorf("jobstore: vacate seat %q: %w", cur.ID, err)
	}
	if _, err := tx.Exec(`INSERT INTO seat_bindings (seat_id, action, from_ref, to_ref, by, at) VALUES (?,'`+action+`',?,'',?,?)`,
		cur.ID, cur.CurrentRef, by, now); err != nil {
		return fmt.Errorf("jobstore: record seat binding: %w", err)
	}
	return nil
}

// ArchiveSeat archives a seat: it leaves the active roster, frees its name and gives up the
// session sitting in it (recorded in the history).
func (s *Store) ArchiveSeat(id, by string) (Seat, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Seat{}, fmt.Errorf("jobstore: begin archive seat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := scanSeat(tx.QueryRow(selectSeatCols+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Seat{}, ErrSeatNotFound
	}
	if err != nil {
		return Seat{}, fmt.Errorf("jobstore: read seat %q: %w", id, err)
	}
	if cur.ArchivedAt != 0 {
		return cur, nil
	}
	now := s.unixNow()
	if cur.CurrentRef != "" {
		if err := s.vacateSeatTx(tx, cur, SeatActionArchive, by, now); err != nil {
			return Seat{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE seats SET archived_at=?, current_ref='', updated_at=? WHERE id=?`, now, now, cur.ID); err != nil {
		return Seat{}, fmt.Errorf("jobstore: archive seat %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return Seat{}, fmt.Errorf("jobstore: commit archive seat: %w", err)
	}
	cur.CurrentRef, cur.ArchivedAt, cur.UpdatedAt = "", now, now
	return cur, nil
}

// DeleteSeat removes a seat and its history. ok is false when the id is unknown.
func (s *Store) DeleteSeat(id string) (bool, error) {
	id = strings.TrimSpace(id)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("jobstore: begin delete seat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{`DELETE FROM seat_bindings WHERE seat_id=?`, `DELETE FROM seat_charter_history WHERE seat_id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return false, fmt.Errorf("jobstore: delete seat history %q: %w", id, err)
		}
	}
	res, err := tx.Exec(`DELETE FROM seats WHERE id=?`, id)
	if err != nil {
		return false, fmt.Errorf("jobstore: delete seat %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("jobstore: commit delete seat: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// clearSeatRefLocked empties the seat ref sits in (if any) and records why. The caller holds
// writeMu; it is the one line DeleteAgentSession adds, so a deleted session never leaves a
// dangling current_ref.
func (s *Store) clearSeatRefLocked(ref, action string) error {
	var cur Seat
	cur, err := scanSeat(s.db.QueryRow(selectSeatCols+` WHERE current_ref = ?`, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("jobstore: find seat of %q: %w", ref, err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin clear seat ref: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.vacateSeatTx(tx, cur, action, "system", s.unixNow()); err != nil {
		return err
	}
	return tx.Commit()
}

// ListSeatBindings returns a seat's binding history, oldest first.
func (s *Store) ListSeatBindings(seatID string) ([]SeatBinding, error) {
	rows, err := s.db.Query(`SELECT id, seat_id, action, from_ref, to_ref, by, at FROM seat_bindings WHERE seat_id=? ORDER BY id`, seatID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list seat bindings: %w", err)
	}
	defer rows.Close()
	var out []SeatBinding
	for rows.Next() {
		var b SeatBinding
		if err := rows.Scan(&b.ID, &b.SeatID, &b.Action, &b.FromRef, &b.ToRef, &b.By, &b.At); err != nil {
			return nil, fmt.Errorf("jobstore: list seat bindings: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListSeatCharterHistory returns a seat's purpose / charter history, oldest first.
func (s *Store) ListSeatCharterHistory(seatID string) ([]SeatCharterRevision, error) {
	rows, err := s.db.Query(`SELECT id, seat_id, purpose, charter, by, at FROM seat_charter_history WHERE seat_id=? ORDER BY id`, seatID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list seat charter history: %w", err)
	}
	defer rows.Close()
	var out []SeatCharterRevision
	for rows.Next() {
		var r SeatCharterRevision
		if err := rows.Scan(&r.ID, &r.SeatID, &r.Purpose, &r.Charter, &r.By, &r.At); err != nil {
			return nil, fmt.Errorf("jobstore: list seat charter history: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
