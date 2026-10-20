package mcpserver

import (
	"context"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gofer_memory_flag flags a repository memory (no scope, the process cwd's tracker) and
// a server scoped memory (scope=project).
func TestMemoryFlagTool(t *testing.T) {
	session, jobs := connect(t)
	root := t.TempDir()
	store, _, err := tracker.Init(root, "mcpflag", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMemory("verify", "make test", "me"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("GOFER_JOB_ID", "job-x")
	call := func(args map[string]any) *mcp.CallToolResult {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "gofer_memory_flag", Arguments: args})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		return res
	}
	if res := call(map[string]any{"key": "verify"}); !res.IsError {
		t.Fatalf("missing reason should fail")
	}
	var out struct {
		Memory tracker.Memory `json:"memory"`
	}
	structured(t, call(map[string]any{"key": "verify", "reason": "renamed"}), &out)
	if len(out.Memory.Flags) != 1 || out.Memory.Flags[0].Job != "job-x" {
		t.Fatalf("repo flag = %+v", out.Memory.Flags)
	}
	if got, _ := store.Memory("verify"); len(got.Flags) != 1 {
		t.Fatalf("repo memory not flagged on disk: %+v", got)
	}

	if _, err := jobs.Meta().PutScopedMemory(jobstore.ScopedMemoryProject, "self", "deploy", "body", nil, "alice"); err != nil {
		t.Fatal(err)
	}
	var scoped struct {
		Memory jobstore.ScopedMemory `json:"memory"`
	}
	structured(t, call(map[string]any{"key": "deploy", "reason": "host moved", "scope": "project", "scope_key": "self"}), &scoped)
	if len(scoped.Memory.Flags) != 1 || scoped.Memory.Flags[0].Reason != "host moved" {
		t.Fatalf("scoped flag = %+v", scoped.Memory)
	}
}

// gofer_memory_set takes an optional title; gofer_memory_get / list return it and a
// later write without a title keeps it.
func TestMemorySetToolTitle(t *testing.T) {
	session, _ := connect(t)
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		return res
	}
	scope := map[string]any{"scope": "project", "scope_key": "self", "key": "deploy"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range scope {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	var row jobstore.ScopedMemory
	structured(t, call("gofer_memory_set", with(map[string]any{"content": "body", "title": "部署手册"})), &row)
	if row.Title != "部署手册" {
		t.Fatalf("set: %+v", row)
	}
	structured(t, call("gofer_memory_set", with(map[string]any{"content": "body v2"})), &row)
	structured(t, call("gofer_memory_get", scope), &row)
	if row.Title != "部署手册" || row.Content != "body v2" {
		t.Fatalf("get after keep: %+v", row)
	}
	var list struct {
		Memories []jobstore.ScopedMemory `json:"memories"`
	}
	structured(t, call("gofer_memory_list", map[string]any{"scope": "project", "scope_key": "self"}), &list)
	if len(list.Memories) != 1 || list.Memories[0].Title != "部署手册" {
		t.Fatalf("list: %+v", list.Memories)
	}
	if res := call("gofer_memory_set", with(map[string]any{"content": "x", "title": "a\nb"})); !res.IsError {
		t.Fatal("multi-line title should fail")
	}
}
