package worker_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/needsreply"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wshub"
)

// runWorkerSessionTurn runs one turn of a resident ACP session ON a worker whose agent ends
// its reply with tail, waits until the hub shows the session awaiting input, and returns the
// hub service and the job.
func runWorkerSessionTurn(t *testing.T, tail string) (*job.Service, *httptest.Server, job.JobResult) {
	t.Helper()
	hub := buildHubSideSel(t, t.TempDir(), t.TempDir(), func(h *wshub.Hub) job.WorkerSelector {
		return sessionSelector{hub: h}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cli, _ := buildWorkerWithACPAgent(t, hub.ts.URL, nil, acptest.Options{FinalTail: tail})
	clientErr := worker.StartClient(t, ctx, cli)
	waitWorkerOnline(t, hub.hub)
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "acpbot", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "first turn", Cwd: ".", Session: true, TimeoutSec: 120, IdleTimeoutSec: 120,
	})
	waitHostAwaitingTurn(t, hub.jobs, created.ID, 1)
	t.Cleanup(func() {
		postHub(t, hub.ts, "/v1/jobs/"+created.ID+"/end", map[string]string{})
		hub.jobs.Wait(created.ID)
		stopWorkerClient(t, cli, cancel, clientErr)
	})
	res, _ := hub.jobs.Get(created.ID)
	return hub.jobs, hub.ts, res
}

// The needs-reply marker of a turn on a worker reaches the hub's stored facts: the worker's
// runner strips the line and the status frame carries the verdict, so the hub's
// awaiting_input event is marked and the 「等我」 query finds the session.
func TestWorkerSessionTurnMarkerReachesTheHub(t *testing.T) {
	jobs, ts, res := runWorkerSessionTurn(t, "\n"+needsreply.Marker)
	wait.For(t, 10*time.Second, "hub marked awaiting turn", func() (bool, any) {
		turns, err := jobs.Meta().ListMarkedAwaitingTurns()
		if err != nil {
			t.Fatal(err)
		}
		_, ok := turns[res.ID]
		return ok, turns
	})
	// the marker line never reaches the hub's log either
	stdout := getLogs(t, ts, res.ID, "stdout")
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == needsreply.Marker {
			t.Fatalf("marker line in hub stdout:\n%s", stdout)
		}
	}
}

func TestWorkerSessionTurnWithoutMarkerIsNotMarkedOnTheHub(t *testing.T) {
	jobs, _, res := runWorkerSessionTurn(t, "")
	turns, err := jobs.Meta().ListMarkedAwaitingTurns()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := turns[res.ID]; ok {
		t.Fatalf("an unmarked turn is marked on the hub: %v", turns)
	}
}
