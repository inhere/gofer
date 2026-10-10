package messenger

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// TestResidentMessengerPeerReplyDoesNotShiftResults pins gofer-6er0: a target that
// answers the web message with SendMessage makes the resident process run an
// unsolicited turn whose result (the model retelling the reply) used to land in
// the one-slot events channel — the NEXT delivery then returned that retelling as
// its own result, and every later one was off by one. The reply itself is captured
// verbatim from the messenger's transcript and handed to the reply handler.
func TestResidentMessengerPeerReplyDoesNotShiftResults(t *testing.T) {
	cfg := t.TempDir()
	cwd := t.TempDir()
	transcript := filepath.Join(cfg, "projects", claudeProjectDirName(cwd), "peer-sid.jsonl")
	t.Setenv("GOFER_TEST_STREAM_JSON_PEER", transcript)
	t.Setenv("GOFER_TEST_STREAM_JSON_PEER_CWD", cwd)
	command := append(testcmd.Cmd(t, "stream-json-fake"), "-p", "hello")

	m := New("", time.Minute)
	m.SetClaudeConfigDir(cfg)
	var mu sync.Mutex
	var got []PeerReply
	m.SetReplyHandler(func(runner string, r PeerReply) {
		mu.Lock()
		defer mu.Unlock()
		if runner != "local" {
			t.Errorf("reply runner = %q, want local", runner)
		}
		got = append(got, r)
	})
	t.Cleanup(m.Close)

	first, err := m.Send(context.Background(), "local", cwd, "target-1", command)
	if err != nil || first != "已发送#1" {
		t.Fatalf("first send = %q, %v; want 已发送#1", first, err)
	}
	wait.Until(t, 10*time.Second, "verbatim peer reply handed to the handler", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})
	second, err := m.Send(context.Background(), "local", cwd, "target-1", command)
	if err != nil || second != "已发送#2" {
		t.Fatalf("second send = %q, %v; want its own result 已发送#2 (not the previous reply's retelling)", second, err)
	}
	mu.Lock()
	r := got[0]
	mu.Unlock()
	if r.Body != "pong: 原文 #1\n第二行" || r.Name != "target-1" || r.From != "uds:/tmp/cc-socks/42.sock" {
		t.Fatalf("peer reply = %+v, want the verbatim body from target-1", r)
	}
	var replies int
	for _, d := range m.Snapshot("local").Deliveries {
		if d.Op == OpReply {
			replies++
			if d.Target != "target-1" || d.Message != "pong: 原文 #1 第二行" {
				t.Fatalf("reply history entry = %+v", d)
			}
		}
	}
	if replies != 1 {
		t.Fatalf("reply history entries = %d, want exactly 1 (reported once)", replies)
	}
}

// replyHistory returns the runner's OpReply delivery-history entries.
func replyHistory(m *Manager, runner string) []Delivery {
	var out []Delivery
	for _, d := range m.Snapshot(runner).Deliveries {
		if d.Op == OpReply {
			out = append(out, d)
		}
	}
	return out
}

func writePeerTranscript(t *testing.T, cfg, cwd string) {
	t.Helper()
	path := filepath.Join(cfg, "projects", claudeProjectDirName(cwd), "peer-sid.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"2026-10-10T10:00:00.000Z","origin":{"kind":"peer","from":"uds:/tmp/cc-socks/42.sock","name":"target-1","msg_id":"m1","body":"pong: 原文 #1\n第二行"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gofer-rgnw: the requested turn's result starts a transcript scan on its own
// goroutine, and the peer's unsolicited turn right behind it starts another. When
// the first scan already reported the peer message, the peer turn must not record
// the model's retelling as a second, nameless reply (the CI failure of
// TestResidentMessengerPeerReplyDoesNotShiftResults, forced here in that order).
func TestPeerTurnAfterScanReportedItsMessageRecordsOneReply(t *testing.T) {
	cfg, cwd := t.TempDir(), t.TempDir()
	writePeerTranscript(t, cfg, cwd)
	m := New("", time.Minute)
	m.SetClaudeConfigDir(cfg)
	t.Cleanup(m.Close)
	p := &process{owner: m, runner: "local"}
	p.setSession("peer-sid", cwd)

	m.collectReplies(p, "", false)                 // requested turn's scan wins the race
	m.collectReplies(p, "target-1 回复说 pong", true) // then the peer's own turn
	got := replyHistory(m, "local")
	if len(got) != 1 || got[0].Target != "target-1" || got[0].Message != "pong: 原文 #1 第二行" {
		t.Fatalf("reply history = %+v, want only the verbatim reply from target-1", got)
	}
}

// A peer turn whose transcript has no readable peer message still leaves the
// model's retelling in the history (the message is not lost entirely).
func TestPeerTurnWithoutTranscriptRecordsRetelling(t *testing.T) {
	m := New("", time.Minute)
	m.SetClaudeConfigDir(t.TempDir())
	t.Cleanup(m.Close)
	p := &process{owner: m, runner: "local"}
	p.setSession("peer-sid", t.TempDir())

	m.collectReplies(p, "target-1 回复说 pong", true)
	got := replyHistory(m, "local")
	if len(got) != 1 || got[0].Target != "" || got[0].Message != "target-1 回复说 pong" {
		t.Fatalf("reply history = %+v, want the retelling once", got)
	}
}

func TestClaudeProjectDirNameMatchesSessionRelay(t *testing.T) {
	for _, p := range []string{"/d/work/a.b", `D:\work\x`, "/tmp/中文 dir"} {
		if got, want := claudeProjectDirName(p), sessionrelay.EncodeClaudeProjectDir(p); got != want {
			t.Fatalf("claudeProjectDirName(%q) = %q, want %q", p, got, want)
		}
	}
}
