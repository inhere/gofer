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
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wshub"
)

type fixedTeamNoter struct {
	mu    sync.Mutex
	block string
	began [][]string
	ended int
}

func (f *fixedTeamNoter) TurnNote(_ i18n.Lang, ids ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.began = append(f.began, ids)
	return f.block
}

func (f *fixedTeamNoter) PeekNote(_ i18n.Lang, _ ...string) string { return f.block }

func (f *fixedTeamNoter) TurnEnded(_ ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended++
}

// A resident ACP session on a worker whose job sits on a team's leader seat takes the leader
// block on every turn (put in front of the text by the hub: the first prompt and each say), and
// the host sees every turn end through the shared idle transition — once per turn.
func TestWorkerSessionTurnsCarryTheTeamBlock(t *testing.T) {
	hub := buildHubSideSel(t, t.TempDir(), t.TempDir(), func(h *wshub.Hub) job.WorkerSelector {
		return sessionSelector{hub: h}
	})
	const block = "[gofer · team] TEAM-BLOCK-LINE\n\n"
	noter := &fixedTeamNoter{block: block}
	hub.jobs.SetTeamNotes(noter)
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
	if n := strings.Count(stdout, "TEAM-BLOCK-LINE"); n != 2 {
		t.Fatalf("team block appears %d times, want once per turn (2):\n%s", n, stdout)
	}
	for _, text := range []string{"first turn", "second turn"} {
		if !strings.Contains(stdout, block+text) {
			t.Fatalf("the team block must sit right before %q:\n%s", text, stdout)
		}
	}
	wait.Until(t, 10*time.Second, "both turn ends reported", func() bool {
		noter.mu.Lock()
		defer noter.mu.Unlock()
		return len(noter.began) == 2 && noter.ended >= 2
	})
	noter.mu.Lock()
	defer noter.mu.Unlock()
	for _, ids := range noter.began {
		if len(ids) == 0 || ids[0] != created.ID {
			t.Fatalf("lookup ids = %v, want the job id first", ids)
		}
	}
}
