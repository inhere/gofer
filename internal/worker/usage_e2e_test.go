package worker_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/runner"
)

// TestOutcomeCarriesUsage proves the SUP-01 E usage capture travels the WHOLE
// worker path: the codex stand-in prints codex's own `tokens used` tail on the
// EXECUTING machine, that machine's job service reads it into its local result,
// the Outcome frame carries it back, and the hub job row (in memory and in the
// store) ends up with it.
func TestOutcomeCarriesUsage(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	cl := buildWorkerSide(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "codex", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "count the tokens", Cwd: ".", TimeoutSec: 30,
	})
	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Usage == nil {
		t.Fatalf("the worker's usage did not reach the hub job (stderr = %q)", getLogs(t, hub.ts, created.ID, "stderr"))
	}
	if final.Usage.TotalTokens != 19802 || final.Usage.Source != runner.UsageSourceCodexStderr {
		t.Fatalf("hub usage = %+v, want total_tokens 19802 from %s", *final.Usage, runner.UsageSourceCodexStderr)
	}

	// The host entry is evicted at terminal, so a later `job show` reads the row.
	got, ok := hub.jobs.Get(created.ID)
	if !ok {
		t.Fatalf("host Get(%s) not found", created.ID)
	}
	if got.Usage == nil || got.Usage.TotalTokens != 19802 {
		t.Fatalf("host usage = %+v, want the worker's token count", got.Usage)
	}
	rec, ok, err := hub.store.GetJob(created.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob persisted: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(rec.UsageJSON, `"total_tokens":19802`) || !strings.Contains(rec.UsageJSON, runner.UsageSourceCodexStderr) {
		t.Fatalf("persisted usage_json = %q, want the worker's capture", rec.UsageJSON)
	}

	cancel()
	select {
	case <-clientErr:
	case <-time.After(3 * time.Second):
		t.Log("worker client did not exit promptly after cancel (non-fatal)")
	}
}
