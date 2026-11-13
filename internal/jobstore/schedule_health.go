package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Schedule overlap policies (schedules.overlap).
const (
	// ScheduleOverlapSkip skips a run while the previous run's job has not finished.
	ScheduleOverlapSkip = "skip"
	// ScheduleOverlapAllow submits regardless of a still-running previous job.
	ScheduleOverlapAllow = "allow"
)

// Schedule context modes (schedules.context): whether each run is told which run of the
// schedule it is and how the previous one ended.
const (
	ScheduleContextOn  = "on"
	ScheduleContextOff = "off"
)

// Schedule paused reasons (schedules.paused_reason). Empty = stopped by hand.
const (
	SchedulePausedFailures = "failures"
	SchedulePausedMaxRuns  = "max_runs"
)

// Job schedule triggers (jobs.schedule_trigger): how a schedule started a job.
const (
	ScheduleTriggerTimer   = "timer"
	ScheduleTriggerRunNow  = "run_now"
	ScheduleTriggerWebhook = "webhook"
)

// scheduleActiveStatuses are the job statuses that count as "not finished yet" for the
// overlap check. needs_review is deliberately absent: the job's work is done, only a
// person's acceptance is pending. The strings mirror the job package's Status*
// constants (jobstore cannot import it); TestScheduleActiveStatuses pins the set.
var scheduleActiveStatuses = []string{
	"queued", "running", "recovering", "waiting_dir", "awaiting_input",
	"awaiting_approval", "pending_interaction", "paused_for_update",
}

// ScheduleActiveStatuses returns a copy of the statuses the overlap check treats as
// unfinished.
func ScheduleActiveStatuses() []string {
	return append([]string(nil), scheduleActiveStatuses...)
}

// migrateScheduleHealth adds the run-health columns of schedules and the schedule
// provenance columns of jobs (additive, idempotent; design 2026-11-12 §1). Rows that
// predate the columns keep their old behaviour: overlap=allow, no failure pause, no
// run limit. Newly created schedules get their defaults from the create path.
func (s *Store) migrateScheduleHealth() error {
	cols, err := s.tableColumns("schedules")
	if err != nil {
		return err
	}
	for _, c := range []struct{ name, ddl string }{
		{"overlap", "overlap TEXT NOT NULL DEFAULT 'allow'"},
		{"max_runs", "max_runs INTEGER NOT NULL DEFAULT 0"},
		{"run_count", "run_count INTEGER NOT NULL DEFAULT 0"},
		{"max_failures", "max_failures INTEGER NOT NULL DEFAULT 0"},
		{"consecutive_failures", "consecutive_failures INTEGER NOT NULL DEFAULT 0"},
		{"paused_reason", "paused_reason TEXT NOT NULL DEFAULT ''"},
		// Pre-run check (design 2026-11-12 §2): the opaque precheck JSON ("" = none) and the
		// fingerprint of the output of the last check whose run was submitted.
		{"precheck_json", "precheck_json TEXT NOT NULL DEFAULT ''"},
		{"precheck_fingerprint", "precheck_fingerprint TEXT NOT NULL DEFAULT ''"},
		// Cross-run context (design 2026-11-12 §3): rows that predate it stay off.
		{"context", "context TEXT NOT NULL DEFAULT 'off'"},
	} {
		if _, ok := cols[c.name]; ok {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE schedules ADD COLUMN " + c.ddl); err != nil {
			return fmt.Errorf("jobstore: migrate schedules add %s: %w", c.name, err)
		}
	}
	jcols, err := s.tableColumns("jobs")
	if err != nil {
		return err
	}
	for _, c := range []struct{ name, ddl string }{
		{"schedule_id", "schedule_id TEXT"},
		{"schedule_trigger", "schedule_trigger TEXT"},
	} {
		if _, ok := jcols[c.name]; ok {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE jobs ADD COLUMN " + c.ddl); err != nil {
			return fmt.Errorf("jobstore: migrate jobs add %s: %w", c.name, err)
		}
	}
	// Created after the column exists (G057): the base schema never references it.
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_jobs_schedule_id ON jobs(schedule_id) WHERE schedule_id <> ''`,
	); err != nil {
		return fmt.Errorf("jobstore: migrate schedule_id index: %w", err)
	}
	return nil
}

// ScheduleActiveJobID returns the id of one job of the schedule that has not finished
// (scheduleActiveStatuses), "" when there is none. A schedule whose jobs were all
// pruned has none.
func (s *Store) ScheduleActiveJobID(scheduleID string) (string, error) {
	if scheduleID == "" {
		return "", nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(scheduleActiveStatuses)), ",")
	args := make([]any, 0, len(scheduleActiveStatuses)+1)
	args = append(args, scheduleID)
	for _, st := range scheduleActiveStatuses {
		args = append(args, st)
	}
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM jobs WHERE schedule_id = ? AND status IN (`+ph+`) ORDER BY started_at DESC, id DESC LIMIT 1`, args...,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("jobstore: schedule %q active job: %w", scheduleID, err)
	}
	return id, nil
}

