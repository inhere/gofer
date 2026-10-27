package xfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// attachmentsFixture is a project whose exchange dir is the default `tmp`, so the
// attachment directory is <root>/tmp/gofer-attachments — the documented location.
func attachmentsFixture(t *testing.T) (root string, cfg *config.Config) {
	t.Helper()
	root = t.TempDir()
	cfg = &config.Config{
		Storage: config.StorageConfig{DefaultExchangeSubdir: config.DefaultExchangeSubdir},
		Projects: map[string]config.ProjectConfig{
			"alpha": {HostPath: root},
		},
	}
	return root, cfg
}

// dateDir makes <dir>/<YYYYMMDD> and returns its path.
func dateDir(t *testing.T, dir string, day time.Time) string {
	t.Helper()
	p := filepath.Join(dir, day.Format(attachmentDateLayout))
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	return p
}

// TestServerAttachmentsDirAndPrune pins where the server places an attachment it had to put
// WITHOUT a project: <config-dir>/run/attachments/<YYYYMMDD>, pruned by the same date rule
// as a project's directory.
func TestServerAttachmentsDirAndPrune(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(config.EnvConfigDir, cfgDir)
	now := time.Now()
	base := filepath.Join(cfgDir, "run", ServerAttachmentsDirName)
	if got, want := ServerAttachmentsBase(), base; got != want {
		t.Fatalf("ServerAttachmentsBase = %q, want %q", got, want)
	}
	if got, want := ServerAttachmentsDir(now), filepath.Join(base, now.Format(attachmentDateLayout)); got != want {
		t.Fatalf("ServerAttachmentsDir = %q, want %q", got, want)
	}

	old := dateDir(t, base, now.Add(-30*24*time.Hour))
	today := ServerAttachmentsDir(now)
	if err := os.MkdirAll(today, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", today, err)
	}
	PruneServerAttachments(now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("an aged date dir survived the prune: %v", err)
	}
	if _, err := os.Stat(today); err != nil {
		t.Fatalf("today's dir was pruned: %v", err)
	}
}

// TestAttachmentsDirResolvesUnderProject pins the one location a placement writes to and a
// prune deletes in: <exchange dir>/gofer-attachments, following a project's own
// exchange_subdir instead of assuming `tmp`.
func TestAttachmentsDirResolvesUnderProject(t *testing.T) {
	root, cfg := attachmentsFixture(t)
	dir, err := AttachmentsDir(cfg, cfg.Projects["alpha"])
	if err != nil {
		t.Fatalf("AttachmentsDir: %v", err)
	}
	if want := filepath.Join(root, "tmp", AttachmentsDirName); dir != want {
		t.Fatalf("AttachmentsDir = %q, want %q", dir, want)
	}

	// A project that moved its exchange dir keeps attachments under it, not under `tmp`.
	moved := config.ProjectConfig{HostPath: root, ExchangeSubdir: "exchange"}
	dir, err = AttachmentsDir(cfg, moved)
	if err != nil {
		t.Fatalf("AttachmentsDir(moved exchange): %v", err)
	}
	if want := filepath.Join(root, "exchange", AttachmentsDirName); dir != want {
		t.Fatalf("AttachmentsDir(moved exchange) = %q, want %q", dir, want)
	}
}

// TestAttachmentsDirRejectsEscape: the directory a prune may delete in is refused when the
// configured exchange subdir escapes the project root (SafeJoin), and no path outside the
// project is ever returned.
func TestAttachmentsDirRejectsEscape(t *testing.T) {
	root, cfg := attachmentsFixture(t)
	// Every form of escape is refused: a parent hop in either separator spelling, and an
	// absolute path (a Windows drive path or a UNC/backslash root, the form a host passes
	// even when gofer itself runs elsewhere).
	for _, sub := range []string{"../outside", "..", `..\outside`, "C:/outside", `\\server\share`} {
		if _, err := AttachmentsDir(cfg, config.ProjectConfig{HostPath: root, ExchangeSubdir: sub}); err == nil {
			t.Fatalf("AttachmentsDir accepted the escaping exchange_subdir %q", sub)
		}
	}
	// IsAttachmentPath is the second half of the boundary: a sibling directory that merely
	// shares a prefix is NOT inside the attachment directory.
	dir := filepath.Join(root, "tmp", AttachmentsDirName)
	for _, outside := range []string{
		filepath.Join(root, "tmp", AttachmentsDirName+"2", "20200101", "x.png"),
		filepath.Join(root, "tmp", "other", "x.png"),
		dir,
		"",
	} {
		if IsAttachmentPath(dir, outside) {
			t.Fatalf("IsAttachmentPath(%q, %q) = true, want false", dir, outside)
		}
	}
	if !IsAttachmentPath(dir, filepath.Join(dir, "20261019", "x.png")) {
		t.Fatal("IsAttachmentPath rejected a real placement")
	}
}

