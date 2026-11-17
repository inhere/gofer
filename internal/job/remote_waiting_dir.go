package job

import "log/slog"

// noteRemoteWaitingDir shows a worker job that is parked on its directory lock as
// `waiting_dir` on the host row. The worker mirrors its job.waiting_dir event, but until
// the job starts the host row only says `queued`, which hides that the job is waiting for
// another job's lock rather than for a free slot. It only moves a row that is still
// `queued`: the started report (markStarted) takes it to `running` as for any remote job,
// and an event that arrives after that changes nothing. It runs after the event itself is
// recorded, so the explanation is on the timeline before the state is visible.
func (s *Service) noteRemoteWaitingDir(jobID, eventType string, detail map[string]any) {
	if eventType != EventJobWaitingDir {
		return
	}
	entry := s.entry(jobID)
	if entry == nil {
		return
	}
	entry.mu.Lock()
	if entry.result.Status != StatusQueued {
		entry.mu.Unlock()
		return
	}
	entry.result.Status = StatusWaitingDir
	if holder, _ := detail["holder_job"].(string); holder != "" {
		entry.result.WaitingOnJob = holder
	}
	snap := entry.result
	entry.mu.Unlock()
	if err := s.persist(snap); err != nil {
		slog.Warn("persist waiting_dir snapshot of a worker job", "job_id", jobID, "err", err)
	}
}
