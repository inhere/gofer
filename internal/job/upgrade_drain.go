package job

// The restart census (gofer-dzv3, docs/design/2026-10-14-update-wait-idle-design.md).
//
// "Idle enough to restart" is ONE question with ONE answer, asked by two callers: the
// one-click update, which waits for it before it hands the server over to `serve
// upgrade`, and that upgrade's own drain bridge, which waits for it before it accepts
// the transaction. Both go through the two methods below, so a job that blocks the
// first can never be ignored by the second.
//
// The rule (see jobstore.upgradeBlockerWhere for the full reasoning):
//   - a job this SERVER owns — local or peer-http — blocks in every live state;
//   - a job a WORKER already runs does not block: the worker keeps it across the
//     restart and the next serve adopts it (RECOV-01);
//   - a worker job the worker has not taken over yet (queued / waiting for the
//     directory lock before dispatch) blocks, because nothing would keep the next
//     serve from failing it as "worker lost";
//   - jobs parked by an update itself never block — they are exactly the work the
//     restart is not waiting for.
//
// The worker runner names come from the same config snapshot the rest of the service
// uses, so a job whose worker_id the hub resolves at dispatch time (D4) is classified
// by its runner, exactly as the orphan reconcile classifies it.

// UpgradeRestartBlocker is one job the census is waiting for, as the update progress
// reports it to a person: the id to click, a title to recognise it by, and what it is
// doing.
type UpgradeRestartBlocker struct {
	ID        string
	Title     string
	Status    string
	Runner    string
	StartedAt int64
}

// CountUpgradeRestartBlockers counts the jobs that keep a server restart from being
// safe. excludedIDs are rows the caller verified itself (the idle ACP sessions the
// shutdown path preserves, the upgrade's own source job).
func (s *Service) CountUpgradeRestartBlockers(excludedIDs []string) (int, error) {
	return s.meta.CountUpgradeRestartBlockers(excludedIDs, s.workerRunnerNames())
}

// ListUpgradeRestartBlockers names up to limit of them (oldest first), for the drain
// log line and for the update progress a person watches. The title is resolved through
// Get (the jobs table has no title column: it lives in request_json).
func (s *Service) ListUpgradeRestartBlockers(excludedIDs []string, limit int) ([]UpgradeRestartBlocker, error) {
	recs, err := s.meta.ListUpgradeRestartBlockers(excludedIDs, s.workerRunnerNames(), limit)
	if err != nil {
		return nil, err
	}
	out := make([]UpgradeRestartBlocker, 0, len(recs))
	for _, rec := range recs {
		b := UpgradeRestartBlocker{ID: rec.ID, Status: rec.Status, Runner: rec.Runner, StartedAt: rec.StartedAt}
		if res, ok := s.Get(rec.ID); ok {
			b.Title = res.Title
		}
		out = append(out, b)
	}
	return out, nil
}
