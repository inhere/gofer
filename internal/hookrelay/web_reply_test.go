package hookrelay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const messengerAddr = "uds:/tmp/cc-socks/4014788.sock"

// webMessageEntry is a transcript line in the shape Claude Code 2.1.296 writes for
// a cross-session message (type=user, isMeta, origin={kind:"peer",…}); mid=true
// gives the attachment form of a message absorbed mid-turn.
func webMessageEntry(t *testing.T, from, body string, mid bool) string {
	t.Helper()
	origin := map[string]any{"kind": "peer", "from": from, "verifiedPeerPid": 4014788, "msg_id": "m-1",
		"name": "msgr-c8", "fromMode": "prompting", "body": body}
	wrapped := "<cross-session-message from=\"" + from + "\" from-name=\"msgr-c8\" from-mode=\"prompting\">\n" + body + "\n</cross-session-message>"
	var entry map[string]any
	if mid {
		entry = map[string]any{"type": "attachment", "attachment": map[string]any{"type": "queued_command", "prompt": wrapped, "origin": origin}}
	} else {
		entry = map[string]any{"type": "user", "isMeta": true, "promptSource": "system", "turnOrigin": "peer",
			"message": map[string]any{"role": "user", "content": "Another Claude session sent a message:\n" + wrapped}, "origin": origin}
	}
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sendMessageInput(t *testing.T, to, message string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"to": to, "summary": "回复 web", "message": message, "type": "message",
		"recipient": to, "recipient_kind": "session", "content": "截断的…"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPostToolUseReportsWebReplyVerbatim(t *testing.T) {
	reply := "收到，进度如下：\n- 第一项 **完成**\n- 第二项进行中"
	cases := []struct {
		name  string
		entry string
	}{
		{"top-level origin", webMessageEntry(t, messengerAddr, "[来自 web，default] 进度如何", false)},
		{"absorbed mid-turn", webMessageEntry(t, messengerAddr, "[来自 web，default] 进度如何", true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newFake()
			transcript := writeTranscript(t, tc.entry,
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`)
			p := Payload{Agent: AgentClaude, Event: "PostToolUse", SessionID: "sid-1", TranscriptPath: transcript,
				ToolName: "SendMessage", ToolInput: sendMessageInput(t, messengerAddr, reply)}
			if _, err := Run(api, p, Options{}); err != nil {
				t.Fatal(err)
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if len(api.replies) != 1 || api.replies[0] != [3]string{"sid-1", reply, messengerAddr} {
				t.Fatalf("replies = %q, want the verbatim SendMessage text to %s", api.replies, messengerAddr)
			}
		})
	}
}

func TestPostToolUseIgnoresOtherSendMessages(t *testing.T) {
	webFromMessenger := webMessageEntry(t, messengerAddr, "[来自 web，default] 进度如何", false)
	peerChat := webMessageEntry(t, "uds:/tmp/cc-socks/99.sock", "普通的会话间协作消息", false)
	cases := []struct {
		name       string
		agent      string
		tool       string
		to         string
		transcript []string
	}{
		{"to another session", AgentClaude, "SendMessage", "uds:/tmp/cc-socks/99.sock", []string{webFromMessenger, peerChat}},
		{"by name, not the web sender", AgentClaude, "SendMessage", "other-session", []string{webFromMessenger}},
		{"no web message in transcript", AgentClaude, "SendMessage", "uds:/tmp/cc-socks/99.sock", []string{peerChat}},
		{"not SendMessage", AgentClaude, "Bash", messengerAddr, []string{webFromMessenger}},
		{"codex hook", AgentCodex, "SendMessage", messengerAddr, []string{webFromMessenger}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newFake()
			p := Payload{Agent: tc.agent, Event: "PostToolUse", SessionID: "sid-2", TranscriptPath: writeTranscript(t, tc.transcript...),
				ToolName: tc.tool, ToolInput: sendMessageInput(t, tc.to, "hello")}
			if _, err := Run(api, p, Options{}); err != nil {
				t.Fatal(err)
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if len(api.replies) != 0 {
				t.Fatalf("replies = %q, want none", api.replies)
			}
		})
	}
}

// A structured SendMessage (agent-team protocol messages carry an object) is not a
// reply text.
func TestWebReplyOfIgnoresStructuredMessage(t *testing.T) {
	transcript := writeTranscript(t, webMessageEntry(t, messengerAddr, "[来自 web，default] hi", false))
	in, _ := json.Marshal(map[string]any{"to": messengerAddr, "message": map[string]any{"type": "shutdown_request"}})
	if text, _ := webReplyOf(Payload{ToolName: "SendMessage", TranscriptPath: transcript, ToolInput: in}); text != "" {
		t.Fatalf("structured message reported as reply %q", text)
	}
}
