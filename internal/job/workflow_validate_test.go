package job

import (
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestValidateRetry is the T1.1 submit-time validation table: on_failure must be a
// known value; on_failure=retry requires a well-formed retry block; a non-retry
// step must not carry a retry block; a v1 spec (no new fields) passes.
func TestValidateRetry(t *testing.T) {
	base := func(s StepSpec) WorkflowSpec { return WorkflowSpec{Steps: []StepSpec{s}} }
	mk := func(onFailure string, retry *RetryPolicy) StepSpec {
		return StepSpec{
			Name: "s", ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"true"}, OnFailure: onFailure, Retry: retry,
		}
	}

	cases := []struct {
		name    string
		spec    WorkflowSpec
		wantErr bool
	}{
		{"v1 no fields", base(mk("", nil)), false},
		{"explicit fail", base(mk(onFailureFail, nil)), false},
		{"continue", base(mk(onFailureContinue, nil)), false},
		{"retry valid", base(mk(onFailureRetry, &RetryPolicy{MaxAttempts: 3})), false},
		{"retry max=1", base(mk(onFailureRetry, &RetryPolicy{MaxAttempts: 1})), false},
		{"unknown on_failure", base(mk("explode", nil)), true},
		{"retry without block", base(mk(onFailureRetry, nil)), true},
		{"retry max=0", base(mk(onFailureRetry, &RetryPolicy{MaxAttempts: 0})), true},
		{"retry max over limit", base(mk(onFailureRetry, &RetryPolicy{MaxAttempts: maxRetryAttempts + 1})), true},
		{"retry block on fail", base(mk(onFailureFail, &RetryPolicy{MaxAttempts: 2})), true},
		{"retry block on continue", base(mk(onFailureContinue, &RetryPolicy{MaxAttempts: 2})), true},
		{"retry block on default", base(mk("", &RetryPolicy{MaxAttempts: 2})), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRetry(tc.spec)
			if tc.wantErr && err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
			// Invalid policies map to ErrInvalidRequest (400 at the HTTP boundary).
			if tc.wantErr && !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error %v is not ErrInvalidRequest", err)
			}
		})
	}
}

// TestSubmitWorkflowRejectsBadRetry asserts SubmitWorkflow surfaces a retry
// validation failure (no DB row, no job started).
func TestSubmitWorkflowRejectsBadRetry(t *testing.T) {
	s := newTestService(t, t.TempDir())
	bad := StepSpec{
		Name: "bad", ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"true"}, OnFailure: onFailureRetry, // retry but no block
	}
	_, err := s.SubmitWorkflow(WorkflowSpec{Steps: []StepSpec{bad}}, "alice")
	if err == nil {
		t.Fatal("expected SubmitWorkflow to reject on_failure=retry without a retry block")
	}
}

// TestStepToRequestDeterministicRequestID asserts the ⭐ idempotency core 1: a
// step-job's request_id is the deterministic "<wf>:s<step>:a<attempt>" so the C5
// unique index dedupes concurrent starts of the same (step, attempt).
func TestStepToRequestDeterministicRequestID(t *testing.T) {
	step := echoStep("x")
	a := stepToRequest(step, "wf-123", 2, 3, 0, "alice")
	if a.RequestID != "wf-123:s2:a3" {
		t.Fatalf("request_id = %q, want wf-123:s2:a3", a.RequestID)
	}
	// Same (wf, step, attempt) -> identical request_id (the dedupe key).
	b := stepToRequest(step, "wf-123", 2, 3, 0, "bob")
	if a.RequestID != b.RequestID {
		t.Fatalf("deterministic request_id mismatch: %q vs %q", a.RequestID, b.RequestID)
	}
	// Different attempt -> different request_id (a new job is allowed).
	c := stepToRequest(step, "wf-123", 2, 4, 0, "alice")
	if c.RequestID == a.RequestID {
		t.Fatalf("attempt change should change request_id, both = %q", a.RequestID)
	}
	// The submit-time pre-validation pass (wfID == "") leaves request_id empty.
	d := stepToRequest(step, "", 1, 1, 0, "alice")
	if d.RequestID != "" {
		t.Fatalf("pre-validation request_id = %q, want empty", d.RequestID)
	}
	// P2: a fan job (fanIndex>=1) appends the fan segment to the request_id.
	e := stepToRequest(step, "wf-123", 2, 3, 1, "alice")
	if e.RequestID != "wf-123:s2:a3:f1" {
		t.Fatalf("fan request_id = %q, want wf-123:s2:a3:f1", e.RequestID)
	}
	if e.FanIndex != 1 {
		t.Fatalf("fan_index = %d, want 1", e.FanIndex)
	}
	if a.Attempt != 3 {
		t.Fatalf("attempt = %d, want 3", a.Attempt)
	}
}

// TestJobLevelRetry is the T1.4 (E24) minimal job-level retry: a non-workflow job
// with a Retry policy (immediate backoff) that fails is re-run attempt+1. Using a
// flaky marker (fail once, succeed after) the FIRST job fails and a SECOND job
// (attempt 2) is auto-submitted and succeeds.
func TestJobLevelRetry(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	marker := root + "/joblevel.marker"
	script := "test -f " + marker + " || { touch " + marker + "; exit 7; }"

	first := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", script}, Cwd: ".", TimeoutSec: 30,
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}}, // immediate retry
	})
	if first.Status != StatusFailed {
		t.Fatalf("first attempt = %s, want failed", first.Status)
	}
	if first.Attempt != 1 {
		t.Fatalf("first attempt number = %d, want 1", first.Attempt)
	}

	// The retry is scheduled via time.AfterFunc(0). Poll the DB for an attempt-2 job
	// (a NEW job id, distinct from the first) that reaches done.
	retried := waitForRetryJob(t, s, 2)
	if retried.Status != StatusDone {
		t.Fatalf("retried attempt = %s, want done (marker now exists)", retried.Status)
	}
	if retried.ID == first.ID {
		t.Fatal("retry reused the same job id; expected a fresh job")
	}
	if retried.RequestID != "" {
		t.Fatalf("job-level retry should NOT carry a request_id (each attempt a distinct job), got %q", retried.RequestID)
	}
}

// TestJobLevelRetryNoPolicyUnchanged asserts a non-workflow job WITHOUT a Retry
// policy is not retried (向后兼容): a single failed job, no extra jobs created.
func TestJobLevelRetryNoPolicyUnchanged(t *testing.T) {
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "exit 7"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusFailed {
		t.Fatalf("job = %s, want failed", final.Status)
	}
	// No retry job should appear.
	jobs, err := s.meta.ListJobs(jobstore.ListQuery{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected exactly 1 job (no retry), got %d", len(jobs))
	}
}

// waitForRetryJob polls the metadata store for a non-workflow job at the given
// attempt that has reached a terminal state.
func waitForRetryJob(t *testing.T, s *Service, attempt int) jobstore.JobRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := s.meta.ListJobs(jobstore.ListQuery{})
		for _, j := range jobs {
			if j.WorkflowID == "" && j.Attempt == attempt && isTerminal(j.Status) {
				return j
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("retry job at attempt %d did not appear in time", attempt)
	return jobstore.JobRecord{}
}
