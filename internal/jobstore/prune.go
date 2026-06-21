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
// that the retention policy evicts, along with their interaction and event rows
// (E13). Live jobs
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
