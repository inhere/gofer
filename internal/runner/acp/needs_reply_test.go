package acp

import (
	"bytes"
	"testing"

	"github.com/inhere/gofer/internal/acp"
)

// feedMarkerChunks plays a turn's message chunks through a hold-mode handler and ends the
// turn; it returns what reached stdout and the turn's needs-reply verdict.
func feedMarkerChunks(t *testing.T, chunks ...string) (string, bool) {
	t.Helper()
	events, err := openEventWriter(t.TempDir(), false)
	if err != nil {
		t.Fatalf("openEventWriter: %v", err)
	}
	t.Cleanup(func() {
		if events.f != nil {
			_ = events.f.Close()
		}
	})
	var stdout bytes.Buffer
	h := &handler{stdout: &stdout, events: events, toolStatus: map[string]string{}, toolEvents: map[string]string{}}
	h.setNeedsReplyHold()
	for _, c := range chunks {
		h.SessionUpdate("s", acp.Update{Kind: acp.UpdateAgentMessageChunk, MessageChunk: &acp.ContentBlock{Type: "text", Text: c}})
	}
	h.endTurn()
	return stdout.String(), h.takeNeedsReply()
}

func TestResidentTurnMarkerIsHeldBackAndReported(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		stdout string
		needs  bool
	}{
		{"marker as the last line", []string{"which plan?\n[needs-reply]"}, "which plan?\n", true},
		{"marker split across chunks", []string{"which plan?\n[need", "s-re", "ply]\n"}, "which plan?\n", true},
		{"blank lines before the marker go too", []string{"which plan?\n\n", "\n[needs-reply]"}, "which plan?\n", true},
		{"no marker", []string{"all done"}, "all done\n", false},
		{"marker followed by more text is prose", []string{"[needs-reply]\n", "more"}, "[needs-reply]\nmore\n", false},
		{"marker with trailing text on its line", []string{"ok\n[needs-reply] please"}, "ok\n[needs-reply] please\n", false},
		{"a prefix of the marker that never completes is shown", []string{"ok\n[need"}, "ok\n[need\n", false},
		{"only the marker", []string{"[needs-reply]"}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, needs := feedMarkerChunks(t, c.chunks...)
			if out != c.stdout || needs != c.needs {
				t.Fatalf("stdout=%q needs=%v, want %q / %v", out, needs, c.stdout, c.needs)
			}
		})
	}
}

func TestMarkerHeldTextIsReleasedByAToolCall(t *testing.T) {
	events, err := openEventWriter(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if events.f != nil {
			_ = events.f.Close()
		}
	})
	var stdout bytes.Buffer
	h := &handler{stdout: &stdout, events: events, toolStatus: map[string]string{}, toolEvents: map[string]string{}}
	h.setNeedsReplyHold()
	h.SessionUpdate("s", acp.Update{Kind: acp.UpdateAgentMessageChunk, MessageChunk: &acp.ContentBlock{Type: "text", Text: "x\n[needs-reply]"}})
	h.SessionUpdate("s", acp.Update{Kind: acp.UpdateToolCall, ToolCall: &acp.ToolCall{ToolCallID: "t1", Title: "ls", Kind: "read"}})
	h.endTurn()
	if h.takeNeedsReply() {
		t.Fatal("a marker followed by a tool call is not the turn's last line")
	}
	if got := stdout.String(); got != "x\n\n[needs-reply]\n" && got != "x\n[needs-reply]\n" {
		t.Fatalf("stdout = %q, want the marker kept as text", got)
	}
}

func TestOneShotTurnIsNeverHeld(t *testing.T) {
	events, err := openEventWriter(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if events.f != nil {
			_ = events.f.Close()
		}
	})
	var stdout bytes.Buffer
	h := &handler{stdout: &stdout, events: events, toolStatus: map[string]string{}, toolEvents: map[string]string{}}
	h.SessionUpdate("s", acp.Update{Kind: acp.UpdateAgentMessageChunk, MessageChunk: &acp.ContentBlock{Type: "text", Text: "x\n[needs-reply]"}})
	h.endTurn()
	if got := stdout.String(); got != "x\n[needs-reply]\n" || h.takeNeedsReply() {
		t.Fatalf("one-shot stdout = %q", got)
	}
}
