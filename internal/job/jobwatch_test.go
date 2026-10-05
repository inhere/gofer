package job

import (
	"testing"
	"time"
)

// TestWatchJobSignals: persist and recordEvent wake a subscriber of that job only,
// bursts coalesce, and cancel unsubscribes.
func TestWatchJobSignals(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	ch, cancel := s.WatchJob("w1")
	other, cancelOther := s.WatchJob("w2")
	defer cancelOther()

	expect := func(c <-chan struct{}, want bool, what string) {
		t.Helper()
		select {
		case <-c:
			if !want {
				t.Fatalf("%s: unexpected signal", what)
			}
		case <-time.After(150 * time.Millisecond):
			if want {
				t.Fatalf("%s: no signal", what)
			}
		}
	}
	s.recordEvent("w1", "job.running", nil)
	expect(ch, true, "recordEvent")
	expect(other, false, "other job")

	_ = s.persist(JobResult{ID: "w1", ProjectKey: "self", Status: StatusRunning, ResultDir: t.TempDir()})
	s.signalJob("w1")
	s.signalJob("w1")
	expect(ch, true, "persist/burst")
	expect(ch, false, "coalesced burst leaves one wake-up")

	cancel()
	s.signalJob("w1")
	expect(ch, false, "after cancel")
}
