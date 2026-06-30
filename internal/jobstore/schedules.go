package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// ScheduleRecord is the SQLite-persisted projection of one cron schedule
// (AUTO-02). RequestJSON holds the marshalled JobRequest; jobstore keeps it as an
// opaque string to avoid importing internal/job.
type ScheduleRecord struct {
	ID          string
	Name        string
	CronExpr    string
	RequestJSON string
	Enabled     int
	NextRunAt   int64
	LastRunAt   int64
	LastJobID   string
	CatchUp     int
	ProjectKey  string
	CreatedAt   int64
	UpdatedAt   int64
}

const selectScheduleCols = `SELECT id, name, cron_expr, request_json, enabled,
  next_run_at, COALESCE(last_run_at,0), COALESCE(last_job_id,''),
  COALESCE(catch_up,0), COALESCE(project_key,''), created_at, updated_at
  FROM schedules`

func scanSchedule(sc rowScanner) (ScheduleRecord, error) {
	var r ScheduleRecord
	err := sc.Scan(
		&r.ID, &r.Name, &r.CronExpr, &r.RequestJSON, &r.Enabled,
		&r.NextRunAt, &r.LastRunAt, &r.LastJobID,
		&r.CatchUp, &r.ProjectKey, &r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

func (s *Store) InsertSchedule(r ScheduleRecord) error {
	if r.ID == "" {
		return errors.New("jobstore: InsertSchedule: empty schedule id")
	}
	const q = `INSERT INTO schedules
  (id, name, cron_expr, request_json, enabled, next_run_at, last_run_at,
   last_job_id, catch_up, project_key, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q,
		r.ID, r.Name, r.CronExpr, r.RequestJSON, r.Enabled, r.NextRunAt, r.LastRunAt,
		r.LastJobID, r.CatchUp, r.ProjectKey, r.CreatedAt, r.UpdatedAt,
	); err != nil {
		return fmt.Errorf("jobstore: insert schedule %q: %w", r.ID, err)
	}
	return nil
}

func (s *Store) GetSchedule(id string) (ScheduleRecord, bool, error) {
	r, err := scanSchedule(s.db.QueryRow(selectScheduleCols+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return ScheduleRecord{}, false, nil
	}
	if err != nil {
		return ScheduleRecord{}, false, fmt.Errorf("jobstore: get schedule %q: %w", id, err)
	}
	return r, true, nil
}

func (s *Store) ListSchedules(projectFilter string, enabledOnly bool) ([]ScheduleRecord, error) {
	q := selectScheduleCols
	var args []any
	if projectFilter != "" || enabledOnly {
		q += " WHERE "
	}
	if projectFilter != "" {
		q += "project_key = ?"
		args = append(args, projectFilter)
		if enabledOnly {
			q += " AND "
		}
	}
	if enabledOnly {
		q += "enabled = 1"
	}
	q += " ORDER BY created_at DESC, id DESC"
	return s.listSchedules(q, args...)
}

func (s *Store) DeleteSchedule(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM schedules WHERE id = ?`, id); err != nil {
		return fmt.Errorf("jobstore: delete schedule %q: %w", id, err)
	}
	return nil
}

func (s *Store) SetScheduleEnabled(id string, enabled int) error {
	now := time.Now().Unix()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE schedules SET enabled = ?, updated_at = ? WHERE id = ?`,
		enabled, now, id,
	); err != nil {
		return fmt.Errorf("jobstore: set schedule %q enabled: %w", id, err)
	}
	return nil
}

func (s *Store) DueSchedules(now int64) ([]ScheduleRecord, error) {
	return s.listSchedules(
		selectScheduleCols+` WHERE enabled = 1 AND next_run_at > 0 AND next_run_at <= ? ORDER BY next_run_at ASC, id ASC`,
		now,
	)
}

func (s *Store) AdvanceSchedule(id string, oldNext, newNext, now int64) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE schedules SET next_run_at = ?, last_run_at = ?, updated_at = ? WHERE id = ? AND enabled = 1 AND next_run_at = ?`,
		newNext, now, now, id, oldNext,
	)
	if err != nil {
		return false, fmt.Errorf("jobstore: advance schedule %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: advance schedule %q rows: %w", id, err)
	}
	return n == 1, nil
}

func (s *Store) SetScheduleLastJob(id, jobID string) error {
	now := time.Now().Unix()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE schedules SET last_job_id = ?, updated_at = ? WHERE id = ?`,
		jobID, now, id,
	); err != nil {
		return fmt.Errorf("jobstore: set schedule %q last job: %w", id, err)
	}
	return nil
}

func (s *Store) listSchedules(q string, args ...any) ([]ScheduleRecord, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list schedules: %w", err)
	}
	defer rows.Close()

	out := make([]ScheduleRecord, 0)
	for rows.Next() {
		r, scanErr := scanSchedule(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("jobstore: scan schedule row: %w", scanErr)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list schedules rows: %w", err)
	}
	return out, nil
}

// NextCronRun parses a standard five-field cron expression and returns the next
// unix-second run strictly after after.
func NextCronRun(expr string, after time.Time) (int64, error) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return 0, fmt.Errorf("jobstore: parse cron %q: %w", expr, err)
	}
	return sched.Next(after).Unix(), nil
}
