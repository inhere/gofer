package job

import (
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/inhere/gofer/internal/jobstore"
)

// ScheduleDispatcher is the one place a schedule's stored request becomes a job
// (design 2026-11-12 §1, plan R4): the timer sweep, run-now and the webhook all go
// through it, so the overlap rule, the run counters and the failure bookkeeping cannot
// differ between entries. The entries keep what is theirs — who the run is attributed
// to, parameter overrides, HTTP status mapping — and hand over a finished JobRequest.
type ScheduleDispatcher struct {
	s *Service
	// pool bounds the pre-run checks (schedule_precheck.go).
	pool *precheckPool
}

func newScheduleDispatcher(s *Service) *ScheduleDispatcher {
	return &ScheduleDispatcher{s: s, pool: newPrecheckPool()}
}

// ScheduleDispatcher returns the service's dispatcher.
func (s *Service) ScheduleDispatcher() *ScheduleDispatcher { return s.schedDisp }

// ScheduleRun is the outcome of dispatching one run. Skipped means nothing was
// submitted on purpose (Reason says why; PrevJobID names the unfinished job of an
// overlap skip); Result is the new job otherwise.
type ScheduleRun struct {
	Result    JobResult
	Skipped   bool
	Reason    string
	PrevJobID string
}

// Reasons of a skipped run (the `reason` of schedule.run_skipped).
const (
	ScheduleSkipPreviousRunning = "previous_running"
	ScheduleSkipPrecheck        = "precheck"
	ScheduleSkipUnchanged       = "unchanged"
	// ScheduleSkipCheckRunning: the schedule's previous pre-run check has not returned yet.
	ScheduleSkipCheckRunning = "check_running"
	// ScheduleSkipDraining: the check passed but the server was draining for an upgrade.
	ScheduleSkipDraining = "server_draining"
)

// Dispatch submits one run of the schedule: overlap check, then the submit, then the
// counters. req is the finished request (identity, params and channel are the
// caller's business); Dispatch stamps the schedule provenance onto it. permit may be
// nil (a plain Submit); the sweeper passes its admission permit.
//
// A skip is not an error: it returns Skipped with a nil error and has already written
// schedule.run_skipped. A refused submit is returned as is; when the trigger counts
// (a timer on a recurring schedule) it also writes schedule.run_failed and adds to the
// failure streak.
func (d *ScheduleDispatcher) Dispatch(permit *AdmissionPermit, rec jobstore.ScheduleRecord, trigger string, req JobRequest) (ScheduleRun, error) {
	// A schedule that is enabled again after hitting max_runs without a higher limit
	// must not squeeze out one more run before it stops.
	if scheduleCounts(rec, trigger) && rec.MaxRuns > 0 && rec.RunCount >= rec.MaxRuns {
		d.s.pauseSchedule(rec.ID, jobstore.SchedulePausedMaxRuns, map[string]any{"runs": rec.RunCount})
		return ScheduleRun{Skipped: true, Reason: jobstore.SchedulePausedMaxRuns}, nil
	}
	if run, skipped, err := d.overlapSkip(rec); skipped || err != nil {
		return run, err
	}
	d.attachScheduleContext(rec, trigger, &req)
	req.ScheduleID = rec.ID
	req.ScheduleTrigger = trigger
	var (
		res JobResult
		err error
	)
	if permit != nil {
		res, err = d.s.SubmitWithPermit(permit, req)
	} else {
		res, err = d.s.Submit(req)
	}
	if err != nil {
		d.submitFailed(rec, trigger, err)
		return ScheduleRun{}, err
	}
	if scheduleCounts(rec, trigger) {
		d.runSubmitted(rec, res.ID)
	}
	return ScheduleRun{Result: res}, nil
}

// overlapSkip applies the schedule's overlap policy. A `once` schedule runs once and is
// never skipped. An unreadable job table is returned as an error (the run is not
// submitted blind).
func (d *ScheduleDispatcher) overlapSkip(rec jobstore.ScheduleRecord) (ScheduleRun, bool, error) {
	if rec.ScheduleType == "once" || rec.Overlap != jobstore.ScheduleOverlapSkip {
		return ScheduleRun{}, false, nil
	}
	prev, err := d.s.meta.ScheduleActiveJobID(rec.ID)
	if err != nil {
		return ScheduleRun{}, false, err
	}
	if prev == "" {
		return ScheduleRun{}, false, nil
	}
	d.recordSkipped(rec, ScheduleSkipPreviousRunning, prev)
	return ScheduleRun{Skipped: true, Reason: ScheduleSkipPreviousRunning, PrevJobID: prev}, true, nil
}

