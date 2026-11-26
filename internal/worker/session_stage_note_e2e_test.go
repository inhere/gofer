package worker_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/needsreply"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wshub"
)

type fixedStageNoter struct {
	mu   sync.Mutex
	line string
	ids  [][]string
}

func (f *fixedStageNoter) TurnNote(_ i18n.Lang, sids ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, sids)
	return f.line
}

// A resident ACP session that runs ON a worker takes, on every turn, the needs-reply
// convention (given once, by the worker's own service) and the stage-flow line (the hub's
// stored fact, put in front of the text by the hub for the first turn and for each say) —
// each exactly once per turn, in front of the person's text.
func TestWorkerSessionTurnsCarryTheNeedsReplyConventionAndTheStageLine(t *testing.T) {
	hub := buildHubSideSel(t, t.TempDir(), t.TempDir(), func(h *wshub.Hub) job.WorkerSelector {
		return sessionSelector{hub: h}
	})
	const line = "[gofer · 流程] STAGE-NOTE-LINE"
	noter := &fixedStageNoter{line: line}
	hub.jobs.SetStageNotes(noter)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cli, _ := buildWorkerWithACPAgent(t, hub.ts.URL, nil, acptest.Options{EchoPrompt: true})
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
	if err := hub.jobs.SaySession(created.ID, "second turn"); err != nil {
		t.Fatal(err)
	}
	waitHostAwaitingTurn(t, hub.jobs, created.ID, 2)

	var stdout string
	wait.For(t, 10*time.Second, "both turns echoed on the hub", func() (bool, any) {
		stdout = getLogs(t, hub.ts, created.ID, "stdout")
		return strings.Contains(stdout, "second turn"), stdout
	})
	if n := strings.Count(stdout, "STAGE-NOTE-LINE"); n != 2 {
		t.Fatalf("stage line appears %d times, want once per turn (2):\n%s", n, stdout)
	}
	for _, text := range []string{"first turn", "second turn"} {
		if !strings.Contains(stdout, line+"\n\n"+text) {
			t.Fatalf("the stage line must sit right before %q:\n%s", text, stdout)
		}
	}
	// the needs-reply convention reaches the worker's session too, once (first turn).
	if n := strings.Count(stdout, needsreply.Marker); n != 1 {
		t.Fatalf("needs-reply convention appears %d times, want 1:\n%s", n, stdout)
	}
	noter.mu.Lock()
	defer noter.mu.Unlock()
	if len(noter.ids) != 2 {
		t.Fatalf("the hub looks the flow up once per turn, got %v", noter.ids)
	}
	for _, ids := range noter.ids {
		if len(ids) == 0 || ids[0] != created.ID {
			t.Fatalf("lookup ids = %v, want the job id first", ids)
		}
	}
}
