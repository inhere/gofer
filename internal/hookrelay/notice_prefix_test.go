package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// A gofer system notice is context for the agent, never a prompt a person typed: the
// following UserPromptSubmit carries it and must keep the relay on (no auto-off, no
// title rewrite). A person's "[来自 web，…]" line stays a human prompt.
func TestHarnessPromptRecognisesTheSystemNotice(t *testing.T) {
	notice := SystemNoticePrefix + " " + JobDoneTag + " job-1 t status=done exit=0"
	if !IsHarnessPrompt(notice) {
		t.Fatalf("%q not recognised as a system notice", notice)
	}
	web := sessionrelay.WebMessagePrefix + "，alice] 进度如何"
	if IsHarnessPrompt(web) {
		t.Fatalf("%q must stay a human prompt", web)
	}
}

// The Stop wait's notice quotes the same prefix the messenger path uses, so a session
// recognises both as one kind of message (gofer-11gk).
func TestStopJobWaitNoticeUsesTheSystemNoticePrefix(t *testing.T) {
	f := newFake()
	sid := "sid-prefix-stop"
	f.sessions[sid] = client.AgentSession{SessionID: sid, RelayMode: client.RelayModeAuto}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-p1", Title: "build", Status: "running"}}
	time.AfterFunc(50*time.Millisecond, func() {
		f.mu.Lock()
		f.watchRows[0].Status, f.watchRows[0].ExitCode = "done", 0
		f.mu.Unlock()
	})
	opts := fastOpts(nil)
	opts.StopJobWait = config.StopJobWaitAlways
	res, err := Run(f, stopPayload(t, "claude", sid), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || !strings.HasPrefix(res.Reason, SystemNoticePrefix+" "+JobDoneTag+" job-p1") {
		t.Fatalf("result=%+v, want the notice under %q", res, SystemNoticePrefix)
	}
	if strings.Contains(res.Reason, sessionrelay.WebMessagePrefix) {
		t.Fatalf("the notice must not wear the web prefix: %q", res.Reason)
	}
}

// The caught-up notice (SessionStart / UserPromptSubmit) opens with the same prefix.
func TestCatchUpNoticeUsesTheSystemNoticePrefix(t *testing.T) {
	f := newFake()
	f.sessions["sid-prefix-cu"] = client.AgentSession{SessionID: "sid-prefix-cu", RelayMode: client.RelayModeAuto}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-c1", Title: "t", Status: "failed", ExitCode: 2}}
	p := payload(t, "claude", map[string]any{"session_id": "sid-prefix-cu", "hook_event_name": "UserPromptSubmit", "prompt": "hello"})
	res, err := Run(f, p, fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Context, SystemNoticePrefix+" "+JobDoneTag+" job-c1") {
		t.Fatalf("context=%q, want the notice under %q", res.Context, SystemNoticePrefix)
	}
	if strings.Contains(res.Context, sessionrelay.WebMessagePrefix) {
		t.Fatalf("the caught-up notice must not wear the web prefix: %q", res.Context)
	}
}

// A notice that reached a WAITING session as its relay-turn answer is handed to the
// agent as the notice itself — not wrapped in "[gofer web 回复]", which marks a
// person's answer. A person's reply keeps that marker.
func TestStopNoticeAnswerKeepsTheSystemNoticeLine(t *testing.T) {
	notice := SystemNoticePrefix + " " + JobDoneTag + " job-t1 t status=done exit=0"
	f := newFake()
	f.sessions["sid-turn-notice"] = client.AgentSession{SessionID: "sid-turn-notice", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 2, notice
	res, err := Run(f, stopPayload(t, "claude", "sid-turn-notice"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || res.Reason != notice {
		t.Fatalf("reason=%q, want the notice %q", res.Reason, notice)
	}

	f = newFake()
	f.sessions["sid-turn-human"] = client.AgentSession{SessionID: "sid-turn-human", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 2, "use plan B"
	res, err = Run(f, stopPayload(t, "claude", "sid-turn-human"), fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != ReplyPrefix+"use plan B" {
		t.Fatalf("a person's reply must keep the marker: %q", res.Reason)
	}
}

// A session answering a gofer system notice is not answering a person: the
// SendMessage must not be posted as a web reply, even though it goes to the very
// address the notice came from. The same transcript with a person's message is
// reported, so the regression is pinned too.
func TestWebReplyIgnoresSystemNoticeAnswer(t *testing.T) {
	notice := webMessageEntry(t, messengerAddr, SystemNoticePrefix+" "+JobDoneTag+" job-9 t status=done exit=0", false)
	transcript := writeTranscript(t, notice,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"noted"}]}}`)
	reply := "收到"
	api := newFake()
	p := Payload{Agent: AgentClaude, Event: "PostToolUse", SessionID: "sid-prefix-reply", TranscriptPath: transcript,
		ToolName: "SendMessage", ToolInput: sendMessageInput(t, messengerAddr, reply)}
	if _, err := Run(api, p, Options{}); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	reported := len(api.replies)
	api.mu.Unlock()
	if reported != 0 {
		t.Fatalf("an answer to a system notice was posted as a web reply (%d)", reported)
	}

	web := writeTranscript(t, webMessageEntry(t, messengerAddr, sessionrelay.WebMessagePrefix+"，alice] 进度如何", false),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`)
	api = newFake()
	p = Payload{Agent: AgentClaude, Event: "PostToolUse", SessionID: "sid-prefix-reply", TranscriptPath: web,
		ToolName: "SendMessage", ToolInput: sendMessageInput(t, messengerAddr, reply)}
	if _, err := Run(api, p, Options{}); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.replies) != 1 || api.replies[0] != [3]string{"sid-prefix-reply", reply, messengerAddr} {
		t.Fatalf("a person's web message must still be answered and reported: %q", api.replies)
	}
}
