package job

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// retryRows lists the durable retry rows owned by a job (R2/AUTO-03).
func retryRows(t *testing.T, s *Service, jobID string) []jobstore.RetryRecord {
	t.Helper()
	rows, err := s.Meta().ListRetriesByJob(jobID)
	if err != nil {
		t.Fatalf("ListRetriesByJob(%s): %v", jobID, err)
	}
	return rows
}

// seedDueRetry writes the pending row a previous process would have left for a
// failed job, with a request_json the sweeper can submit as-is. It stands in for
// maybeRetryJob when a test wants to exercise ONLY the sweeper.
func seedDueRetry(t *testing.T, s *Service, src JobResult, attempt int) jobstore.RetryRecord {
	t.Helper()
	b, err := json.Marshal(JobRequest{
		ProjectKey: src.ProjectKey, Agent: src.Agent, Runner: src.Runner,
		Cmd: []string{"sh", "-c", "exit 0"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("marshal retry request: %v", err)
	}
	now := time.Now().Unix()
	rec := jobstore.RetryRecord{
		ID: jobstore.NewRetryID(), SourceJobID: src.ID, Attempt: attempt,
		RequestJSON: string(b), Reason: "exit_code=7",
		NextRunAt: now - 1, CreatedAt: now,
	}
	if err := s.Meta().InsertRetry(rec); err != nil {
		t.Fatalf("InsertRetry: %v", err)
	}
	return rec
}

// eventDetail returns the detail JSON of the LAST event of a type on a job ("" when
// the job never recorded it).
func eventDetail(t *testing.T, s *Service, jobID, eventType string) string {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", jobID, err)
	}
	detail := ""
	for _, ev := range evs {
		if ev.Type == eventType {
			detail = ev.Detail
		}
	}
	return detail
}

// waitEvent polls (bounded) until a job's timeline carries eventType and returns its
// detail. The retry events are recorded from the FINISH path, after the terminal row
// is already persisted, so a test that waited for the status could legitimately read
// the job before its exhausted event exists.
func waitEvent(t *testing.T, s *Service, jobID, eventType string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if d := eventDetail(t, s, jobID, eventType); d != "" {
			return d
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never recorded %s", jobID, eventType)
	return ""
}

// TestRetryRowWrittenOnFailure: a failed job with a retry budget leaves exactly one
// PENDING row (attempt=2, reason=exit_code=N, due after the policy's backoff) — the
// durable half of AUTO-03. Nothing is submitted in-process any more.
func TestRetryRowWrittenOnFailure(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}},
	})
	if final.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed", final.Status)
	}

	rows := retryRows(t, s, final.ID)
	if len(rows) != 1 {
		t.Fatalf("retry rows = %+v, want exactly one pending row", rows)
	}
	rec := rows[0]
	if rec.State != jobstore.RetryPending {
		t.Fatalf("retry state = %q, want pending", rec.State)
	}
	if rec.Attempt != 2 {
		t.Fatalf("retry attempt = %d, want 2 (the attempt about to run)", rec.Attempt)
	}
	if !strings.Contains(rec.Reason, "exit_code=7") {
		t.Fatalf("retry reason = %q, want it to name the exit code", rec.Reason)
	}
	if rec.NextRunAt > time.Now().Unix() {
		t.Fatalf("retry next_run_at = %d, want it due now (backoff_sec [0])", rec.NextRunAt)
	}
	if !strings.Contains(rec.RequestJSON, `"agent":"exec"`) {
		t.Fatalf("request_json = %q, want the failed job's request", rec.RequestJSON)
	}

	detail := eventDetail(t, s, final.ID, EventJobRetryScheduled)
	if !strings.Contains(detail, `"attempt":2`) || !strings.Contains(detail, rec.ID) {
		t.Fatalf("job.retry_scheduled detail = %q, want retry_id %s and attempt 2", detail, rec.ID)
	}
}

// TestSuccessNoRetryRow: a job that succeeds never schedules a retry, even with a
// budget configured.
func TestSuccessNoRetryRow(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 0"}, Cwd: ".", TimeoutSec: 30,
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}},
	})
	if final.Status != StatusDone {
		t.Fatalf("setup: status = %s, want done", final.Status)
	}
	if rows := retryRows(t, s, final.ID); len(rows) != 0 {
		t.Fatalf("a done job must not schedule a retry: %+v", rows)
	}
}

