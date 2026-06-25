package workflow

import (
	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// stepJob returns the step-job whose step_index == stepIndex (1-based), or nil. It
// returns the FIRST match in step order. For a retried step (multiple attempts at
// the same step_index) prefer stepJobAttempt to disambiguate by attempt; stepJob is
// kept for ref resolution (which reads any prior step's output, attempt-agnostic).
func stepJob(jobs []jobstore.JobRecord, stepIndex int) *jobstore.JobRecord {
	for i := range jobs {
		if jobs[i].StepIndex == stepIndex {
			return &jobs[i]
		}
	}
	return nil
}

// stepJobAttempt returns the step-job matching BOTH step_index and attempt (P1), or
// nil. It is the workflow engine's lookup for "the current (step, attempt) job":
// a retried step has several jobs at the same step_index distinguished by attempt,
// so the engine must match the二元组 to find the run it is deciding on.
//
// A persisted job whose attempt is 0 (a v1/legacy step-job created before the
// attempt column existed, OR a crash-recovery row written with the field unset) is
// treated as attempt 1 — attempt is 1-based, so 0 == "unset" == the first run. This
// keeps crash recovery of a pre-P1 workflow from spuriously starting a duplicate
// first-attempt job (which would break the一个 (step,attempt) 只一 job invariant).
func stepJobAttempt(jobs []jobstore.JobRecord, stepIndex, attempt int) *jobstore.JobRecord {
	for i := range jobs {
		ja := jobs[i].Attempt
		if ja == 0 {
			ja = 1
		}
		if jobs[i].StepIndex == stepIndex && ja == attempt {
			return &jobs[i]
		}
	}
	return nil
}

// stepFanJobs returns ALL fan jobs of a (step_index, attempt) generation (P2): for a
// single-job step it is the one job (fan_index 0); for a fan-out step it is every
// started fan (fan_index 1..N). attempt is normalised like stepJobAttempt (a 0 attempt
// from a v1/legacy/unset row counts as 1) so the engine matches the二元组 unambiguously.
// The returned slice points into jobs (no copy); callers read-only.
func stepFanJobs(jobs []jobstore.JobRecord, stepIndex, attempt int) []*jobstore.JobRecord {
	out := make([]*jobstore.JobRecord, 0, 4)
	for i := range jobs {
		ja := jobs[i].Attempt
		if ja == 0 {
			ja = 1
		}
		if jobs[i].StepIndex == stepIndex && ja == attempt {
			out = append(out, &jobs[i])
		}
	}
	return out
}

// fanCounts tallies a generation's fan jobs into (done, terminal): done is fan jobs
// with job.StatusDone; terminal is fan jobs in ANY terminal state (done/failed/timeout/
// cancelled). Shared by fanTerminal and fanVerdict so they agree on the same census.
func fanCounts(fanJobs []*jobstore.JobRecord) (done, terminal int) {
	for _, j := range fanJobs {
		if j.Status == job.StatusDone {
			done++
		}
		if job.IsTerminal(j.Status) {
			terminal++
		}
	}
	return done, terminal
}

// fanTerminal reports whether a fan-out step's (step,attempt) generation has reached a
// DECIDABLE state under its join policy (P2, design §5.1, D15), where `want` is the
// configured parallelism (max 1):
//   - all:    every fan must be terminal (then verdict = all-done?). Until then, wait.
//   - any:    decidable as soon as ONE fan is done (success short-circuit), OR every
//     fan is terminal (then it is an all-failed → failed). A still-running fan
//     with no done yet means "maybe still succeeds" → wait.
//   - quorum: decidable once a majority (> want/2) are done (success short-circuit) OR
//     enough have failed that a quorum of done is impossible (→ failed) OR all
//     terminal. Otherwise wait.
//
// In all cases, once every fan is terminal the generation is trivially decidable (the
// `terminal == want` guard), so a generation never hangs.
func fanTerminal(fanJobs []*jobstore.JobRecord, want int, join string) bool {
	done, terminal := fanCounts(fanJobs)
	if terminal >= want {
		return true // every fan terminal: always decidable (success or failure)
	}
	switch join {
	case joinAny:
		return done >= 1 // first done short-circuits success
	case joinQuorum:
		need := want/2 + 1 // strict majority of `want`
		if done >= need {
			return true // quorum of done reached: success short-circuit
		}
		failed := terminal - done
		// If too many have already failed for a done-quorum to remain possible, decide
		// now (failure) rather than wait for the rest.
		return want-failed < need
	default: // all
		return false // all needs every fan terminal (handled by the guard above)
	}
}

// fanVerdict aggregates a DECIDABLE fan-out generation to job.StatusDone or job.StatusFailed
// under its join policy (P2, design §5.1, D15): all → done iff every fan is done;
// any → done iff ≥1 fan is done; quorum → done iff a strict majority (> want/2) of
// fans are done. Anything else is failed. `want` is the configured parallelism.
// Precondition: fanTerminal(fanJobs, want, join) == true.
func fanVerdict(fanJobs []*jobstore.JobRecord, want int, join string) string {
	done, _ := fanCounts(fanJobs)
	switch join {
	case joinAny:
		if done >= 1 {
			return job.StatusDone
		}
	case joinQuorum:
		if done >= want/2+1 {
			return job.StatusDone
		}
	default: // all
		if done >= want {
			return job.StatusDone
		}
	}
	return job.StatusFailed
}

// fanFailStatus returns a representative NON-done terminal status among a generation's
// fan jobs (failed/timeout/cancelled), for the failure message / skipped event. Falls
// back to job.StatusFailed when none is found (defensive; the verdict was failed).
func fanFailStatus(fanJobs []*jobstore.JobRecord) string {
	for _, j := range fanJobs {
		if job.IsTerminal(j.Status) && j.Status != job.StatusDone {
			return j.Status
		}
	}
	return job.StatusFailed
}

// fanFailExitCode returns a representative exit code of a NON-done terminal fan job,
// used to gate on_exit_codes retry (a fan-out step is retried if ANY failed fan is
// retryable). 0 when no failed fan is found (defensive).
func fanFailExitCode(fanJobs []*jobstore.JobRecord) int {
	for _, j := range fanJobs {
		if job.IsTerminal(j.Status) && j.Status != job.StatusDone {
			return j.ExitCode
		}
	}
	return 0
}

// cancelInflightFans best-effort cancels every NON-terminal fan job of a generation
// (P2): used by the any/quorum success short-circuit to stop the leftover running fans
// once the step is already decided done. Cancel is a stable no-op for an already-
// terminal job, so this is safe to call on the whole generation. Errors are ignored
// (the workflow has already advanced; a stray running fan finishing later is harmless).
func (e *Engine) cancelInflightFans(fanJobs []*jobstore.JobRecord) {
	for _, j := range fanJobs {
		if !job.IsTerminal(j.Status) {
			_ = e.ops.Cancel(j.ID)
		}
	}
}
