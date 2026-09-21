package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestWorkerUploadAndCollectRoundTrip is the XFER-01 X2 acceptance on the WORKER path:
// a staged upload reaches the worker's cwd before the agent starts, the agent's own
// output is collected after the job, and the collected bytes land in the HUB's result
// dir as an artifact while the transfer summary is persisted on the hub's job row.
//
// The single exec job proves both directions in one pass: it COPIES the uploaded file
// into the collect glob, so the artifact the hub ends up serving is literally the
// payload the hub staged — a dropped, truncated or reordered step anywhere in the
// upload → cwd → collect → artifact chain breaks the assertion.
func TestWorkerUploadAndCollectRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub, workerHost := startXferWorker(t, ctx)

	payload := []byte("firmware-payload-v2\n")
	// The upload is staged for the WORKER (its id is the transfer's runner), exactly
	// as the CLI stages one for `job run --upload`.
	rec := stagePutPayload(t, hub.mgr, "alpha", "tmp/in/firmware.bin", payload, false)

	created, err := hub.jobs.Submit(job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cwd: ".", TimeoutSec: 45,
		Cmd: testcmd.Cmd(t, "copy-file", "in/firmware.bin", "out/report.csv"),
		Uploads: []job.UploadSpec{{
			XferID: rec.ID,
			Dest:   "in/firmware.bin",
		}},
		Collect: []string{"out/*.csv"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final, ok := hub.jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("hub job %s not found", created.ID)
	}
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}

	// The worker placed the upload in ITS cwd (the agent read it to produce the copy).
	if _, err := os.Stat(filepath.Join(workerHost, "in", "firmware.bin")); err != nil {
		t.Fatalf("the upload did not reach the worker's cwd: %v", err)
	}

	// The collected file was pulled back into the HUB's result dir, under the artifact
	// path the download/preview surface already serves.
	collected := filepath.Join(final.ResultDir, "artifacts", "collected", "out", "report.csv")
	got, err := os.ReadFile(collected)
	if err != nil {
		t.Fatalf("read collected artifact on the hub: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("collected artifact = %q, want the uploaded payload %q", got, payload)
	}
	if !strings.Contains(final.ArtifactsJSON, "collected/out/report.csv") {
		t.Fatalf("hub artifact manifest = %s, want it to list the collected file", final.ArtifactsJSON)
	}

	// The summary is persisted on the hub row (jobs.xfer_json), so `job show` / a
	// restarted serve / the web detail still see what moved.
	stored, ok := hub.jobs.Get(created.ID)
	if !ok {
		t.Fatalf("job %s not readable after terminal", created.ID)
	}
	if stored.Xfer == nil {
		t.Fatal("the hub row carries no xfer summary")
	}
	if len(stored.Xfer.Uploads) != 1 || !stored.Xfer.Uploads[0].OK || stored.Xfer.Uploads[0].Dest != "in/firmware.bin" {
		t.Fatalf("persisted uploads = %+v, want one ok entry for in/firmware.bin", stored.Xfer.Uploads)
	}
	if len(stored.Xfer.Collected) != 1 || stored.Xfer.Collected[0].Name != "out/report.csv" {
		t.Fatalf("persisted collected = %+v, want out/report.csv", stored.Xfer.Collected)
	}
	if stored.Xfer.Collected[0].Size != int64(len(payload)) {
		t.Fatalf("persisted collected size = %d, want %d", stored.Xfer.Collected[0].Size, len(payload))
	}
}
