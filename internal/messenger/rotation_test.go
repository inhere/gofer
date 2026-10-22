package messenger

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// residentPID is the pid of the runner's current resident process (0 = none).
func residentPID(t *testing.T, m *Manager) int {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.processes["local"]
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// sendFake delivers one message through the fake resident process and returns the
// delivered output.
func sendFake(t *testing.T, m *Manager) string {
	t.Helper()
	command := append(testcmd.Cmd(t, "stream-json-fake"), "-p", "hello")
	got, err := m.Send(context.Background(), "local", "", "t", command)
	if err != nil {
		t.Fatalf("send = %v", err)
	}
	return got
}

// TestResidentMessengerRotatesAfterMaxMessages pins gofer-li3t: a resident process
// that handled the configured number of messages is replaced before the next one,
// the message is still delivered (not lost), and the rebuild is recorded as a
// rotation with the retired process's counters.
func TestResidentMessengerRotatesAfterMaxMessages(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	m := New("", time.Minute)
	m.SetRotation(2, 0)
	t.Cleanup(m.Close)

	for i := 0; i < 2; i++ {
		if got := sendFake(t, m); got != "已发送" {
			t.Fatalf("send %d output = %q", i+1, got)
		}
	}
	first := residentPID(t, m)
	if first == 0 {
		t.Fatal("no resident process after two sends")
	}
	// Second message: limit reached, so the third send must run on a new process.
	if got := sendFake(t, m); got != "已发送" {
		t.Fatalf("send after the limit output = %q, want the reply (not lost)", got)
	}
	if second := residentPID(t, m); second == first {
		t.Fatalf("resident pid = %d, want a new process after the message limit", second)
	}
	s := m.Snapshot("local")
	if len(s.Rotations) != 1 {
		t.Fatalf("rotations = %+v, want one max_messages rebuild", s.Rotations)
	}
	r := s.Rotations[0]
	if r.Reason != ReasonMaxMessages || r.Messages != 2 || r.At == 0 {
		t.Fatalf("rotation = %+v, want reason %q after 2 messages", r, ReasonMaxMessages)
	}
	if r.Usage == nil || r.Usage.InputTokens != 20 || r.Usage.OutputTokens != 10 || r.Usage.CostUSD != 0.5 {
		t.Fatalf("rotation usage = %+v, want the retired process's two result frames", r.Usage)
	}
	// The new process starts its own counters.
	if s.Messages != 1 || s.Usage == nil || s.Usage.InputTokens != 10 {
		t.Fatalf("snapshot after rotation = %+v, want the new process's own counters", s)
	}
	if s.MaxMessages != 2 || s.MaxAgeSec != 0 {
		t.Fatalf("snapshot limits = %d/%d, want 2/0", s.MaxMessages, s.MaxAgeSec)
	}
}

// TestResidentMessengerRotatesAfterMaxAge covers the lifetime limit: the rebuild
// happens on the next message once the process is older than maxAge.
func TestResidentMessengerRotatesAfterMaxAge(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	const maxAge = 50 * time.Millisecond
	m := New("", time.Minute)
	m.SetRotation(0, maxAge)
	t.Cleanup(m.Close)

	if got := sendFake(t, m); got != "已发送" {
		t.Fatalf("output = %q", got)
	}
	first := residentPID(t, m)
	m.mu.Lock()
	p := m.processes["local"]
	m.mu.Unlock()
	if p == nil {
		t.Fatal("no resident process after the first send")
	}
	wait.Until(t, 5*time.Second, "resident process older than the age limit", func() bool {
		return p.age(time.Now()) >= maxAge
	})
	if got := sendFake(t, m); got != "已发送" {
		t.Fatalf("output after the age limit = %q, want the reply (not lost)", got)
	}
	if second := residentPID(t, m); second == first {
		t.Fatalf("resident pid = %d, want a new process after the age limit", second)
	}
	s := m.Snapshot("local")
	if len(s.Rotations) != 1 || s.Rotations[0].Reason != ReasonMaxAge {
		t.Fatalf("rotations = %+v, want one max_age rebuild", s.Rotations)
	}
	if s.Rotations[0].Messages != 1 {
		t.Fatalf("rotation = %+v, want the one message the retired process handled", s.Rotations[0])
	}
}

// TestResidentMessengerZeroLimitsNeverRotate pins "0 = no limit": both limits off
// keep one process across many messages and record no rotation.
func TestResidentMessengerZeroLimitsNeverRotate(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	m := New("", time.Minute)
	m.SetRotation(0, 0)
	t.Cleanup(m.Close)

	for i := 0; i < 5; i++ {
		if got := sendFake(t, m); got != "已发送" {
			t.Fatalf("send %d output = %q", i+1, got)
		}
	}
	if pid := residentPID(t, m); pid == 0 {
		t.Fatal("no resident process")
	}
	s := m.Snapshot("local")
	if s.Messages != 5 {
		t.Fatalf("messages = %d, want 5 on the same process", s.Messages)
	}
	if len(s.Rotations) != 0 {
		t.Fatalf("rotations = %+v, want none with both limits off", s.Rotations)
	}
	if s.MaxMessages != 0 || s.MaxAgeSec != 0 {
		t.Fatalf("snapshot limits = %d/%d, want 0/0 (unlimited)", s.MaxMessages, s.MaxAgeSec)
	}
}

// TestResidentMessengerRecordsIdleRebuild pins the idle reason: the idle timer is
// one of the recorded rebuild events, so a reader can tell it apart from a limit.
func TestResidentMessengerRecordsIdleRebuild(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	const idle = 40 * time.Millisecond
	m := New("", idle)
	m.SetRotation(50, time.Hour)
	t.Cleanup(m.Close)

	if got := sendFake(t, m); got != "已发送" {
		t.Fatalf("output = %q", got)
	}
	wait.Until(t, 5*time.Second, "idle rebuild recorded", func() bool {
		for _, r := range m.Snapshot("local").Rotations {
			if r.Reason == ReasonIdle {
				return true
			}
		}
		return false
	})
	s := m.Snapshot("local")
	if s.Status != StatusStopped {
		t.Fatalf("status = %q, want stopped after the idle exit", s.Status)
	}
	if s.Rotations[0].Messages != 1 {
		t.Fatalf("rotation = %+v, want the one message the process handled", s.Rotations[0])
	}
}

// TestRotationsAreCapped pins the ring buffer: the history keeps the newest
// MaxRotations rebuilds.
func TestRotationsAreCapped(t *testing.T) {
	m := New("", time.Minute)
	for i := 0; i < MaxRotations+3; i++ {
		m.mu.Lock()
		m.statsFor("local").recordRotation(Rotation{At: int64(i), Reason: ReasonIdle})
		m.mu.Unlock()
	}
	s := m.Snapshot("local")
	if len(s.Rotations) != MaxRotations {
		t.Fatalf("rotations = %d, want %d", len(s.Rotations), MaxRotations)
	}
	if s.Rotations[0].At != int64(MaxRotations+2) {
		t.Fatalf("newest rotation = %d, want the newest first", s.Rotations[0].At)
	}
}
