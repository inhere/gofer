package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// leaderwakes.go is the durable "wake the leader" queue (MCP-05 阶段 B, design §二.B).
//
// A member job of a plan reaching a FINISHED state writes ONE pending row here; the
// sweeper turns a due row into the leader JOB (a different agent, its own prompt) and
// records it in leader_job_id. The row is the feature's whole memory: it survives a
// serve restart (an in-process timer would lose the round silently), it is what a
// human's comment cancels, and the plan's spent round budget is derived from it — no
// extra counter to keep in sync.
//
// States: pending (armed, waiting for due_at) | firing (a sweep claimed it; a submit is
// in flight, so a second sweep must not start a second leader job) | fired
// (leader_job_id holds the job it started) | cancelled (a human spoke, or the plan was
// paused before it fired). `firing` mirrors job_wakeups' WakeupClaimPending sentinel.

// LeaderWake states (see the file comment).
const (
	LeaderWakePending   = "pending"
	LeaderWakeFiring    = "firing"
	LeaderWakeFired     = "fired"
	LeaderWakeCancelled = "cancelled"
)

// LeaderWake is one row of plan_leader_wakes. It is a neutral struct (no internal/job
// import, like every other type here): MemberStatus is the member job's terminal
// status as a plain string, and Round is the 1-based leader round this wake belongs to.
type LeaderWake struct {
	ID           string
	PlanID       string
	MemberJobID  string
	MemberStatus string
	DueAt        int64
	State        string
	Round        int
	LeaderJobID  string
	CreatedAt    int64
	CancelledBy  string
	CancelledAt  int64
}

const selectLeaderWakeCols = `SELECT id, plan_id, member_job_id, member_status, due_at, state,
  COALESCE(round,1), COALESCE(leader_job_id,''), created_at,
  COALESCE(cancelled_by,''), COALESCE(cancelled_at,0)
  FROM plan_leader_wakes`

func scanLeaderWake(sc rowScanner) (LeaderWake, error) {
	var w LeaderWake
	err := sc.Scan(&w.ID, &w.PlanID, &w.MemberJobID, &w.MemberStatus, &w.DueAt, &w.State,
		&w.Round, &w.LeaderJobID, &w.CreatedAt, &w.CancelledBy, &w.CancelledAt)
	return w, err
}

