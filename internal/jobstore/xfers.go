package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// XferRecord is the SQLite-persisted journal row of one file transfer (XFER-01
// design §一.2). The payload bytes are NOT here: they live in the staging area
// (internal/xfer's Store). jobstore owns the row because the row is what the
// HTTP API lists/filters and what the prune loop expires; the transfer STATE
// vocabulary lives in internal/xfer (this package only stores the string).
type XferRecord struct {
	ID         string
	Op         string // put | get
	Runner     string // worker id, or `local`/`server` for the server's own machine
	ProjectKey string
	Path       string // project-relative path on the EXECUTING machine
	Size       int64
	SHA256     string
	State      string // staged | dispatched | done | failed | expired
	Error      string
	CallerID   string
	Force      int // 1 = overwrite an existing destination
	// JobID is RESERVED for X2 (`job run --upload/--collect`): the job a transfer
	// belongs to. X1 always leaves it empty.
	JobID      string
	CreatedAt  int64
	FinishedAt int64
	ExpiresAt  int64
}

// XferFilter selects rows for ListXfers. Zero values mean "no filter"; Limit<=0
// falls back to DefaultListLimit.
type XferFilter struct {
	State  string
	Runner string
	Limit  int
}

const selectXferCols = `SELECT id, op, runner, project_key, path, size, COALESCE(sha256,''), state,
  COALESCE(error,''), COALESCE(caller_id,''), COALESCE(force,0), COALESCE(job_id,''),
  created_at, COALESCE(finished_at,0), COALESCE(expires_at,0)
  FROM xfers`

func scanXfer(sc rowScanner) (XferRecord, error) {
	var r XferRecord
	err := sc.Scan(
		&r.ID, &r.Op, &r.Runner, &r.ProjectKey, &r.Path, &r.Size, &r.SHA256, &r.State,
		&r.Error, &r.CallerID, &r.Force, &r.JobID,
		&r.CreatedAt, &r.FinishedAt, &r.ExpiresAt,
	)
	return r, err
}

// InsertXfer records a new transfer row. It fails when the id or an essential
// field is empty, so a half-built row can never be stored.
func (s *Store) InsertXfer(r XferRecord) error {
	if r.ID == "" {
		return errors.New("jobstore: InsertXfer: empty xfer id")
	}
	if r.Op == "" || r.Runner == "" || r.ProjectKey == "" || r.Path == "" || r.State == "" {
		return fmt.Errorf("jobstore: InsertXfer %q: missing op/runner/project/path/state", r.ID)
	}
	const q = `INSERT INTO xfers
  (id, op, runner, project_key, path, size, sha256, state, error, caller_id, force, job_id,
   created_at, finished_at, expires_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q,
		r.ID, r.Op, r.Runner, r.ProjectKey, r.Path, r.Size, r.SHA256, r.State, r.Error, r.CallerID,
		r.Force, r.JobID, r.CreatedAt, r.FinishedAt, r.ExpiresAt,
	); err != nil {
		return fmt.Errorf("jobstore: insert xfer %q: %w", r.ID, err)
	}
	return nil
}

// GetXfer returns one transfer row; ok=false when the id is unknown.
func (s *Store) GetXfer(id string) (XferRecord, bool, error) {
	r, err := scanXfer(s.db.QueryRow(selectXferCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return XferRecord{}, false, nil
	}
	if err != nil {
		return XferRecord{}, false, fmt.Errorf("jobstore: get xfer %q: %w", id, err)
	}
	return r, true, nil
}

// ListXfers returns transfer rows newest-first, optionally filtered by state
// and/or runner.
func (s *Store) ListXfers(f XferFilter) ([]XferRecord, error) {
	q := selectXferCols
	var args []any
	where := func(cond string, arg any) {
		if len(args) == 0 {
			q += " WHERE "
		} else {
			q += " AND "
		}
		q += cond
		args = append(args, arg)
	}
	if f.State != "" {
		where("state = ?", f.State)
	}
	if f.Runner != "" {
		where("runner = ?", f.Runner)
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	args = append(args, limit)
	return s.listXfers(q, args...)
}

// UpdateXferState moves a transfer to a terminal (or dispatched) state. errMsg
// and finishedAt are optional: an empty errMsg / zero finishedAt leaves the
// stored values untouched, so a state move never has to invent a timestamp.
func (s *Store) UpdateXferState(id, state, errMsg string, finishedAt int64) error {
	if state == "" {
		return errors.New("jobstore: UpdateXferState: empty state")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var err error
	switch {
	case errMsg != "" && finishedAt > 0:
		_, err = s.db.Exec(`UPDATE xfers SET state = ?, error = ?, finished_at = ? WHERE id = ?`, state, errMsg, finishedAt, id)
	case errMsg != "":
		_, err = s.db.Exec(`UPDATE xfers SET state = ?, error = ? WHERE id = ?`, state, errMsg, id)
	case finishedAt > 0:
		_, err = s.db.Exec(`UPDATE xfers SET state = ?, finished_at = ? WHERE id = ?`, state, finishedAt, id)
	default:
		_, err = s.db.Exec(`UPDATE xfers SET state = ? WHERE id = ?`, state, id)
	}
	if err != nil {
		return fmt.Errorf("jobstore: update xfer %q state: %w", id, err)
	}
	return nil
}

// SetXferContent records the byte size + sha256 of the transfer's staged payload
// (a get's result arrives only once the worker has uploaded it).
func (s *Store) SetXferContent(id string, size int64, sha256 string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE xfers SET size = ?, sha256 = ? WHERE id = ?`, size, sha256, id); err != nil {
		return fmt.Errorf("jobstore: set xfer %q content: %w", id, err)
	}
	return nil
}

// DeleteXfer removes a transfer row (the `xfer rm` path; the staging directory is
// removed by the caller).
func (s *Store) DeleteXfer(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM xfers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("jobstore: delete xfer %q: %w", id, err)
	}
	return nil
}

// ExpiredXfers returns the transfers whose TTL has passed (expires_at <= now).
// Rows already marked expired are excluded so the sweep never re-processes them.
func (s *Store) ExpiredXfers(now int64) ([]XferRecord, error) {
	return s.listXfers(
		selectXferCols+` WHERE COALESCE(expires_at,0) > 0 AND expires_at <= ? AND state != 'expired'
     ORDER BY expires_at ASC, id ASC`,
		now,
	)
}

func (s *Store) listXfers(q string, args ...any) ([]XferRecord, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list xfers: %w", err)
	}
	defer rows.Close()

	out := make([]XferRecord, 0)
	for rows.Next() {
		r, scanErr := scanXfer(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan xfer row: %w", scanErr)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list xfers rows: %w", err)
	}
	return out, nil
}
