package xfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// newLocalRunnerFixture wires a LocalRunner exactly like the assembly does (core):
// the config is read through a function so a hot-reloaded project root is honoured,
// and OnGetContent is the manager's CommitGet — the get's staged payload is only
// usable because that callback finalizes it and records the digest.
func newLocalRunnerFixture(t *testing.T, cfg **config.Config) *Manager {
	t.Helper()
	mgr, err := NewManager(Options{Root: t.TempDir(), Repo: newMemRepo()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.SetRunner(&LocalRunner{
		Config: func() *config.Config { return *cfg },
		Store:  mgr.Store(),
		OnGetContent: func(id string, size int64, sha256 string) error {
			return mgr.CommitGet(id, size, sha256)
		},
	})
	return mgr
}

// TestFileXferServerRunnerDirect is the `runner=local` acceptance (XFER-01 §一.2):
// the server's own process copies the bytes in both directions under the project's
// execution root, with no worker and no wire, and every payload is sha256-verified.
func TestFileXferServerRunnerDirect(t *testing.T) {
	root := t.TempDir() // the server-side project root
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"alpha": {HostPath: root},
	}}
	mgr := newLocalRunnerFixture(t, &cfg)
	ctx := context.Background()

	// --- put: staged on the server, copies into the project ---
	payload := bytes.Repeat([]byte("gofer-xfer-put\n"), 4096)
	sum := sha256Hex(payload)
	rec, err := mgr.StagePut("tester", "server", "alpha", "tmp/in/firmware.bin", int64(len(payload)), sum, false)
	if err != nil {
		t.Fatalf("StagePut: %v", err)
	}
	w, err := mgr.Store().Writer(rec.ID)
	if err != nil {
		t.Fatalf("Store().Writer: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write staged payload: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close staged payload: %v", err)
	}
	if err := mgr.CommitPut(rec.ID, int64(len(payload)), sum); err != nil {
		t.Fatalf("CommitPut: %v", err)
	}
	if err := mgr.Deliver(ctx, rec.ID); err != nil {
		t.Fatalf("Deliver(put): %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "tmp", "in", "firmware.bin"))
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("destination differs from the staged payload (%d vs %d bytes)", len(got), len(payload))
	}
	assertXferState(t, mgr, rec.ID, StateDone, "")

	// The `server` alias resolves to the in-process runner (NormalizeRunnerName), and a
	// second put onto the same destination must refuse without force.
	again, err := mgr.StagePut("tester", "local", "alpha", "tmp/in/firmware.bin", int64(len(payload)), sum, false)
	if err != nil {
		t.Fatalf("StagePut (second): %v", err)
	}
	if err := mgr.Deliver(ctx, again.ID); err != nil {
		t.Fatalf("Deliver(second put): %v", err)
	}
	assertXferState(t, mgr, again.ID, StateFailed, "exists")

	// --- get: copied out of the project into the staging area ---
	collected := []byte("collected-report\n")
	if err := os.MkdirAll(filepath.Join(root, "tmp", "out"), 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tmp", "out", "report.csv"), collected, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	get, err := mgr.StageGet("tester", "server", "alpha", "tmp/out/report.csv")
	if err != nil {
		t.Fatalf("StageGet: %v", err)
	}
	if err := mgr.Deliver(ctx, get.ID); err != nil {
		t.Fatalf("Deliver(get): %v", err)
	}
	assertXferState(t, mgr, get.ID, StateDone, "")
	rc, err := mgr.Store().Reader(get.ID)
	if err != nil {
		t.Fatalf("Reader(staged get): %v", err)
	}
	defer rc.Close()
	staged, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read staged get payload: %v", err)
	}
	if !bytes.Equal(staged, collected) {
		t.Fatalf("staged payload = %q, want %q", staged, collected)
	}
	row, ok, err := mgr.Get(get.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if row.Size != int64(len(collected)) || row.SHA256 != sha256Hex(collected) {
		t.Fatalf("journal content = (%d, %s), want (%d, %s)", row.Size, row.SHA256, len(collected), sha256Hex(collected))
	}

	// A get whose source is gone reports the not-found sentinel, which the manager
	// renders as the operator-facing reason.
	missing, err := mgr.StageGet("tester", "server", "alpha", "tmp/out/nope.csv")
	if err != nil {
		t.Fatalf("StageGet (missing): %v", err)
	}
	if err := mgr.Deliver(ctx, missing.ID); err != nil {
		t.Fatalf("Deliver(missing get): %v", err)
	}
	assertXferState(t, mgr, missing.ID, StateFailed, "source not found")

	// An escaping path and an unknown project are refused before any filesystem work.
	escape, err := mgr.StageGet("tester", "server", "alpha", "../../etc/passwd")
	if err != nil {
		t.Fatalf("StageGet (escape): %v", err)
	}
	if err := mgr.Deliver(ctx, escape.ID); err != nil {
		t.Fatalf("Deliver(escape): %v", err)
	}
	row, _, _ = mgr.Get(escape.ID)
	if row.State != string(StateFailed) || !strings.Contains(row.Error, "escapes project") {
		t.Fatalf("escaping path row = %+v, want a failure naming the escape", row)
	}
	unknown, err := mgr.StageGet("tester", "server", "ghost", "a.txt")
	if err != nil {
		t.Fatalf("StageGet (unknown project): %v", err)
	}
	if err := mgr.Deliver(ctx, unknown.ID); err != nil {
		t.Fatalf("Deliver(unknown project): %v", err)
	}
	assertXferState(t, mgr, unknown.ID, StateFailed, "unknown project \"ghost\"")
}

// TestLocalRunnerReadsConfigPerCall proves the hot-reload contract: the runner must
// resolve the project root against the config in force at CALL time, so moving a
// project's host_path (core.Update) takes effect for the very next transfer instead of
// writing into the old checkout.
func TestLocalRunnerReadsConfigPerCall(t *testing.T) {
	oldRoot, newRoot := t.TempDir(), t.TempDir()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{"alpha": {HostPath: oldRoot}}}
	mgr := newLocalRunnerFixture(t, &cfg)

	cfg = &config.Config{Projects: map[string]config.ProjectConfig{"alpha": {HostPath: newRoot}}}
	payload := []byte("after-reload")
	rec, err := mgr.StagePut("tester", "local", "alpha", "tmp/a.bin", int64(len(payload)), sha256Hex(payload), false)
	if err != nil {
		t.Fatalf("StagePut: %v", err)
	}
	w, err := mgr.Store().Writer(rec.ID)
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := mgr.CommitPut(rec.ID, int64(len(payload)), sha256Hex(payload)); err != nil {
		t.Fatalf("CommitPut: %v", err)
	}
	if err := mgr.Deliver(context.Background(), rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldRoot, "tmp", "a.bin")); err == nil {
		t.Fatal("payload landed in the OLD project root: the runner cached the config")
	}
	if got, err := os.ReadFile(filepath.Join(newRoot, "tmp", "a.bin")); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("new project root read = %q, %v", got, err)
	}
}

