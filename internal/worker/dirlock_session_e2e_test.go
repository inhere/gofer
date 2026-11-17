package worker_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wshub"
)

// R8 on a worker: the directory lock of a worker-hosted session is taken by the
// worker's own job.Service. The hub cannot resolve the worker-only agent, so the
// lock decision it stamps on the dispatch must still end up as "exclusive" on the
// worker; the session then gives the lock back while awaiting input (default) or
// keeps it (keep_dir_lock), and a same-directory writable job queues accordingly.

func startWorkerSession(t *testing.T, keep bool) (*hubSide, *job.Service, job.JobResult, *acpStreamReader, func(), func()) {
	t.Helper()
	hub := buildHubSideSel(t, t.TempDir(), t.TempDir(), func(h *wshub.Hub) job.WorkerSelector { return sessionSelector{hub: h} })
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	cli, localJobs := buildWorkerWithACPAgent(t, hub.ts.URL, nil, acptest.Options{EchoPrompt: true})
	clientErr := worker.StartClient(t, ctx, cli)
	waitWorkerOnline(t, hub.hub)
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "acpbot", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "first turn", Cwd: ".", Session: true, TimeoutSec: 120, IdleTimeoutSec: 120,
		KeepDirLock: keep,
	})
	stream := openACPStream(t, hub.ts, created.ID)
	waitHostAwaitingTurn(t, hub.jobs, created.ID, 1)
	end := func() {
		postHub(t, hub.ts, "/v1/jobs/"+created.ID+"/end", map[string]string{})
		hub.jobs.Wait(created.ID)
	}
	stop := func() { stopWorkerClient(t, cli, cancel, clientErr) }
	return hub, localJobs, created, stream, end, stop
}

func TestE2EWorkerSessionYieldsDirLockAndNotesNextTurn(t *testing.T) {
	hub, localJobs, sess, stream, end, stop := startWorkerSession(t, false)
	defer stop()
	defer end()

	// the worker's own row for the session decided "exclusive"
	var local job.JobResult
	list, err := localJobs.ListJobs(job.ListOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.Session {
			local = r
		}
	}
	if !local.DirExclusive {
		t.Fatalf("worker-hosted session is not exclusive on the worker (dir_exclusive=false): %+v", local)
	}

	// a same-directory writable job on the same worker starts at once
	other := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "acpbot", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "side work", Title: "side change", Cwd: ".", TimeoutSec: 60,
	})
	waitHostStatus(t, hub.jobs, other.ID, job.StatusDone, 30*time.Second)

	postHub(t, hub.ts, "/v1/jobs/"+sess.ID+"/say", map[string]string{"message": "second turn"})
	wait.For(t, 30*time.Second, "turn 2 prompt carries the directory note", func() (bool, any) {
		events, _ := stream.snapshot()
		for _, ev := range events {
			// the note rides the prompt record's injected part (split from the text)
			if inj, _ := ev["injected"].(string); ev["kind"] == "prompt" && ev["text"] == "second turn" && strings.Contains(inj, "gofer:rules-end") {
				return true, nil
			}
		}
		return false, fmt.Sprint(events)
	})
}

func TestE2EWorkerSessionKeepDirLockQueuesSameDirJob(t *testing.T) {
	hub, _, _, _, end, stop := startWorkerSession(t, true)
	defer stop()
	other := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "acpbot", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "side work", Cwd: ".", TimeoutSec: 60,
	})
	// the worker mirrors the wait, so the hub shows WHY the job is not starting
	wait.Until(t, 20*time.Second, "job.waiting_dir mirrored to the hub", func() bool {
		evs, _ := hub.jobs.ListJobEvents(other.ID, 0)
		for _, e := range evs {
			if e.Type == job.EventJobWaitingDir {
				return true
			}
		}
		return false
	})
	// it must stay queued behind the session's lock (a "should not happen" window)
	time.Sleep(1 * time.Second)
	if r, _ := hub.jobs.Get(other.ID); job.IsTerminal(r.Status) {
		t.Fatalf("same-dir job finished (%s) although the session kept its directory lock", r.Status)
	}
	end()
	waitHostStatus(t, hub.jobs, other.ID, job.StatusDone, 30*time.Second)
}
