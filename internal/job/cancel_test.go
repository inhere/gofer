package job

import (
	"testing"
	"time"
)

// blockingMetrics is a MetricsSink that PARKS Submit's tail: JobSubmitted is the
// last thing Submit does before it launches the execute goroutine, so a test holding
// it there observes the exact state F3 (bd h-aii-tcpm) is about — the job is
// published and persisted (`queued`), while its execute goroutine has not run yet and
// therefore has not installed the entry's cancellable context.
type blockingMetrics struct {
	entered chan struct{} // buffered(1): signalled once JobSubmitted is entered
	release chan struct{} // closed by the test to let Submit finish
}

func (m *blockingMetrics) JobSubmitted(string, string, string, string) {
	select {
	case m.entered <- struct{}{}:
	default:
	}
	<-m.release
}

func (m *blockingMetrics) JobTerminal(string, string, string, string, string, float64) {}
func (m *blockingMetrics) WorkflowTerminal(string, float64)                            {}

// TestCancelOnPublishedJobBeforeExecuteIsHonoured (F3, bd h-aii-tcpm): a cancel that
// arrives for a job the service has ALREADY published (visible as `queued`, and the
// id is therefore cancellable from every surface) must be honoured even though the
// execute goroutine has not installed the job's cancellable context yet. Cancelling
// there used to be a silent no-op — the intent was dropped and the job ran to
// completion — which is exactly what a hub-issued cancel hits: the hub dispatches,
// the worker publishes the local job, and the cancel frame lands in that window.
func TestCancelOnPublishedJobBeforeExecuteIsHonoured(t *testing.T) {
	s := newTestService(t, t.TempDir())
	m := &blockingMetrics{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.SetMetrics(m)

	submitErr := make(chan error, 1)
	go func() {
		_, err := s.Submit(JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{"sleep", "5"}, Cwd: ".", TimeoutSec: 60,
		})
		submitErr <- err
	}()

	// The job is a fact (published + persisted) but execute has not started: this is
	// the window, held open by the metrics sink.
	select {
	case <-m.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Submit never reached its post-publication hook")
	}
	id := waitOnlyJobID(t, s)
	if r, ok := s.Get(id); !ok || r.Status != StatusQueued {
		t.Fatalf("job %s = %+v, want a published queued job", id, r)
	}

	if err := s.Cancel(id); err != nil {
		t.Fatalf("Cancel on a published job: %v", err)
	}
	close(m.release)
	if err := <-submitErr; err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The cancel was issued BEFORE execute started, so the job must never run its
	// command to completion: it reaches cancelled, promptly.
	waitForStatus(t, s, id, StatusCancelled, 2*time.Second)
}

// waitOnlyJobID polls the service until exactly one job is visible and returns its
// id (Submit has not returned yet, so the id is not otherwise available).
func waitOnlyJobID(t *testing.T, s *Service) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, err := s.ListJobs(ListOpts{Limit: 10})
		if err != nil {
			t.Fatalf("list jobs: %v", err)
		}
		if len(list) == 1 {
			return list[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	list, _ := s.ListJobs(ListOpts{Limit: 10})
	t.Fatalf("expected exactly one job, got %d", len(list))
	return ""
}
