package hookrelay

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

func gpayload(t *testing.T, key string, m map[string]any) Payload {
	t.Helper()
	b, _ := json.Marshal(m)
	p, err := ParseGenericPayload(key, strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

func TestGenericPayloadKeyAndValidation(t *testing.T) {
	p := gpayload(t, "MyAgent-CLI", map[string]any{"session_id": "g1", "hook_event_name": "SessionStart", "cwd": "/w", "transcript_path": "/t.jsonl", "source": "startup"})
	if p.Agent != "myagent-cli" || p.Dialect != DialectGeneric || p.dialect() != DialectGeneric {
		t.Fatalf("payload = %+v", p)
	}
	for _, bad := range []string{"", "a b", "../x", strings.Repeat("a", 70)} {
		if _, err := ParseGenericPayload(bad, strings.NewReader(`{"session_id":"s","hook_event_name":"Stop"}`)); err == nil {
			t.Fatalf("key %q accepted", bad)
		}
	}
	if _, err := ParsePayload("generic", strings.NewReader(`{}`)); err == nil {
		t.Fatal("plain `hook generic` without --agent path must not parse as a built-in agent")
	}
}

// Every event registers under the --agent key; an agent unknown to the server's config
// is the server's call (it accepts any key) — the hook never crashes on it.
func TestGenericEventsRegisterUnderAgentKey(t *testing.T) {
	f := newFake()
	run := func(m map[string]any) Result {
		m["session_id"] = "g-ev"
		res, err := Run(f, gpayload(t, "myagent", m), fastOpts(nil))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	run(map[string]any{"hook_event_name": "SessionStart", "cwd": "/w/repo"})
	if f.sessions["g-ev"].Agent != "myagent" {
		t.Fatalf("registered agent = %q", f.sessions["g-ev"].Agent)
	}
	run(map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "do the thing"})
	run(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "shell", "tool_output": "job j-77 submitted"})
	run(map[string]any{"hook_event_name": "Notification", "notification_type": "other", "message": "hi"})
	run(map[string]any{"hook_event_name": "Interrupt"})
	run(map[string]any{"hook_event_name": "SessionEnd"})
	if len(f.jobWatches) != 1 || f.jobWatches[0] != "j-77" {
		t.Fatalf("job watches = %v", f.jobWatches)
	}
	var events []string
	for _, b := range f.beats {
		events = append(events, b.Event)
	}
	want := "UserPromptSubmit,PostToolUse,Notification,Interrupt,SessionEnd"
	if got := strings.Join(events, ","); !strings.Contains(got, "UserPromptSubmit") || !strings.HasSuffix(got, "Interrupt,SessionEnd") {
		t.Fatalf("beats = %s, want ~%s", got, want)
	}
	if f.beats[0].Title == "" {
		t.Fatal("a human prompt must title the session")
	}
}

func TestGenericPromptWithReplyPrefixIsInjected(t *testing.T) {
	f := newFake()
	f.sessions["g-inj"] = client.AgentSession{SessionID: "g-inj", Agent: "myagent"}
	p := gpayload(t, "myagent", map[string]any{"session_id": "g-inj", "hook_event_name": "UserPromptSubmit", "prompt": ReplyPrefix + "go on"})
	if _, err := Run(f, p, fastOpts(nil)); err != nil {
		t.Fatal(err)
	}
	if len(f.beats) != 1 || !f.beats[0].Injected || f.humanEvents != 0 {
		t.Fatalf("beats = %+v humanEvents=%d", f.beats, f.humanEvents)
	}
}

// The generic `injected` flag marks agent-injected input whatever its text; the
// job-done notice prefix is recognised as harness input without the flag.
func TestGenericInjectedFlag(t *testing.T) {
	f := newFake()
	f.sessions["g-flag"] = client.AgentSession{SessionID: "g-flag", Agent: "myagent"}
	run := func(extra map[string]any) client.SessionHeartbeat {
		m := map[string]any{"session_id": "g-flag", "hook_event_name": "UserPromptSubmit", "cwd": "/w/repo", "prompt": "plain text"}
		for k, v := range extra {
			m[k] = v
		}
		if _, err := Run(f, gpayload(t, "myagent", m), fastOpts(nil)); err != nil {
			t.Fatal(err)
		}
		return f.beats[len(f.beats)-1]
	}
	b := run(map[string]any{"injected": true})
	if !b.Injected || b.Title != "" || f.humanEvents != 0 {
		t.Fatalf("injected=true beat = %+v humanEvents=%d", b, f.humanEvents)
	}
	b = run(map[string]any{"prompt": "[gofer job 完成] job-1 t status=done exit=0 耗时1s"})
	if !b.Injected || b.Title != "" {
		t.Fatalf("job notice beat = %+v", b)
	}
	b = run(nil) // no flag: unchanged human prompt
	if b.Injected || b.Title == "" {
		t.Fatalf("plain beat = %+v", b)
	}
	b = run(map[string]any{"injected": false})
	if b.Injected || b.Title == "" {
		t.Fatalf("injected=false beat = %+v", b)
	}
	if !IsHarnessPrompt(JobDoneTag+" x") || !strings.HasPrefix(formatWatchedJobCompletion(WatchedJob{ID: "j"}), JobDoneTag) {
		t.Fatal("JobDoneTag not recognised")
	}
}

// Stop: the last message comes from the payload (the transcript is never read), the
// wait is answered by a web reply and printed as the block decision.
func TestGenericStopWaitsAndBlocksWithReply(t *testing.T) {
	f := newFake()
	f.sessions["g-stop"] = client.AgentSession{SessionID: "g-stop", Agent: "myagent", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 2, "use plan B"
	p := gpayload(t, "myagent", map[string]any{"session_id": "g-stop", "hook_event_name": "Stop",
		"last_assistant_message": "A or B?", "transcript_path": "/definitely/not/read.jsonl"})
	res, err := Run(f, p, fastOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || res.Reason != ReplyPrefix+"use plan B" {
		t.Fatalf("res = %+v", res)
	}
	if f.turns["dec-1"].Question != "A or B?" {
		t.Fatalf("turn question = %q", f.turns["dec-1"].Question)
	}
	if string(BlockJSON(res.Reason)) != `{"decision":"block","reason":"[gofer web 回复] use plan B"}` {
		t.Fatalf("block json = %s", BlockJSON(res.Reason))
	}
	// An empty payload message never falls back to a transcript read.
	f2 := newFake()
	f2.sessions["g2"] = client.AgentSession{SessionID: "g2", Agent: "myagent", RelayMode: client.RelayModeOff}
	p2 := gpayload(t, "myagent", map[string]any{"session_id": "g2", "hook_event_name": "Stop", "transcript_path": "/definitely/not/read.jsonl"})
	var log strings.Builder
	if _, err := Run(f2, p2, fastOpts(&log)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "transcript read failed") {
		t.Fatalf("generic Stop read the transcript: %s", log.String())
	}
}

// Interrupt closes the OPEN turn (the integrator sends it before ending a background
// Stop wait).
func TestGenericInterruptClosesOpenTurn(t *testing.T) {
	f := newFake()
	f.sessions["g-int"] = client.AgentSession{SessionID: "g-int", Agent: "myagent", WaitReason: client.WaitModeOn, State: "waiting_reply"}
	f.turns["d1"] = client.Decision{ID: "d1", State: "OPEN", SessionID: "g-int", Kind: "relay"}
	if _, err := Run(f, gpayload(t, "myagent", map[string]any{"session_id": "g-int", "hook_event_name": "Interrupt"}), fastOpts(nil)); err != nil {
		t.Fatal(err)
	}
	if f.turns["d1"].State != "EXPIRED" || f.sessions["g-int"].State != "idle" {
		t.Fatalf("turn=%+v session=%+v", f.turns["d1"], f.sessions["g-int"])
	}
}

// Catch-up: finished-but-undelivered job notices ride additionalContext once, on both
// SessionStart and UserPromptSubmit, for a generic agent (its output reaches the model).
func TestGenericCatchUpOnce(t *testing.T) {
	f := newFake()
	f.sessions["g-cu"] = client.AgentSession{SessionID: "g-cu", Agent: "myagent", RelayMode: client.RelayModeAuto}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-7", Title: "t", Status: "failed", ExitCode: 2}}
	p := gpayload(t, "myagent", map[string]any{"session_id": "g-cu", "hook_event_name": "UserPromptSubmit", "prompt": "hello"})
	res, _ := Run(f, p, fastOpts(nil))
	if !strings.Contains(res.Context, "[gofer job 完成] job-7 t status=failed exit=2") {
		t.Fatalf("res = %+v", res)
	}
	if res, _ = Run(f, p, fastOpts(nil)); res.Context != "" {
		t.Fatalf("delivered twice: %q", res.Context)
	}
	f.watchRows = []client.SessionJobWatch{{JobID: "job-8", Status: "done"}}
	ss := gpayload(t, "myagent", map[string]any{"session_id": "g-cu", "hook_event_name": "SessionStart", "source": "resume"})
	if res, _ = Run(f, ss, fastOpts(nil)); !strings.Contains(res.Context, "job-8") {
		t.Fatalf("SessionStart catch-up = %+v", res)
	}
}
