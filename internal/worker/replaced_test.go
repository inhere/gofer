package worker

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func replacedErr() error {
	return fmt.Errorf("session: %w", websocket.CloseError{Code: websocket.StatusNormalClosure, Reason: replacedCloseReason})
}

// TestNoteReplacedBacksOffAfterThreeInFiveMinutes: the first two replacements only
// log; the third inside five minutes makes the worker wait 5 minutes (not exit).
func TestNoteReplacedBacksOffAfterThreeInFiveMinutes(t *testing.T) {
	cl := &Client{workerID: "w1", instanceID: "inst"}
	now := time.Unix(1_000_000, 0)
	cl.replaced.now = func() time.Time { return now }

	for i := 1; i <= 2; i++ {
		if d := cl.noteReplaced(replacedErr(), false); d != 0 {
			t.Fatalf("replacement %d paused %v", i, d)
		}
		now = now.Add(time.Minute)
	}
	if d := cl.noteReplaced(replacedErr(), false); d != replacedPause {
		t.Fatalf("third replacement pause = %v, want %v", d, replacedPause)
	}
	// The count starts over after a pause.
	if d := cl.noteReplaced(replacedErr(), false); d != 0 {
		t.Fatalf("replacement after the pause = %v, want 0", d)
	}
}

// TestNoteReplacedIgnoresOldReplacementsAndOtherCloses: replacements older than the
// window do not count; other errors and the upgrade handover never pause.
func TestNoteReplacedIgnoresOldReplacementsAndOtherCloses(t *testing.T) {
	cl := &Client{workerID: "w1", instanceID: "inst"}
	now := time.Unix(1_000_000, 0)
	cl.replaced.now = func() time.Time { return now }

	cl.noteReplaced(replacedErr(), false)
	now = now.Add(3 * time.Minute)
	cl.noteReplaced(replacedErr(), false)
	now = now.Add(3 * time.Minute) // the first one is now out of the window
	if d := cl.noteReplaced(replacedErr(), false); d != 0 {
		t.Fatalf("pause %v although only 2 replacements are inside the window", d)
	}
	if d := cl.noteReplaced(errors.New("network died"), false); d != 0 {
		t.Fatalf("non-replace error paused %v", d)
	}
	other := fmt.Errorf("x: %w", websocket.CloseError{Code: websocket.StatusNormalClosure, Reason: "shutdown"})
	if d := cl.noteReplaced(other, false); d != 0 {
		t.Fatalf("other close reason paused %v", d)
	}
	for i := 0; i < 5; i++ {
		if d := cl.noteReplaced(replacedErr(), true); d != 0 {
			t.Fatalf("handover (held) paused %v", d)
		}
	}
}
