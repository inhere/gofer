package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/mcptools"
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

// gateHandler builds the approval handler under test: the resolved policy for mode, the
// given sink, an acp.jsonl in a temp dir (nil-safe, but then the audit line is lost) and a
// session that WAS given gofer's own MCP server — the steward's and a resident 收件会话's
// sessions are, which is what makes a gofer_* tool name credible. Tests about that
// attribution clear goferMCPServers themselves.
func gateHandler(t *testing.T, mode string, sink runner.ApprovalSink) (*handler, *eventWriter) {
	t.Helper()
	return gateHandlerPolicy(t, config.ApprovalConfig{Mode: mode}, sink)
}

// gateHandlerPolicy is gateHandler with an explicit approval policy (the tool lists are
// what most of the gofer-78on cases are about).
func gateHandlerPolicy(t *testing.T, pol config.ApprovalConfig, sink runner.ApprovalSink) (*handler, *eventWriter) {
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
	return &handler{
		ctx: t.Context(), policy: pol.WithDefaults(), approvals: sink, events: events, jobID: "job-gate",
		goferMCPServers: map[string]bool{mcptools.ServerName: true},
	}, events
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

// TestGoferAdviceToolIsAutoAllowed is the gofer-78on rule: a steward's gofer_today_advise
// (and the other advice-only writes) arrives as kind=other, which is in the default
// ask_kinds — so without the tool-name rule every inspection parked the steward on a
// human. The default auto_allow_tools list is gofer's read-only tools plus these, and the
// decision is recorded with its own reason.
func TestGoferAdviceToolIsAutoAllowed(t *testing.T) {
	for _, title := range []string{
		"gofer_today_advise",
		"mcp__gofer__gofer_today_advise",
		"gofer_work_merge_suggest",
		"gofer_memory_suggest",
	} {
		t.Run(title, func(t *testing.T) {
			sink := &recordingSink{}
			h, events := gateHandler(t, config.ApprovalAsk, sink)
			out := h.RequestPermission(permissionRequest(title, acp.ToolKindOther))
			if out.Outcome != acp.OutcomeSelected || out.OptionID != "allow" {
				t.Fatalf("outcome = %+v, want selected allow", out)
			}
			if len(sink.asked) != 0 {
				t.Fatalf("a human was asked %d time(s) for an advice-only gofer tool", len(sink.asked))
			}
			if events.f != nil {
				b, err := os.ReadFile(filepath.Join(filepath.Dir(events.f.Name()), ACPFileName))
				if err != nil {
					t.Fatalf("read acp.jsonl: %v", err)
				}
				if !strings.Contains(string(b), `"reason":"gofer_advice_tool"`) {
					t.Fatalf("acp.jsonl does not record the advice-tool reason:\n%s", b)
				}
			}
		})
	}
}

// TestApprovalToolAttribution pins requirement ③ of gofer-78on: a gofer_* name only
// counts when the request really came from gofer's own MCP server. Otherwise any agent
// could skip the gate by naming its tool gofer_today_advise.
func TestApprovalToolAttribution(t *testing.T) {
	goferMCP := map[string]bool{mcptools.ServerName: true}
	// An operator's own name for the same gofer binary: trusted for THAT server only.
	renamed := map[string]bool{"house-gofer": true}
	cases := []struct {
		name        string
		servers     map[string]bool
		title       string
		wantToHuman bool
	}{
		{"bare name, gofer MCP advertised", goferMCP, "gofer_today_advise", false},
		{"bare name, no gofer MCP advertised", nil, "gofer_today_advise", true},
		{"bare name, gofer MCP under another name", renamed, "gofer_today_advise", false},
		{"qualified by gofer", goferMCP, "mcp__gofer__gofer_today_advise", false},
		{"qualified by another server", goferMCP, "mcp__docs__gofer_today_advise", true},
		{"qualified by gofer but not advertised", renamed, "mcp__gofer__gofer_today_advise", true},
		{"renamed gofer server", renamed, "mcp__house-gofer__gofer_today_advise", false},
		{"renamed gofer server, bare name", renamed, "gofer_today_advise", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			h, _ := gateHandler(t, config.ApprovalAsk, sink)
			h.goferMCPServers = tc.servers
			h.RequestPermission(permissionRequest(tc.title, acp.ToolKindOther))
			if got := len(sink.asked) == 1; got != tc.wantToHuman {
				t.Fatalf("asked a human = %v (%d time(s)), want %v", got, len(sink.asked), tc.wantToHuman)
			}
		})
	}
}

