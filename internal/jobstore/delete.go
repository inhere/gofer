package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// DeleteJob permanently removes one terminal job and its job-owned records.
// The job_events row inserted after the cleanup is the durable deletion audit;
// it contains no original title, command or prompt.
func (s *Store) DeleteJob(jobID, actor string) error {
	if strings.TrimSpace(jobID) == "" {
		return errors.New("jobstore: delete: empty job id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var status, resultDir string
	if err := s.db.QueryRow(`SELECT status, COALESCE(result_dir,'') FROM jobs WHERE id=?`, jobID).Scan(&status, &resultDir); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("jobstore: delete: job %q not found", jobID)
		}
		return fmt.Errorf("jobstore: delete: read job: %w", err)
	}
	if !isTerminalJobStatus(status) {
		return fmt.Errorf("jobstore: delete: job %q is not terminal (status %s)", jobID, status)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: delete: begin: %w", err)
	}
	for _, stmt := range []string{
		`DELETE FROM interactions WHERE job_id=?`,
		`DELETE FROM event_deliveries WHERE job_id=?`,
		`DELETE FROM pty_sessions WHERE job_id=?`,
		`DELETE FROM session_job_watches WHERE job_id=?`,
		`DELETE FROM xfers WHERE job_id=?`,
		`DELETE FROM job_wakeups WHERE job_id=?`,
		`DELETE FROM job_retries WHERE source_job_id=?`,
		`DELETE FROM job_tokens WHERE job_id=?`,
	} {
		if _, err := tx.Exec(stmt, jobID); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("jobstore: delete %q: %w", jobID, err)
		}
	}
	if err := deleteCommentsTx(tx, CommentScopeJob, jobID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM job_events WHERE job_id=?`, jobID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("jobstore: delete events %q: %w", jobID, err)
	}
	if _, err := tx.Exec(`DELETE FROM jobs WHERE id=?`, jobID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("jobstore: delete job %q: %w", jobID, err)
	}
	detail, _ := json.Marshal(map[string]string{"actor": actor, "title": "已删除"})
	if _, err := tx.Exec(`INSERT INTO job_events (job_id,type,detail_json,at) VALUES (?,?,?,strftime('%s','now'))`, jobID, "job.deleted", string(detail)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("jobstore: delete audit %q: %w", jobID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("jobstore: delete commit: %w", err)
	}
	if resultDir != "" {
		if err := os.RemoveAll(resultDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("jobstore: delete result dir: %w", err)
		}
	}
	return nil
}
