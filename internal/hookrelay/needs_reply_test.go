package hookrelay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/client"
)

func TestSplitNeedsReply(t *testing.T) {
	cases := []struct {
		name, in, body string
		needs          bool
	}{
		{"last line marker", "which plan?\n[needs-reply]", "which plan?", true},
		{"marker with surrounding spaces", "which plan?\n  [needs-reply]  ", "which plan?", true},
		{"trailing newlines after marker", "which plan?\n[needs-reply]\n\n", "which plan?", true},
		{"blank lines before marker are dropped", "which plan?\n\n\n[needs-reply]", "which plan?", true},
		{"marker in the middle is prose", "[needs-reply]\nmore text", "[needs-reply]\nmore text", false},
		{"marker followed by text on its line", "ok\n[needs-reply] please", "ok\n[needs-reply] please", false},
		{"case differs", "ok\n[Needs-Reply]", "ok\n[Needs-Reply]", false},
		{"only the marker", "[needs-reply]", "", true},
		{"no marker", "all done", "all done", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, needs := splitNeedsReply(c.in)
			assert.Eq(t, c.body, body)
			assert.Eq(t, c.needs, needs)
		})
	}
}

func needsReplyStop(t *testing.T, msg string) Payload {
	return payload(t, "codex", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": msg})
}

func TestStopReportsNeedsReply(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 1, "go"
	_, err := Run(f, needsReplyStop(t, "A or B?\n[needs-reply]"), fastOpts(nil))
	assert.NoErr(t, err)
	assert.Eq(t, []bool{true}, f.openedNeedsReply)
	assert.Eq(t, "A or B?", f.turns["dec-1"].Question) // marker never reaches the relayed body

	f = newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 1, "go"
	_, err = Run(f, needsReplyStop(t, "all done"), fastOpts(nil))
	assert.NoErr(t, err)
	assert.Eq(t, []bool{false}, f.openedNeedsReply)
}

// The marker is the last line of a long reply: judged before truncation, so it
// survives MaxMessage and is still stripped from the body.
func TestStopNeedsReplySurvivesTruncation(t *testing.T) {
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 1, "go"
	opts := fastOpts(nil)
	opts.MaxMessage = 200
	_, err := Run(f, needsReplyStop(t, strings.Repeat("long text ", 100)+"\n[needs-reply]"), opts)
	assert.NoErr(t, err)
	assert.Eq(t, []bool{true}, f.openedNeedsReply)
	assert.False(t, strings.Contains(f.turns["dec-1"].Question, NeedsReplyMarker))
	assert.True(t, len(f.turns["dec-1"].Question) <= 200)
}

// A real Claude Stop carries no usable last_assistant_message in some versions: the
// transcript tail is the fallback and must carry the marker the same way.
func TestStopNeedsReplyFromTranscript(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"pick one\n[needs-reply]"}]}}` + "\n"
	assert.NoErr(t, os.WriteFile(transcript, []byte(line), 0o600))
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 1, "go"
	p := payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "transcript_path": transcript})
	_, err := Run(f, p, fastOpts(nil))
	assert.NoErr(t, err)
	assert.Eq(t, []bool{true}, f.openedNeedsReply)
	assert.Eq(t, "pick one", f.turns["dec-1"].Question)
}

// TestStopNeedsReplyWithEmptyAssistantMessage covers clients that send an explicit empty
// last_assistant_message while the transcript still contains the final marker.
func TestStopNeedsReplyWithEmptyAssistantMessage(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"please answer\n[needs-reply]"}]}}` + "\n"
	assert.NoErr(t, os.WriteFile(transcript, []byte(line), 0o600))
	f := newFake()
	f.sessions["s1"] = client.AgentSession{SessionID: "s1", RelayMode: client.RelayModeOn, WaitReason: client.WaitModeOn}
	f.answerAfter, f.answer = 1, "go"
	p := payload(t, "claude", map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "", "transcript_path": transcript})
	_, err := Run(f, p, fastOpts(nil))
	assert.NoErr(t, err)
	assert.Eq(t, []bool{true}, f.openedNeedsReply)
	assert.Eq(t, "please answer", f.turns["dec-1"].Question)
}

func TestSessionStartNeedsReplyReminder(t *testing.T) {
	start := func(agent string) Result {
		res, err := Run(newFake(), payload(t, agent, map[string]any{"session_id": "r1", "hook_event_name": "SessionStart", "cwd": "/w"}), fastOpts(nil))
		assert.NoErr(t, err)
		return res
	}
	assert.StrContains(t, start("claude").Context, NeedsReplyMarker)
	assert.False(t, strings.Contains(start("jcode").Context, NeedsReplyMarker))
}