func (d *ScheduleDispatcher) recordSkipped(rec jobstore.ScheduleRecord, reason, prevJobID string) {
	detail := map[string]any{"schedule_id": rec.ID, "reason": reason}
	if prevJobID != "" {
		detail["job_id"] = prevJobID
	}
	d.s.RecordScopedEvent(ScheduleEventScope(rec.ID), EventScheduleRunSkipped, rec.ProjectKey, detail)
}

// submitFailed records a refused run. A counting trigger always writes the event and
// adds to the failure streak; any other trigger only records a ceiling refusal (the
// person or system that asked sees every other error in its own response).
func (d *ScheduleDispatcher) submitFailed(rec jobstore.ScheduleRecord, trigger string, err error) {
	if scheduleCounts(rec, trigger) {
		d.s.recordScheduleRunFailed(rec, err.Error(), nil)
		d.s.scheduleRunFailed(rec.ID, "")
		return
	}
	if errors.Is(err, ErrCeilingDenied) {
		d.s.recordScheduleRunFailed(rec, err.Error(), nil)
	}
}

// runSubmitted counts one timer-started submission and stops the schedule once it has
// used up its max_runs.
func (d *ScheduleDispatcher) runSubmitted(rec jobstore.ScheduleRecord, jobID string) {
	count, limit, ok, err := d.s.meta.IncScheduleRun(rec.ID)
	if err != nil {
		slog.Warn("schedule run count", "schedule_id", rec.ID, "err", err)
		return
	}
	if !ok || limit <= 0 || count < limit {
		return
	}
	d.s.pauseSchedule(rec.ID, jobstore.SchedulePausedMaxRuns, map[string]any{
		"runs": count, "last_job_id": jobID,
	})
}

// FireTimer runs one due schedule on its timer: it builds the request from the
// stored one (the schedule's creator is the ceiling owner, parameters take their
// defaults, a cron tick is gofer's own trigger) and dispatches it. The returned id is
// "" when the run was skipped.
//
// The ceiling owner of a due run is the schedule's CREATOR: nobody asked for this run,
// so it must stay bounded by whoever configured the timer (design 2026-10-18, 上限所有者).
// A schedule written before the column falls back to the caller id its stored request
// carries — the same person.
//
// A PERMANENT refusal (ErrCeilingDenied) is recorded on the schedule's own stream; the
// schedule itself is KEPT (the sweep has already advanced its next instant).
func (d *ScheduleDispatcher) FireTimer(permit *AdmissionPermit, rec jobstore.ScheduleRecord) (string, error) {
	req, err := d.timerRequest(rec)
	var pc *SchedulePrecheck
	if err == nil {
		pc, err = DecodeSchedulePrecheck(rec.PrecheckJSON)
	}
	if err != nil {
		// A row that cannot even be decoded is a failed run, not a silent one.
		d.submitFailed(rec, jobstore.ScheduleTriggerTimer, err)
		return "", err
	}
	if pc != nil {
		// The overlap rule comes before the check: no point in checking while the
		// previous run is still going. The check itself runs in the pool, so the sweep
		// is not held up; the run is submitted (or skipped, or failed) from there.
		if _, skipped, oerr := d.overlapSkip(rec); oerr != nil {
			return "", oerr
		} else if skipped {
			return "", nil
		}
		d.startCheck(rec, *pc, req)
		return "", nil
	}
	run, err := d.Dispatch(permit, rec, jobstore.ScheduleTriggerTimer, req)
	if err != nil || run.Skipped {
		return "", err
	}
	return run.Result.ID, nil
}

func (d *ScheduleDispatcher) timerRequest(rec jobstore.ScheduleRecord) (JobRequest, error) {
	var req JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &req); err != nil {
		return JobRequest{}, err
	}
	req.Channel = "cron"
	req.OriginCaller = rec.CreatorCaller
	if req.OriginCaller == "" {
		req.OriginCaller = req.CallerID
	}
	// Design 2026-10-22 §D2: a due run uses the schedule's parameter DEFAULTS, overlaid on
	// the env the request stored. A row whose params cannot be decoded is a broken row, not
	// a reason to fire it with a half-applied env: report the error instead.
	params, err := DecodeScheduleParams(rec.ParamsJSON)
	if err != nil {
		return JobRequest{}, err
	}
	if err := ApplyScheduleParams(&req, params, nil); err != nil {
		return JobRequest{}, err
	}
	// gofer-3db4: a cron tick is gofer's own trigger — an end-notification the stored
	// request asks for but nobody can be told about is dropped (with a
	// job.notify_dropped event) instead of losing the run.
	req.NotifyDegradable = true
	return req, nil
}