// TestApprovalToolListsConfigurable pins the config side (requirement ②): auto_allow_tools
// and ask_tools are read from the policy, ask_tools wins, an explicit list REPLACES the
// default one, and a tool of another MCP server can be listed by name.
func TestApprovalToolListsConfigurable(t *testing.T) {
	cases := []struct {
		name        string
		policy      config.ApprovalConfig
		title       string
		kind        string
		wantToHuman bool
		wantReason  string
	}{
		{
			name:       "default list allows an advice tool",
			policy:     config.ApprovalConfig{Mode: config.ApprovalAsk},
			title:      "gofer_today_advise",
			kind:       acp.ToolKindOther,
			wantReason: "gofer_advice_tool",
		},
		{
			name:        "explicit empty list replaces the default",
			policy:      config.ApprovalConfig{Mode: config.ApprovalAsk, AutoAllowTools: []string{}},
			title:       "gofer_today_advise",
			kind:        acp.ToolKindOther,
			wantToHuman: true,
		},
		{
			name:        "ask_tools wins over the default list",
			policy:      config.ApprovalConfig{Mode: config.ApprovalAsk, AskTools: []string{"gofer_today_list"}},
			title:       "gofer_today_list",
			kind:        acp.ToolKindOther,
			wantToHuman: true,
		},
		{
			name:        "ask_tools wins over auto_allow_tools",
			policy:      config.ApprovalConfig{Mode: config.ApprovalAsk, AutoAllowTools: []string{"Bash"}, AskTools: []string{"Bash"}},
			title:       "Bash",
			kind:        acp.ToolKindExecute,
			wantToHuman: true,
		},
		{
			name:       "another server's tool listed by name",
			policy:     config.ApprovalConfig{Mode: config.ApprovalAsk, AutoAllowTools: []string{"mcp__docs__search"}},
			title:      "mcp__docs__search",
			kind:       acp.ToolKindOther,
			wantReason: "auto_allow_tool",
		},
		{
			name:        "gofer name from another server cannot be listed in",
			policy:      config.ApprovalConfig{Mode: config.ApprovalAsk, AutoAllowTools: []string{"gofer_today_advise"}},
			title:       "mcp__docs__gofer_today_advise",
			kind:        acp.ToolKindOther,
			wantToHuman: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			h, events := gateHandlerPolicy(t, tc.policy, sink)
			out := h.RequestPermission(permissionRequest(tc.title, tc.kind))
			if got := len(sink.asked) == 1; got != tc.wantToHuman {
				t.Fatalf("asked a human = %v (%d time(s)), want %v (outcome %+v)", got, len(sink.asked), tc.wantToHuman, out)
			}
			if tc.wantReason != "" && events.f != nil {
				b, err := os.ReadFile(filepath.Join(filepath.Dir(events.f.Name()), ACPFileName))
				if err != nil {
					t.Fatalf("read acp.jsonl: %v", err)
				}
				if !strings.Contains(string(b), `"reason":"`+tc.wantReason+`"`) {
					t.Fatalf("acp.jsonl does not record reason %q:\n%s", tc.wantReason, b)
				}
			}
		})
	}
}

// TestGateHandlerGoferServers pins what attribution trusts: gofer's canonical server
// name, and any entry running this same gofer binary (an operator may bind it under a
// name of their own). Another program's server is not gofer's, whatever it is called.
func TestGateHandlerGoferServers(t *testing.T) {
	self, err := os.Executable()
	if err != nil || self == "" {
		t.Fatalf("os.Executable: %v", err)
	}
	servers := []runner.ACPMCPServer{
		{Name: "gofer", Command: `C:\other\gofer.exe`},
		{Name: "house-gofer", Command: self},
		{Name: "docs", Command: `C:\tools\docs-mcp.exe`},
		{Name: "", Command: self},
	}
	got := goferAdvertisedMCPServers(servers)
	if !got["gofer"] || !got["house-gofer"] {
		t.Fatalf("gofer's own servers = %v, want gofer and house-gofer", got)
	}
	if got["docs"] {
		t.Fatalf("another program's server was taken for gofer's: %v", got)
	}
	if goferAdvertisedMCPServers(nil) != nil {
		t.Error("no advertised servers must resolve to no gofer server")
	}
	if got := goferAdvertisedMCPServers([]runner.ACPMCPServer{{Name: "docs", Command: "docs-mcp"}}); got != nil {
		t.Errorf("a session with no gofer MCP resolved to %v, want nil", got)
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
