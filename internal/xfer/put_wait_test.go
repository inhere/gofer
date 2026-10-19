package xfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// senderFunc is a WorkerSender stand-in: it is the seam the hub satisfies in production,
// so a transfer to a worker can be driven without a connection.
type senderFunc func(ctx context.Context, workerID string, req FileXferRequest) (FileXferResult, error)

func (f senderFunc) SendFileXfer(ctx context.Context, workerID string, req FileXferRequest) (FileXferResult, error) {
	return f(ctx, workerID, req)
}

// putWaitFixture builds a manager over an in-memory journal whose executor is a worker
// sender (no local runner, so every transfer goes through the sender).
func putWaitFixture(t *testing.T, send senderFunc, timeout time.Duration) *Manager {
	t.Helper()
	mgr, err := NewManager(Options{Root: t.TempDir(), Repo: newMemRepo()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetRunner(&Router{Sender: send, Timeout: timeout})
	return mgr
}

// putWaitSource writes the file a test pushes and returns its path.
func putWaitSource(t *testing.T, body string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "attachment.png")
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return src
}

// TestPutAndWaitReportsWorkerAbsPath is the v25 path: the worker names the absolute
// destination it wrote, and the caller gets it back from the journal.
func TestPutAndWaitReportsWorkerAbsPath(t *testing.T) {
	const absPath = "/home/ci/proj/tmp/gofer-attachments/20261019/xf-1.png"
	var gotReq FileXferRequest
	mgr := putWaitFixture(t, func(_ context.Context, workerID string, req FileXferRequest) (FileXferResult, error) {
		gotReq = req
		if workerID != "w-docker" {
			t.Errorf("worker id = %q, want w-docker", workerID)
		}
		return FileXferResult{XferID: req.XferID, OK: true, Size: req.Size, SHA256: req.SHA256, AbsPath: absPath}, nil
	}, time.Minute)

	abs, err := mgr.PutAndWait(context.Background(), "tester", "w-docker", "alpha",
		putWaitSource(t, "png-bytes"), "tmp/gofer-attachments/20261019/xf-1.png", 0)
	if err != nil {
		t.Fatalf("PutAndWait: %v", err)
	}
	if abs != absPath {
		t.Fatalf("abs path = %q, want %q", abs, absPath)
	}
	if gotReq.Op != "put" || gotReq.ProjectKey != "alpha" || gotReq.Path != "tmp/gofer-attachments/20261019/xf-1.png" {
		t.Fatalf("dispatch = %+v, want a put to alpha at the attachment path", gotReq)
	}
	rec, ok, err := mgr.Get(gotReq.XferID)
	if err != nil || !ok {
		t.Fatalf("Get(%s): ok=%v err=%v", gotReq.XferID, ok, err)
	}
	if State(rec.State) != StateDone || rec.AbsPath != absPath {
		t.Fatalf("journal row = (%s, abs_path=%q), want (done, %q)", rec.State, rec.AbsPath, absPath)
	}
}

// TestPutAndWaitEmptyAbsPathFromOldWorker is the pre-v25 fallback: a worker that cannot
// report a destination still delivers the file, and the empty path is what tells the
// caller to fall back to the project-relative one.
func TestPutAndWaitEmptyAbsPathFromOldWorker(t *testing.T) {
	mgr := putWaitFixture(t, func(_ context.Context, _ string, req FileXferRequest) (FileXferResult, error) {
		return FileXferResult{XferID: req.XferID, OK: true, Size: req.Size, SHA256: req.SHA256}, nil
	}, time.Minute)

	abs, err := mgr.PutAndWait(context.Background(), "tester", "w-old", "alpha",
		putWaitSource(t, "png-bytes"), "tmp/gofer-attachments/20261019/xf-2.png", 0)
	if err != nil {
		t.Fatalf("PutAndWait: %v", err)
	}
	if abs != "" {
		t.Fatalf("abs path = %q, want empty from a worker that cannot report one", abs)
	}
}

// TestPutAndWaitFailureReasons keeps the operator-facing reasons intact: an offline worker
// and a refused placement both come back as errors (never as a silent empty path).
func TestPutAndWaitFailureReasons(t *testing.T) {
	t.Run("worker offline", func(t *testing.T) {
		mgr := putWaitFixture(t, func(context.Context, string, FileXferRequest) (FileXferResult, error) {
			return FileXferResult{}, errors.New("worker offline")
		}, time.Minute)
		_, err := mgr.PutAndWait(context.Background(), "tester", "w-gone", "alpha",
			putWaitSource(t, "png-bytes"), "tmp/gofer-attachments/20261019/xf-3.png", 0)
		if err == nil || !strings.Contains(err.Error(), "worker offline") {
			t.Fatalf("err = %v, want the worker-offline reason", err)
		}
	})
	t.Run("placement refused", func(t *testing.T) {
		mgr := putWaitFixture(t, func(_ context.Context, _ string, req FileXferRequest) (FileXferResult, error) {
			return FileXferResult{XferID: req.XferID, Error: "path escapes project"}, nil
		}, time.Minute)
		_, err := mgr.PutAndWait(context.Background(), "tester", "w-1", "alpha",
			putWaitSource(t, "png-bytes"), "tmp/gofer-attachments/20261019/xf-4.png", 0)
		if err == nil || !strings.Contains(err.Error(), "path escapes project") {
			t.Fatalf("err = %v, want the placement's own reason", err)
		}
	})
	t.Run("no executor wired", func(t *testing.T) {
		// A deployment with no runner (mcp, a standalone tool) must fail the record rather
		// than leave it staged forever.
		mgr, err := NewManager(Options{Root: t.TempDir(), Repo: newMemRepo()})
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		_, perr := mgr.PutAndWait(context.Background(), "tester", "w-1", "alpha",
			putWaitSource(t, "png-bytes"), "tmp/gofer-attachments/20261019/xf-5.png", 0)
		if perr == nil || !strings.Contains(perr.Error(), "no runner wired") {
			t.Fatalf("err = %v, want the no-runner reason", perr)
		}
	})
}

// TestPutAndWaitTimesOut: a far side that never answers leaves the caller with a timeout
// error after the budget (not a hang), and the record is settled failed so it is not left
// dispatched forever.
func TestPutAndWaitTimesOut(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	mgr := putWaitFixture(t, func(ctx context.Context, _ string, _ FileXferRequest) (FileXferResult, error) {
		select {
		case <-ctx.Done():
			return FileXferResult{}, ctx.Err()
		case <-blocked:
			return FileXferResult{}, errors.New("released")
		}
	}, time.Minute) // the ROUTER's own budget is generous: PutAndWait's timeout is the one under test

	started := time.Now()
	_, err := mgr.PutAndWait(context.Background(), "tester", "w-slow", "alpha",
		putWaitSource(t, "png-bytes"), "tmp/gofer-attachments/20261019/xf-6.png", 150*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("PutAndWait took %s for a 150ms budget", took)
	}
	rows, lerr := mgr.List(jobstore.XferFilter{})
	if lerr != nil {
		t.Fatalf("List: %v", lerr)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the one transfer", len(rows))
	}
	if State(rows[0].State) != StateFailed {
		t.Fatalf("timed-out transfer state = %s, want failed", rows[0].State)
	}
}
