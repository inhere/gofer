package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/messenger"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// promptMessenger is a one-shot messenger that remembers the prompt it was asked
// to deliver — the text Claude Code would hand the target session.
type promptMessenger struct {
	mu     sync.Mutex
	prompt string
}

func (m *promptMessenger) SubmitMessenger(_, _, _ string, command []string, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, part := range command {
		if part == "-p" && i+1 < len(command) {
			m.prompt = command[i+1]
		}
	}
	return "msgr-job", nil
}

func (*promptMessenger) MessengerJob(string) (bool, string, int, string, error) {
	return true, "done", 0, "已发送", nil
}

func (m *promptMessenger) delivered() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prompt[strings.LastIndex(m.prompt, "\n")+1:]
}

func sessionReplies(t *testing.T, c *client.Client, sid string) []client.SessionMessage {
	t.Helper()
	list, err := c.ListSessionMessages(sid)
	if err != nil {
		t.Fatalf("list session messages: %v", err)
	}
	var out []client.SessionMessage
	for _, m := range list {
		if m.Direction == jobstore.SessionMessageReply {
			out = append(out, m)
		}
	}
	return out
}

// TestWebMessageReplyReachesWebVerbatim is the gofer-6er0 end-to-end path: the web
// sends a message, the messenger delivers it, the target session answers it with
// SendMessage to the messenger's address, and the target's own PostToolUse hook
// reports that answer — the web conversation (the session outbox) then shows the
// session's words verbatim as its reply, not a retelling.
func TestWebMessageReplyReachesWebVerbatim(t *testing.T) {
	s := newTestServer(t, testToken, false)
	msgr := &promptMessenger{}
	s.relay.SetMessenger(msgr)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	c := client.New(ts.URL, testToken)

	const sid = "e2e-reply"
	transcript := filepath.Join(t.TempDir(), sid+".jsonl")
	if _, err := c.RegisterSession(client.SessionRegister{SessionID: sid, Agent: "claude", Runner: "builder",
		ProjectKey: "self", Event: "SessionStart", Transcript: transcript,
		PeerName: "proj-a", PeerStatus: "busy", PeerMessaging: true}); err != nil {
		t.Fatal(err)
	}
	sent, err := c.SendSessionMessage(sid, "进度如何？")
	if err != nil || sent.Status != jobstore.SessionMessageDelivered {
		t.Fatalf("web message = %+v, %v; want delivered", sent, err)
	}

	// Claude Code hands the target the messenger's text as a cross-session message
	// from the messenger's socket (the transcript shape of Claude Code 2.1.296)…
	const addr = "uds:/tmp/cc-socks/777.sock"
	body := msgr.delivered()
	if !strings.HasPrefix(body, sessionrelay.WebMessagePrefix+"，") || !strings.HasSuffix(body, "] 进度如何？") {
		t.Fatalf("delivered text = %q, want the [来自 web，…] line", body)
	}
	entry, _ := json.Marshal(map[string]any{"type": "user", "isMeta": true, "turnOrigin": "peer",
		"message": map[string]any{"role": "user", "content": "Another Claude session sent a message:\n<cross-session-message from=\"" + addr + "\" from-name=\"msgr-1\">\n" + body + "\n</cross-session-message>"},
		"origin":  map[string]any{"kind": "peer", "from": addr, "name": "msgr-1", "msg_id": "m-1", "body": body}})
	if err := os.WriteFile(transcript, append(entry, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	// …and the target answers it with SendMessage back to that address.
	reply := "进度：\n1. 接口完成\n2. web 还差 **一个** 按钮"
	input, _ := json.Marshal(map[string]any{"to": addr, "summary": "进度", "message": reply, "type": "message",
		"recipient": addr, "recipient_kind": "session", "content": "进度：…"})
	if _, err := hookrelay.Run(c, hookrelay.Payload{Agent: hookrelay.AgentClaude, Event: "PostToolUse", SessionID: sid,
		TranscriptPath: transcript, ToolName: "SendMessage", ToolInput: input}, hookrelay.Options{}); err != nil {
		t.Fatal(err)
	}

	got := sessionReplies(t, c, sid)
	if len(got) != 1 {
		t.Fatalf("replies = %+v, want exactly one", got)
	}
	r := got[0]
	if r.Text != reply || r.Source != jobstore.SessionReplySourceSession || r.Peer != addr || r.ReplyTo != sent.ID {
		t.Fatalf("reply = %+v, want the session's verbatim text answering %s", r, sent.ID)
	}
	// The messenger received the same SendMessage: its report is the same reply.
	if _, created, err := s.relay.RecordMessengerReply("builder", "proj-a", addr, reply); err != nil || created {
		t.Fatalf("messenger duplicate created=%v err=%v, want deduped", created, err)
	}
	if n := len(sessionReplies(t, c, sid)); n != 1 {
		t.Fatalf("replies after messenger report = %d, want 1", n)
	}
}

// TestResidentMessengerReplyFallback: when the target's hook does not report (old
// hook, no PostToolUse), the server's resident messenger still turns the reply it
// received into the session's verbatim reply, and the next web message gets its
// own delivery result instead of the previous reply's retelling.
func TestResidentMessengerReplyFallback(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resident, ok := s.residentMessenger.(*messenger.Manager)
	if !ok {
		t.Fatalf("resident messenger = %T, want *messenger.Manager", s.residentMessenger)
	}
	t.Cleanup(resident.Close)
	cfgDir, cwd := t.TempDir(), t.TempDir()
	resident.SetClaudeConfigDir(cfgDir)
	t.Setenv("GOFER_TEST_STREAM_JSON_PEER", filepath.Join(cfgDir, "projects", sessionrelay.EncodeClaudeProjectDir(cwd), "peer-sid.jsonl"))
	t.Setenv("GOFER_TEST_STREAM_JSON_PEER_CWD", cwd)
	s.relay.ConfigureMessaging(true, testcmd.Path(t), 30*time.Second, time.Minute)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	c := client.New(ts.URL, testToken)

	const sid = "e2e-fallback"
	if _, err := c.RegisterSession(client.SessionRegister{SessionID: sid, Agent: "claude", Runner: "server",
		ProjectKey: "self", Cwd: cwd, Event: "SessionStart", PeerName: "target-1", PeerStatus: "busy", PeerMessaging: true}); err != nil {
		t.Fatal(err)
	}
	if m, err := c.SendSessionMessage(sid, "第一条"); err != nil || m.Channel != "messenger" {
		t.Fatalf("first message = %+v, %v", m, err)
	}
	var got []client.SessionMessage
	wait.Until(t, 10*time.Second, "messenger fallback reply recorded", func() bool {
		got = sessionReplies(t, c, sid)
		return len(got) == 1
	})
	if got[0].Text != "pong: 原文 #1\n第二行" || got[0].Source != jobstore.SessionReplySourceMessenger {
		t.Fatalf("fallback reply = %+v, want the verbatim body from the messenger transcript", got[0])
	}
	if m, err := c.SendSessionMessage(sid, "第二条"); err != nil || m.Status != jobstore.SessionMessageDelivered {
		t.Fatalf("second message = %+v, %v; want delivered", m, err)
	}
	for _, d := range resident.Snapshot("local").Deliveries {
		if d.Op == "send" && !d.OK {
			t.Fatalf("delivery history has a failed send: %+v", d)
		}
	}
}
