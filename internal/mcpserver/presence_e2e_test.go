package mcpserver

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/presence"
)

// TestPresenceToolsE2E drives the full multi-agent collaboration semantic through
// the actual gofer_* presence tools over the SDK transport — the deterministic
// in-process equivalent of the dual-mcp-client E2E (P1.5): two driver agents
// register on one serve, see each other in presence, A posts to B, B polls +
// consumes, token isolation holds, and role: fan-out reaches every matching agent.
func TestPresenceToolsE2E(t *testing.T) {
	session, _ := connect(t)
	ctx := context.Background()

	reg := func(name, role string) presence.RegisterResult {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "gofer_register",
			Arguments: map[string]any{"name": name, "role": role},
		})
		if err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		var out presence.RegisterResult
		structured(t, res, &out)
		if out.AgentID == "" || out.AgentToken == "" {
			t.Fatalf("register %s returned empty ids: %+v", name, out)
		}
		return out
	}

	a := reg("alice", "reviewer")
	b := reg("bob", "")

	// gofer_list_presence shows both registered agents.
	lres, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "gofer_list_presence"})
	if err != nil {
		t.Fatalf("list_presence: %v", err)
	}
	var ll listPresenceOutput
	structured(t, lres, &ll)
	if len(ll.Agents) != 2 {
		t.Fatalf("presence count=%d, want 2: %+v", len(ll.Agents), ll.Agents)
	}

	// A → B direct post; delivered=1.
	pres, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "gofer_post_message",
		Arguments: map[string]any{
			"from_agent": a.AgentID, "to": b.AgentID,
			"kind": "task", "body": "审 PR", "ref": "job:1",
		},
	})
	if err != nil {
		t.Fatalf("post_message: %v", err)
	}
	var pp postMessageOutput
	structured(t, pres, &pp)
	if pp.Delivered != 1 {
		t.Fatalf("delivered=%d, want 1", pp.Delivered)
	}

	poll := func(id, token string, peek bool) pollInboxOutput {
		args := map[string]any{"agent_id": id, "agent_token": token}
		if peek {
			args["ack"] = false
		}
		r, perr := session.CallTool(ctx, &mcp.CallToolParams{Name: "gofer_poll_inbox", Arguments: args})
		if perr != nil {
			t.Fatalf("poll_inbox: %v", perr)
		}
		var o pollInboxOutput
		structured(t, r, &o)
		return o
	}

	// B polls and consumes the task.
	got := poll(b.AgentID, b.AgentToken, false)
	if len(got.Messages) != 1 {
		t.Fatalf("bob inbox=%d, want 1", len(got.Messages))
	}
	if got.Messages[0].FromAgent != a.AgentID || got.Messages[0].Body != "审 PR" || got.Messages[0].Ref != "job:1" {
		t.Fatalf("unexpected message: %+v", got.Messages[0])
	}
	// Second poll is empty (already read).
	if again := poll(b.AgentID, b.AgentToken, false); len(again.Messages) != 0 {
		t.Fatalf("inbox not consumed: %+v", again.Messages)
	}

	// Wrong token → tool error result (soft isolation).
	bad, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "gofer_poll_inbox",
		Arguments: map[string]any{"agent_id": b.AgentID, "agent_token": "wrong"},
	})
	if err != nil {
		t.Fatalf("poll bad-token transport err: %v", err)
	}
	if !bad.IsError {
		t.Fatal("expected IsError for wrong agent_token")
	}

	// role: fan-out — register a second reviewer; alice is also reviewer, so
	// role:reviewer reaches both (2 rows).
	reg("rev2", "reviewer")
	fres, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "gofer_post_message",
		Arguments: map[string]any{
			"from_agent": b.AgentID, "to": "role:reviewer", "kind": "task", "body": "x",
		},
	})
	if err != nil {
		t.Fatalf("role post: %v", err)
	}
	var fp postMessageOutput
	structured(t, fres, &fp)
	if fp.Delivered != 2 {
		t.Fatalf("role fan-out delivered=%d, want 2 (alice+rev2)", fp.Delivered)
	}
}
