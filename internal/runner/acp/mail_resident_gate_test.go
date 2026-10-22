package acp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/config"
)

// TestMailResidentReplyIsAutoAllowed is the gofer-sh7r rule: under approval: ask a
// resident 收件会话 answers a letter with gofer_session_mail_reply and nobody is asked —
// answering is what the session is for. The decision is recorded with its own reason.
func TestMailResidentReplyIsAutoAllowed(t *testing.T) {
	for _, title := range []string{"gofer_session_mail_reply", "mcp__gofer__gofer_session_mail_reply"} {
		t.Run(title, func(t *testing.T) {
			sink := &recordingSink{}
			h, events := gateHandler(t, config.ApprovalAsk, sink)
			h.mailResident = true
			out := h.RequestPermission(permissionRequest(title, acp.ToolKindOther))
			if out.Outcome != acp.OutcomeSelected || out.OptionID != "allow" {
				t.Fatalf("outcome = %+v, want selected allow", out)
			}
			if len(sink.asked) != 0 {
				t.Fatalf("a human was asked %d time(s) for the session's own letter answer", len(sink.asked))
			}
			if events.f != nil {
				b, err := os.ReadFile(filepath.Join(filepath.Dir(events.f.Name()), ACPFileName))
				if err != nil {
					t.Fatalf("read acp.jsonl: %v", err)
				}
				if !strings.Contains(string(b), `"reason":"`+autoReasonMailResidentReply+`"`) {
					t.Fatalf("acp.jsonl does not record the resident-reply reason:\n%s", b)
				}
			}
		})
	}
}

// TestMailResidentGateKeepsEverythingElseGated pins the boundaries: the allowance exists
// only for a resident mailbox session and only for that exact tool. An ordinary session's
// reply, another gofer write tool, a write file, another MCP server's look-alike and
// mode=strict all still go to a human.
func TestMailResidentGateKeepsEverythingElseGated(t *testing.T) {
	cases := []struct {
		name         string
		mode         string
		resident     bool
		title        string
		kind         string
		wantToAHuman bool
	}{
		{"ordinary session replying", config.ApprovalAsk, false, "gofer_session_mail_reply", acp.ToolKindOther, true},
		{"resident answering", config.ApprovalAsk, true, "gofer_session_mail_reply", acp.ToolKindOther, false},
		{"resident sending a new letter", config.ApprovalAsk, true, "gofer_session_mail_send", acp.ToolKindOther, true},
		{"resident using another gofer write tool", config.ApprovalAsk, true, "gofer_run_job", acp.ToolKindOther, true},
		{"resident writing a file", config.ApprovalAsk, true, "Write", acp.ToolKindEdit, true},
		{"resident writing a file, kind=other", config.ApprovalAsk, true, "edit_file", acp.ToolKindOther, true},
		{"resident typing a shell command", config.ApprovalAsk, true, "Bash", acp.ToolKindExecute, true},
		{"another server's look-alike", config.ApprovalAsk, true, "mcp__other__gofer_session_mail_reply", acp.ToolKindOther, true},
		{"strict mode keeps asking", config.ApprovalStrict, true, "gofer_session_mail_reply", acp.ToolKindOther, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			h, _ := gateHandler(t, tc.mode, sink)
			h.mailResident = tc.resident
			h.RequestPermission(permissionRequest(tc.title, tc.kind))
			if got := len(sink.asked) == 1; got != tc.wantToAHuman {
				t.Fatalf("asked a human = %v (%d time(s)), want %v", got, len(sink.asked), tc.wantToAHuman)
			}
		})
	}
}
