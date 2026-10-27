package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/xfer"
)

// xfer_own_attachments_test.go is the worker half of the project-less placement (protocol
// v27): a put whose target is the worker's OWN attachment directory lands in
// <worker config-dir>/run/attachments/<date>/, the absolute path that machine wrote is what
// the journal records, and the placement prunes the old date dirs there.

// TestFileXferOwnAttachmentTarget drives a real put through the hub and the in-process
// worker with target=attachments and no project at all.
func TestFileXferOwnAttachmentTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The worker resolves its own attachment base from ITS config dir, so the fixture pins
	// one and the test then knows where the file must appear.
	workerCfgDir := t.TempDir()
	t.Setenv("GOFER_CONFIG_DIR", workerCfgDir)
	base := filepath.Join(workerCfgDir, "run", "attachments")

	// A date dir left over from an earlier run: the placement must remove it.
	stale := filepath.Join(base, time.Now().AddDate(0, 0, -30).Format("20060102"))
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale attachment dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, "old.png"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write stale attachment: %v", err)
	}

	hub, _ := startXferWorker(t, ctx)

	payload := []byte("project-less attachment bytes")
	today := time.Now().Format("20060102")
	src := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	abs, err := hub.mgr.PutAttachmentsAndWait(ctx, "e2e-tester", e2eWorkerID, src, today+"/shot.png", 30*time.Second)
	if err != nil {
		t.Fatalf("PutAttachmentsAndWait: %v", err)
	}
	want := filepath.Join(base, today, "shot.png")
	if abs != want {
		t.Fatalf("reported path = %q, want the worker's own attachment path %q", abs, want)
	}
	if got, err := os.ReadFile(want); err != nil || string(got) != string(payload) {
		t.Fatalf("read %s = %q, %v; want the placed payload", want, got, err)
	}

	// The journal carries the same absolute path, and it is recorded for a row that has no
	// project (the target is what names the destination tree).
	rows, err := hub.mgr.List(xferFilterRunner(e2eWorkerID))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("journal rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.State != string(xfer.StateDone) || row.AbsPath != want {
		t.Fatalf("journal row = state %s abs_path %q (error %q), want done at %q", row.State, row.AbsPath, row.Error, want)
	}
	if row.Target != xfer.TargetAttachments || row.ProjectKey != "" {
		t.Fatalf("journal row target = %q project = %q, want the attachments target with no project", row.Target, row.ProjectKey)
	}

	// The prune ran on the worker with the same retention the project attachment dir uses.
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale attachment date dir survived the placement (err=%v)", err)
	}
}
