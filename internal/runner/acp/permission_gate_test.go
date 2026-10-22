package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
)

// recordingSink stands in for the job's interaction surface: it records every request
// the gate put to a human and answers allow_once.
type recordingSink struct {
	asked []runner.ApprovalRequest
}

func (s *recordingSink) RequestApproval(_ context.Context, req runner.ApprovalRequest) (runner.ApprovalOutcome, error) {
	s.asked = append(s.asked, req)
	return runner.ApprovalOutcome{Answer: "allow", By: "human"}, nil
}

// gateHandler builds the approval handler under test: the resolved policy, the given
// sink and an acp.jsonl in a temp dir (nil-safe, but then the audit line is lost).
func gateHandler(t *testing.T, mode string, sink runner.ApprovalSink) (*handler, *eventWriter) {
	t.Helper()
	pol := config.ApprovalConfig{Mode: mode}.WithDefaults()
	events, err := openEventWriter(t.TempDir(), false)
	if err != nil {
		t.Fatalf("openEventWriter: %v", err)
	}
	t.Cleanup(func() {
		if events.f != nil {
			_ = events.f.Close()
		}
	})
	return &handler{ctx: t.Context(), policy: pol, approvals: sink, events: events, jobID: "job-gate"}, events
}

// permissionRequest is one session/request_permission as an agent sends it: a tool call
// (title + kind) and the options it offers.
func permissionRequest(title, kind string) acp.RequestPermissionParams {
	return acp.RequestPermissionParams{
		ToolCall: &acp.ToolCall{ToolCallID: "tc-1", Title: title, Kind: kind},
		Options: []acp.PermissionOption{
			{OptionID: "allow", Name: "Allow once", Kind: acp.OptionAllowOnce},
			{OptionID: "allow_always", Name: "Allow always", Kind: acp.OptionAllowAlways},
			{OptionID: "reject", Name: "Reject", Kind: acp.OptionRejectOnce},
			{OptionID: "reject_always", Name: "Reject always", Kind: acp.OptionRejectAlways},
		},
	}
}

// TestGoferReadOnlyMCPToolIsAutoAllowed is the gofer-o49v rule: a steward's call to
// gofer_work_list / gofer_today_list arrives as kind=other (suga sends MCP tool calls
// that way), other is in the default ask_kinds, and without this rule the job parked on
// a human for a read-only list. The decision is recorded in acp.jsonl with the reason.
func TestGoferReadOnlyMCPToolIsAutoAllowed(t *testing.T) {
	for _, title := range []string{"gofer_work_list", "mcp__gofer__gofer_today_list"} {
		t.Run(title, func(t *testing.T) {
			sink := &recordingSink{}
			h, events := gateHandler(t, config.ApprovalAsk, sink)
			out := h.RequestPermission(permissionRequest(title, acp.ToolKindOther))
			if out.Outcome != acp.OutcomeSelected || out.OptionID != "allow" {
				t.Fatalf("outcome = %+v, want selected allow", out)
			}
			if len(sink.asked) != 0 {
				t.Fatalf("a human was asked %d time(s) for a read-only gofer tool", len(sink.asked))
			}
			if events.f != nil {
				b, err := os.ReadFile(filepath.Join(filepath.Dir(events.f.Name()), ACPFileName))
				if err != nil {
					t.Fatalf("read acp.jsonl: %v", err)
				}
				if !strings.Contains(string(b), `"reason":"gofer_readonly_tool"`) {
					t.Fatalf("acp.jsonl does not record the auto-allow reason:\n%s", b)
				}
			}
		})
	}
}

// TestApprovalGateStillAsksForEverythingElse pins the boundaries of that rule: a gofer
// WRITE tool, a destructive one, a look-alike from another MCP server, a name that only
// starts like a gofer tool, and (mode=strict) any tool at all still go to a human.
func TestApprovalGateStillAsksForEverythingElse(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		title string
		kind  string
	}{
		{"gofer write tool", config.ApprovalAsk, "gofer_run_job", acp.ToolKindOther},
		{"gofer destructive tool", config.ApprovalAsk, "gofer_memory_rm", acp.ToolKindOther},
		{"other server's look-alike", config.ApprovalAsk, "mcp__other__gofer_work_list", acp.ToolKindOther},
		{"prefix look-alike", config.ApprovalAsk, "gofer_work_list_v2", acp.ToolKindOther},
		{"no tool call", config.ApprovalAsk, "", acp.ToolKindOther},
		{"strict mode keeps asking", config.ApprovalStrict, "gofer_work_list", acp.ToolKindOther},
		{"non-gofer tool, kind=other", config.ApprovalAsk, "Bash", acp.ToolKindOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			h, _ := gateHandler(t, tc.mode, sink)
			out := h.RequestPermission(permissionRequest(tc.title, tc.kind))
			if len(sink.asked) != 1 {
				t.Fatalf("asked a human %d time(s), want exactly 1 (outcome %+v)", len(sink.asked), out)
			}
			if out.Outcome != acp.OutcomeSelected || out.OptionID != "allow" {
				t.Fatalf("outcome = %+v, want the human's selected allow", out)
			}
		})
	}
}

// TestKindReadStaysAutoAllowed: an agent that reports the ACP native kind=read was
// already auto-allowed by auto_allow_kinds. This is the path suga will take once it
// reports readOnlyHint tools as read — it must stay a no-prompt answer with the
// auto_allow_kind reason, not the new one.
func TestKindReadStaysAutoAllowed(t *testing.T) {
	sink := &recordingSink{}
	h, events := gateHandler(t, config.ApprovalAsk, sink)
	out := h.RequestPermission(permissionRequest("some_non_gofer_tool", acp.ToolKindRead))
	if out.Outcome != acp.OutcomeSelected || out.OptionID != "allow" {
		t.Fatalf("outcome = %+v, want selected allow", out)
	}
	if len(sink.asked) != 0 {
		t.Fatalf("kind=read asked a human %d time(s)", len(sink.asked))
	}
	if events.f != nil {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(events.f.Name()), ACPFileName))
		if err != nil {
			t.Fatalf("read acp.jsonl: %v", err)
		}
		if !strings.Contains(string(b), `"reason":"auto_allow_kind"`) {
			t.Fatalf("acp.jsonl does not record auto_allow_kind:\n%s", b)
		}
	}
}
