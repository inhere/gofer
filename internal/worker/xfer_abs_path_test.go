package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/xfer"
)

// xfer_abs_path_test.go is the worker half of the file-delivery placement (protocol v25):
// a put that lands on the WORKER must report the absolute destination that machine wrote,
// and a placement under tmp/gofer-attachments must prune the old date dirs there.

// TestFileXferWorkerReportsAbsPath drives a real put through the hub and the in-process
// worker and checks the two things the caller depends on: the journal carries the worker's
// own absolute destination, and the placement cleaned an attachment date dir left over from
// an earlier run while keeping today's.
func TestFileXferWorkerReportsAbsPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub, workerHost := startXferWorker(t, ctx)

	// An attachment directory from an old run, plus the stale date dir a prune must remove.
	// The worker's project uses the default exchange subdir (`tmp`), so the attachment
	// directory it prunes in is <worker project root>/tmp/gofer-attachments.
	attachments := filepath.Join(workerHost, "tmp", "gofer-attachments")
	stale := filepath.Join(attachments, time.Now().AddDate(0, 0, -30).Format("20060102"))
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale attachment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, "old.png"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write stale attachment: %v", err)
	}

	payload := []byte("new attachment bytes")
	const rel = "tmp/gofer-attachments/20261019/xf-worker01.png"
	rec := stagePutPayload(t, hub.mgr, "alpha", rel, payload, false)
	if err := hub.mgr.Deliver(ctx, rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	row := xferRow(t, hub.mgr, rec.ID)
	if row.State != string(xfer.StateDone) {
		t.Fatalf("put state = %s (error=%q), want done", row.State, row.Error)
	}
	want := filepath.Join(workerHost, "tmp", "gofer-attachments", "20261019", "xf-worker01.png")
	if row.AbsPath != want {
		t.Fatalf("journal abs_path = %q, want the worker's own destination %q", row.AbsPath, want)
	}
	if got, err := os.ReadFile(want); err != nil || string(got) != string(payload) {
		t.Fatalf("read %s = %q, %v; want the placed payload", want, got, err)
	}

	// The prune ran on the worker: the 30-day-old date dir is gone, the one this placement
	// just created is untouched.
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale attachment date dir survived the placement (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(attachments, "20261019")); err != nil {
		t.Fatalf("the placement's own date dir is missing: %v", err)
	}
}

// TestFileXferWorkerGetReportsNoAbsPath: only a PUT has a destination on the executing
// machine, so a get's result leaves the field empty (the caller must not be handed the
// source's path as if it were a delivery).
func TestFileXferWorkerGetReportsNoAbsPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub, workerHost := startXferWorker(t, ctx)

	src := filepath.Join(workerHost, "tmp", "out", "report.csv")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(src, []byte("a,b\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	rec, err := hub.mgr.StageGet("e2e-tester", e2eWorkerID, "alpha", "tmp/out/report.csv")
	if err != nil {
		t.Fatalf("StageGet: %v", err)
	}
	if err := hub.mgr.Deliver(ctx, rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	row := xferRow(t, hub.mgr, rec.ID)
	if row.State != string(xfer.StateDone) {
		t.Fatalf("get state = %s (error=%q), want done", row.State, row.Error)
	}
	if row.AbsPath != "" {
		t.Fatalf("get abs_path = %q, want empty", row.AbsPath)
	}
}
