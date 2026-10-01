package job

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
)

func TestACPResumeCreatesContinuousSession(t *testing.T) {
	t.Parallel()
	s := newACPService(t, t.TempDir(), acptest.Options{})
	src := acpSubmit(t, s, 30)
	if src.Status != StatusDone || src.SessionID == "" {
		t.Fatalf("source = %s/%q, want done with session", src.Status, src.SessionID)
	}

	resumed, err := s.ResumeJob(src.ID, "", "", "caller-continuous")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	if !resumed.Session {
		t.Fatalf("resumed session = false, want a continuous session")
	}
	if resumed.SessionID != src.SessionID {
		t.Fatalf("resumed session_id = %q, want %q", resumed.SessionID, src.SessionID)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := s.Get(resumed.ID)
		if ok && current.Status == StatusAwaitingInput {
			if err := s.EndSession(resumed.ID); err != nil {
				t.Fatalf("EndSession: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, _ := s.Get(resumed.ID)
	t.Fatalf("resumed status = %s, want awaiting_input", current.Status)
}