// TestCancelledNeverRetried: a cancel is intentional — it is never re-run.
func TestCancelledNeverRetried(t *testing.T) {
	s := newTestService(t, t.TempDir())
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 30,
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForStatus(t, s, res.ID, StatusRunning, 5*time.Second)
	if err := s.Cancel(res.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	final, _ := s.Wait(res.ID)
	if final.Status != StatusCancelled {
		t.Fatalf("setup: status = %s, want cancelled", final.Status)
	}
	if rows := retryRows(t, s, final.ID); len(rows) != 0 {
		t.Fatalf("a cancelled job must not schedule a retry: %+v", rows)
	}
}

// TestTimeoutNeverRetried: a timeout means the work itself overran its deadline —
// re-running it identically is not a retry, it is a loop.
func TestTimeoutNeverRetried(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 1,
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}},
	})
	if final.Status != StatusTimeout {
		t.Fatalf("setup: status = %s, want timeout (err=%s)", final.Status, final.Error)
	}
	if rows := retryRows(t, s, final.ID); len(rows) != 0 {
		t.Fatalf("a timed-out job must not schedule a retry: %+v", rows)
	}
}

// TestNeedsReviewNoRetryRow: a job parked for人工验收 is not finished — a human is
// already in the loop, and a retry would run work nobody has judged yet.
func TestNeedsReviewNoRetryRow(t *testing.T) {
	s := newReviewService(t, t.TempDir(), reviewServiceOpts{successCode: 0, requireReview: true})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, Review: true,
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}},
	})
	if final.Status != StatusNeedsReview {
		t.Fatalf("setup: status = %s, want needs_review", final.Status)
	}
	if rows := retryRows(t, s, final.ID); len(rows) != 0 {
		t.Fatalf("a needs_review job must not schedule a retry: %+v", rows)
	}
}

// TestAutoResumeWinsOverRetry: the transient-failure family (auto-resume / the
// AUTO-05 stall takeover / fallback) owns such a failure — its own continuation is
// started, and a durable retry is NOT scheduled on top of it. That holds whether or
// not auto-resume is enabled: a transient failure is never retried as a plain job.
func TestAutoResumeWinsOverRetry(t *testing.T) {
	const transient = "ERROR: Selected model is at capacity. Please try a different model."

	root := t.TempDir()
	s := newAutoResumeService(t, root, transient, nil)
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-cap",
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}},
	})
	if src.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed", src.Status)
	}
	src = waitAutoResumed(t, s, src.ID, true)
	_, _ = s.Wait(src.AutoResumedBy)
	// The takeover is submitted from the finish path; poll (bounded) so a retry row
	// that shows up late still fails the test rather than being read past.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if rows := retryRows(t, s, src.ID); len(rows) != 0 {
			t.Fatalf("an auto-resumed transient failure must not schedule a retry: %+v", rows)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Same failure with the takeover DISABLED (auto_resume_max=0): still no retry —
	// transient failures are the takeover family's, not the retry's.
	s2 := newAutoResumeService(t, t.TempDir(), transient, intPtr(0))
	final := submitAndWait(t, s2, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 30, SessionID: "sess-cap",
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}},
	})
	if final.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed", final.Status)
	}
	if rows := retryRows(t, s2, final.ID); len(rows) != 0 {
		t.Fatalf("a transient failure must not schedule a retry even without auto-resume: %+v", rows)
	}
}

// TestRetrySweeperSubmitsDue: one sweeper pass claims a due row, submits it as a
// NEW job (attempt from the row, retry tags) and marks the row done.
func TestRetrySweeperSubmitsDue(t *testing.T) {
	s := newTestService(t, t.TempDir())
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
	})
	rec := seedDueRetry(t, s, src, 2)

	started, failed, err := s.SweepDueRetries(time.Now().Unix(), 10, 60)
	if err != nil {
		t.Fatalf("SweepDueRetries: %v", err)
	}
	if started != 1 || failed != 0 {
		t.Fatalf("sweep started/failed = %d/%d, want 1/0", started, failed)
	}

	rows := retryRows(t, s, src.ID)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the claimed one", rows)
	}
	if rows[0].State != jobstore.RetryDone || rows[0].NewJobID == "" || rows[0].LeaseUntil != 0 {
		t.Fatalf("row after the sweep = %+v, want done + new_job_id + no lease", rows[0])
	}
	if rows[0].ID != rec.ID {
		t.Fatalf("row id = %q, want the seeded %q", rows[0].ID, rec.ID)
	}

	final := waitStatus(t, s, rows[0].NewJobID, 10*time.Second, StatusDone)
	if final.Attempt != 2 {
		t.Fatalf("retried attempt = %d, want 2 (from the row)", final.Attempt)
	}
	got := strings.Join(final.Tags, ",")
	if !strings.Contains(got, "retry") || !strings.Contains(got, "retry_of:"+src.ID) {
		t.Fatalf("retried tags = %q, want retry + retry_of:%s", got, src.ID)
	}

	// The start is announced on the SOURCE job's timeline, and a second pass has
	// nothing left to do.
	detail := eventDetail(t, s, src.ID, EventJobRetryStarted)
	if !strings.Contains(detail, rec.ID) || !strings.Contains(detail, final.ID) {
		t.Fatalf("job.retry_started detail = %q, want retry_id %s + new_job_id %s", detail, rec.ID, final.ID)
	}
	again, failed2, err := s.SweepDueRetries(time.Now().Unix(), 10, 60)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again != 0 || failed2 != 0 {
		t.Fatalf("second sweep started/failed = %d/%d, want 0/0", again, failed2)
	}
}

