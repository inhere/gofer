package worker_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// hubEventDetail returns the detail maps of every event of type eventType the HUB
// recorded for a job — the mirror's landing place (SUP-01 G).
func hubEventDetail(t *testing.T, st *jobstore.Store, jobID, eventType string) []map[string]any {
	t.Helper()
	events, err := st.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	var out []map[string]any
	for _, e := range events {
		if e.Type != eventType {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatalf("event %s detail is not JSON: %q", e.Type, e.Detail)
		}
		out = append(out, d)
	}
	return out
}

// waitHubEvent polls until the hub recorded `want` events of type eventType for the
// job (the mirror rides the worker's own frame pump, so it lands asynchronously).
func waitHubEvent(t *testing.T, st *jobstore.Store, jobID, eventType string, want int, d time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	var last []map[string]any
	for time.Now().Before(deadline) {
		last = hubEventDetail(t, st, jobID, eventType)
		if len(last) >= want {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("hub recorded %d %s events for job %s within %s, want %d", len(last), eventType, jobID, d, want)
	return nil
}

// TestWorkerVerifyEventsMirrored: the verify step's events are raised by the WORKER
// (it runs the step) and must reach the hub's own event log, tagged with the worker
// that raised them — that is what makes a webhook subscription to
// job.verify_finished fire for a job that ran remotely (SUP-01 G / h-aii-msm2).
func TestWorkerVerifyEventsMirrored(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	cl := buildWorkerSide(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	bin := testcmd.Path(t)
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{bin, "exit", "0"}, Cwd: ".", TimeoutSec: 30,
		Verify: []string{bin, "exit", "3"},
	})
	if _, ok := hub.jobs.Wait(created.ID); !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}

	started := waitHubEvent(t, hub.store, created.ID, job.EventJobVerifyStarted, 1, 10*time.Second)
	if started[0]["command"] == "" {
		t.Fatalf("mirrored %s detail = %v, want the command", job.EventJobVerifyStarted, started[0])
	}
	if started[0]["origin"] != "worker:"+e2eWorkerID {
		t.Fatalf("mirrored %s origin = %v, want worker:%s", job.EventJobVerifyStarted, started[0]["origin"], e2eWorkerID)
	}
	finished := waitHubEvent(t, hub.store, created.ID, job.EventJobVerifyFinished, 1, 10*time.Second)
	if finished[0]["status"] != job.VerifyFailed || finished[0]["origin"] != "worker:"+e2eWorkerID {
		t.Fatalf("mirrored %s detail = %v, want failed from worker:%s", job.EventJobVerifyFinished, finished[0], e2eWorkerID)
	}

	stopWorkerClient(t, cl, cancel, clientErr)
}

// TestWorkerPermissionEventMirroredToHub: the S1 approval e2e plus the part the hub
// could never see before — the WORKER's `job.permission_requested` /
// `job.permission_answered` events land in the hub's event log for the host job,
// tagged with their origin and EXACTLY ONCE (the interaction mirror and the event
// mirror are separate channels and must not double-record the request).
func TestWorkerPermissionEventMirroredToHub(t *testing.T) {
	hub := buildHubSide(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cli := buildWorkerWithACP(t, hub.ts.URL)
	clientErr := make(chan error, 1)
	go func() { clientErr <- cli.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)

	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "acpbot", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "List the files in this directory.", Cwd: ".", TimeoutSec: 60,
	})

	// Wait for the gate to surface on the hub (the existing interaction mirror), then
	// for the worker's own event to arrive on the host job.
	var iid string
	overall := time.Now().Add(45 * time.Second)
	for time.Now().Before(overall) {
		for _, it := range hubInteractions(t, hub.ts, created.ID) {
			if it["status"] == "pending" {
				iid, _ = it["id"].(string)
				break
			}
		}
		if iid != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if iid == "" {
		snap, _ := hub.jobs.Get(created.ID)
		t.Fatalf("no pending interaction surfaced on the hub; hub job status=%s", snap.Status)
	}

	reqs := waitHubEvent(t, hub.store, created.ID, job.EventJobPermissionRequested, 1, 15*time.Second)
	if reqs[0]["interaction_id"] != iid {
		t.Fatalf("mirrored request detail = %v, want interaction_id=%s", reqs[0], iid)
	}
	if reqs[0]["origin"] != "worker:"+e2eWorkerID {
		t.Fatalf("mirrored request origin = %v, want worker:%s", reqs[0]["origin"], e2eWorkerID)
	}

	answerHub(t, hub.ts, created.ID, iid, acptest.AllowOnceOptionID)
	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("hub job status = %s (err=%s), want done after the answer", final.Status, final.Error)
	}

	// The answer mirrors too, and the request is never doubled: the interaction
	// mirror records the interaction, the event mirror the event.
	answered := waitHubEvent(t, hub.store, created.ID, job.EventJobPermissionAnswered, 1, 10*time.Second)
	if answered[0]["origin"] != "worker:"+e2eWorkerID {
		t.Fatalf("mirrored answer origin = %v, want worker:%s", answered[0]["origin"], e2eWorkerID)
	}
	if again := hubEventDetail(t, hub.store, created.ID, job.EventJobPermissionRequested); len(again) != 1 {
		t.Fatalf("hub recorded %d job.permission_requested events, want exactly 1 (no double-record)", len(again))
	}

	stopWorkerClient(t, cli, cancel, clientErr)
}
