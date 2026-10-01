package jobstore

import "testing"

func TestRemoteSessionAdoptedAfterServerRestart(t *testing.T) {
	want := map[string]bool{"awaiting_input": false, "pending_interaction": false}
	for _, status := range orphanWorkerJobStatuses {
		if _, ok := want[status]; ok {
			want[status] = true
		}
	}
	for status, found := range want {
		if !found {
			t.Fatalf("orphan worker status %q is not recoverable", status)
		}
	}
}

func TestRemoteSessionFailsOnWorkerRestart(t *testing.T) {
	if len(orphanWorkerJobStatuses) == 0 {
		t.Fatal("worker orphan status set is empty")
	}
}
