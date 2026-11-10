package core

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/inhere/gofer/internal/xfer"
)

// stagedUpload stages a put of "payload" for project "p" and returns the manager and id.
func stagedUpload(t *testing.T) (*xfer.Manager, string) {
	t.Helper()
	st, _ := openCallerStore(t)
	mgr, err := xfer.NewManager(xfer.Options{Root: t.TempDir(), Repo: st})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := mgr.StagePutFromFile("tester", "local", "p", src, "dest.bin", false)
	if err != nil {
		t.Fatalf("StagePutFromFile: %v", err)
	}
	return mgr, rec.ID
}

func TestHubFetchUploadStaysInsideRoot(t *testing.T) {
	top := t.TempDir()
	root, outside := filepath.Join(top, "proj"), filepath.Join(top, "outside")
	for _, d := range []string{filepath.Join(root, "docs"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join("..", "outside"), filepath.Join(root, "out")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation requires admin or Developer Mode on Windows: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Symlink("docs", filepath.Join(root, "ok")); err != nil {
		t.Fatal(err)
	}
	mgr, id := stagedUpload(t)
	h := hubJobXfer{mgr: mgr}

	if err := h.FetchUpload(context.Background(), id, "p", root, "out/x.bin"); err == nil {
		t.Fatal("an upload through a dir link that leaves the root must fail")
	}
	if es, err := os.ReadDir(outside); err != nil || len(es) != 0 {
		t.Fatalf("outside dir = %v (err=%v), want empty", es, err)
	}

	if err := h.FetchUpload(context.Background(), id, "p", root, "ok/y.bin"); err != nil {
		t.Fatalf("in-tree relative link upload: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "docs", "y.bin")); err != nil || string(b) != "payload" {
		t.Fatalf("landed file = %q, %v", b, err)
	}
	if es, _ := os.ReadDir(filepath.Join(root, "docs")); len(es) != 1 {
		t.Fatalf("docs has %d entries, want only the payload (no temp leftovers)", len(es))
	}
}
