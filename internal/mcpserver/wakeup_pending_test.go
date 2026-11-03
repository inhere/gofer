package mcpserver

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestWakeupViewShowsPendingDelivery: the gofer_wakeup_* output reports the pending-delivery
// sentinel as pending_delivery and never as continuation_job_id, so an agent driving MCP
// does not read `await_input` as a job id. The in-flight claim stays masked as before.
func TestWakeupViewShowsPendingDelivery(t *testing.T) {
	t.Parallel()
	pending := toWakeupView(jobstore.WakeupRecord{
		ID: "wk-1", JobID: "job-1", Kind: jobstore.WakeupKindEvent,
		ContinuationJobID: jobstore.WakeupPendingSay, Enabled: 1,
	})
	if !pending.PendingDelivery {
		t.Errorf("pending_delivery = false, want true for the sentinel row")
	}
	if pending.ContinuationJobID != "" {
		t.Errorf("continuation_job_id = %q, want it empty (the sentinel is not a job)", pending.ContinuationJobID)
	}

	claim := toWakeupView(jobstore.WakeupRecord{
		ID: "wk-2", JobID: "job-1", Kind: jobstore.WakeupKindEvent,
		ContinuationJobID: jobstore.WakeupClaimPending, Enabled: 1,
	})
	if claim.PendingDelivery || claim.ContinuationJobID != "" {
		t.Errorf("in-flight claim = %+v, want neither a job id nor a pending delivery", claim)
	}
}
