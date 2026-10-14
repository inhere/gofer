package jobstore

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/util"
)

// upgradeBlockerStatuses are the live states of a job the SERVER still has to
// finish: it is either running here or waiting for a gate this process owns. A job
// in `recovering` is deliberately absent — only the worker processes own those, and
// a server restart hands them to the next serve (RECOV-01 adoption) instead of
// killing them. Nor is the update's own `paused_for_update` state here: a parked job
// is precisely the work the restart is NOT waiting for.
const upgradeBlockerStatuses = `('queued','running','awaiting_input','pending_interaction','waiting_dir')`

// upgradeBlockerWhere is the predicate — plus the args it binds — for "this job keeps
// a server restart from being safe" (gofer-dzv3). It is the census the drain bridge
// and the one-click update's wait-for-idle both read, so they can never disagree about
// when the machine is idle enough to restart:
//
//   - a job that belongs to the SERVER (a local runner, or a peer-http runner, which
//     the next serve would fail as an orphan): blocking in every live state;
//   - a job a WORKER already runs: the worker process keeps it across the restart and
//     the next serve ADOPTS it (ReconcileOrphanJobs holds such a row in `recovering`
//     and the worker's reconnect claims it) — so it does not block;
//   - a worker job the worker has NOT taken over yet (`queued`, or parked on the
//     directory lock before dispatch): blocking, like a server-side job, because
//     nothing keeps it alive across the restart — the next serve would fail it as
//     "worker lost" instead of adopting it. It is blocked rather than parked because
//     the job is already inside the dispatcher; the wait is normally over in seconds.
func upgradeBlockerWhere(excludedIDs, workerRunners []string) (string, []any) {
	workerPred, workerArgs := orphanWorkerPred(workerRunners)
	where := ` WHERE status IN ` + upgradeBlockerStatuses +
		` AND (status IN ('queued','waiting_dir') OR NOT (` + workerPred + `))`
	args := make([]any, 0, util.CapSum(len(workerArgs), len(excludedIDs)))
	args = append(args, workerArgs...)
	ids := make([]any, 0, len(excludedIDs))
	for _, id := range excludedIDs {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		where += ` AND id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
		args = append(args, ids...)
	}
	return where, args
}

// CountUpgradeRestartBlockers counts the durable job rows that still carry work this
// server owns (see upgradeBlockerWhere). The caller may exclude ids it has separately
// verified (the idle ACP sessions that survive a restart, the upgrade's own source
// job).
func (s *Store) CountUpgradeRestartBlockers(excludedIDs, workerRunners []string) (int, error) {
	where, args := upgradeBlockerWhere(excludedIDs, workerRunners)
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs`+where, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("jobstore: count upgrade restart blockers: %w", err)
	}
	return count, nil
}

// UpgradeBlocker is one job row that keeps a server restart waiting.
type UpgradeBlocker struct {
	ID     string
	Status string
	Runner string
	// StartedAt is when the job was submitted (the row's started_at, 0 for a job a
	// worker has not begun yet).
	StartedAt int64
}

// ListUpgradeRestartBlockers returns up to limit of the rows the restart census counts
// (oldest first), so an operator — or the update progress on the console — can see
// what the restart is waiting for. The job's TITLE is not a column: the caller that
// needs it reads the row through the job package (fromRecord).
func (s *Store) ListUpgradeRestartBlockers(excludedIDs, workerRunners []string, limit int) ([]UpgradeBlocker, error) {
	if limit <= 0 {
		limit = 10
	}
	where, args := upgradeBlockerWhere(excludedIDs, workerRunners)
	rows, err := s.db.Query(`SELECT id, status, runner, started_at FROM jobs`+
		where+` ORDER BY started_at, id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list upgrade restart blockers: %w", err)
	}
	defer rows.Close()
	out := make([]UpgradeBlocker, 0)
	for rows.Next() {
		var b UpgradeBlocker
		if err := rows.Scan(&b.ID, &b.Status, &b.Runner, &b.StartedAt); err != nil {
			return nil, fmt.Errorf("jobstore: scan upgrade restart blocker: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list upgrade restart blocker rows: %w", err)
	}
	return out, nil
}
