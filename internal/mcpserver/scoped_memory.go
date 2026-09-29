package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type scopedMemoryInput struct {
	Scope    string   `json:"scope"`
	ScopeKey string   `json:"scope_key,omitempty"`
	Key      string   `json:"key,omitempty"`
	Content  string   `json:"content,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Keyword  string   `json:"keyword,omitempty"`
}

func registerScopedMemoryTools(s *mcp.Server, b Backend) {
	mcp.AddTool(s, &mcp.Tool{Name: "gofer_memory_list", Description: "List server global or project memories, optionally filtered by keyword and tags."}, memoryListHandler(b))
	mcp.AddTool(s, &mcp.Tool{Name: "gofer_memory_get", Description: "Read one server global or project memory."}, memoryGetHandler(b))
	mcp.AddTool(s, &mcp.Tool{Name: "gofer_memory_set", Description: "Write a server global or project memory."}, memorySetHandler(b))
	mcp.AddTool(s, &mcp.Tool{Name: "gofer_memory_rm", Description: "Soft-delete a server global or project memory."}, memoryRemoveHandler(b))
}

func memoryListHandler(b Backend) mcp.ToolHandlerFor[scopedMemoryInput, map[string]any] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in scopedMemoryInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, key, err := validScopedMemoryInput(in, false)
		if err != nil {
			return nil, map[string]any{}, err
		}
		rows, err := b.ListScopedMemories(scope, key, in.Keyword, in.Tags)
		return nil, map[string]any{"memories": rows}, err
	}
}
func memoryGetHandler(b Backend) mcp.ToolHandlerFor[scopedMemoryInput, jobstore.ScopedMemory] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in scopedMemoryInput) (*mcp.CallToolResult, jobstore.ScopedMemory, error) {
		scope, key, err := validScopedMemoryInput(in, true)
		if err != nil {
			return nil, jobstore.ScopedMemory{}, err
		}
		row, err := b.GetScopedMemory(scope, key, in.Key)
		return nil, row, err
	}
}
func memorySetHandler(b Backend) mcp.ToolHandlerFor[scopedMemoryInput, jobstore.ScopedMemory] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in scopedMemoryInput) (*mcp.CallToolResult, jobstore.ScopedMemory, error) {
		scope, key, err := validScopedMemoryInput(in, true)
		if err != nil {
			return nil, jobstore.ScopedMemory{}, err
		}
		if strings.TrimSpace(in.Content) == "" {
			return nil, jobstore.ScopedMemory{}, fmt.Errorf("content is required")
		}
		row, err := b.PutScopedMemory(scope, key, in.Key, in.Content, in.Tags)
		return nil, row, err
	}
}
func memoryRemoveHandler(b Backend) mcp.ToolHandlerFor[scopedMemoryInput, map[string]string] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in scopedMemoryInput) (*mcp.CallToolResult, map[string]string, error) {
		scope, key, err := validScopedMemoryInput(in, true)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]string{"status": "deleted"}, b.DeleteScopedMemory(scope, key, in.Key)
	}
}

func validScopedMemoryInput(in scopedMemoryInput, requireKey bool) (string, string, error) {
	scope := strings.TrimSpace(in.Scope)
	key := strings.TrimSpace(in.ScopeKey)
	if _, _, err := jobstore.NormalizeScopedMemoryScope(scope, key); err != nil {
		return "", "", err
	}
	if requireKey && strings.TrimSpace(in.Key) == "" {
		return "", "", fmt.Errorf("key is required")
	}
	return scope, key, nil
}
