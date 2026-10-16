package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
)

// Session topics over MCP: gofer_session_set_topic (the session names what it works on) and
// gofer_session_find (who works on this?). Both need a running server; the current session is
// identified from the MCP process environment only, never from the working directory.

type sessionSetTopicInput struct {
	// Topic is one sentence; "" leaves the topic unchanged.
	Topic string `json:"topic,omitempty"`
	// Scope are project-root-relative sub-paths this session owns; empty leaves it unchanged.
	Scope []string `json:"scope,omitempty"`
	// ClearScope empties the scope (alone or with a topic).
	ClearScope bool `json:"clear_scope,omitempty"`
	// SessionID names the session explicitly; default: the session this process runs in.
	SessionID string `json:"session_id,omitempty"`
}

type sessionFindInput struct {
	Query   string `json:"query,omitempty"`
	Path    string `json:"path,omitempty"`
	Project string `json:"project,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type sessionFindOutput struct {
	Sessions []client.SessionFound `json:"sessions"`
}

func registerSessionTopicTools(s *mcp.Server, b Backend) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "gofer_session_set_topic",
		Description: "Record what THIS session works on so others (and the person) can find it: topic is one short sentence, scope are the project-relative sub-paths you own (clear_scope empties them). " +
			"Omitted fields stay unchanged. The current session comes from the environment (session_id overrides); an ended session is refused. Needs a running gofer server.",
	}, sessionSetTopicHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name: "gofer_session_find",
		Description: "Find which live session works on something (read-only): query matches topics / session names / projects, path matches the sessions' scopes. Returns ranked sessions with the reasons they matched; " +
			"maybe_offline marks one that stopped heart-beating. Needs a running gofer server.",
	}, sessionFindHandler(b))
}

func sessionSetTopicHandler(b Backend) mcp.ToolHandlerFor[sessionSetTopicInput, client.AgentSession] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in sessionSetTopicInput) (*mcp.CallToolResult, client.AgentSession, error) {
		var p client.SessionTopicPatch
		if t := strings.TrimSpace(in.Topic); t != "" {
			p.Topic = &t
		}
		switch {
		case in.ClearScope && len(in.Scope) > 0:
			return nil, client.AgentSession{}, fmt.Errorf("scope and clear_scope are mutually exclusive")
		case in.ClearScope:
			empty := []string{}
			p.Scope = &empty
		case len(in.Scope) > 0:
			sc := in.Scope
			p.Scope = &sc
		}
		if p.Topic == nil && p.Scope == nil {
			return nil, client.AgentSession{}, fmt.Errorf("give a topic and/or a scope (or clear_scope)")
		}
		a, err := b.SetSessionTopic(strings.TrimSpace(in.SessionID), p)
		return nil, a, err
	}
}

func sessionFindHandler(b Backend) mcp.ToolHandlerFor[sessionFindInput, sessionFindOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in sessionFindInput) (*mcp.CallToolResult, sessionFindOutput, error) {
		if strings.TrimSpace(in.Query) == "" && strings.TrimSpace(in.Path) == "" {
			return nil, sessionFindOutput{}, fmt.Errorf("give a query and/or a path")
		}
		r, err := b.FindSessions(client.SessionFindOpts{Q: strings.TrimSpace(in.Query), Path: strings.TrimSpace(in.Path), Project: strings.TrimSpace(in.Project), Limit: in.Limit})
		if r == nil {
			r = []client.SessionFound{}
		}
		return nil, sessionFindOutput{Sessions: r}, err
	}
}
