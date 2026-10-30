package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
)

type refsToolInput struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type refsToolOutput struct {
	Refs []client.Ref `json:"refs"`
}

func refsHandler(b Backend) mcp.ToolHandlerFor[refsToolInput, refsToolOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in refsToolInput) (*mcp.CallToolResult, refsToolOutput, error) {
		in.Kind = strings.TrimSpace(in.Kind)
		in.ID = strings.TrimSpace(in.ID)
		if in.Kind == "" || in.ID == "" {
			return nil, refsToolOutput{}, fmt.Errorf("kind and id are required")
		}
		rows, err := b.ListRefs(in.Kind, in.ID)
		if err != nil {
			return nil, refsToolOutput{}, err
		}
		if rows == nil {
			rows = []client.Ref{}
		}
		return nil, refsToolOutput{Refs: rows}, nil
	}
}