func assertXferState(t *testing.T, mgr *Manager, id string, want State, wantErr string) {
	t.Helper()
	rec, ok, err := mgr.Get(id)
	if err != nil || !ok {
		t.Fatalf("Get(%s): ok=%v err=%v", id, ok, err)
	}
	if State(rec.State) != want {
		t.Fatalf("xfer %s state = %s (error=%q), want %s", id, rec.State, rec.Error, want)
	}
	if wantErr != "" && rec.Error != wantErr {
		t.Fatalf("xfer %s error = %q, want %q", id, rec.Error, wantErr)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// linkFixture is a project root plus a sibling directory the root must never reach.
type linkFixture struct {
	mgr           *Manager
	root, outside string
}

func newLinkFixture(t *testing.T) linkFixture {
	t.Helper()
	top := t.TempDir()
	root, outside := filepath.Join(top, "proj"), filepath.Join(top, "outside")
	for _, d := range []string{filepath.Join(root, "docs"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{"alpha": {HostPath: root}}}
	return linkFixture{mgr: newLocalRunnerFixture(t, &cfg), root: root, outside: outside}
}

// mustSymlink mirrors the repo convention: Windows without the symlink privilege
// skips, any other host fails.
func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation requires admin or Developer Mode on Windows: %v", err)
		}
		t.Fatalf("symlink %s -> %s: %v", newname, oldname, err)
	}
}

