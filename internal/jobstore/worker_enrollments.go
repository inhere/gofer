package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Worker enrollment states (design 2026-10-17-worker-self-enrollment, 「名词」).
const (
	EnrollPending    = "pending"
	EnrollAccepting  = "accepting"
	EnrollAccepted   = "accepted"
	EnrollCollected  = "collected"
	EnrollRejected   = "rejected"
	EnrollExpired    = "expired"
	EnrollSuperseded = "superseded"
	EnrollCancelled  = "cancelled"
)

// ErrWorkerEnrollmentNotFound is returned when no enrollment row has the id.
var ErrWorkerEnrollmentNotFound = errors.New("jobstore: worker enrollment not found")

// ErrWorkerEnrollmentCap is returned by InsertWorkerEnrollment when the global
// pending cap is already reached (nothing was written).
var ErrWorkerEnrollmentCap = errors.New("jobstore: too many pending worker enrollments")

// WorkerEnrollment is one worker_enrollments row. Times are unix seconds. The row
// never holds a worker token, only its fingerprint.
type WorkerEnrollment struct {
	ID             string
	State          string
	SecretHash     string
	PairingCode    string
	CodeFailures   int
	SecretFailures int
	RequestedID    string
	WorkerID       string
	LabelsJSON     string
	ProjectsJSON   string
	HostJSON       string
	Source         string
	TokenFP        string
	CreatedAt      int64
	DecideUntil    int64
	CollectUntil   int64
	DecidedAt      int64
	DecidedBy      string
	Reason         string
	CollectedAt    int64
}

const workerEnrollCols = `id, state, secret_hash, pairing_code, code_failures, secret_failures,
  requested_id, worker_id, labels_json, projects_json, host_json, source, token_fp,
  created_at, decide_until, collect_until, decided_at, decided_by, reason, collected_at`

func scanWorkerEnrollment(r rowScanner) (WorkerEnrollment, error) {
	var e WorkerEnrollment
	err := r.Scan(&e.ID, &e.State, &e.SecretHash, &e.PairingCode, &e.CodeFailures, &e.SecretFailures,
		&e.RequestedID, &e.WorkerID, &e.LabelsJSON, &e.ProjectsJSON, &e.HostJSON, &e.Source, &e.TokenFP,
		&e.CreatedAt, &e.DecideUntil, &e.CollectUntil, &e.DecidedAt, &e.DecidedBy, &e.Reason, &e.CollectedAt)
	return e, err
}

// InsertWorkerEnrollment writes e (state forced to pending) in one transaction. First
// every pending row of the same non-empty source is set to superseded (their ids are
// returned), then, if maxPending > 0 and the number of still-open pending rows
// (decide_until > e.CreatedAt) has reached it, the whole transaction rolls back with
// ErrWorkerEnrollmentCap so a rejected request does not supersede anything.
func (s *Store) InsertWorkerEnrollment(e WorkerEnrollment, maxPending int) (superseded []string, err error) {
	if e.ID == "" || e.SecretHash == "" || e.PairingCode == "" {
		return nil, errors.New("jobstore: InsertWorkerEnrollment: id, secret hash and pairing code are required")
	}
	e.State = EnrollPending
	s.writeMu.Lock()
	tx, err := s.db.Begin()
	if err != nil {
		s.writeMu.Unlock()
		return nil, fmt.Errorf("jobstore: begin worker enrollment insert: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
		s.writeMu.Unlock()
		if err == nil {
			s.emit(Change{Kind: ChangeWorkerEnrollment, ID: e.ID, Status: EnrollPending})
			for _, id := range superseded {
				s.emit(Change{Kind: ChangeWorkerEnrollment, ID: id, Status: EnrollSuperseded})
			}
		}
	}()
	if e.Source != "" {
		rows, qerr := tx.Query(`SELECT id FROM worker_enrollments WHERE source=? AND state=?`, e.Source, EnrollPending)
		if qerr != nil {
			return nil, fmt.Errorf("jobstore: list same-source enrollments: %w", qerr)
		}
		for rows.Next() {
			var id string
			if serr := rows.Scan(&id); serr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("jobstore: scan same-source enrollment: %w", serr)
			}
			superseded = append(superseded, id)
		}
		if rerr := rows.Err(); rerr != nil {
			_ = rows.Close()
			return nil, rerr
		}
		_ = rows.Close()
		for _, id := range superseded {
			if _, xerr := tx.Exec(`UPDATE worker_enrollments SET state=?, decided_at=?, reason=? WHERE id=? AND state=?`,
				EnrollSuperseded, e.CreatedAt, "superseded by "+e.ID, id, EnrollPending); xerr != nil {
				return nil, fmt.Errorf("jobstore: supersede enrollment %q: %w", id, xerr)
			}
		}
	}
	if maxPending > 0 {
		var n int
		if qerr := tx.QueryRow(`SELECT COUNT(*) FROM worker_enrollments WHERE state=? AND decide_until>?`,
			EnrollPending, e.CreatedAt).Scan(&n); qerr != nil {
			return nil, fmt.Errorf("jobstore: count pending enrollments: %w", qerr)
		}
		if n >= maxPending {
			return nil, ErrWorkerEnrollmentCap
		}
	}
	if _, xerr := tx.Exec(`INSERT INTO worker_enrollments (`+workerEnrollCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.State, e.SecretHash, e.PairingCode, e.CodeFailures, e.SecretFailures,
		e.RequestedID, e.WorkerID, e.LabelsJSON, e.ProjectsJSON, e.HostJSON, e.Source, e.TokenFP,
		e.CreatedAt, e.DecideUntil, e.CollectUntil, e.DecidedAt, e.DecidedBy, e.Reason, e.CollectedAt); xerr != nil {
		return nil, fmt.Errorf("jobstore: insert worker enrollment: %w", xerr)
	}
	if cerr := tx.Commit(); cerr != nil {
		return nil, fmt.Errorf("jobstore: commit worker enrollment insert: %w", cerr)
	}
	committed = true
	return superseded, nil
}

