package httpapi

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// liveMessenger counts what the relay actually sent through the messenger seam. The count is
// guarded: the came-back flush runs on its own goroutine (that is the point of the hook).
type liveMessenger struct {
	mu    sync.Mutex
	sentN int
}

func (m *liveMessenger) SubmitMessenger(string, string, string, []string, string, string) (string, error) {
	m.mu.Lock()
	m.sentN++
	m.mu.Unlock()
	return "live-job", nil
}

func (*liveMessenger) MessengerJob(string) (bool, string, int, string, string, error) {
	return true, "done", 0, "已发送", "已发送", nil
}

func (m *liveMessenger) sent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sentN
}

// TestSeatPendingFlushedTheMomentTheSessionComesBack: a seat schedule that found its terminal
// offline queues the text (no 30-second wait), and the session's own hook beat that brings it
// back sends it at once — through the real state-write entry (POST /v1/sessions/{sid}/heartbeat),
// exactly once, and nothing for a session that sits in no seat.
func TestSeatPendingFlushedTheMomentTheSessionComesBack(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	msgr := &liveMessenger{}
	s.relay.SetMessenger(msgr)
	seats := s.seats()
	if seats == nil {
		t.Fatal("this server has no seat service")
	}

	x := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{"project_key": "p1", "name": "desk"}, 200)
	// The session registers with its own address: the queued text goes out through the messenger.
	for _, sid := range []string{"sess-live", "sess-free"} {
		register := do(t, s, http.MethodPost, "/v1/sessions", ceilTokPlain, map[string]any{
			"session_id": sid, "agent": "claude", "project_key": "p1", "runner": "local", "event": "SessionStart",
			"peer_name": "inspect-" + sid, "peer_messaging": true,
		})
		register.Body.Close()
	}
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+x.ID+"/bind", map[string]any{"ref": "sess-live"}, 200)

	// The seat's terminal goes silent and is marked offline by the sweep; the due run queues.
	if _, err := s.jobs.Meta().SetSessionState("sess-live", jobstore.SessionOffline); err != nil {
		t.Fatal(err)
	}
	req := job.SeatDeliverRequest{SeatID: x.ID, Text: "send the weekly report", ScheduleID: "sch-live", ScheduleName: "weekly", Origin: "plain"}
	if res, err := seats.DeliverToSeat(context.Background(), req); err != nil || res.Outcome != jobstore.ScheduleResultOffline {
		t.Fatalf("an offline seat = %+v, %v", res, err)
	}
	if n, err := s.jobs.Meta().SeatPendingCount("sch-live"); err != nil || n != 1 || msgr.sent() != 0 {
		t.Fatalf("queued = %d (%v), sent = %d: nothing may go out while the session is offline", n, err, msgr.sent())
	}

	// The terminal beats again — the beat the hook uses to say "I am here".
	beat := do(t, s, http.MethodPost, "/v1/sessions/sess-live/heartbeat", ceilTokPlain, map[string]any{"event": "Notification"})
	if beat.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat = %d", beat.StatusCode)
	}
	beat.Body.Close()
	wait.Until(t, 10*time.Second, "the queued text goes out as soon as the session is back", func() bool {
		n, err := s.jobs.Meta().SeatPendingCount("sch-live")
		return err == nil && n == 0 && msgr.sent() == 1
	})

	// A second beat sends nothing more: the queue is gone and the claim was conditional.
	again := do(t, s, http.MethodPost, "/v1/sessions/sess-live/heartbeat", ceilTokPlain, map[string]any{"event": "Notification"})
	again.Body.Close()
	wait.Until(t, 5*time.Second, "the second beat reports nothing new", func() bool {
		n, err := s.jobs.Meta().SeatPendingCount("sch-live")
		return err == nil && n == 0
	})
	if msgr.sent() != 1 {
		t.Fatalf("the queued text went out %d times, want once", msgr.sent())
	}

	// The session in no seat comes back too: the beat succeeds, and the flush the hook runs is a
	// no-op for it (no queue, no send, no error). A sync call reads the same code the hook body
	// reads, so nothing here depends on when the hook's goroutine runs.
	free := do(t, s, http.MethodPost, "/v1/sessions/sess-free/heartbeat", ceilTokPlain, map[string]any{"event": "Notification"})
	if free.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat of a session in no seat = %d", free.StatusCode)
	}
	free.Body.Close()
	if n, err := seats.FlushSeatOf(context.Background(), "sess-free"); err != nil || n != 0 {
		t.Fatalf("a session in no seat flushed %d (%v), want a no-op", n, err)
	}
	if n, err := s.jobs.Meta().SeatPendingCount("sch-live"); err != nil || n != 0 {
		t.Fatalf("a session in no seat touched the queue: %d (%v)", n, err)
	}
}