// nullString / nullInt store an absent value as SQL NULL (the columns carry COALESCE on
// the read side, so "" and 0 round-trip and a NULL is indistinguishable from either —
// which is what "no leader job yet" means here).
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// InsertLeaderWake arms one pending round. It refuses a half-built row (no id / plan /
// member job) — a row nobody can fire or cancel is worse than no row at all.
func (s *Store) InsertLeaderWake(w LeaderWake) error {
	if w.ID == "" || w.PlanID == "" || w.MemberJobID == "" {
		return errors.New("jobstore: InsertLeaderWake needs id, plan_id and member_job_id")
	}
	if w.State == "" {
		w.State = LeaderWakePending
	}
	if w.Round <= 0 {
		w.Round = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO plan_leader_wakes
  (id, plan_id, member_job_id, member_status, due_at, state, round, leader_job_id, created_at, cancelled_by, cancelled_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		w.ID, w.PlanID, w.MemberJobID, w.MemberStatus, w.DueAt, w.State, w.Round,
		nullString(w.LeaderJobID), w.CreatedAt, nullString(w.CancelledBy), nullInt(w.CancelledAt))
	if err != nil {
		return fmt.Errorf("jobstore: insert leader wake: %w", err)
	}
	return nil
}

// GetLeaderWake reads one row; ok=false (nil error) when the id is unknown.
func (s *Store) GetLeaderWake(id string) (LeaderWake, bool, error) {
	w, err := scanLeaderWake(s.db.QueryRow(selectLeaderWakeCols+" WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return LeaderWake{}, false, nil
	}
	if err != nil {
		return LeaderWake{}, false, fmt.Errorf("jobstore: get leader wake: %w", err)
	}
	return w, true, nil
}

// ListLeaderWakesForPlan returns a plan's rounds oldest-first (the order they were
// armed in, which is how the plan reads them and how the tests count them).
func (s *Store) ListLeaderWakesForPlan(planID string) ([]LeaderWake, error) {
	rows, err := s.db.Query(selectLeaderWakeCols+" WHERE plan_id=? ORDER BY created_at, id", planID)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list leader wakes: %w", err)
	}
	defer rows.Close()
	var out []LeaderWake
	for rows.Next() {
		w, err := scanLeaderWake(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan leader wake: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DueLeaderWakes returns the armed rounds that have come due at now. Only `pending`
// rows are returned: a `firing` one belongs to a sweep that is mid-submit, and a
// `fired`/`cancelled` one is history.
func (s *Store) DueLeaderWakes(now int64) ([]LeaderWake, error) {
	rows, err := s.db.Query(selectLeaderWakeCols+
		" WHERE state=? AND due_at<=? ORDER BY due_at, id", LeaderWakePending, now)
	if err != nil {
		return nil, fmt.Errorf("jobstore: due leader wakes: %w", err)
	}
	defer rows.Close()
	var out []LeaderWake
	for rows.Next() {
		w, err := scanLeaderWake(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan due leader wake: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// LeaderRoundsSpent reports how many leader rounds a plan has already used: the rows
// that started a leader job (fired) plus the one a sweep is submitting right now
// (firing). Cancelled and still-pending rows are NOT rounds — nothing ran for them.
func (s *Store) LeaderRoundsSpent(planID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM plan_leader_wakes WHERE plan_id=? AND state IN (?,?)`,
		planID, LeaderWakeFired, LeaderWakeFiring).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("jobstore: count leader rounds: %w", err)
	}
	return n, nil
}

// LeaderWakeArmedForMember reports whether a member job already has a live (not
// cancelled) round. The terminal path consults it so a job that reaches finish twice —
// a re-run, or the accept/reject path after a park — arms ONE round, not two.
func (s *Store) LeaderWakeArmedForMember(memberJobID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM plan_leader_wakes WHERE member_job_id=? AND state<>?`,
		memberJobID, LeaderWakeCancelled).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("jobstore: count leader wakes for member: %w", err)
	}
	return n > 0, nil
}

// LatestLeaderJob returns the id of the leader job a plan's most recent fired round
// started ("" when there is none) — the link the plan view shows.
func (s *Store) LatestLeaderJob(planID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT COALESCE(leader_job_id,'') FROM plan_leader_wakes
  WHERE plan_id=? AND state=? ORDER BY created_at DESC, id DESC LIMIT 1`, planID, LeaderWakeFired).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("jobstore: latest leader job: %w", err)
	}
	return id, nil
}

// ClaimLeaderWake takes a due round for THIS sweep, so two sweeps (or a sweep and a
// serve restart) can never start two leader jobs for one member terminal. It is a
// conditional UPDATE — whoever flips pending→firing owns the round.
func (s *Store) ClaimLeaderWake(id string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_leader_wakes SET state=? WHERE id=? AND state=?`,
		LeaderWakeFiring, id, LeaderWakePending)
	if err != nil {
		return false, fmt.Errorf("jobstore: claim leader wake: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: claim leader wake rows: %w", err)
	}
	return n > 0, nil
}

// SetLeaderWakeFired records the job a claimed round started, and the round number that
// was decided when it fired (two members can arm inside one wake window, so the number
// is settled at fire time). It CASes on `firing`, so a round cancelled in between is not
// overwritten by a late submit.
func (s *Store) SetLeaderWakeFired(id, jobID string, round int) (bool, error) {
	if round <= 0 {
		round = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_leader_wakes SET state=?, leader_job_id=?, round=? WHERE id=? AND state=?`,
		LeaderWakeFired, jobID, round, id, LeaderWakeFiring)
	if err != nil {
		return false, fmt.Errorf("jobstore: set leader wake fired: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: set leader wake fired rows: %w", err)
	}
	return n > 0, nil
}

// CancelLeaderWakesForPlan cancels every round of a plan that has not started a leader
// job yet (pending or firing) and reports how many it stopped. A `fired` round is
// history and is left alone: the leader job it started is already running, and
// cancelling the row would neither stop it nor explain it. by is the human who spoke.
// Skips (state cancelled with a reason instead of a person) go through the same write.
func (s *Store) CancelLeaderWakesForPlan(planID, by string, now int64) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_leader_wakes SET state=?, cancelled_by=?, cancelled_at=?
  WHERE plan_id=? AND state IN (?,?)`,
		LeaderWakeCancelled, by, now, planID, LeaderWakePending, LeaderWakeFiring)
	if err != nil {
		return 0, fmt.Errorf("jobstore: cancel leader wakes: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("jobstore: cancel leader wakes rows: %w", err)
	}
	return n, nil
}

// CancelLeaderWake cancels ONE round by id (the sweep's plan-paused path). It is a
// conditional UPDATE, so a round that raced into `firing`/`fired` is not touched.
func (s *Store) CancelLeaderWake(id, by string, now int64) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE plan_leader_wakes SET state=?, cancelled_by=?, cancelled_at=?
  WHERE id=? AND state=?`,
		LeaderWakeCancelled, by, now, id, LeaderWakePending)
	if err != nil {
		return false, fmt.Errorf("jobstore: cancel leader wake: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: cancel leader wake rows: %w", err)
	}
	return n > 0, nil
}