// ScheduleHasActiveJob reports whether the schedule has a job that has not finished.
func (s *Store) ScheduleHasActiveJob(scheduleID string) (bool, error) {
	id, err := s.ScheduleActiveJobID(scheduleID)
	return id != "", err
}

// LatestScheduleJob returns the newest job a schedule started by timer, run-now or
// webhook (by started_at, then id); ok is false when none is left.
func (s *Store) LatestScheduleJob(scheduleID string) (JobRecord, bool, error) {
	r, err := scanJob(s.db.QueryRow(
		selectCols+` WHERE schedule_id = ? ORDER BY started_at DESC, id DESC LIMIT 1`, scheduleID))
	if errors.Is(err, sql.ErrNoRows) {
		return JobRecord{}, false, nil
	}
	if err != nil {
		return JobRecord{}, false, fmt.Errorf("jobstore: latest schedule job %q: %w", scheduleID, err)
	}
	return r, true, nil
}

// IncScheduleRun counts one timer-started submission in a single statement and returns
// the new count with the schedule's max_runs. ok is false when the schedule is gone
// or is a once schedule (which uses no run counters).
func (s *Store) IncScheduleRun(id string) (count, max int, ok bool, err error) {
	defer s.emit(Change{Kind: ChangeSchedule})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err = s.db.QueryRow(
		`UPDATE schedules SET run_count = run_count + 1, updated_at = ? WHERE id = ? AND COALESCE(schedule_type,'cron') <> 'once' RETURNING run_count, max_runs`,
		time.Now().Unix(), id,
	).Scan(&count, &max)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("jobstore: inc schedule %q run: %w", id, err)
	}
	return count, max, true, nil
}

// IncScheduleFailure counts one more consecutive failure in a single statement and
// returns the new count with the schedule's max_failures. ok is false when the
// schedule is gone.
func (s *Store) IncScheduleFailure(id string) (failures, max int, ok bool, err error) {
	defer s.emit(Change{Kind: ChangeSchedule})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err = s.db.QueryRow(
		`UPDATE schedules SET consecutive_failures = consecutive_failures + 1, updated_at = ? WHERE id = ? AND COALESCE(schedule_type,'cron') <> 'once' RETURNING consecutive_failures, max_failures`,
		time.Now().Unix(), id,
	).Scan(&failures, &max)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("jobstore: inc schedule %q failure: %w", id, err)
	}
	return failures, max, true, nil
}

// ResetScheduleFailures clears the consecutive-failure counter (a successful run).
func (s *Store) ResetScheduleFailures(id string) error {
	defer s.emit(Change{Kind: ChangeSchedule})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE schedules SET consecutive_failures = 0, updated_at = ? WHERE id = ? AND consecutive_failures <> 0`,
		time.Now().Unix(), id,
	); err != nil {
		return fmt.Errorf("jobstore: reset schedule %q failures: %w", id, err)
	}
	return nil
}

// PauseSchedule stops a schedule with a reason, only when it is still enabled: the
// first caller wins (true) so the pause is recorded, and announced, exactly once.
func (s *Store) PauseSchedule(id, reason string) (bool, error) {
	defer s.emit(Change{Kind: ChangeSchedule})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(
		`UPDATE schedules SET enabled = 0, paused_reason = ?, updated_at = ? WHERE id = ? AND enabled = 1`,
		reason, time.Now().Unix(), id,
	)
	if err != nil {
		return false, fmt.Errorf("jobstore: pause schedule %q: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobstore: pause schedule %q rows: %w", id, err)
	}
	return n == 1, nil
}

// SetSchedulePrecheckFingerprint records the output fingerprint of the pre-run check
// whose run was just submitted (the basis of skip_unchanged).
func (s *Store) SetSchedulePrecheckFingerprint(id, fingerprint string) error {
	defer s.emit(Change{Kind: ChangeSchedule})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(
		`UPDATE schedules SET precheck_fingerprint = ?, updated_at = ? WHERE id = ?`,
		fingerprint, time.Now().Unix(), id,
	); err != nil {
		return fmt.Errorf("jobstore: set schedule %q precheck fingerprint: %w", id, err)
	}
	return nil
}
