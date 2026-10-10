package messenger

import (
	"context"
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

func TestClaudeProjectDirNameMatchesSessionRelay(t *testing.T) {
	for _, p := range []string{"/d/work/a.b", `D:\work\x`, "/tmp/中文 dir"} {
		if got, want := claudeProjectDirName(p), sessionrelay.EncodeClaudeProjectDir(p); got != want {
			t.Fatalf("claudeProjectDirName(%q) = %q, want %q", p, got, want)
		}
	}
}
