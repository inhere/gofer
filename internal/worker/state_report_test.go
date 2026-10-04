package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/wsproto"
)

func TestWorkerMessengerListAgentsDispatchReturnsRawListing(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON_AGENTS", "1")
	jobs := &stubJobs{}
	cl, frames, sessionURL := dialLiveClient(t, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl.handleDispatch(ctx, sessionURL, wsproto.Dispatch{
		JobID: "list1", Runner: builtinLocalRunner,
		Messenger: &wsproto.MessengerDispatch{Op: "list_agents", Command: testcmd.Cmd(t, "x")},
	})
	res, text := waitForMessengerFrames(t, frames, "list1")
	if res.Status != job.StatusDone {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(text, "Peer sessions (2)") || strings.Contains(text, "retold") {
		t.Fatalf("stdout must be the raw ListAgents tool_result, got %q", text)
	}
	snap := cl.residentMessenger.Snapshot(builtinLocalRunner)
	if len(snap.Deliveries) != 1 || snap.Deliveries[0].Op != "list_agents" {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestStateReportCarriesMessengerAndDirs(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("GOFER_WORKSPACE", ws)
	to := t.TempDir()
	missing := filepath.Join(to, "gone")
	jobs := &stubJobs{}
	cl, _, _ := dialLiveClient(t, jobs)
	cl.dirsFn = func() ([]config.WorkerRoot, map[string]string) {
		return []config.WorkerRoot{{From: "/srv", To: to}, {From: "/old", To: missing}},
			map[string]string{"b": missing, "a": to}
	}
	m, d := cl.stateReport()
	if m.Status != "stopped" {
		t.Fatalf("messenger status = %q, want stopped", m.Status)
	}
	if d.Workspace == nil || d.Workspace.Path != ws || !d.Workspace.Exists {
		t.Fatalf("workspace = %+v", d.Workspace)
	}
	if len(d.Roots) != 2 || !d.Roots[0].Exists || d.Roots[1].Exists {
		t.Fatalf("roots = %+v", d.Roots)
	}
	if len(d.Projects) != 2 || d.Projects[0].Key != "a" || !d.Projects[0].Exists || d.Projects[1].Exists {
		t.Fatalf("projects = %+v", d.Projects)
	}
	if err := os.RemoveAll(ws); err != nil {
		t.Fatal(err)
	}
	if _, d2 := cl.stateReport(); d2.Workspace.Exists {
		t.Fatal("a vanished workspace must be reported missing on the next report")
	}
}
