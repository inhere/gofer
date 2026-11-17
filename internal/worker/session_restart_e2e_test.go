package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wshub"
)

// TestE2EWorkerProcessRestartReloadsHostedSession: the worker process that runs a hosted
// session exits (its context ends, as on a restart or an upgrade) and a NEW process registers
// under the same worker id. The host job is not failed and not given a new id: it waits as
// `recovering`, is dispatched to the new process with the session to load, goes back to
// waiting for input, keeps the earlier conversation in its stream, and takes the next message
// as a turn after the ones it already had. The old process must not have reported an ending
// for it on the way out.
func TestE2EWorkerProcessRestartReloadsHostedSession(t *testing.T) {
	hub := buildHubSideSel(t, t.TempDir(), t.TempDir(), func(h *wshub.Hub) job.WorkerSelector { return sessionSelector{hub: h} })
	hub.hub.SetRecoverWindow(30 * time.Second) // what serve wires from server.job_recover_window_sec
	ctx1, cancel1 := context.WithTimeout(context.Background(), 120*time.Second)
	cli1, _ := buildWorkerWithACPAgent(t, hub.ts.URL, nil, acptest.Options{EchoPrompt: true})
	clientErr1 := worker.StartClient(t, ctx1, cli1)
	waitWorkerOnline(t, hub.hub)

	sess, err := hub.jobs.Submit(job.JobRequest{
		ProjectKey: "alpha", Agent: "acpbot", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "first turn", Cwd: ".", Session: true, Hosted: true, TimeoutSec: 120,
	})
	if err != nil {
		t.Fatalf("submit hosted session: %v", err)
	}
	stream := openACPStream(t, hub.ts, sess.ID)
	waitHostAwaitingTurn(t, hub.jobs, sess.ID, 1)
	// The first process goes away.
	stopWorkerClient(t, cli1, cancel1, clientErr1)
	if r, _ := hub.jobs.Get(sess.ID); job.IsTerminal(r.Status) {
		t.Fatalf("the old process reported an ending for the session: %s %q", r.Status, r.Error)
	}

	// A new process registers under the same worker id.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 120*time.Second)
	cli2, local2 := buildWorkerWithACPAgent(t, hub.ts.URL, nil, acptest.Options{EchoPrompt: true})
	clientErr2 := worker.StartClient(t, ctx2, cli2)
	defer stopWorkerClient(t, cli2, cancel2, clientErr2)
	waitWorkerOnline(t, hub.hub)

	wait.For(t, 30*time.Second, "the same host job reloaded on the new process and waiting for input", func() (bool, any) {
		r, _ := hub.jobs.Get(sess.ID)
		if job.IsTerminal(r.Status) {
			t.Fatalf("host session ended instead of being reloaded: %s %q", r.Status, r.Error)
		}
		list, _ := local2.ListJobs(job.ListOpts{Limit: 10})
		return r.Status == job.StatusAwaitingInput && len(list) == 1 && list[0].Session, r.Status
	})
	if r, _ := hub.jobs.Get(sess.ID); r.ID != sess.ID || r.WorkerID != e2eWorkerID {
		t.Fatalf("job identity changed: %+v", r)
	}

	postHub(t, hub.ts, "/v1/jobs/"+sess.ID+"/say", map[string]string{"message": "after restart"})
	waitHostAwaitingTurn(t, hub.jobs, sess.ID, 2)
	wait.Until(t, 20*time.Second, "the conversation before and after the restart in one stream", func() bool {
		return stream.has("prompt", "first turn") && stream.has("prompt", "after restart")
	})

	postHub(t, hub.ts, "/v1/jobs/"+sess.ID+"/end", map[string]string{})
	if final, _ := hub.jobs.Wait(sess.ID); final.Status != job.StatusDone {
		t.Fatalf("final = %s %q, want done", final.Status, final.Error)
	}
}
