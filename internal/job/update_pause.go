package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Update parks (gofer-dzv3, docs/design/2026-10-14-update-wait-idle-design.md).
//
// A one-click update downloads and verifies the new release and then has to restart
// the server, which used to mean "wait until every job is done, give up after 5
// minutes". With agent jobs running 20–60 minutes and new ones arriving all the time,
// there is never such a moment. So the update instead parks new work: the pause switch
// below stops each NEW submit at the SAME point a --hold submit stops (after admission,
// before anything is created) and the job waits in StatusPausedForUpdate — one row, no
// in-memory entry, no concurrency slot, no directory lock, no dispatch. Jobs already
// running finish normally, and when nothing is left that a restart would kill, the
// update goes ahead. The new server (or the failure / timeout / cancel path of this
// one) then releases every parked job: the ORIGINAL request is submitted again under
// the same id, exactly as an approved --hold job is.
//
// The pause lives in this process only. A restarted server must not inherit it (the
// update that owned it is gone), so a new server RELEASES whatever it finds parked
// instead of resuming the pause — see ResumeStartsForUpdate, which serve calls once at
// startup.

// Lifecycle events of an update park. Both names are separate from the hold_* family:
// an update park is not a decision anybody makes.
const (
	// EventJobPausedForUpdate is a job parked by an update waiting for idle:
	// {apply_id, reason}.
	EventJobPausedForUpdate = "job.paused_for_update"
	// EventJobUpdateResumed is the update releasing it: {by, count?}. The job's own
	// job.running / job.terminal follow as for any job.
	EventJobUpdateResumed = "job.update_resumed"
)

// updatePauseActor is recorded as the author of a release nobody asked for by hand.
const updatePauseActor = "update"

// updatePauseReason is what the parked row tells a reader (JobResult.Hold.Reason).
const updatePauseReason = "an update is waiting for the server to go idle"

// updatePauseReleaseBatch bounds one release pass.
const updatePauseReleaseBatch = 500

// ErrJobNotPausedForUpdate — the release of a job that is not (or no longer) parked by
// an update, including the loser of a race with a cancel or another release.
var ErrJobNotPausedForUpdate = errors.New("job is not paused for an update")

// PauseStartsForUpdate stops every NEW job from starting until ResumeStartsForUpdate:
// the update that owns applyID is waiting for the machine to go idle before it
// restarts the server. Jobs already running are untouched, and the submit still
// answers normally (the job is admitted and parked, never refused).
//
// The pause belongs to the PROCESS. It is not persisted, so a server that restarts
// does not resume a pause whose update is gone — it releases the parked jobs instead.
func (s *Service) PauseStartsForUpdate(applyID string) {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	s.pauseApplyID = applyID
	if applyID == "" {
		return
	}
	slog.Info("update.pause_starts", "event", "update.pause_starts", "apply_id", applyID)
}

// UpdatePauseOwner is the apply id of the update currently pausing new jobs ("" =
// none). Also the switch Submit reads while the server is draining for that update.
func (s *Service) UpdatePauseOwner() string {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	return s.pauseApplyID
}

// parkForUpdate parks this admitted submit when an update is pausing starts.
//
// The pause lock is held ACROSS the park, and the flag is read inside it, because that
// is what makes the release airtight: a submit that read the flag before it was cleared
// either lands its row while the lock is held (and is therefore seen by the release's
// list) or sees the cleared flag and simply runs. There is no window in which a parked
// row can appear after the release swept.
//
// parked=false means no update is pausing: the caller carries on with the normal path.
func (s *Service) parkForUpdate(cfg *config.Config, proj config.ProjectConfig, req *JobRequest, workDir string, rawJSON []byte, carry *heldInternals) (res JobResult, parked bool, err error) {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	if s.pauseApplyID == "" {
		return JobResult{}, false, nil
	}
	res, err = s.parkJob(cfg, proj, req, workDir, rawJSON, carry, parkSpec{
		Status: StatusPausedForUpdate, Kind: HoldKindUpdate,
		Reason: updatePauseReason,
		Event:  EventJobPausedForUpdate,
		Detail: map[string]any{"apply_id": s.pauseApplyID, "reason": updatePauseReason},
	})
	return res, true, err
}

// ResumeStartsForUpdate ends the pause (if any) and releases every job it parked: the
// original request runs again under the job's own id. It is called by the new server at
// startup (the previous process parked rows and died / was replaced), by the apply when
// it fails, times out or is cancelled, and by `update cancel`.
//
// It is idempotent and safe on a server that never paused anything: no flag, no parked
// rows, nothing to do. Returns how many jobs it released.
func (s *Service) ResumeStartsForUpdate() (int, error) {
	s.pauseMu.Lock()
	had := s.pauseApplyID
	s.pauseApplyID = ""
	recs, err := s.meta.ListPausedForUpdate(updatePauseReleaseBatch)
	s.pauseMu.Unlock()
	if err != nil {
		return 0, err
	}
	if had == "" && len(recs) == 0 {
		return 0, nil
	}
	released := 0
	errs := make([]error, 0, len(recs)) // at most one per parked job
	// One pass is enough (no new row can appear after the flag was cleared under the
	// lock); the loop only guards against a list that was itself truncated.
	for len(recs) > 0 {
		for _, rec := range recs {
			if _, rerr := s.releasePausedJob(rec.ID, updatePauseActor); rerr != nil {
				if errors.Is(rerr, ErrJobNotPausedForUpdate) {
					continue // cancelled or released by somebody else first
				}
				errs = append(errs, fmt.Errorf("job %s: %w", rec.ID, rerr))
				continue
			}
			released++
		}
		recs, err = s.meta.ListPausedForUpdate(updatePauseReleaseBatch)
		if err != nil {
			errs = append(errs, err)
			break
		}
	}
	if released > 0 {
		slog.Info("update.paused_jobs_released", "event", "update.paused_jobs_released",
			"apply_id", had, "count", released)
	}
	return released, errors.Join(errs...)
}

