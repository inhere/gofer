package job

import (
	"log/slog"

	"github.com/inhere/gofer/internal/jobstore"
)

// Schedule run health (design 2026-11-12 §1): the terminal-state bookkeeping of the
// jobs a schedule's timer started. Only timer-started jobs count — a run-now or a
// webhook is a person's or another system's act, so it neither adds to the failure
// streak nor clears it. A `once` schedule uses no counters at all (the store skips it).

// scheduleOutcome folds one finished job into its schedule's failure streak. It sits
// next to every linkTodoOutcome call so the two see the same terminal events:
// failed / timeout add one, done / needs_review clear the streak, cancelled / rejected
// leave it alone. resumePending is the uncommitted-work guard's "resume" decision — the
// job is about to be continued, so it is not an outcome yet. Best-effort: a counter
// write must never affect a job.
func (s *Service) scheduleOutcome(snap JobResult, resumePending bool) {
	if snap.ScheduleID == "" || snap.ScheduleTrigger != jobstore.ScheduleTriggerTimer || resumePending {
		return
	}
	switch snap.Status {
	case StatusFailed, StatusTimeout:
		s.scheduleRunFailed(snap.ScheduleID, snap.ID)
	case StatusDone, StatusNeedsReview:
		if err := s.meta.ResetScheduleFailures(snap.ScheduleID); err != nil {
			slog.Warn("schedule outcome: reset failures", "schedule_id", snap.ScheduleID, "job_id", snap.ID, "err", err)
		}
	}
}

// scheduleRunFailed counts one failed timer run (a failed job, or a run that could not
// be submitted) and pauses the schedule when the streak reaches its max_failures. The
// counter is one atomic UPDATE … RETURNING and the pause is conditional on the schedule
// still being enabled, so concurrent failures announce the pause exactly once.
func (s *Service) scheduleRunFailed(scheduleID, lastJobID string) {
	failures, limit, ok, err := s.meta.IncScheduleFailure(scheduleID)
	if err != nil {
		slog.Warn("schedule failure count", "schedule_id", scheduleID, "err", err)
		return
	}
	if !ok || limit <= 0 || failures < limit {
		return
	}
	s.pauseSchedule(scheduleID, jobstore.SchedulePausedFailures, map[string]any{
		"failures": failures, "last_job_id": lastJobID,
	})
}

// pauseSchedule stops the schedule for a reason and records schedule.paused — only for
// the caller whose conditional update actually flipped it, so the event is written once.
func (s *Service) pauseSchedule(scheduleID, reason string, extra map[string]any) {
	paused, err := s.meta.PauseSchedule(scheduleID, reason)
	if err != nil {
		slog.Warn("schedule pause", "schedule_id", scheduleID, "reason", reason, "err", err)
		return
	}
	if !paused {
		return
	}
	project := ""
	if rec, found, gerr := s.meta.GetSchedule(scheduleID); gerr == nil && found {
		project = rec.ProjectKey
	}
	detail := map[string]any{"schedule_id": scheduleID, "reason": reason}
	for k, v := range extra {
		detail[k] = v
	}
	s.RecordScopedEvent(ScheduleEventScope(scheduleID), EventSchedulePaused, project, detail)
}

// recordScheduleRunFailed writes schedule.run_failed on the schedule's own stream.
func (s *Service) recordScheduleRunFailed(rec jobstore.ScheduleRecord, reason string, extra map[string]any) {
	detail := map[string]any{"schedule_id": rec.ID, "reason": reason}
	for k, v := range extra {
		detail[k] = v
	}
	s.RecordScopedEvent(ScheduleEventScope(rec.ID), EventScheduleRunFailed, rec.ProjectKey, detail)
}

// scheduleCounts reports whether a trigger of this schedule feeds the run and failure
// counters: only the timer does, and never a `once` schedule.
func scheduleCounts(rec jobstore.ScheduleRecord, trigger string) bool {
	return trigger == jobstore.ScheduleTriggerTimer && rec.ScheduleType != "once"
}
