package client

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
)

// gofer-5foz: a sync submit is held open by the server for up to its wait cap, which
// equals the 30s client default, so the HTTP deadline must sit above that cap.
func TestSubmitTimeoutCoversSyncWait(t *testing.T) {
	if got := submitTimeout(job.JobRequest{}); got != 0 {
		t.Fatalf("async submit must keep the default deadline, got %v", got)
	}
	def := submitTimeout(job.JobRequest{Sync: true})
	if def <= 30*time.Second {
		t.Fatalf("sync default deadline %v must exceed the 30s server wait", def)
	}
	max := submitTimeout(job.JobRequest{Sync: true, WaitTimeoutSec: 9999})
	if max < job.SyncWaitDuration(9999)+time.Second {
		t.Fatalf("sync max deadline %v must exceed the server cap", max)
	}
}
