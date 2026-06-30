package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/answerguard"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/presence"
)

// liveJobOwnedBy submits a long-lived exec job over the run_job tool with an explicit
// origin_agent and waits until it is running/pending, returning its id.
func liveJobOwnedBy(t *testing.T, session *mcp.ClientSession, jobs *job.Service, owner string) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"sleep", "30"}, "cwd": ".", "timeout_sec": 60,
			"origin_agent": owner,
		},
	})
	if err != nil {
		t.Fatalf("run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if created.OriginAgent != owner {
		t.Fatalf("origin_agent = %q, want %q", created.OriginAgent, owner)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := jobs.Get(created.ID); ok &&
			(r.Status == job.StatusRunning || r.Status == job.StatusPendingInteraction) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = jobs.Cancel(created.ID) })
	return created.ID
}

// TestAnswerInteractionGateE2E proves the派生作答闸 fires through the full MCP answer chain
// (handler→backend→job): a sup-attributed answer (server originAgent registered role=supervisor)
// is refused on a non-whitelisted confirmation but allowed on a whitelisted choice, stamping
// answered_by=agent:<sup>. The job is owned by a DIFFERENT agent so the sup is not放行 as owner.
func TestAnswerInteractionGateE2E(t *testing.T) {
	jobs, projects, agents, pres := testCore(t)
	// Gate wired over the REAL presence (role lookup) + a "^pick " whitelist.
	jobs.SetAnswerGuard(answerguard.New([]string{"^pick "}, pres))

	// Register a supervisor driver; the MCP server attributes every answer to it.
	supReg, err := pres.Register(presence.RegisterInput{Name: "sup-daemon", Role: "supervisor"})
	if err != nil {
		t.Fatalf("register sup: %v", err)
	}
	supID := supReg.AgentID

	b := newLocalBackend(jobs, projects, agents, pres)
	session := connectTo(t, newServer(b, supID, ""))

	jobID := liveJobOwnedBy(t, session, jobs, "agt_owner")

	// (A) confirmation answered by the sup → refused (tool error); stays pending.
	c1, err := jobs.CreateInteraction(jobID, job.InteractionInput{Type: job.InteractionTypeConfirmation, Prompt: "delete prod?"})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_answer_interaction",
		Arguments: map[string]any{"id": jobID, "interaction_id": c1.ID, "answer": "yes"},
	})
	if err != nil {
		t.Fatalf("answer transport: %v", err)
	}
	if !res.IsError {
		t.Fatalf("sup confirmation answer must be a tool error (gate refusal)")
	}
	if list, _ := jobs.GetInteractions(jobID); list[0].Status != job.InteractionPending {
		t.Fatalf("refused interaction must stay pending, got %s", list[0].Status)
	}

	// (B) whitelisted choice answered by the sup → allowed, answered_by=agent:<sup>.
	c2, err := jobs.CreateInteraction(jobID, job.InteractionInput{
		Type: job.InteractionTypeChoice, Prompt: "pick one format",
		Options: []job.InteractionOption{{Value: "json"}, {Value: "yaml"}},
	})
	if err != nil {
		t.Fatalf("CreateInteraction(2): %v", err)
	}
	res2, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_answer_interaction",
		Arguments: map[string]any{"id": jobID, "interaction_id": c2.ID, "answer": "json"},
	})
	if err != nil {
		t.Fatalf("answer(2) transport: %v", err)
	}
	var answered interactionView
	structured(t, res2, &answered)
	if answered.Status != job.InteractionAnswered {
		t.Fatalf("sup whitelisted choice must be answered, got %s", answered.Status)
	}
	if answered.AnsweredBy != "agent:"+supID {
		t.Fatalf("answered_by = %q, want agent:%s", answered.AnsweredBy, supID)
	}
}
