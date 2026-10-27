package xfer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// own_attachments_test.go covers the transfer target of a session that belongs to no
// project: a put into the executing machine's OWN attachment directory
// (<config-dir>/run/attachments/<date>/), its confinement, its cleanup, and how the
// manager answers whether a given executor takes one.

// ownCfgDir points GOFER_CONFIG_DIR at a fresh directory and returns the attachment base
// the helpers must resolve to.
func ownCfgDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	return filepath.Join(dir, "run", OwnAttachmentsDirName)
}

// TestOwnAttachmentsPathStaysInsideBase: the relative path a transfer carries may not
// escape the machine's attachment directory — the same boundary a project transfer obeys.
func TestOwnAttachmentsPathStaysInsideBase(t *testing.T) {
	base := ownCfgDir(t)
	if got, want := OwnAttachmentsDir(), base; got != want {
		t.Fatalf("OwnAttachmentsDir = %q, want %q", got, want)
	}
	got, err := OwnAttachmentsPath("20261027/xf-1.png")
	if err != nil {
		t.Fatalf("OwnAttachmentsPath: %v", err)
	}
	if want := filepath.Join(base, "20261027", "xf-1.png"); got != want {
		t.Fatalf("OwnAttachmentsPath = %q, want %q", got, want)
	}
	for _, bad := range []string{"", "../escape.png", "../../etc/passwd"} {
		if _, err := OwnAttachmentsPath(bad); err == nil {
			t.Fatalf("OwnAttachmentsPath(%q) accepted a path outside the attachment dir", bad)
		}
	}
}

// TestLocalRunnerOwnAttachmentPutAndPrune is the server-side half: a project-less
// placement lands in <config-dir>/run/attachments/<today>/, its absolute path is what the
// journal records, and the same placement prunes the date dirs older than the retention —
// exactly like a project attachment dir.
func TestLocalRunnerOwnAttachmentPutAndPrune(t *testing.T) {
	base := ownCfgDir(t)
	cfg := &config.Config{Storage: config.StorageConfig{DefaultExchangeSubdir: config.DefaultExchangeSubdir}}
	mgr := newLocalRunnerFixture(t, &cfg)

	stale := filepath.Join(base, time.Now().AddDate(0, 0, -30).Format(attachmentDateLayout))
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale date dir: %v", err)
	}
	today := time.Now().Format(attachmentDateLayout)

	src := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(src, []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	abs, err := mgr.PutAttachmentsAndWait(t.Context(), "tester", "server", src, today+"/shot.png", 30*time.Second)
	if err != nil {
		t.Fatalf("PutAttachmentsAndWait: %v", err)
	}
	want := filepath.Join(base, today, "shot.png")
	if abs != want {
		t.Fatalf("abs path = %q, want %q", abs, want)
	}
	if got, err := os.ReadFile(want); err != nil || string(got) != "png-bytes" {
		t.Fatalf("read %s = %q, %v; want the placed payload", want, got, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale date dir survived the placement (err=%v)", err)
	}
	// The journal keeps the target, so a listing can tell this row from a project placement.
	rows, err := mgr.List(jobstore.XferFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].Target != TargetAttachments || rows[0].ProjectKey != "" {
		t.Fatalf("journal row = %+v, want the attachments target with no project", rows)
	}
}

// TestLocalRunnerOwnAttachmentRefusesEscape: the path boundary is enforced by the runner
// itself, not only by the caller.
func TestLocalRunnerOwnAttachmentRefusesEscape(t *testing.T) {
	ownCfgDir(t)
	cfg := &config.Config{}
	mgr := newLocalRunnerFixture(t, &cfg)
	src := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	_, err := mgr.PutAttachmentsAndWait(t.Context(), "tester", "server", src, "../escape.png", 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("PutAttachmentsAndWait(../escape.png) = %v, want an escape refusal", err)
	}
}

// capsSender is a WorkerSender that also answers the capability query, the way the hub
// adapter does in production.
type capsSender struct {
	send      senderFunc
	supported bool
	asked     []string
}

func (s *capsSender) SendFileXfer(ctx context.Context, workerID string, req FileXferRequest) (FileXferResult, error) {
	return s.send(ctx, workerID, req)
}

func (s *capsSender) SupportsWorkerAttach(workerID string) bool {
	s.asked = append(s.asked, workerID)
	return s.supported
}

// TestPutAttachmentsAndWaitCarriesTarget: the frame the worker receives names the target
// and carries NO project (there is none to resolve against), and the caller gets the
// machine's reported absolute path back.
func TestPutAttachmentsAndWaitCarriesTarget(t *testing.T) {
	var gotReq FileXferRequest
	sender := &capsSender{supported: true, send: func(_ context.Context, _ string, req FileXferRequest) (FileXferResult, error) {
		gotReq = req
		return FileXferResult{XferID: req.XferID, OK: true, Size: req.Size, SHA256: req.SHA256,
			AbsPath: "/home/ci/.config/gofer/run/attachments/20261027/xf-1.png"}, nil
	}}
	mgr, err := NewManager(Options{Root: t.TempDir(), Repo: newMemRepo()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetRunner(&Router{Sender: sender, Timeout: time.Minute})

	abs, err := mgr.PutAttachmentsAndWait(context.Background(), "tester", "w-docker",
		putWaitSource(t, "png-bytes"), "20261027/xf-1.png", 0)
	if err != nil {
		t.Fatalf("PutAttachmentsAndWait: %v", err)
	}
	if want := "/home/ci/.config/gofer/run/attachments/20261027/xf-1.png"; abs != want {
		t.Fatalf("abs path = %q, want %q", abs, want)
	}
	if gotReq.Target != TargetAttachments || gotReq.ProjectKey != "" || gotReq.Path != "20261027/xf-1.png" {
		t.Fatalf("dispatch = %+v, want a put to the attachments target with no project", gotReq)
	}
	if !mgr.OwnAttachmentsSupported("w-docker") {
		t.Fatal("OwnAttachmentsSupported(w-docker) = false, the sender declared it")
	}
	// The server's own machine always takes one, whatever the sender says.
	if !mgr.OwnAttachmentsSupported("server") {
		t.Fatal("OwnAttachmentsSupported(server) = false")
	}
}

// TestOwnAttachmentsSupportedOldWorker: a sender that cannot answer the capability query
// (an old hub build, a stub) counts as "this worker cannot take the placement", and a
// worker reporting no absolute path fails the placement instead of handing back a path
// nothing can resolve.
func TestOwnAttachmentsSupportedOldWorker(t *testing.T) {
	plain := putWaitFixture(t, func(_ context.Context, _ string, req FileXferRequest) (FileXferResult, error) {
		return FileXferResult{XferID: req.XferID, OK: true, AbsPath: ""}, nil
	}, time.Minute)
	if plain.OwnAttachmentsSupported("w-docker") {
		t.Fatal("a sender without the capability query must not claim the attachments target")
	}
	_, err := plain.PutAttachmentsAndWait(context.Background(), "tester", "w-docker",
		putWaitSource(t, "png-bytes"), "20261027/xf-1.png", 0)
	if err == nil || !strings.Contains(err.Error(), "did not report") {
		t.Fatalf("PutAttachmentsAndWait without a reported path = %v, want a clear failure", err)
	}
}
