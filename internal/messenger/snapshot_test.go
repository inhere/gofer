package messenger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func TestSnapshotLifecycleAndDeliveries(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	m := New("", time.Minute)
	if s := m.Snapshot("local"); s.Status != StatusStopped || len(s.Deliveries) != 0 {
		t.Fatalf("fresh snapshot = %+v", s)
	}
	command := append(testcmd.Cmd(t, "stream-json-fake"), "-p", "请转发\n[来自 web，alice] "+strings.Repeat("长", 300))
	if _, err := m.Send(context.Background(), "local", "", "proj-a", command); err != nil {
		t.Fatal(err)
	}
	s := m.Snapshot("local")
	if s.Status != StatusIdle || s.StartedAt == 0 || s.LastUsedAt == 0 || s.IdleDeadline == 0 {
		t.Fatalf("after send snapshot = %+v, want idle with timestamps", s)
	}
	if len(s.Deliveries) != 1 || !s.Deliveries[0].OK || s.Deliveries[0].Target != "proj-a" || s.Deliveries[0].Op != "send" {
		t.Fatalf("deliveries = %+v", s.Deliveries)
	}
	if got := []rune(s.Deliveries[0].Message); len(got) > maxSummaryRunes+1 {
		t.Fatalf("message excerpt has %d runes, want <= %d", len(got), maxSummaryRunes+1)
	}
	if !strings.Contains(s.StderrTail, "resident messenger started") {
		t.Fatalf("stderr tail = %q, want the start marker", s.StderrTail)
	}
}

func TestSnapshotBusyWhileRequestInFlight(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	m := New("", time.Minute)
	done := m.begin("local", "send", "x", "hi")
	if got := m.Status("local"); got != StatusBusy {
		t.Fatalf("status during request = %q, want busy", got)
	}
	done(context.DeadlineExceeded)
	s := m.Snapshot("local")
	if s.Status != StatusStopped {
		t.Fatalf("status after request = %q, want stopped (no process)", s.Status)
	}
	if len(s.Deliveries) != 1 || s.Deliveries[0].OK || s.Deliveries[0].Error == "" {
		t.Fatalf("failed delivery not recorded: %+v", s.Deliveries)
	}
}

func TestSnapshotKeepsNewestTwentyDeliveriesNewestFirst(t *testing.T) {
	m := New("", time.Minute)
	for i := 0; i < MaxDeliveries+5; i++ {
		m.begin("local", "send", string(rune('a'+i%26)), "m")(nil)
	}
	s := m.Snapshot("local")
	if len(s.Deliveries) != MaxDeliveries {
		t.Fatalf("deliveries = %d, want %d", len(s.Deliveries), MaxDeliveries)
	}
}

func TestStderrTailIsBounded(t *testing.T) {
	tb := &tailBuffer{max: 16}
	_, _ = tb.Write([]byte(strings.Repeat("x", 100) + "END"))
	if got := tb.String(); len(got) != 16 || !strings.HasSuffix(got, "END") {
		t.Fatalf("tail = %q", got)
	}
}

func TestSnapshotStatusStoppedAfterProcessStops(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	m := New("", time.Minute)
	command := append(testcmd.Cmd(t, "stream-json-fake"), "-p", "hello")
	if _, err := m.Send(context.Background(), "local", "", "t", command); err != nil {
		t.Fatal(err)
	}
	p, _ := m.process("local", "", command)
	p.stop()
	if got := m.Status("local"); got != StatusStopped {
		t.Fatalf("status after stop = %q, want stopped", got)
	}
	if s := m.Snapshot("local"); len(s.Deliveries) != 1 || s.StderrTail == "" {
		t.Fatalf("history must survive the process: %+v", s)
	}
}

func TestListAgentsReadsToolResult(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON_AGENTS", "1")
	m := New("", time.Minute)
	command := testcmd.Cmd(t)
	got, err := m.ListAgents(context.Background(), "local", "", command)
	if err != nil {
		t.Fatal(err)
	}
	if got.Self != "msgr" || len(got.Agents) != 2 {
		t.Fatalf("list = %+v", got)
	}
	a := got.Agents[1]
	if a.Name != "proj-b" || a.ShortID != "e7bd68" || a.Kind != "interactive" || a.Status != "busy" || a.Started != "32m ago" {
		t.Fatalf("agent = %+v", a)
	}
	if strings.Contains(got.RawOutput, "retold") || !strings.Contains(got.RawOutput, "Peer sessions (2)") {
		t.Fatalf("raw_output must be the tool_result, not the model retelling: %q", got.RawOutput)
	}
	// A second call reuses the process and also lands in the delivery history.
	if _, err := m.ListAgents(context.Background(), "local", "", command); err != nil {
		t.Fatal(err)
	}
	s := m.Snapshot("local")
	if len(s.Deliveries) != 2 || s.Deliveries[0].Op != "list_agents" || !strings.Contains(s.StderrTail, "fake-stderr") {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestParseAgentsToleratesFencesAndUnknownSegments(t *testing.T) {
	raw := "```\nThis session is me [abc123] — hi\n\nPeer sessions (2):\n  a b [111111]  ·  interactive  ·  idle  ·  started 3d ago  ·  extra\n  c [222222]  ·  headless\n```"
	got := ParseAgents(raw)
	if got.Self != "me" || len(got.Agents) != 2 {
		t.Fatalf("parsed = %+v", got)
	}
	if got.Agents[0].Name != "a b" || got.Agents[0].Status != "idle" || got.Agents[0].Started != "3d ago" {
		t.Fatalf("agent0 = %+v", got.Agents[0])
	}
	if got.Agents[1].Kind != "headless" || got.Agents[1].Status != "" {
		t.Fatalf("agent1 = %+v", got.Agents[1])
	}
	if empty := ParseAgents("No peer sessions."); len(empty.Agents) != 0 || empty.Agents == nil {
		t.Fatalf("empty listing must give a non-nil empty slice: %+v", empty)
	}
}

func TestOriginalMessage(t *testing.T) {
	if got := originalMessage("请使用 SendMessage\n[来自 web，bob] 你好"); got != "你好" {
		t.Fatalf("got %q", got)
	}
	if got := originalMessage("plain"); got != "plain" {
		t.Fatalf("got %q", got)
	}
}

func TestUsableDirPrefersSessionThenWorkspaceThenHome(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("GOFER_WORKSPACE", ws)
	if got := workspaceDir(); got != ws {
		t.Fatalf("workspaceDir = %q, want %q", got, ws)
	}
	if got := usableDir("/definitely/missing", workspaceDir(), homeDir()); got != ws {
		t.Fatalf("fallback = %q, want workspace %q", got, ws)
	}
}