// TestPruneAttachmentsKeepsRecentAndForeign is the cleanup rule: only DATE directories
// older than the retention are deleted — a recent date dir, a non-date directory, a plain
// file and everything outside the directory survive.
func TestPruneAttachmentsKeepsRecentAndForeign(t *testing.T) {
	root, cfg := attachmentsFixture(t)
	dir, err := AttachmentsDir(cfg, cfg.Projects["alpha"])
	if err != nil {
		t.Fatalf("AttachmentsDir: %v", err)
	}
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local)
	old := dateDir(t, dir, now.AddDate(0, 0, -9))
	// Exactly the retention: "7 days old" still counts as inside the window and stays.
	edge := dateDir(t, dir, now.AddDate(0, 0, -7))
	fresh := dateDir(t, dir, now)
	notes := filepath.Join(dir, "notes") // a non-date directory under the attachment dir
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	loose := filepath.Join(dir, "20200101.txt") // a file, not a date directory
	if err := os.WriteFile(loose, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	outside := dateDir(t, root, now.AddDate(0, 0, -30)) // a date dir OUTSIDE the attachment dir

	removed, err := PruneAttachments(dir, now)
	if err != nil {
		t.Fatalf("PruneAttachments: %v", err)
	}
	if len(removed) != 1 || removed[0] != filepath.Base(old) {
		t.Fatalf("removed = %v, want just %s", removed, filepath.Base(old))
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the 9-day-old date dir survived the prune (err=%v)", err)
	}
	for _, keep := range []string{edge, fresh, notes, loose, outside} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s was removed by the prune: %v", keep, err)
		}
	}

	// A directory that was never created is not an error (nothing was ever placed here).
	missing := filepath.Join(root, "tmp", "not-there")
	if removed, err := PruneAttachments(missing, now); err != nil || len(removed) != 0 {
		t.Fatalf("PruneAttachments(missing) = %v, %v; want no error and nothing removed", removed, err)
	}
}

// TestPruneAttachmentsAfterPutOnlyInsideAttachmentsDir covers the placement-triggered
// cleanup's boundary: an ordinary transfer destination is left alone (an old date dir
// elsewhere in the project is the operator's data), while a placement under the attachment
// directory prunes it.
func TestPruneAttachmentsAfterPutOnlyInsideAttachmentsDir(t *testing.T) {
	root, cfg := attachmentsFixture(t)
	proj := cfg.Projects["alpha"]
	dir, err := AttachmentsDir(cfg, proj)
	if err != nil {
		t.Fatalf("AttachmentsDir: %v", err)
	}
	stale := dateDir(t, dir, time.Now().AddDate(0, 0, -30))

	// A put that landed somewhere else in the project: nothing is pruned.
	PruneAttachmentsAfterPut(cfg, proj, filepath.Join(root, "tmp", "in", "x.bin"), "server")
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("a non-attachment placement pruned the attachment dir: %v", err)
	}
	// A put that escapes the project root: still nothing (the path is simply not inside).
	PruneAttachmentsAfterPut(cfg, proj, filepath.Join(root, "..", "elsewhere", "x.png"), "server")
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("an outside path pruned the attachment dir: %v", err)
	}

	// A placement under the attachment dir prunes it.
	PruneAttachmentsAfterPut(cfg, proj, filepath.Join(dir, "20261019", "x.png"), "server")
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("an attachment placement left the stale date dir behind (err=%v)", err)
	}
}

// TestLocalRunnerPutPrunesAndReportsAbsPath is the server-side placement in one call: the
// put lands under the attachment dir, its absolute path is reported to the journal and the
// stale date dir is cleaned in the same placement.
func TestLocalRunnerPutPrunesAndReportsAbsPath(t *testing.T) {
	root, cfg := attachmentsFixture(t)
	mgr := newLocalRunnerFixture(t, &cfg)
	dir, err := AttachmentsDir(cfg, cfg.Projects["alpha"])
	if err != nil {
		t.Fatalf("AttachmentsDir: %v", err)
	}
	stale := dateDir(t, dir, time.Now().AddDate(0, 0, -30))
	// The placement's own date dir is today's: a fixed date would itself age past the
	// retention window and be pruned by this very placement.
	today := time.Now().Format("20060102")

	src := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(src, []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	absPath, err := mgr.PutAndWait(t.Context(), "tester", "server", "alpha", src, "tmp/gofer-attachments/"+today+"/shot.png", 30*time.Second)
	if err != nil {
		t.Fatalf("PutAndWait: %v", err)
	}
	want := filepath.Join(root, "tmp", AttachmentsDirName, today, "shot.png")
	if absPath != want {
		t.Fatalf("abs path = %q, want %q", absPath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the attachment was not placed at its absolute path: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale date dir survived the placement (err=%v)", err)
	}
	if !strings.HasSuffix(absPath, filepath.Join(AttachmentsDirName, today, "shot.png")) {
		t.Fatalf("reported path %q is not the attachment layout", absPath)
	}
}
