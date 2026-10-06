package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/work"
)

// X2: gofer_session_ask ("带话") and the read-only issue tools. Registered for the
// steward and for an ordinary (unscoped) MCP; the server decides who may call them (the
// steward credential is default-deny, a plain job credential cannot write a session).

type sessionAskInput struct {
	// ID is a terminal session id (gofer_session_list) or an ACP / pty session JOB id.
	ID   string `json:"id"`
	Text string `json:"text"`
	// WorkID names the work item to log it on (default: the session's own item(s)).
	WorkID string `json:"work_id,omitempty"`
}

type issueListInput struct {
	Project   string   `json:"project,omitempty"`
	TrackerID string   `json:"tracker_id,omitempty"`
	Repo      string   `json:"repo,omitempty"`
	Status    string   `json:"status,omitempty"`
	Type      string   `json:"type,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Query     string   `json:"query,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

type issueGetInput struct {
	ID        string `json:"id"`
	TrackerID string `json:"tracker_id,omitempty"`
}

func registerSessionAskTool(s *mcp.Server, b Backend) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "gofer_session_ask",
		Description: "带话: deliver a short message to a RUNNING session over the existing relay channels (\"the resource arrived, you can continue\"). id is a terminal session id or an ACP / pty session job id. " +
			"A session that is not online is an error (nothing is queued — tell the person instead). The message is logged on the session's work item as yours. Not for asking a session to report — use gofer_work_request_report for that.",
	}, sessionAskHandler(b))
}

func registerIssueReadTools(s *mcp.Server, b Backend, scoped string) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_issue_list",
		Description: "List tracker issues from the server's mirror (read-only). Filters: project, tracker_id / repo (rel path), status (open|in_progress|closed…), type, tags[] (all must match), query (id / title / description substring), limit (default 50, max 200). Returns brief rows plus total / truncated.",
	}, issueListHandler(b, scoped))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_issue_get",
		Description: "Read one tracker issue in full from the server's mirror (read-only). Give tracker_id when the same id exists in several repos.",
	}, issueGetHandler(b))
}

func sessionAskHandler(b Backend) mcp.ToolHandlerFor[sessionAskInput, work.AskResult] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in sessionAskInput) (*mcp.CallToolResult, work.AskResult, error) {
		if strings.TrimSpace(in.ID) == "" || strings.TrimSpace(in.Text) == "" {
			return nil, work.AskResult{}, fmt.Errorf("id and text are required")
		}
		r, err := b.SessionAsk(strings.TrimSpace(in.ID), in.Text, strings.TrimSpace(in.WorkID))
		return nil, r, err
	}
}

func issueListHandler(b Backend, scoped string) mcp.ToolHandlerFor[issueListInput, client.IssueListResp] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in issueListInput) (*mcp.CallToolResult, client.IssueListResp, error) {
		project := strings.TrimSpace(in.Project)
		if scoped != "" {
			if project != "" && project != scoped {
				return nil, client.IssueListResp{}, fmt.Errorf("project-scoped MCP(--project %s): cannot list project %q", scoped, project)
			}
			project = scoped
		}
		r, err := b.IssueList(client.IssueListOpts{
			Project: project, TrackerID: strings.TrimSpace(in.TrackerID), Repo: strings.TrimSpace(in.Repo),
			Status: strings.TrimSpace(in.Status), Type: strings.TrimSpace(in.Type), Query: in.Query, Tags: in.Tags, Limit: in.Limit,
		})
		if r.Issues == nil {
			r.Issues = []client.IssueBrief{}
		}
		return nil, r, err
	}
}

func issueGetHandler(b Backend) mcp.ToolHandlerFor[issueGetInput, client.IssueGetResp] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in issueGetInput) (*mcp.CallToolResult, client.IssueGetResp, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, client.IssueGetResp{}, fmt.Errorf("id is required")
		}
		r, err := b.IssueGet(strings.TrimSpace(in.ID), strings.TrimSpace(in.TrackerID))
		return nil, r, err
	}
}
