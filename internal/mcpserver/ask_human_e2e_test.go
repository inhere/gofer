package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestAskHumanAnsweredE2E covers 验收2 branch 1: the gofer_ask_human tool call
// is blocked mid-poll when a HUMAN answers through another channel (the store,
// which is where the web answer endpoint lands server-side); the tool then
// returns {state:"answered", answer}.
func TestAskHumanAnsweredE2E(t *testing.T) {
	fastAskHumanPoll(t)
	jobs, projects, agents, pres := testCore(t)
	session := connectTo(t, newServer(newLocalBackend(jobs, projects, agents, pres), "", "", ""))

	// Plan raised through the MCP tool itself (full chain).
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_create_plan",
		Arguments: map[string]any{"title": "decision e2e"},
	})
	if err != nil {
		t.Fatalf("create_plan: %v", err)
	}
	var plan planView
	structured(t, res, &plan)

	type callResult struct {
		res *mcp.CallToolResult
		err error
	}
	done := make(chan callResult, 1)
	go func() {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "gofer_ask_human",
			Arguments: map[string]any{
				"plan_id": plan.PlanID, "title": "deploy window", "question": "which window?",
				"options": []string{"tonight", "tomorrow"}, "timeout_sec": 30,
			},
		})
		done <- callResult{res, err}
	}()

	// Answer while the tool call is blocked.
	var decID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, err := jobs.Meta().ListDecisions(jobstore.DecisionOpen, plan.PlanID)
		if err != nil {
			t.Fatalf("ListDecisions: %v", err)
		}
		if len(list) == 1 {
			decID = list[0].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if decID == "" {
		t.Fatal("no OPEN decision landed")
	}
	if ok, err := jobs.Meta().AnswerDecision(decID, "tonight", "human"); err != nil || !ok {
		t.Fatalf("AnswerDecision ok=%v err=%v", ok, err)
	}

	select {
	case rc := <-done:
		if rc.err != nil {
			t.Fatalf("ask_human: %v", rc.err)
		}
		var out askHumanOutput
		structured(t, rc.res, &out)
		if out.State != "answered" || out.Answer != "tonight" {
			t.Fatalf("ask_human output = %+v, want answered/tonight", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ask_human did not return after the answer")
	}
}

// TestAskHumanExpiredE2E covers 验收2 branch 2: with timeout_sec=2 and nobody
// answering, the tool returns {state:"expired"} at the deadline and the stored
// decision is EXPIRED (lazy expiry on the read path — the tool never actively
// expires anything, plan H1).
func TestAskHumanExpiredE2E(t *testing.T) {
	fastAskHumanPoll(t)
	jobs, projects, agents, pres := testCore(t)
	session := connectTo(t, newServer(newLocalBackend(jobs, projects, agents, pres), "", "", ""))

	start := time.Now()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_ask_human",
		Arguments: map[string]any{
			"title": "unattended", "question": "nobody answers this", "timeout_sec": 2,
		},
	})
	if err != nil {
		t.Fatalf("ask_human: %v", err)
	}
	var out askHumanOutput
	structured(t, res, &out)
	if out.State != "expired" {
		t.Fatalf("ask_human output = %+v, want expired", out)
	}
	if out.Answer != "" {
		t.Fatalf("expired output must not carry an answer: %+v", out)
	}
	// ~timeout_sec wall time, not an instant return (proves it actually waited).
	// asked_at is stored with SECOND granularity (jobstore convention), so
	// truncation can shave up to ~1s off the wait: floor is timeout_sec-1s,
	// not timeout_sec.
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("ask_human returned after %v, want ~2s wait (±1s second-granularity)", elapsed)
	}

	// The stored decision is EXPIRED, not still OPEN.
	list, err := jobs.Meta().ListDecisions("", "")
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 decision, got %+v", list)
	}
	if list[0].State != jobstore.DecisionExpired {
		t.Fatalf("stored decision state = %q, want EXPIRED", list[0].State)
	}
}
