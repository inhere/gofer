package worker

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/wsproto"
)

// runMCPDispatch dispatches a job carrying MCP servers (and a continuation argv with
// the MCP placeholder) to a worker at proto and returns the frame and the events.
func runMCPDispatch(t *testing.T, proto int) (wsproto.Dispatch, []string) {
	t.Helper()
	h := &fakeHub{workerProto: proto}
	r := newRunnerWithHub(h)
	var (
		mu     sync.Mutex
		events []string
	)
	done := make(chan runner.Result, 1)
	go func() {
		done <- r.Run(context.Background(), runner.Request{
			JobID: "j1",
			Forward: &runner.Forward{
				ProjectKey: "p", Agent: "exec",
				Cmd: []string{"claude", "--resume", "sid", agent.MCPArgsPlaceholder, "-p", "next"},
				MCP: []wsproto.MCPSpec{{Name: "db", Command: "db-mcp", SecretEnv: map[string]string{"DB_URL": "SRC_DB_URL"}}},
			},
			OnJobEvent: func(eventType string, _ map[string]any) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, eventType)
			},
		})
	}()
	deadline := time.Now().Add(wait.Timeout(t, 5*time.Second))
	for time.Now().Before(deadline) && h.getSink() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	sink := h.getSink()
	if sink == nil {
		t.Fatal("sink never registered")
	}
	sink.Finish(wsproto.Result{JobID: "j1", Status: "done", ExitCode: 0})
	select {
	case <-done:
	case <-time.After(wait.Timeout(t, 2*time.Second)):
		t.Fatal("Run did not return after Finish")
	}
	mu.Lock()
	defer mu.Unlock()
	return h.dispatchedFrame(), append([]string(nil), events...)
}

// MCP-06: a v23 worker gets the MCP declarations (variable names, no values) and the
// continuation argv as is; an older one gets neither, the placeholder is stripped, and
// the omission is job.mcp_skipped.
func TestDispatchCarriesMCPByProtocol(t *testing.T) {
	d, events := runMCPDispatch(t, wsproto.MCPMinProtocolVersion)
	if len(d.MCP) != 1 || d.MCP[0].Name != "db" || d.MCP[0].SecretEnv["DB_URL"] != "SRC_DB_URL" {
		t.Fatalf("v23 dispatch mcp = %+v", d.MCP)
	}
	if !slices.Contains(d.Cmd, agent.MCPArgsPlaceholder) || slices.Contains(events, runner.EventMCPSkipped) {
		t.Fatalf("v23: cmd=%v events=%v", d.Cmd, events)
	}

	old, oldEvents := runMCPDispatch(t, wsproto.MCPMinProtocolVersion-1)
	if len(old.MCP) != 0 || slices.Contains(old.Cmd, agent.MCPArgsPlaceholder) {
		t.Fatalf("old worker: mcp=%+v cmd=%v", old.MCP, old.Cmd)
	}
	if !slices.Equal(old.Cmd, []string{"claude", "--resume", "sid", "-p", "next"}) {
		t.Fatalf("old worker cmd = %v", old.Cmd)
	}
	if !slices.Contains(oldEvents, runner.EventMCPSkipped) {
		t.Fatalf("old worker events = %v, want %s", oldEvents, runner.EventMCPSkipped)
	}
}
