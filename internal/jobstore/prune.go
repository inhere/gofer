package jobstore

import (
	"fmt"
	"strings"
	"time"
)

// terminalStatuses is the set of job statuses PruneJobs is allowed to evict.
// Live jobs (queued/running/pending_interaction) are never pruned: removing one
// would lose an in-flight job's metadata that the job service falls back to. The
// values mirror internal/job's status constants but are kept as literals here to
// avoid a job -> jobstore -> job import cycle (see package doc).
var terminalStatuses = []string{"done", "failed", "cancelled", "timeout"}

// terminalPlaceholders / terminalArgs render terminalStatuses into a reusable
// "status IN (?,?,...)" fragment and its bind args.
var (
	terminalPlaceholders = strings.TrimSuffix(strings.Repeat("?,", len(terminalStatuses)), ",")
	terminalArgs         = func() []any {
		a := make([]any, len(terminalStatuses))
		for i, st := range terminalStatuses {
			a[i] = st
		}
		return a
	}()
)

// RetentionPolicy bounds how many terminal jobs (and their logs) are kept,
// solving the disk side of C1 (design §13 SP5). A zero value (both fields <= 0)
// prunes nothing.
type RetentionPolicy struct {
	// MaxAge, when > 0, deletes terminal jobs whose effective end time (ended_at,
	// or updated_at when ended_at is 0) is older than now-MaxAge.
	MaxAge time.Duration
	// MaxCount, when > 0, keeps only the newest MaxCount terminal jobs (by
	// started_at desc) and deletes the rest.
	MaxCount int
}

// IsZero reports whether the policy would prune nothing.
func (p RetentionPolicy) IsZero() bool { return p.MaxAge <= 0 && p.MaxCount <= 0 }

