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
	ID           string
	Name         string
	ScheduleType string
	CronExpr     string
	RequestJSON  string
	Enabled      int
	NextRunAt    int64
	LastRunAt    int64
	LastJobID    string
	CatchUp      int
	ProjectKey   string
	// TriggerToken is the schedule's OWN webhook secret (AUTO-02b): the token
	// `POST /v1/schedules/{id}/trigger` accepts. Empty = the webhook endpoint is not
	// enabled for this schedule. It is NOT the caller bearer and never authenticates
	// anything else.
	// CreatorCaller is the CEILING OWNER of the schedule: the caller that created it. A
	// due run (the sweeper) and a webhook trigger submit with this owner, so a timer
	// created by a restricted caller stays bounded after the creating request is long
	// gone (design 2026-10-18, 上限所有者). Empty = created before the column / by a path
	// with no caller.
	CreatorCaller string
	TriggerToken  string
	// ParamsJSON is the schedule's declared parameter list as opaque JSON (design
	// 2026-10-22 §D1), decoded by job.DecodeScheduleParams. Empty = no params.
	ParamsJSON string
	CreatedAt  int64
	UpdatedAt  int64
}

const selectScheduleCols = `SELECT id, name, COALESCE(NULLIF(schedule_type,''),'cron'), cron_expr, request_json, enabled,
  next_run_at, COALESCE(last_run_at,0), COALESCE(last_job_id,''),
  COALESCE(catch_up,0), COALESCE(project_key,''), COALESCE(trigger_token,''),
  COALESCE(creator_caller,''), COALESCE(params_json,''), created_at, updated_at
  FROM schedules`

func scanSchedule(sc rowScanner) (ScheduleRecord, error) {
	var r ScheduleRecord
	err := sc.Scan(
		&r.ID, &r.Name, &r.ScheduleType, &r.CronExpr, &r.RequestJSON, &r.Enabled,
		&r.NextRunAt, &r.LastRunAt, &r.LastJobID,
		&r.CatchUp, &r.ProjectKey, &r.TriggerToken, &r.CreatorCaller, &r.ParamsJSON, &r.CreatedAt, &r.UpdatedAt,
	)
	return r, err
}

func (s *Store) InsertSchedule(r ScheduleRecord) error {
	defer s.emit(Change{Kind: ChangeSchedule})
	if r.ID == "" {
		return errors.New("jobstore: InsertSchedule: empty schedule id")
	}
	if r.ScheduleType == "" {
		r.ScheduleType = "cron"
	}
	const q = `INSERT INTO schedules
  (id, name, schedule_type, cron_expr, request_json, enabled, next_run_at, last_run_at,
   last_job_id, catch_up, project_key, trigger_token, creator_caller, params_json, created_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q,
		r.ID, r.Name, r.ScheduleType, r.CronExpr, r.RequestJSON, r.Enabled, r.NextRunAt, r.LastRunAt,
		r.LastJobID, r.CatchUp, r.ProjectKey, r.TriggerToken, r.CreatorCaller, r.ParamsJSON, r.CreatedAt, r.UpdatedAt,
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
	defer s.emit(Change{Kind: ChangeSchedule})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM schedules WHERE id = ?`, id); err != nil {
		return fmt.Errorf("jobstore: delete schedule %q: %w", id, err)
	}
	return nil
}

func (s *Store) SetScheduleEnabled(id string, enabled int) error {
	defer s.emit(Change{Kind: ChangeSchedule})
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

// SetScheduleTriggerToken replaces a schedule's webhook token (AUTO-02b). An empty
// token DISABLES the endpoint for that schedule; the rotation and the enable/disable
// paths are the only writers.
func (s *Store) SetScheduleTriggerToken(id, token string) error {
	defer s.emit(Change{Kind: ChangeSchedule})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE schedules SET trigger_token = ?, updated_at = ? WHERE id = ?`,
		token, time.Now().Unix(), id,
	); err != nil {
		return fmt.Errorf("jobstore: set schedule %q trigger token: %w", id, err)
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
	defer s.emit(Change{Kind: ChangeSchedule})
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
	defer s.emit(Change{Kind: ChangeSchedule})
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

// ScheduleUpdate is the MUTABLE half of a schedule row (design 2026-10-22 §D4): what
// PATCH /v1/schedules/{id} may replace. The identity fields — id, project, the stored
// request's command/agent/runner — are deliberately absent: changing those is a
// different schedule, which is deleted and recreated instead.
type ScheduleUpdate struct {
	Name         string
	ScheduleType string
	CronExpr     string
	// RequestJSON replaces the stored request; the handler rewrites it only for
	// timeout_sec / the parameter env of a param-bearing schedule.
	RequestJSON string
	ParamsJSON  string
	Enabled     int
	NextRunAt   int64
}

// UpdateSchedule writes a schedule's mutable fields in one statement.
func (s *Store) UpdateSchedule(id string, u ScheduleUpdate) error {
	defer s.emit(Change{Kind: ChangeSchedule})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE schedules SET name = ?, schedule_type = ?, cron_expr = ?, request_json = ?,
   params_json = ?, enabled = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
		u.Name, u.ScheduleType, u.CronExpr, u.RequestJSON, u.ParamsJSON, u.Enabled, u.NextRunAt,
		time.Now().Unix(), id,
	); err != nil {
		return fmt.Errorf("jobstore: update schedule %q: %w", id, err)
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
