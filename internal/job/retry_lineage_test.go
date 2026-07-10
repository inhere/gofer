package job

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestJobLevelRetryPreservesSourceJobID(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	marker := filepath.ToSlash(filepath.Join(root, "retry-lineage.marker"))
	script := fmt.Sprintf("test -f %q || { touch %q; exit 7; }", marker, marker)

	first := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		SourceJobID: "job-src-retry", Cmd: []string{"sh", "-c", script}, Cwd: ".", TimeoutSec: 30,
		Retry: &RetryPolicy{MaxAttempts: 3, BackoffSec: []int{0}},
	})
	if first.Status != StatusFailed {
		t.Fatalf("first attempt = %s, want failed", first.Status)
	}
	if first.SourceJobID != "job-src-retry" {
		t.Fatalf("first source_job_id = %q, want job-src-retry", first.SourceJobID)
	}

	retried := waitForRetryRecord(t, s, 2)
	if retried.Status != StatusDone {
		t.Fatalf("retried attempt = %s, want done", retried.Status)
	}
	if retried.ID == first.ID {
		t.Fatal("retry reused the same job id; expected a fresh job")
	}
	if retried.SourceJobID != "job-src-retry" {
		t.Fatalf("retried source_job_id = %q, want job-src-retry", retried.SourceJobID)
	}
}

func waitForRetryRecord(t *testing.T, s *Service, attempt int) jobstore.JobRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := s.Meta().ListJobs(jobstore.ListQuery{})
		for _, j := range jobs {
			if j.WorkflowID == "" && j.Attempt == attempt && IsTerminal(j.Status) {
				return j
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("retry job at attempt %d did not appear in time", attempt)
	return jobstore.JobRecord{}
}