// putAndDeliver stages payload as a put to rel and delivers it.
func (f linkFixture) putAndDeliver(t *testing.T, rel string, payload []byte, force bool) string {
	t.Helper()
	rec, err := f.mgr.StagePut("tester", "local", "alpha", rel, int64(len(payload)), sha256Hex(payload), force)
	if err != nil {
		t.Fatalf("StagePut: %v", err)
	}
	w, err := f.mgr.Store().Writer(rec.ID)
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := f.mgr.CommitPut(rec.ID, int64(len(payload)), sha256Hex(payload)); err != nil {
		t.Fatalf("CommitPut: %v", err)
	}
	if err := f.mgr.Deliver(context.Background(), rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	return rec.ID
}

func (f linkFixture) getAndDeliver(t *testing.T, rel string) string {
	t.Helper()
	rec, err := f.mgr.StageGet("tester", "local", "alpha", rel)
	if err != nil {
		t.Fatalf("StageGet: %v", err)
	}
	if err := f.mgr.Deliver(context.Background(), rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	return rec.ID
}

func entryCount(t *testing.T, dir string) int {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

func TestLocalRunnerGetLinkOutsideRoot(t *testing.T) {
	f := newLinkFixture(t)
	if err := os.WriteFile(filepath.Join(f.outside, "secret.txt"), []byte("LEAK"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, filepath.Join("..", "outside", "secret.txt"), filepath.Join(f.root, "leak.txt"))
	mustSymlink(t, filepath.Join("..", "outside"), filepath.Join(f.root, "out"))

	for _, rel := range []string{"leak.txt", "out/secret.txt"} {
		id := f.getAndDeliver(t, rel)
		rec, _, _ := f.mgr.Get(id)
		if State(rec.State) != StateFailed {
			t.Fatalf("%s: state=%s, want failed", rel, rec.State)
		}
		if rec.Error == "source not found" || strings.Contains(rec.Error, f.outside) {
			t.Fatalf("%s: error=%q must be an escape error without the real path", rel, rec.Error)
		}
	}
}

func TestLocalRunnerGetRelativeLinkInsideRoot(t *testing.T) {
	f := newLinkFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "docs", "a.md"), []byte("INSIDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "docs", filepath.Join(f.root, "ok"))
	id := f.getAndDeliver(t, "ok/a.md")
	assertXferState(t, f.mgr, id, StateDone, "")
}

func TestLocalRunnerPutDirLinkOutsideRoot(t *testing.T) {
	f := newLinkFixture(t)
	mustSymlink(t, filepath.Join("..", "outside"), filepath.Join(f.root, "out"))

	for _, force := range []bool{false, true} {
		id := f.putAndDeliver(t, "out/x.txt", []byte("payload"), force)
		rec, _, _ := f.mgr.Get(id)
		if State(rec.State) != StateFailed {
			t.Fatalf("force=%v: state=%s, want failed", force, rec.State)
		}
	}
	id := f.putAndDeliver(t, "out/new/deeper.txt", []byte("payload"), false)
	assertFailed(t, f.mgr, id)
	if n := entryCount(t, f.outside); n != 0 {
		t.Fatalf("outside dir has %d entries, want none", n)
	}
}

func assertFailed(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	rec, _, _ := mgr.Get(id)
	if State(rec.State) != StateFailed {
		t.Fatalf("xfer %s state=%s, want failed", id, rec.State)
	}
}

func TestLocalRunnerPutRelativeLinkInsideRoot(t *testing.T) {
	f := newLinkFixture(t)
	mustSymlink(t, "docs", filepath.Join(f.root, "ok"))
	id := f.putAndDeliver(t, "ok/new.md", []byte("hello"), false)
	assertXferState(t, f.mgr, id, StateDone, "")
	if b, err := os.ReadFile(filepath.Join(f.root, "docs", "new.md")); err != nil || string(b) != "hello" {
		t.Fatalf("landed file = %q, %v", b, err)
	}
}

func TestLocalRunnerPutExistingLinkNeedsForce(t *testing.T) {
	f := newLinkFixture(t)
	secret := filepath.Join(f.outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("LEAK"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, filepath.Join("..", "outside", "secret.txt"), filepath.Join(f.root, "lnk.txt"))
	mustSymlink(t, "no-such-target", filepath.Join(f.root, "dangling.txt"))

	for _, rel := range []string{"lnk.txt", "dangling.txt"} {
		id := f.putAndDeliver(t, rel, []byte("NEW"), false)
		assertXferState(t, f.mgr, id, StateFailed, "exists")
	}
	// force replaces the link itself; the file it pointed at is untouched.
	id := f.putAndDeliver(t, "lnk.txt", []byte("NEW"), true)
	assertXferState(t, f.mgr, id, StateDone, "")
	if b, _ := os.ReadFile(secret); string(b) != "LEAK" {
		t.Fatalf("the file outside the root was modified: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(f.root, "lnk.txt")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("lnk.txt should now be a regular file: %v %v", fi, err)
	}
}

func TestLocalRunnerOwnAttachmentLinkOutside(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("GOFER_CONFIG_DIR", cfgDir)
	cfg := &config.Config{}
	mgr := newLocalRunnerFixture(t, &cfg)
	base := OwnAttachmentsDir()
	outside := t.TempDir()
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, filepath.Join(base, "20260101"))

	srcFile := filepath.Join(t.TempDir(), "src.txt")
	if err := os.WriteFile(srcFile, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := mgr.StagePutToAttachments("tester", "local", srcFile, "20260101/x.txt", false)
	if err != nil {
		t.Fatalf("StagePutToAttachments: %v", err)
	}
	if err := mgr.Deliver(context.Background(), rec.ID); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	assertFailed(t, mgr, rec.ID)
	if n := entryCount(t, outside); n != 0 {
		t.Fatalf("outside dir has %d entries, want none", n)
	}
}
