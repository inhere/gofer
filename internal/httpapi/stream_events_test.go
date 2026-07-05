package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/streaming"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestStreamEventFrames connects to an already-terminal job's SSE stream and
// asserts the E13 `event` frames are replayed (submitted -> running -> terminal,
// seq-ordered) alongside the unchanged log/status/end frames — proving pumpEvents
// is woven into the stream without regressing the existing frame types.
func TestStreamEventFrames(t *testing.T) {
	s := newTestServer(t, testToken, false)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	id := createStreamJob(t, srv.URL, testcmd.Cmd(t, "printf", "hello-events\n"))
	final := waitDoneHTTP(t, srv.URL, id)
	if final.Status != job.StatusDone {
		t.Fatalf("setup: status=%q, want done", final.Status)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, scanner := openStream(t, ctx, srv.URL, id, "")
	defer resp.Body.Close()

	frames := readFrames(t, resp, scanner, 10*time.Second)

	var events []streaming.EventFrame
	var sawLog, sawStatus, sawEnd bool
	for _, ev := range frames {
		switch ev.Event {
		case "event":
			var ef streaming.EventFrame
			if err := json.Unmarshal([]byte(ev.Data), &ef); err != nil {
				t.Fatalf("bad event frame %q: %v", ev.Data, err)
			}
			events = append(events, ef)
		case "log":
			sawLog = true
		case "status":
			sawStatus = true
		case "end":
			sawEnd = true
		}
	}

	// Regression: the pre-E13 frame types are still present.
	if !sawLog || !sawStatus || !sawEnd {
		t.Fatalf("missing baseline frames: log=%v status=%v end=%v", sawLog, sawStatus, sawEnd)
	}

	// E13: the lifecycle events were replayed, in seq order.
	if len(events) < 3 {
		t.Fatalf("expected >=3 event frames, got %d (%+v)", len(events), events)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("event frames not in seq order: %+v", events)
		}
	}
	if events[0].Type != job.EventJobSubmitted {
		t.Fatalf("first event frame=%q, want %q", events[0].Type, job.EventJobSubmitted)
	}
	var sawTerminal bool
	for _, ef := range events {
		if ef.Type == job.EventJobTerminal {
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatalf("event frames missing %q: %+v", job.EventJobTerminal, events)
	}
}
