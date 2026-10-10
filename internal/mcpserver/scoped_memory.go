package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
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
	mcp.AddTool(s, &mcp.Tool{Name: "gofer_memory_flag", Description: "Report an injected memory / rule as out of date instead of silently working around it: it is marked 「⚠ 待复核（reason）」 wherever it is injected until a human reviews it. Omit scope for the repository tracker of this process's cwd; scope=global|project (+scope_key) for server memories. reason is required."}, memoryFlagHandler(b))
}

type memoryFlagInput struct {
	Key      string `json:"key"`
	Reason   string `json:"reason"`
	Scope    string `json:"scope,omitempty"`
	ScopeKey string `json:"scope_key,omitempty"`
}

// memoryFlagHandler flags a repository memory (scope "") or a server scoped one.
func memoryFlagHandler(b Backend) mcp.ToolHandlerFor[memoryFlagInput, map[string]any] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in memoryFlagInput) (*mcp.CallToolResult, map[string]any, error) {
		key := strings.TrimSpace(in.Key)
		if key == "" {
			return nil, nil, fmt.Errorf("key is required")
		}
		jobID := strings.TrimSpace(os.Getenv("GOFER_JOB_ID"))
		if strings.TrimSpace(in.Scope) != "" {
			scope, scopeKey, err := validScopedMemoryInput(scopedMemoryInput{Scope: in.Scope, ScopeKey: in.ScopeKey, Key: key}, true)
			if err != nil {
				return nil, nil, err
			}
			row, err := b.FlagScopedMemory(scope, scopeKey, key, in.Reason, jobID)
			if err != nil {
				return nil, nil, err
			}
			return nil, map[string]any{"memory": row}, nil
		}
		by := strings.TrimSpace(os.Getenv("GOFER_CALLER"))
		if by == "" {
			by = "mcp"
		}
		flag, err := tracker.NewMemoryFlag(in.Reason, by, jobID, time.Now())
		if err != nil {
			return nil, nil, err
		}
		store, err := tracker.Discover(".", "")
		if err != nil {
			return nil, nil, err
		}
		item, err := store.FlagMemory(key, flag)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"memory": item}, nil
	}
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
