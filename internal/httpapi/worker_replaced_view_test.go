package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestWorkerViewShowsLastReplaced: GET /v1/workers/{id} carries the newest
// worker.replaced observation, and nothing for a worker that was never replaced.
func TestWorkerViewShowsLastReplaced(t *testing.T) {
	s, _, _ := newUpgradeServer(t, map[string]WorkerStatus{"w1": connectedWorker("linux", "amd64", 16)})
	var view struct {
		LastReplaced *workerReplacedView `json:"last_replaced"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/workers/w1", testToken, nil), &view)
	if view.LastReplaced != nil {
		t.Fatalf("never-replaced worker shows %+v", view.LastReplaced)
	}

	s.jobs.RecordScopedEvent(job.WorkerEventScope("w1"), job.EventWorkerReplaced, "", map[string]any{
		"old_instance": "inst-a", "new_instance": "inst-b", "old_remote": "10.0.0.1:1", "new_remote": "10.0.0.2:2",
	})
	s.jobs.RecordScopedEvent(job.WorkerEventScope("w2"), job.EventWorkerReplaced, "", map[string]any{"new_instance": "other"})
	view.LastReplaced = nil
	decode(t, do(t, s, http.MethodGet, "/v1/workers/w1", testToken, nil), &view)
	got := view.LastReplaced
	if got == nil || got.OldInstance != "inst-a" || got.NewInstance != "inst-b" || got.NewRemote != "10.0.0.2:2" || got.At == 0 {
		t.Fatalf("last_replaced = %+v", got)
	}
}
