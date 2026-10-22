package job

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// gofer-81b4: a job may carry its OWN approval gate (JobRequest.SessionApproval, the
// 常驻收件会话's mail.agents.<名字>.approval). It REPLACES the project's policy for that one
// job, so a mailbox can ask a human before writes while the project keeps auto-allowing.

// submitSessionApprovalJob submits the scripted acp-agent prompt with an explicit
// session approval policy (nil = none, i.e. the project policy).
func submitSessionApprovalJob(t *testing.T, s *Service, ap *config.ApprovalConfig) string {
	t.Helper()
	res, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 30, SessionApproval: ap,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return res.ID
}

// TestSessionApprovalAsksWhereProjectAutoAllows: the project gate is off (every
// permission is auto-allowed) and the job brings mode=ask — a write-kind tool call must
// park on a human interaction. This is the 收件会话 case: the project is untouched for
// every other ACP session.
func TestSessionApprovalAsksWhereProjectAutoAllows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{}) // project gate: off
	jobID := submitSessionApprovalJob(t, s, &config.ApprovalConfig{Mode: config.ApprovalAsk})

	it := waitPermissionInteraction(t, s, jobID, 15*time.Second)
	if it.ToolCall == nil || it.ToolCall.Kind != acp.ToolKindEdit {
		t.Fatalf("tool_call = %+v, want the scripted edit-kind request", it.ToolCall)
	}
	if !strings.Contains(it.PolicyHint, config.ApprovalAsk) {
		t.Errorf("policy_hint = %q, want it to name the job's mode %q", it.PolicyHint, config.ApprovalAsk)
	}
	// The default deadline of the override's own policy is on the card.
	if got := it.ExpiresAt - it.CreatedAt; got != int64(config.DefaultApprovalTimeoutSec) {
		t.Errorf("expires_at - created_at = %d, want %d", got, config.DefaultApprovalTimeoutSec)
	}
	if _, err := s.AnswerInteractionByHuman(jobID, it.ID, acptest.AllowOnceOptionID, ""); err != nil {
		t.Fatalf("AnswerInteractionByHuman: %v", err)
	}
	final, ok := s.Wait(jobID)
	if !ok {
		t.Fatalf("job %s not found", jobID)
	}
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	// The approval policy rides request_json: the row says how it was approved.
	var stored JobRequest
	if err := json.Unmarshal([]byte(final.RequestJSON), &stored); err != nil {
		t.Fatalf("decode request_json: %v", err)
	}
	if stored.SessionApproval == nil || stored.SessionApproval.Mode != config.ApprovalAsk {
		t.Errorf("request_json session_approval = %+v, want mode=ask", stored.SessionApproval)
	}
}

// TestSessionApprovalAutoAllowsReadKinds: the override is mode=ask, so its own
// auto_allow_kinds still release a read-kind tool call without a human.
func TestSessionApprovalAutoAllowsReadKinds(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{PermissionKind: acp.ToolKindRead})
	jobID := submitSessionApprovalJob(t, s, &config.ApprovalConfig{Mode: config.ApprovalAsk})

	final, ok := s.Wait(jobID)
	if !ok {
		t.Fatalf("job %s not found", jobID)
	}
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if list, _ := s.GetInteractions(jobID); len(list) != 0 {
		t.Fatalf("an auto-allowed read raised %d interactions, want none: %+v", len(list), list)
	}
	asks := readPermAsks(t, s, root, jobID)
	if len(asks) != 1 || asks[0].outcome != "selected" || asks[0].kind != "allow_once" {
		t.Fatalf("agent saw %+v, want one selected allow_once", asks)
	}
}

// TestSessionApprovalReplacesAskingProject: the override REPLACES the project's policy
// instead of tightening it — a project at ask with a job at off auto-allows again. (An
// agent's own acp.permission_policy can still raise it; that is the only-tighten rule.)
func TestSessionApprovalReplacesAskingProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newACPServiceWith(t, root, acptest.Options{}, &config.ApprovalConfig{Mode: config.ApprovalAsk}, "")
	jobID := submitSessionApprovalJob(t, s, &config.ApprovalConfig{Mode: config.ApprovalOff})

	final, ok := s.Wait(jobID)
	if !ok {
		t.Fatalf("job %s not found", jobID)
	}
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if list, _ := s.GetInteractions(jobID); len(list) != 0 {
		t.Fatalf("job-level mode=off raised %d interactions, want none: %+v", len(list), list)
	}
	if asks := readPermAsks(t, s, root, jobID); len(asks) != 1 || asks[0].kind != "allow_once" {
		t.Fatalf("agent saw %+v, want one auto allow_once", asks)
	}
}

// TestSessionApprovalSurvivesResume: the continuation of a job that carried its own
// approval gate carries it again (it rides request_json and continuationBase inherits
// it), so a resumed 收件会话 still asks its human.
func TestSessionApprovalSurvivesResume(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{}) // project gate: off
	src := submitSessionApprovalJob(t, s, &config.ApprovalConfig{Mode: config.ApprovalAsk})

	it := waitPermissionInteraction(t, s, src, 15*time.Second)
	if _, err := s.AnswerInteractionByHuman(src, it.ID, acptest.AllowOnceOptionID, ""); err != nil {
		t.Fatalf("AnswerInteractionByHuman: %v", err)
	}
	if final, ok := s.Wait(src); !ok || final.Status != StatusDone {
		t.Fatalf("source status = %+v, want done", final)
	}

	resumed, err := s.ResumeJob(src, "keep going", "", "caller-mail")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	if resumed.RequestJSON == "" {
		t.Fatal("the continuation recorded no request_json")
	}
	var got JobRequest
	if err := json.Unmarshal([]byte(resumed.RequestJSON), &got); err != nil {
		t.Fatalf("decode the continuation's request_json: %v", err)
	}
	if got.SessionApproval == nil || got.SessionApproval.Mode != config.ApprovalAsk {
		t.Fatalf("continuation session_approval = %+v, want the source's ask", got.SessionApproval)
	}
	// And it is ENFORCED: the continuation's turn asks again.
	it2 := waitPermissionInteraction(t, s, resumed.ID, 15*time.Second)
	if _, err := s.AnswerInteractionByHuman(resumed.ID, it2.ID, acptest.AllowOnceOptionID, ""); err != nil {
		t.Fatalf("AnswerInteractionByHuman(continuation): %v", err)
	}
	wait.For(t, 15*time.Second, "the continuation parks at awaiting_input", func() (bool, any) {
		cur, ok := s.Get(resumed.ID)
		return ok && cur.Status == StatusAwaitingInput, cur.Status
	})
	if err := s.EndSession(resumed.ID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, ok := s.Wait(resumed.ID); !ok {
		t.Fatalf("continuation %s disappeared", resumed.ID)
	}
}
