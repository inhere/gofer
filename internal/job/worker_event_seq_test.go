package job

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func countingObserver(s *Service) func() int {
	var (
		mu sync.Mutex
		n  int
	)
	s.AddEventObserver(func(_, eventType string, _ map[string]any) {
		if eventType != EventJobVerifyStarted && eventType != EventJobPermissionRequested {
			return
		}
		mu.Lock()
		n++
		mu.Unlock()
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// TestRecordWorkerEventReplayAddsNothing: a numbered worker event that is replayed
// (same or lower seq) inserts no second row, advances nothing and does not re-fire the
// observers that drive webhooks / notifications; a genuinely new seq still applies, and
// the cursor survives a job row re-save.
func TestRecordWorkerEventReplayAddsNothing(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	if err := s.meta.UpsertJob(jobstore.JobRecord{ID: "wj-1", ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		Status: StatusRunning, Cwd: ".", RequestJSON: `{}`, StartedAt: 100, UpdatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	observed := countingObserver(s)

	s.recordWorkerEvent("wj-1", EventJobPermissionRequested, map[string]any{"interaction_id": "i-1"}, 1)
	s.recordWorkerEvent("wj-1", EventJobVerifyStarted, map[string]any{"command": "x"}, 2)
	// The reconnect replays both, plus a new one.
	s.recordWorkerEvent("wj-1", EventJobPermissionRequested, map[string]any{"interaction_id": "i-1"}, 1)
	s.recordWorkerEvent("wj-1", EventJobVerifyStarted, map[string]any{"command": "x"}, 2)
	s.recordWorkerEvent("wj-1", EventJobVerifyFinished, map[string]any{"status": "passed"}, 3)

	if got := len(jobEventDetails(t, s, "wj-1", EventJobPermissionRequested)); got != 1 {
		t.Fatalf("permission_requested rows = %d, want 1", got)
	}
	if got := len(jobEventDetails(t, s, "wj-1", EventJobVerifyStarted)); got != 1 {
		t.Fatalf("verify_started rows = %d, want 1", got)
	}
	if got := len(jobEventDetails(t, s, "wj-1", EventJobVerifyFinished)); got != 1 {
		t.Fatalf("verify_finished rows = %d, want the new seq applied", got)
	}
	if got := observed(); got != 2 {
		t.Fatalf("observers fired %d times, want 2 (replays must not re-notify)", got)
	}
	if got := s.WorkerEventSeq("wj-1"); got != 3 {
		t.Fatalf("WorkerEventSeq = %d, want 3", got)
	}
	// An unnumbered event (older worker) is unaffected by the cursor.
	s.recordEvent("wj-1", EventJobVerifyStarted, nil)
	evs, _ := s.ListJobEvents("wj-1", 0)
	n := 0
	for _, e := range evs {
		if e.Type == EventJobVerifyStarted {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("verify_started rows after an unnumbered event = %d, want 2", n)
	}
}

// TestAdoptedJobDedupsWorkerEvents: the adopt path (server restarted, worker
// reconnects) applies numbered events against the persisted cursor, so the events the
// previous process already recorded are not recorded or notified again.
func TestAdoptedJobDedupsWorkerEvents(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newWorkerTestService(t, root, &stubWorkerRunner{})
	rec := jobstore.JobRecord{ID: "adopted-ev", ProjectKey: "self", Agent: "exec", Runner: "remote-w1",
		WorkerID: "w1", WorkerInstanceID: "inst-1", Status: StatusRecovering, Cwd: ".",
		ResultDir: filepath.Join(root, "adopted-ev"), RequestJSON: `{}`, StartedAt: 100, UpdatedAt: 100}
	if err := os.MkdirAll(rec.ResultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.meta.UpsertJob(rec); err != nil {
		t.Fatal(err)
	}
	// The previous serve process applied events 1 and 2 before it went away.
	s.recordWorkerEvent(rec.ID, EventJobPermissionRequested, map[string]any{"interaction_id": "i-1"}, 1)
	s.recordWorkerEvent(rec.ID, EventJobVerifyStarted, map[string]any{"command": "x"}, 2)
	if got := s.WorkerEventSeq(rec.ID); got != 2 {
		t.Fatalf("persisted cursor = %d, want 2", got)
	}

	observed := countingObserver(s)
	adopted, lost := s.ReconcileAdoption("w1", "inst-1", []WorkerInflightJob{{JobID: rec.ID, Status: StatusRunning}}, AdoptBackend{})
	aj := adopted[rec.ID]
	if aj == nil {
		t.Fatalf("job not adopted (lost=%v)", lost)
	}
	// The worker replays 1..3 after the restart.
	aj.OnWorkerJobEvent(EventJobPermissionRequested, map[string]any{"interaction_id": "i-1"}, 1)
	aj.OnWorkerJobEvent(EventJobVerifyStarted, map[string]any{"command": "x"}, 2)
	aj.OnWorkerJobEvent(EventJobVerifyFinished, map[string]any{"status": "passed"}, 3)

	if got := len(jobEventDetails(t, s, rec.ID, EventJobPermissionRequested)); got != 1 {
		t.Fatalf("permission_requested rows = %d, want 1", got)
	}
	if got := len(jobEventDetails(t, s, rec.ID, EventJobVerifyFinished)); got != 1 {
		t.Fatalf("verify_finished rows = %d, want the new event applied once", got)
	}
	if got := observed(); got != 0 {
		t.Fatalf("observers fired %d times for replays, want 0", got)
	}
	if got := s.WorkerEventSeq(rec.ID); got != 3 {
		t.Fatalf("cursor after replay = %d, want 3", got)
	}
}
