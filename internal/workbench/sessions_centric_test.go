package workbench

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestProjectThreadsOnlyIncludesInteractiveAndContinuousSessions(t *testing.T) {
	snapshot := jobstore.WorkbenchSnapshot{Jobs: []jobstore.JobRecord{
		{ID: "batch", ProjectKey: "p", Agent: "acp", SessionID: "sid-batch", Status: "done"},
		{ID: "one-shot-acp", ProjectKey: "p", Agent: "acp", SessionID: "sid-one", Status: "done"},
		{ID: "pty", ProjectKey: "p", Agent: "tty", Interactive: true, SessionID: "sid-pty", Status: "done"},
		{ID: "continuous", ProjectKey: "p", Agent: "acp", SessionID: "sid-cont", SessionStateJSON: `{"session":true,"turn_no":1}`, Status: "awaiting_input"},
	}}

	got := projectThreads(snapshot, 0)
	if len(got) != 2 {
		t.Fatalf("projected %d threads, want 2", len(got))
	}
	seen := map[string]bool{}
	for _, item := range got {
		seen[item.thread.ID] = true
	}
	if seen["s:sid-batch"] || seen["s:sid-one"] {
		t.Fatalf("one-shot jobs leaked into workbench: %#v", seen)
	}
	if !seen["s:sid-pty"] || !seen["s:sid-cont"] {
		t.Fatalf("session threads missing: %#v", seen)
	}
}