// TestRetrySurvivesRestart is the core AUTO-03 assertion: a pending retry written
// by one process is still submitted by the sweeper of the NEXT one. The first
// "process" is closed without ever sweeping.
func TestRetrySurvivesRestart(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "gofer.db")

	s1 := newTestServiceWithDB(t, root, dbPath)
	src := submitAndWait(t, s1, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
	})
	seedDueRetry(t, s1, src, 2)
	if err := s1.Meta().Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	// A brand-new Service over the same db file — the "restart".
	s2 := newTestServiceWithDB(t, root, dbPath)
	started, failed, err := s2.SweepDueRetries(time.Now().Unix(), 10, 60)
	if err != nil {
		t.Fatalf("SweepDueRetries after restart: %v", err)
	}
	if started != 1 || failed != 0 {
		t.Fatalf("sweep after restart started/failed = %d/%d, want 1/0 (the retry must survive)", started, failed)
	}

	rows := retryRows(t, s2, src.ID)
	if len(rows) != 1 || rows[0].State != jobstore.RetryDone || rows[0].NewJobID == "" {
		t.Fatalf("row after restart = %+v, want a done row with the new job id", rows)
	}
	waitStatus(t, s2, rows[0].NewJobID, 10*time.Second, StatusDone)
}

// TestRetryExhaustedEvent: the LAST attempt failing is the "a human must look at
// this" signal — job.retry_exhausted with the attempt count, and no further row.
func TestRetryExhaustedEvent(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
		Retry: &RetryPolicy{MaxAttempts: 2, BackoffSec: []int{0}},
	})
	if final.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed", final.Status)
	}
	rows := retryRows(t, s, final.ID)
	if len(rows) != 1 || rows[0].Attempt != 2 {
		t.Fatalf("rows = %+v, want the attempt-2 row (2 attempts total)", rows)
	}

	if _, _, err := s.SweepDueRetries(time.Now().Unix(), 10, 60); err != nil {
		t.Fatalf("SweepDueRetries: %v", err)
	}
	rows = retryRows(t, s, final.ID)
	if rows[0].NewJobID == "" {
		t.Fatalf("row = %+v, want the submitted job id", rows[0])
	}
	second := waitStatus(t, s, rows[0].NewJobID, 10*time.Second, StatusFailed)

	if detail := waitEvent(t, s, second.ID, EventJobRetryExhausted); !strings.Contains(detail, `"attempts":2`) {
		t.Fatalf("job.retry_exhausted detail = %q, want attempts=2", detail)
	}
	if extra := retryRows(t, s, second.ID); len(extra) != 0 {
		t.Fatalf("the exhausted attempt must not schedule another retry: %+v", extra)
	}
}

// TestRetryFromConfigLevel: the policy does not have to come from the request — a
// server/agent/project level `retry` schedules the retry too (the point of the R2
// 触发面扩大: nobody was using a policy only a caller could pass), and the row then
// carries the RESOLVED policy, so the chain explains itself (`attempt 2/2`) and keeps
// the policy it was scheduled under.
func TestRetryFromConfigLevel(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Retry: &config.RetryPolicy{MaxAttempts: 2, BackoffSec: []int{0}}},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusFailed {
		t.Fatalf("setup: status = %s, want failed", final.Status)
	}
	rows := retryRows(t, s, final.ID)
	if len(rows) != 1 || rows[0].Attempt != 2 {
		t.Fatalf("rows = %+v, want one row for attempt 2 (from server.retry)", rows)
	}
	if got := RetryMaxAttempts(rows[0]); got != 2 {
		t.Fatalf("row ceiling = %d, want 2 (the resolved server-level policy rides the row)", got)
	}

	if _, _, err := s.SweepDueRetries(time.Now().Unix(), 10, 60); err != nil {
		t.Fatalf("SweepDueRetries: %v", err)
	}
	rows = retryRows(t, s, final.ID)
	second := waitStatus(t, s, rows[0].NewJobID, 10*time.Second, StatusFailed)
	if detail := waitEvent(t, s, second.ID, EventJobRetryExhausted); !strings.Contains(detail, `"attempts":2`) {
		t.Fatalf("job.retry_exhausted detail = %q, want attempts=2", detail)
	}
}
