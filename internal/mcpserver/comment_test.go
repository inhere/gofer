package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/jobstore"
)

// commentListView / commentSchemaT are the wire shapes this file decodes.
type commentListView struct {
	Comments []commentView `json:"comments"`
}

// TestMCPCommentToolRecordsAgentAuthor: `gofer_comment` speaks as the agent of the job
// that is calling (GOFER_JOB_ID), so the row is author_kind=agent with that job's agent
// key — and, per MCP-05 阶段 A, an AGENT's comment is only recorded: even a mention of
// a perfectly dispatchable agent starts nothing. `gofer_list_comments` reads the thread
// back.
func TestMCPCommentToolRecordsAgentAuthor(t *testing.T) {
	session, jobs := connect(t)

	// The "author": a real job whose agent key the tool must stamp.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_run_job",
		Arguments: map[string]any{
			"project_key": "self", "agent": "exec", "runner": "local",
			"cmd": []string{"go", "version"}, "cwd": ".", "timeout_sec": 30,
		},
	})
	if err != nil {
		t.Fatalf("CallTool run_job: %v", err)
	}
	var created jobView
	structured(t, res, &created)
	if _, ok := jobs.Wait(created.ID); !ok {
		t.Fatalf("Wait: job %s not found", created.ID)
	}

	t.Setenv("GOFER_JOB_ID", created.ID)
	commentRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_comment",
		Arguments: map[string]any{
			"scope": "job", "id": created.ID, "body": "@exec 请你重跑一遍",
		},
	})
	if err != nil {
		t.Fatalf("CallTool gofer_comment: %v", err)
	}
	var got commentView
	structured(t, commentRes, &got)
	if got.ID == "" || got.Scope != jobstore.CommentScopeJob || got.ScopeID != created.ID {
		t.Fatalf("comment = %+v", got)
	}
	if got.AuthorKind != jobstore.CommentAuthorAgent || got.Author != "exec" {
		t.Fatalf("author = %s/%s, want exec/agent", got.Author, got.AuthorKind)
	}
	if len(got.Mentions) != 1 || got.Mentions[0] != "exec" {
		t.Fatalf("mentions = %v, want [exec] (the mention is parsed and recorded)", got.Mentions)
	}
	if len(got.Dispatched) != 0 || got.TriggeredJobID != "" {
		t.Fatalf("an agent's mention dispatched: %+v", got)
	}
	if evs, err := jobs.ListJobEvents(created.ID, 0); err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	} else {
		for _, e := range evs {
			if e.Type == "comment.triggered" {
				t.Fatalf("comment.triggered was recorded for an agent comment: %+v", e)
			}
		}
	}

	// The thread is readable through the read-only tool.
	listRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_list_comments",
		Arguments: map[string]any{"scope": "job", "id": created.ID},
	})
	if err != nil {
		t.Fatalf("CallTool gofer_list_comments: %v", err)
	}
	var listed commentListView
	structured(t, listRes, &listed)
	if len(listed.Comments) != 1 || listed.Comments[0].ID != got.ID {
		t.Fatalf("list comments = %+v, want the one comment", listed.Comments)
	}
	if listed.Comments[0].Body != "@exec 请你重跑一遍" {
		t.Fatalf("body did not round-trip: %q", listed.Comments[0].Body)
	}
}

// TestMCPCommentRequiresAnAuthor: with no GOFER_JOB_ID in the environment and no
// explicit as_job, the tool refuses — a comment that cannot say who wrote it is worse
// than no comment (every gate downstream is keyed on the author).
func TestMCPCommentRequiresAnAuthor(t *testing.T) {
	session, _ := connect(t)
	t.Setenv("GOFER_JOB_ID", "")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "gofer_comment",
		Arguments: map[string]any{"scope": "job", "id": "job-1", "body": "hi"},
	})
	if err == nil && !res.IsError {
		t.Fatalf("gofer_comment without an author succeeded: %+v", res)
	}
}

// TestCommentToolsDeclareTheirSchema pins the discoverable input shape.
func TestCommentToolsDeclareTheirSchema(t *testing.T) {
	session, _ := connect(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := map[string]map[string]any{}
	for _, tl := range res.Tools {
		if tl.Name != "gofer_comment" && tl.Name != "gofer_list_comments" {
			continue
		}
		b, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatalf("marshal input schema: %v", err)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatalf("unmarshal input schema: %v", err)
		}
		found[tl.Name] = schema.Properties
	}
	for _, name := range []string{"gofer_comment", "gofer_list_comments"} {
		props, ok := found[name]
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		for _, want := range []string{"scope", "id"} {
			if _, ok := props[want]; !ok {
				t.Fatalf("%s input schema is missing %s; properties=%v", name, want, props)
			}
		}
	}
	if _, ok := found["gofer_comment"]["body"]; !ok {
		t.Fatalf("gofer_comment input schema is missing body; properties=%v", found["gofer_comment"])
	}
}
