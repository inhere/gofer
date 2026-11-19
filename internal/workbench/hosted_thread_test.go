package workbench

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestProjectJobThreadCarriesHosted: a resident session that never ends for being idle
// reaches the workbench list with `hosted` set, read from the session state the job row
// already persists, so the thread row can show the same badge as the job detail.
func TestProjectJobThreadCarriesHosted(t *testing.T) {
	snapshot := jobstore.WorkbenchSnapshot{Jobs: []jobstore.JobRecord{
		{ID: "hosted-turn", ProjectKey: "p", Agent: "acp", SessionID: "sid-hosted", Status: "awaiting_input",
			SessionStateJSON: `{"session":true,"turn_no":3,"hosted":true}`},
		{ID: "plain-turn", ProjectKey: "p", Agent: "acp", SessionID: "sid-plain", Status: "awaiting_input",
			SessionStateJSON: `{"session":true,"turn_no":1,"idle_timeout_sec":900}`},
	}}

	got := map[string]Thread{}
	for _, item := range projectThreads(snapshot, 0, nil, 1000000) {
		got[item.thread.ID] = item.thread
	}
	hosted, ok := got["s:sid-hosted"]
	if !ok {
		t.Fatalf("hosted thread missing: %#v", got)
	}
	if !hosted.Hosted || hosted.Turns != 3 {
		t.Fatalf("hosted thread = hosted %v / turns %d, want true / 3", hosted.Hosted, hosted.Turns)
	}
	plain, ok := got["s:sid-plain"]
	if !ok {
		t.Fatalf("plain thread missing: %#v", got)
	}
	if plain.Hosted {
		t.Fatalf("a plain session reported hosted: %#v", plain)
	}
}
