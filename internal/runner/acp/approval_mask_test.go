package acp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/config"
)

const approvalSecret = "SECRETVALUE-0123456789-abcdef"

func approvalMask(s string) string { return strings.ReplaceAll(s, approvalSecret, "***") }

// TestApprovalCardMasksGrantedSecrets: the card an approver reads (rawInput summary,
// title, locations, prompt) carries a granted secret only masked — including a secret
// that straddles the 512-byte cut of the summary.
func TestApprovalCardMasksGrantedSecrets(t *testing.T) {
	for name, input := range map[string]string{
		"straddles the cut": strings.Repeat("a", maxRawBytes-10) + approvalSecret + strings.Repeat("b", 100),
		"inside":            "deploy with " + approvalSecret,
	} {
		t.Run(name, func(t *testing.T) {
			sink := &recordingSink{}
			h, _ := gateHandler(t, config.ApprovalAsk, sink)
			h.mask = approvalMask
			raw, _ := json.Marshal(input)
			p := acp.RequestPermissionParams{
				ToolCall: &acp.ToolCall{
					ToolCallID: "tc-1", Title: "run " + approvalSecret, Kind: acp.ToolKindOther,
					RawInput:  raw,
					Locations: []acp.ToolCallLocation{{Path: "/work/" + approvalSecret + "/f.txt"}},
				},
				Options: permissionRequest("x", acp.ToolKindOther).Options,
			}
			h.RequestPermission(p)
			if len(sink.asked) != 1 {
				t.Fatalf("asked %d times, want 1", len(sink.asked))
			}
			req := sink.asked[0]
			all := req.Prompt + "|" + req.ToolCall.Title + "|" + req.ToolCall.RawInputSummary + "|" + strings.Join(req.ToolCall.Locations, "|")
			for n := 4; n <= len(approvalSecret); n++ {
				if strings.Contains(all, approvalSecret[:n]) {
					t.Fatalf("card kept a %d-byte prefix of the secret: %s", n, all)
				}
			}
			if !strings.Contains(req.Prompt, `"run ***"`) || req.ToolCall.Title != "run ***" || req.ToolCall.Locations[0] != "/work/***/f.txt" {
				t.Fatalf("card = %+v / %q", req.ToolCall, req.Prompt)
			}
		})
	}
}

// TestApprovalCardWithoutMaskIsUnchanged: a job with no granted secrets keeps the old
// projection byte for byte.
func TestApprovalCardWithoutMaskIsUnchanged(t *testing.T) {
	tc := &acp.ToolCall{ToolCallID: "t", Title: "run x", Kind: "other", RawInput: json.RawMessage(`{"a":1}`),
		Locations: []acp.ToolCallLocation{{Path: "/p"}}}
	got := approvalRequestToolCall(tc, nil)
	if got.Title != "run x" || got.RawInputSummary != `{"a":1}` || len(got.Locations) != 1 || got.Locations[0] != "/p" {
		t.Fatalf("got %+v", got)
	}
}