// PruneJobs deletes the terminal jobs (status in done/failed/cancelled/timeout)
// that the retention policy evicts, along with their interaction, event (E13)
// and webhook-delivery (E14) rows. Live jobs
// (queued/running/pending_interaction) are NEVER deleted. It returns the number
// of jobs deleted and the result_dir of each, so the caller can best-effort
// remove the on-disk log directories (the DB does not own those files).
//
// now is injected (unix seconds) so tests can pin the clock; production passes
// time.Now().Unix().
//
// All work runs under writeMu (like every other writer) so SQLite never sees a
// concurrent writer; the deletes run inside a single transaction so a job row and
// its interactions are removed atomically.
func (s *Store) PruneJobs(policy RetentionPolicy, now int64) (deleted int, prunedDirs []string, err error) {
	if policy.IsZero() {
		return 0, nil, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ids, dirs, err := s.selectPruneVictims(policy, now)
	if err != nil {
		return 0, nil, err
	}
	if len(ids) == 0 {
		return 0, nil, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, nil, fmt.Errorf("jobstore: prune begin tx: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.Exec("DELETE FROM interactions WHERE job_id = ?", id); err != nil {
			_ = tx.Rollback()
			return 0, nil, fmt.Errorf("jobstore: prune interactions %q: %w", id, err)
		}
		// E13: the append-only event stream is owned by the job; drop it with the
		// job row (same tx) so a pruned job leaves no orphaned events.
		if _, err := tx.Exec("DELETE FROM job_events WHERE job_id = ?", id); err != nil {
			_ = tx.Rollback()
			return 0, nil, fmt.Errorf("jobstore: prune job events %q: %w", id, err)
		}
		// E14: the webhook delivery rows are also owned by the job; drop them in the
		// same tx so a pruned job leaves no orphaned deliveries.
		if _, err := tx.Exec("DELETE FROM event_deliveries WHERE job_id = ?", id); err != nil {
			_ = tx.Rollback()
			return 0, nil, fmt.Errorf("jobstore: prune deliveries %q: %w", id, err)
		}
		// WEB-03 P3 (retention regime 2, D-P3-6): pty_sessions are jobstore-owned;
		// drop this job's session rows in the same tx so a pruned job leaves no
		// orphaned recording metadata (the cast file rides result_dir removal).
		if _, err := tx.Exec("DELETE FROM pty_sessions WHERE job_id = ?", id); err != nil {
			_ = tx.Rollback()
			return 0, nil, fmt.Errorf("jobstore: prune pty sessions %q: %w", id, err)
		}
		if _, err := tx.Exec("DELETE FROM jobs WHERE id = ?", id); err != nil {
			_ = tx.Rollback()
			return 0, nil, fmt.Errorf("jobstore: prune job %q: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("jobstore: prune commit: %w", err)
	}
	return len(ids), dirs, nil
}

// terminalWorkflowStatuses is the set of workflow statuses PruneWorkflows may
// evict (a running workflow is never pruned). Kept as literals here to avoid a
// job -> jobstore -> job import cycle (mirrors terminalStatuses).
var terminalWorkflowStatuses = []string{"done", "failed", "cancelled"}

var (
	terminalWfPlaceholders = strings.TrimSuffix(strings.Repeat("?,", len(terminalWorkflowStatuses)), ",")
	terminalWfArgs         = func() []any {
		a := make([]any, len(terminalWorkflowStatuses))
		for i, st := range terminalWorkflowStatuses {
			a[i] = st
		}
		return a
	}()
)

// WorkflowRetentionPolicy bounds how long terminal workflows are kept (P1, design
// §5.4 / D22). A zero MaxAge prunes nothing.
type WorkflowRetentionPolicy struct {
	// MaxAge, when > 0, deletes terminal workflows whose updated_at is older than
	// now-MaxAge, along with their step-jobs and workflow_events.
	MaxAge time.Duration
}

// IsZero reports whether the workflow policy would prune nothing.
func (p WorkflowRetentionPolicy) IsZero() bool { return p.MaxAge <= 0 }

// PruneWorkflows deletes terminal workflows (status in done/failed/cancelled) older
// than the policy's MaxAge, and连带删 each workflow's step-jobs (+ their job_events
// / interactions / event_deliveries) and its workflow_events — so a pruned workflow
// leaves NO悬挂 rows (D22). Running workflows are NEVER deleted. It returns the
// number of workflows deleted and the result_dir of every deleted step-job, so the
// caller can best-effort remove the on-disk log directories.
//
// now is injected (unix seconds) so tests can pin the clock. All work runs under
// writeMu and inside a single transaction (a workflow header + its step-jobs + all
// event/delivery rows are removed atomically).
func (s *Store) PruneWorkflows(policy WorkflowRetentionPolicy, now int64) (deleted int, prunedDirs []string, err error) {
	if policy.IsZero() {
		return 0, nil, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cutoff := now - int64(policy.MaxAge/time.Second)
	// Select victim workflow ids (terminal AND older than the cutoff by updated_at).
	q := fmt.Sprintf(
		"SELECT id FROM workflows WHERE status IN (%s) AND updated_at < ?",
		terminalWfPlaceholders,
	)
	args := append([]any{}, terminalWfArgs...)
	args = append(args, cutoff)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return 0, nil, fmt.Errorf("jobstore: prune workflows select: %w", err)
	}
	var wfIDs []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			rows.Close()
			return 0, nil, fmt.Errorf("jobstore: prune workflows scan: %w", scanErr)
		}
		wfIDs = append(wfIDs, id)
	}
	if rErr := rows.Err(); rErr != nil {
		rows.Close()
		return 0, nil, fmt.Errorf("jobstore: prune workflows select rows: %w", rErr)
	}
	rows.Close()
	if len(wfIDs) == 0 {
		return 0, nil, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, nil, fmt.Errorf("jobstore: prune workflows begin tx: %w", err)
	}
	for _, wfID := range wfIDs {
		// Collect this workflow's step-job ids + dirs (so we can drop their owned
		// rows and report their dirs for on-disk cleanup).
		jrows, jerr := tx.Query("SELECT id, result_dir FROM jobs WHERE workflow_id = ?", wfID)
		if jerr != nil {
			_ = tx.Rollback()
			return 0, nil, fmt.Errorf("jobstore: prune workflows jobs select %q: %w", wfID, jerr)
		}
		var jobIDs []string
		for jrows.Next() {
			var jid, dir string
			if scanErr := jrows.Scan(&jid, &dir); scanErr != nil {
				jrows.Close()
				_ = tx.Rollback()
				return 0, nil, fmt.Errorf("jobstore: prune workflows jobs scan %q: %w", wfID, scanErr)
			}
			jobIDs = append(jobIDs, jid)
			if dir != "" {
				prunedDirs = append(prunedDirs, dir)
			}
		}
		if jrErr := jrows.Err(); jrErr != nil {
			jrows.Close()
			_ = tx.Rollback()
			return 0, nil, fmt.Errorf("jobstore: prune workflows jobs rows %q: %w", wfID, jrErr)
		}
		jrows.Close()
		// Drop each step-job's owned rows (interactions / job_events / deliveries) and
		// the job row itself — same连带删 set as PruneJobs (no悬挂).
		for _, jid := range jobIDs {
			for _, stmt := range []string{
				"DELETE FROM interactions WHERE job_id = ?",
				"DELETE FROM job_events WHERE job_id = ?",
				"DELETE FROM event_deliveries WHERE job_id = ?",
				// WEB-03 P3 (D-P3-6): drop each step-job's pty_sessions in the same tx.
				"DELETE FROM pty_sessions WHERE job_id = ?",
				"DELETE FROM jobs WHERE id = ?",
			} {
				if _, derr := tx.Exec(stmt, jid); derr != nil {
					_ = tx.Rollback()
					return 0, nil, fmt.Errorf("jobstore: prune workflow job %q: %w", jid, derr)
				}
			}
		}
		// Drop the workflow's events and the header row.
		if _, derr := tx.Exec("DELETE FROM workflow_events WHERE workflow_id = ?", wfID); derr != nil {
			_ = tx.Rollback()
			return 0, nil, fmt.Errorf("jobstore: prune workflow events %q: %w", wfID, derr)
		}
		if _, derr := tx.Exec("DELETE FROM workflows WHERE id = ?", wfID); derr != nil {
			_ = tx.Rollback()
			return 0, nil, fmt.Errorf("jobstore: prune workflow %q: %w", wfID, derr)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, fmt.Errorf("jobstore: prune workflows commit: %w", err)
	}
	return len(wfIDs), prunedDirs, nil
}

// selectPruneVictims returns the ids and result_dirs of terminal jobs evicted by
// the policy. Two independent predicates are OR'd: a job is a victim if it is too
// old (MaxAge) OR it falls outside the newest-MaxCount window. It runs under the
// caller's writeMu.
func (s *Store) selectPruneVictims(policy RetentionPolicy, now int64) (ids []string, dirs []string, err error) {
	var conds []string
	var args []any

	// Age predicate: effective end time = ended_at, or updated_at when ended_at
	// is 0 (job that never stamped an end), older than the cutoff.
	if policy.MaxAge > 0 {
		cutoff := now - int64(policy.MaxAge/time.Second)
		conds = append(conds, "(CASE WHEN ended_at > 0 THEN ended_at ELSE updated_at END) < ?")
		args = append(args, cutoff)
	}

	// Count predicate: a terminal job is outside the keep window when at least
	// MaxCount other terminal jobs are newer than it (started_at desc, id desc as
	// the stable tiebreaker matching ListJobs ordering).
	if policy.MaxCount > 0 {
		rank := fmt.Sprintf(`id IN (
  SELECT id FROM jobs t
  WHERE t.status IN (%s)
    AND (
      SELECT COUNT(*) FROM jobs n
      WHERE n.status IN (%s)
        AND (n.started_at > t.started_at
             OR (n.started_at = t.started_at AND n.id > t.id))
    ) >= ?
)`, terminalPlaceholders, terminalPlaceholders)
		conds = append(conds, rank)
		args = append(args, terminalArgs...)
		args = append(args, terminalArgs...)
		args = append(args, policy.MaxCount)
	}

	// Always restrict to terminal jobs; OR the active predicates together.
	q := fmt.Sprintf(
		"SELECT id, result_dir FROM jobs WHERE status IN (%s) AND (%s)",
		terminalPlaceholders, strings.Join(conds, " OR "),
	)
	full := append([]any{}, terminalArgs...)
	full = append(full, args...)

	rows, err := s.db.Query(q, full...)
	if err != nil {
		return nil, nil, fmt.Errorf("jobstore: prune select: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, dir string
		if scanErr := rows.Scan(&id, &dir); scanErr != nil {
			return nil, nil, fmt.Errorf("jobstore: prune scan: %w", scanErr)
		}
		ids = append(ids, id)
		dirs = append(dirs, dir)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("jobstore: prune select rows: %w", err)
	}
	return ids, dirs, nil
}