// releasePausedJob is ApproveJob's counterpart for a job an update parked: the row
// moves paused_for_update -> queued and the ORIGINAL request is submitted again under
// the same id, so the job keeps its id, dir, plan links and lineage.
//
// Unlike an approval there is nothing to verify against what somebody was shown: no
// human decided anything, so the digest check does not apply and the row keeps no hold
// record afterwards — the park is audited by job.paused_for_update / job.update_resumed
// and by the row's own history. What IS restored is the request's internal markers (the
// carry), exactly as for an approved hold.
func (s *Service) releasePausedJob(jobID, by string) (JobResult, error) {
	rec, hr, err := s.pausedRecord(jobID)
	if err != nil {
		return JobResult{}, err
	}
	var req JobRequest
	if err := json.Unmarshal([]byte(rec.RequestJSON), &req); err != nil {
		return JobResult{}, fmt.Errorf("decode request_json of %q: %w", jobID, err)
	}
	now := s.nowFn().Unix()
	carry := hr.Carry
	won, err := s.meta.DecideHold(jobstore.HoldDecision{
		ID: jobID, From: StatusPausedForUpdate, To: StatusQueued, At: now,
	})
	if err != nil {
		return JobResult{}, err
	}
	if !won {
		return JobResult{}, fmt.Errorf("%w: job %q was released by someone else", ErrJobNotPausedForUpdate, jobID)
	}
	s.signalJob(jobID)
	s.recordEvent(jobID, EventJobUpdateResumed, map[string]any{"by": by})

	carry.restore(&req)
	req.heldJobID = jobID // the job keeps its id: dir, links and lineage stay
	// gofer-3db4: gofer itself released this job for the update; an end-notification it
	// can no longer address is dropped rather than keeping the parked job from starting.
	req.NotifyDegradable = true
	res, serr := s.submitAdmitted(req)
	if serr != nil {
		s.failReleasedPause(jobID, serr)
		return JobResult{}, fmt.Errorf("job %q was released for an update but could not start: %w", jobID, serr)
	}
	slog.Info("update.paused_job_released", "event", "update.paused_job_released",
		"job_id", jobID, "by", by)
	return res, nil
}

// pausedRecord loads a job's row and its park record, refusing a job that is not parked
// by an update.
func (s *Service) pausedRecord(jobID string) (jobstore.JobRecord, holdRecord, error) {
	rec, ok, err := s.meta.GetJob(jobID)
	if err != nil {
		return jobstore.JobRecord{}, holdRecord{}, err
	}
	if !ok {
		return jobstore.JobRecord{}, holdRecord{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if rec.Status != StatusPausedForUpdate {
		return jobstore.JobRecord{}, holdRecord{}, fmt.Errorf("%w: job %q is %s", ErrJobNotPausedForUpdate, jobID, rec.Status)
	}
	var hr holdRecord
	if state, sec := unmarshalHold(rec.HoldJSON); state != nil {
		hr.HoldState = *state
		hr.holdSecret = *sec
	}
	return rec, hr, nil
}

// failReleasedPause ends a released job whose re-submit was refused: its row is still
// the queued one releasePausedJob wrote (nothing else owns it), so it goes
// queued -> failed with the reason and the usual terminal tail.
func (s *Service) failReleasedPause(jobID string, cause error) {
	now := s.nowFn().Unix()
	msg := "released for an update but could not start: " + cause.Error()
	won, err := s.meta.DecideHold(jobstore.HoldDecision{
		ID: jobID, From: StatusQueued, To: StatusFailed, Error: msg, At: now,
	})
	if err != nil || !won {
		slog.Warn("update: fail released job", "job_id", jobID, "won", won, "err", err)
		return
	}
	s.signalJob(jobID)
	s.recordEvent(jobID, EventJobTerminal, map[string]any{"status": StatusFailed, "exit_code": 0, "error": msg})
	if snap, ok := s.Get(jobID); ok {
		s.finishUnrun(snap)
	}
}

// cancelPausedJob is Cancel for a job an update parked: the submitter withdrawing it
// while the update waits. Like a held job it never ran, so the row goes straight to
// cancelled through the same compare-and-set. The park record is dropped with the
// cancel: nobody decided anything about the job, and the cancel is audited by
// job.paused_for_update / job.cancelled.
func (s *Service) cancelPausedJob(jobID string) error {
	if _, _, err := s.pausedRecord(jobID); err != nil {
		return err
	}
	_, won, err := s.endUnrun(jobID, StatusPausedForUpdate, "", "cancelled while paused for an update",
		EventJobCancelled, map[string]any{"was_terminal": false})
	if err != nil {
		return err
	}
	if !won {
		return fmt.Errorf("%w: job %q was cancelled or released by someone else", ErrJobNotPausedForUpdate, jobID)
	}
	return nil
}

// CountUpdatePaused reports how many jobs an update has parked right now.
func (s *Service) CountUpdatePaused() (int, error) { return s.meta.CountPausedForUpdate() }

// ListUpdatePaused returns the parked jobs, newest first (the apply progress names
// them so a reader can tell "waiting" from "parked").
func (s *Service) ListUpdatePaused(limit int) ([]JobResult, error) {
	recs, err := s.meta.ListPausedForUpdate(limit)
	if err != nil {
		return nil, err
	}
	out := make([]JobResult, 0, len(recs))
	for _, rec := range recs {
		out = append(out, fromRecord(rec))
	}
	return out, nil
}