// GetWorkerEnrollment returns the row, or ErrWorkerEnrollmentNotFound.
func (s *Store) GetWorkerEnrollment(id string) (WorkerEnrollment, error) {
	e, err := scanWorkerEnrollment(s.db.QueryRow(`SELECT `+workerEnrollCols+` FROM worker_enrollments WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkerEnrollment{}, ErrWorkerEnrollmentNotFound
	}
	if err != nil {
		return WorkerEnrollment{}, fmt.Errorf("jobstore: get worker enrollment %q: %w", id, err)
	}
	return e, nil
}

func (s *Store) queryWorkerEnrollments(q string, args ...any) ([]WorkerEnrollment, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list worker enrollments: %w", err)
	}
	defer rows.Close()
	out := make([]WorkerEnrollment, 0)
	for rows.Next() {
		e, err := scanWorkerEnrollment(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan worker enrollment: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListWorkerEnrollmentsAdmin is the admin view: every open row (pending, accepting,
// accepted) plus ended ones created at or after endedSince, newest first. Callers must
// not expose SecretHash / TokenFP.
func (s *Store) ListWorkerEnrollmentsAdmin(endedSince int64) ([]WorkerEnrollment, error) {
	return s.queryWorkerEnrollments(`SELECT `+workerEnrollCols+` FROM worker_enrollments
  WHERE state IN (?,?,?) OR created_at>=? ORDER BY created_at DESC, id DESC`,
		EnrollPending, EnrollAccepting, EnrollAccepted, endedSince)
}

// ListWorkerEnrollmentsByState returns the rows in any of states, oldest first
// (sweep / reconcile seam).
func (s *Store) ListWorkerEnrollmentsByState(states ...string) ([]WorkerEnrollment, error) {
	if len(states) == 0 {
		return nil, nil
	}
	args := make([]any, len(states))
	for i, st := range states {
		args[i] = st
	}
	return s.queryWorkerEnrollments(`SELECT `+workerEnrollCols+` FROM worker_enrollments
  WHERE state IN (`+strings.TrimSuffix(strings.Repeat("?,", len(states)), ",")+`) ORDER BY created_at, id`, args...)
}

// FindAcceptedWorkerEnrollment returns the newest accepted row for workerID
// (ErrWorkerEnrollmentNotFound when none).
func (s *Store) FindAcceptedWorkerEnrollment(workerID string) (WorkerEnrollment, error) {
	e, err := scanWorkerEnrollment(s.db.QueryRow(`SELECT `+workerEnrollCols+` FROM worker_enrollments
  WHERE worker_id=? AND state=? ORDER BY created_at DESC, id DESC LIMIT 1`, workerID, EnrollAccepted))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkerEnrollment{}, ErrWorkerEnrollmentNotFound
	}
	if err != nil {
		return WorkerEnrollment{}, fmt.Errorf("jobstore: find accepted enrollment: %w", err)
	}
	return e, nil
}

// WorkerEnrollmentClaim carries the decision made at pending -> accepting.
type WorkerEnrollmentClaim struct {
	ID           string
	Now          int64
	WorkerID     string
	LabelsJSON   string
	ProjectsJSON string
	TokenFP      string
	DecidedBy    string
}

// ClaimWorkerEnrollment is the conditional pending -> accepting placeholder: it wins
// only while the row is still pending and decide_until > Now, so an accept and an
// expiry race on the same row resolve to exactly one winner.
func (s *Store) ClaimWorkerEnrollment(c WorkerEnrollmentClaim) (bool, error) {
	return s.execEnrollTransition(c.ID, EnrollAccepting, `UPDATE worker_enrollments
  SET state=?, worker_id=?, labels_json=?, projects_json=?, token_fp=?, decided_by=?, decided_at=?
  WHERE id=? AND state=? AND decide_until>?`,
		EnrollAccepting, c.WorkerID, c.LabelsJSON, c.ProjectsJSON, c.TokenFP, c.DecidedBy, c.Now,
		c.ID, EnrollPending, c.Now)
}

// FinishWorkerEnrollmentAccept is accepting -> accepted with the collect deadline.
func (s *Store) FinishWorkerEnrollmentAccept(id string, collectUntil int64) (bool, error) {
	return s.execEnrollTransition(id, EnrollAccepted,
		`UPDATE worker_enrollments SET state=?, collect_until=? WHERE id=? AND state=?`,
		EnrollAccepted, collectUntil, id, EnrollAccepting)
}

// RevertWorkerEnrollmentAccept is accepting -> pending after a failed registration;
// it clears the decision so the row looks untouched.
func (s *Store) RevertWorkerEnrollmentAccept(id string) (bool, error) {
	return s.execEnrollTransition(id, EnrollPending, `UPDATE worker_enrollments
  SET state=?, worker_id='', labels_json='', projects_json='', token_fp='', decided_by='', decided_at=0
  WHERE id=? AND state=?`, EnrollPending, id, EnrollAccepting)
}

// TransitionWorkerEnrollment is the generic conditional move from any of from to `to`
// (pending -> rejected / cancelled / superseded / expired, accepted -> collected /
// expired, ...). It stamps decided_at (collected_at for collected), and decidedBy /
// reason when non-empty. It reports whether this call won.
func (s *Store) TransitionWorkerEnrollment(id string, from []string, to string, now int64, decidedBy, reason string) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("jobstore: TransitionWorkerEnrollment: empty from set")
	}
	set := `state=?, decided_at=?`
	args := []any{to, now}
	if to == EnrollCollected {
		set = `state=?, collected_at=?`
	}
	set += `, decided_by=CASE WHEN ?<>'' THEN ? ELSE decided_by END, reason=CASE WHEN ?<>'' THEN ? ELSE reason END`
	args = append(args, decidedBy, decidedBy, reason, reason, id)
	for _, f := range from {
		args = append(args, f)
	}
	return s.execEnrollTransition(id, to, `UPDATE worker_enrollments SET `+set+
		` WHERE id=? AND state IN (`+strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")+`)`, args...)
}

func (s *Store) execEnrollTransition(id, to, q string, args ...any) (bool, error) {
	s.writeMu.Lock()
	res, err := s.db.Exec(q, args...)
	s.writeMu.Unlock()
	if err != nil {
		return false, fmt.Errorf("jobstore: worker enrollment %q -> %s: %w", id, to, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: worker enrollment %q rows: %w", id, err)
	}
	if n == 1 {
		s.emit(Change{Kind: ChangeWorkerEnrollment, ID: id, Status: to})
	}
	return n == 1, nil
}

// BumpWorkerEnrollmentCodeFailures atomically adds one wrong pairing code and returns
// the new total.
func (s *Store) BumpWorkerEnrollmentCodeFailures(id string) (int, error) {
	return s.bumpEnrollCounter(id, "code_failures")
}

// BumpWorkerEnrollmentSecretFailures atomically adds one wrong enroll secret and
// returns the new total.
func (s *Store) BumpWorkerEnrollmentSecretFailures(id string) (int, error) {
	return s.bumpEnrollCounter(id, "secret_failures")
}

func (s *Store) bumpEnrollCounter(id, col string) (int, error) {
	s.writeMu.Lock()
	var n int
	tx, err := s.db.Begin()
	if err != nil {
		s.writeMu.Unlock()
		return 0, fmt.Errorf("jobstore: begin enrollment counter: %w", err)
	}
	res, err := tx.Exec(`UPDATE worker_enrollments SET `+col+`=`+col+`+1 WHERE id=?`, id)
	if err == nil {
		var k int64
		if k, err = res.RowsAffected(); err == nil && k == 0 {
			err = ErrWorkerEnrollmentNotFound
		}
	}
	if err == nil {
		err = tx.QueryRow(`SELECT `+col+` FROM worker_enrollments WHERE id=?`, id).Scan(&n)
	}
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	s.writeMu.Unlock()
	if err != nil {
		if errors.Is(err, ErrWorkerEnrollmentNotFound) {
			return 0, err
		}
		return 0, fmt.Errorf("jobstore: bump enrollment counter %q: %w", id, err)
	}
	s.emit(Change{Kind: ChangeWorkerEnrollment, ID: id})
	return n, nil
}

// DeleteEndedWorkerEnrollmentsBefore removes rows in an ended state (everything but
// pending / accepting / accepted) created before cutoff and returns how many.
func (s *Store) DeleteEndedWorkerEnrollmentsBefore(cutoff int64) (int, error) {
	s.writeMu.Lock()
	res, err := s.db.Exec(`DELETE FROM worker_enrollments WHERE state NOT IN (?,?,?) AND created_at<?`,
		EnrollPending, EnrollAccepting, EnrollAccepted, cutoff)
	s.writeMu.Unlock()
	if err != nil {
		return 0, fmt.Errorf("jobstore: prune worker enrollments: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.emit(Change{Kind: ChangeWorkerEnrollment})
	}
	return int(n), nil
}
