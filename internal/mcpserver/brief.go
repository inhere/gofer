package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/brief"
	"github.com/inhere/gofer/internal/tracker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type briefInput struct {
	ID       string `json:"id"`
	MaxLines int    `json:"max_lines,omitempty"`
	// Project overrides the project key used for job / plan / project-memory lookups
	// (default: the repository tracker's project_key).
	Project string `json:"project,omitempty"`
}

type briefOutput struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Text string `json:"text"`
}

func registerBriefTools(s *mcp.Server, b Backend) {
	mcp.AddTool(s, &mcp.Tool{Name: "gofer_issue_brief", Description: "Handoff brief (接手包) of a repository issue — the same text as `gofer issue brief <id>`: the issue, its parent/siblings/deps, design docs that name it, related commits and code entries, linked jobs / plans, applicable memories and takeover hints. Read this first when taking over an issue. Uses the repository tracker of this process's cwd."}, issueBriefHandler(b))
	mcp.AddTool(s, &mcp.Tool{Name: "gofer_plan_brief", Description: "Handoff brief (接手包) of a plan — the same text as `gofer plan brief <id>`: fields, todos (status / deps / acceptance / job outcome), handoff note and a short brief of each issue it names."}, planBriefHandler(b))
}

// briefOptionsFor binds the brief inputs of an MCP call: the cwd's repository tracker
// and, in client mode, the central serve. A standalone (local) MCP has no server
// client, so its server sections say so.
func briefOptionsFor(b Backend, in briefInput) brief.Options {
	store, _ := tracker.Discover(".", "")
	opts := brief.Options{Store: store, MaxLines: in.MaxLines, ProjectKey: strings.TrimSpace(in.Project)}
	if cb, ok := b.(*clientBackend); ok {
		opts.Client, opts.ClientNote = brief.Connect(cb.cli)
	} else {
		opts.ClientNote = "standalone MCP has no server client"
	}
	if opts.ProjectKey == "" && store != nil {
		if cfg, err := store.ReadConfig(); err == nil {
			opts.ProjectKey = strings.TrimSpace(cfg.ProjectKey)
		}
	}
	return opts
}

func issueBriefHandler(b Backend) mcp.ToolHandlerFor[briefInput, briefOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in briefInput) (*mcp.CallToolResult, briefOutput, error) {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, briefOutput{}, fmt.Errorf("id is required")
		}
		opts := briefOptionsFor(b, in)
		if opts.Store == nil {
			return nil, briefOutput{}, fmt.Errorf("no repository tracker in the MCP server's cwd")
		}
		out, err := brief.IssueBrief(id, opts)
		if err != nil {
			return nil, briefOutput{}, err
		}
		return nil, briefOutput{Kind: out.Kind, ID: out.ID, Text: out.Text()}, nil
	}
}

func planBriefHandler(b Backend) mcp.ToolHandlerFor[briefInput, briefOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in briefInput) (*mcp.CallToolResult, briefOutput, error) {
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return nil, briefOutput{}, fmt.Errorf("id is required")
		}
		out, err := brief.PlanBrief(id, briefOptionsFor(b, in))
		if err != nil {
			return nil, briefOutput{}, err
		}
		return nil, briefOutput{Kind: out.Kind, ID: out.ID, Text: out.Text()}, nil
	}
}
