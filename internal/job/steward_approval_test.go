package job

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
)

// gofer-j0aq: the steward's session carries its OWN approval gate
// (config.DefaultStewardApproval) on its job request. A call to one of the gofer writes the
// steward is FOR — gofer_work_remind here — then runs without a human even though the
// project asks for every tool call, which is what makes the daily tidy-up finish instead
// of timing out on a card nobody answers. The same tool name in an ordinary ACP session of
// the same project still asks: the allowance belongs to the steward's policy, it is not a
// global rule about the tool.
func TestStewardApprovalAutoAllowsItsOwnGoferWrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	o := acptest.Options{PermissionKind: acp.ToolKindOther, PermissionTool: "gofer_work_remind"}
	s := newACPServiceWith(t, root, o, &config.ApprovalConfig{Mode: config.ApprovalAsk}, "")

	stewardApproval := config.DefaultStewardApproval()
	created, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "steward tidy-up turn", Session: true, TimeoutSec: 60, IdleTimeoutSec: 60,
		Tags: []string{StewardTag}, Steward: true, SessionApproval: &stewardApproval,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	stewardID := created.ID
	waitSessionTurn(t, s, stewardID, 1)

	if list, _ := s.GetInteractions(stewardID); len(list) != 0 {
		t.Fatalf("the steward's gofer_work_remind raised %d interactions, want none: %+v", len(list), list)
	}
	if evs := jobEventDetails(t, s, stewardID, "job.permission_requested"); len(evs) != 0 {
		t.Fatalf("the steward's gofer_work_remind announced %d permission requests, want none", len(evs))
	}
	asks := readPermAsks(t, s, root, stewardID)
	if len(asks) != 1 || asks[0].outcome != "selected" || asks[0].kind != "allow_once" {
		t.Fatalf("agent saw %+v, want one selected allow_once", asks)
	}
	// The audit trail says which rule let it through.
	snap, ok := s.Get(stewardID)
	if !ok {
		t.Fatalf("job %s not found", stewardID)
	}
	sum := acpSummary(t, s, stewardID)
	if sum["permissions"] != float64(1) || sum["permissions_auto"] != float64(1) {
		t.Fatalf("summary = %+v, want permissions=1 permissions_auto=1", sum)
	}
	// The gate rides request_json, which is what a resumed / recovered session keeps.
	if snap.RequestJSON == "" {
		t.Fatal("the steward job recorded no request_json")
	}
	var stored JobRequest
	if err := json.Unmarshal([]byte(snap.RequestJSON), &stored); err != nil {
		t.Fatalf("decode request_json: %v", err)
	}
	if stored.SessionApproval == nil || stored.SessionApproval.Mode != config.ApprovalAsk {
		t.Fatalf("request_json session_approval = %+v, want the steward's mode=ask", stored.SessionApproval)
	}
	_ = s.EndSession(stewardID)
	s.Wait(stewardID)

	// The control: the same tool name, an ordinary ACP session, the project's policy.
	plain, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "plain turn", Session: true, TimeoutSec: 60, IdleTimeoutSec: 60,
	})
	if err != nil {
		t.Fatalf("Submit (plain): %v", err)
	}
	it := waitPermissionInteraction(t, s, plain.ID, 15*time.Second)
	if it.ToolCall == nil || it.ToolCall.Title != "gofer_work_remind" {
		t.Fatalf("interaction tool_call = %+v, want the scripted gofer_work_remind", it.ToolCall)
	}
	if _, err := s.AnswerInteractionByHuman(plain.ID, it.ID, acptest.AllowOnceOptionID, ""); err != nil {
		t.Fatalf("AnswerInteractionByHuman: %v", err)
	}
	waitSessionTurn(t, s, plain.ID, 1)
	_ = s.EndSession(plain.ID)
	s.Wait(plain.ID)
}
