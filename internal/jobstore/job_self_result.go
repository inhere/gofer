package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrSelfResultExists is returned by SetJobSelfResult when the job already carries a
// self-reported result: a job reports its own outcome once.
var ErrSelfResultExists = errors.New("jobstore: job already reported a result")

// migrateJobSelfResult adds the columns that hold the outcome a job's agent reports about
// itself (gofer_submit_result). They live beside, not inside, the job's own result
// document (jobs.result_json), and are written only by SetJobSelfResult, never by the
// general job upsert. Additive; rows written before read back as "no report".
func (s *Store) migrateJobSelfResult(cols map[string]struct{}) error {
	for _, c := range []struct{ name, ddl string }{
		{"result_decision", "result_decision TEXT"},
		{"result_reason", "result_reason TEXT"},
		{"result_report_json", "result_report_json TEXT"},
		{"result_attempt", "result_attempt INTEGER NOT NULL DEFAULT 0"},
	} {
		if _, ok := cols[c.name]; ok {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE jobs ADD COLUMN " + c.ddl); err != nil {
			return fmt.Errorf("jobstore: migrate add %s: %w", c.name, err)
		}
	}
	return nil
}

// SetJobSelfResult records the outcome a job reported about itself. reportJSON is the
// full submitted document (decision, reason, action_required, findings). The write is a
// single conditional UPDATE: only a job with no report yet is changed, so a second
// submission for the same job (attempt) returns ErrSelfResultExists. The attempt stored is
// the job's own attempt number (a workflow step retry is a separate job row).
func (s *Store) SetJobSelfResult(jobID, decision, reason, reportJSON string) (attempt int, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE jobs SET result_decision=?, result_reason=?, result_report_json=?,
  result_attempt=MAX(COALESCE(attempt,1),1) WHERE id=? AND COALESCE(result_decision,'')=''`,
		decision, reason, reportJSON, jobID)
	if err != nil {
		return 0, fmt.Errorf("jobstore: set job self result: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var cur sql.NullString
		qerr := s.db.QueryRow(`SELECT result_decision FROM jobs WHERE id=?`, jobID).Scan(&cur)
		if errors.Is(qerr, sql.ErrNoRows) {
			return 0, fmt.Errorf("jobstore: set job self result: job %q not found", jobID)
		}
		if qerr != nil {
			return 0, fmt.Errorf("jobstore: set job self result: %w", qerr)
		}
		return 0, ErrSelfResultExists
	}
	if qerr := s.db.QueryRow(`SELECT result_attempt FROM jobs WHERE id=?`, jobID).Scan(&attempt); qerr != nil {
		return 0, fmt.Errorf("jobstore: read job self result attempt: %w", qerr)
	}
	return attempt, nil
}
