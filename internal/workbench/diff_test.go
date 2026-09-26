package workbench

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestThreadDiffSpansChain(t *testing.T) {
	repo := t.TempDir()
	runWorkbenchGit(t, repo, "init")
	runWorkbenchGit(t, repo, "config", "user.email", "gofer-test@example.invalid")
	runWorkbenchGit(t, repo, "config", "user.name", "gofer test")
	writeWorkbenchFile(t, repo, "base.txt", "base one\nbase two\nbase three\n")
	runWorkbenchGit(t, repo, "add", "base.txt")
	runWorkbenchGit(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(runWorkbenchGit(t, repo, "rev-parse", "HEAD"))

	store := openWorkbenchStore(t)
	firstDir := filepath.Join(t.TempDir(), "first")
	secondDir := filepath.Join(t.TempDir(), "second")
	latestDir := filepath.Join(t.TempDir(), "latest")
	for _, dir := range []string{firstDir, secondDir, latestDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir result dir: %v", err)
		}
	}
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "turn-1", ProjectKey: "self", Agent: "cli", Runner: "local", Cwd: repo,
		SessionID: "diff-chain", Status: job.StatusDone, StartedAt: 10, UpdatedAt: 11,
		BaseSHA: base, ResultDir: firstDir,
	}, "first", "first")

	writeWorkbenchFile(t, repo, "committed.txt", "committed by turn two\n")
	runWorkbenchGit(t, repo, "add", "committed.txt")
	runWorkbenchGit(t, repo, "commit", "-m", "turn two")
	head := strings.TrimSpace(runWorkbenchGit(t, repo, "rev-parse", "HEAD"))
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "turn-2", ProjectKey: "self", Agent: "cli", Runner: "local", Cwd: repo,
		SessionID: "diff-chain", ResumedFrom: "turn-1", Status: job.StatusDone,
		StartedAt: 20, UpdatedAt: 21, BaseSHA: head, ResultDir: secondDir,
		CommitsJSON: `[{"sha":"` + head + `","subject":"turn two"}]`,
	}, "second", "second")

	writeWorkbenchFile(t, repo, "base.txt", "base one\nbase two changed by turn three\nbase three\n")
	writeWorkbenchFile(t, repo, "untracked.txt", "UNTRACKED-CONTENT-MUST-NOT-ENTER-PATCH\n")
	latest := jobstore.JobRecord{
		ID: "turn-3", ProjectKey: "self", Agent: "cli", Runner: "local", Cwd: repo,
		SessionID: "diff-chain", ResumedFrom: "turn-2", Status: job.StatusDone,
		StartedAt: 30, UpdatedAt: 31, BaseSHA: head, ResultDir: latestDir,
		CommitsJSON: `[{"sha":"` + head + `","subject":"turn two"}]`,
	}
	putWorkbenchJob(t, store, latest, "third", "third")

	service := NewService(store, nil, nil)
	live, err := service.Diff("s:diff-chain")
	if err != nil {
		t.Fatalf("Diff live: %v", err)
	}
	if live.Source != DiffSourceLive || live.Base != base || live.Head != head {
		t.Fatalf("live identity = source=%q base=%q head=%q", live.Source, live.Base, live.Head)
	}
	for _, marker := range []string{"committed by turn two", "base two changed by turn three"} {
		if !strings.Contains(live.Patch, marker) {
			t.Fatalf("live patch missing %q:\n%s", marker, live.Patch)
		}
	}
	if strings.Contains(live.Patch, "UNTRACKED-CONTENT-MUST-NOT-ENTER-PATCH") {
		t.Fatalf("untracked content leaked into patch")
	}
	for _, path := range []string{"base.txt", "committed.txt", "untracked.txt"} {
		if _, ok := findThreadDiffFile(live.Files, path); !ok {
			t.Fatalf("live files missing %q: %+v", path, live.Files)
		}
	}
	untracked, _ := findThreadDiffFile(live.Files, "untracked.txt")
	if untracked.Status != "?" || untracked.Additions != 0 || untracked.Deletions != 0 {
		t.Fatalf("untracked metadata = %+v", untracked)
	}

	writeWorkbenchFile(t, repo, "base.txt", strings.Repeat("large line for truncation\n", 120000))
	large, err := service.Diff("s:diff-chain")
	if err != nil {
		t.Fatalf("Diff large: %v", err)
	}
	if !large.Truncated || len(large.Patch) > 2*1024*1024 {
		t.Fatalf("large patch truncated=%v bytes=%d", large.Truncated, len(large.Patch))
	}

	capturedPatch := "diff --git a/captured.txt b/captured.txt\n" +
		"new file mode 100644\n--- /dev/null\n+++ b/captured.txt\n@@ -0,0 +1 @@\n+captured latest round\n"
	if err := os.WriteFile(filepath.Join(latestDir, "changes.diff"), []byte(capturedPatch), 0o644); err != nil {
		t.Fatalf("write captured patch: %v", err)
	}
	latest.Runner = "worker-1"
	latest.CommitsJSON = `[{"sha":"captured-head","subject":"captured commit"}]`
	putWorkbenchJob(t, store, latest, "third", "third")
	captured, err := service.Diff("s:diff-chain")
	if err != nil {
		t.Fatalf("Diff captured remote: %v", err)
	}
	if captured.Source != DiffSourceCaptured || captured.Patch != capturedPatch || len(captured.Commits) != 1 {
		t.Fatalf("captured response = %+v", captured)
	}
	if !strings.Contains(captured.Notice, "最新一轮") {
		t.Fatalf("captured notice = %q", captured.Notice)
	}

	latest.Runner = "local"
	latest.Cwd = filepath.Join(repo, "missing")
	putWorkbenchJob(t, store, latest, "third", "third")
	missingCwd, err := service.Diff("s:diff-chain")
	if err != nil || missingCwd.Source != DiffSourceCaptured {
		t.Fatalf("missing cwd fallback = %+v err=%v", missingCwd, err)
	}

	first, ok, err := store.GetJob("turn-1")
	if err != nil || !ok {
		t.Fatalf("GetJob first: ok=%v err=%v", ok, err)
	}
	first.BaseSHA = ""
	first.WorktreeBaseSHA = ""
	if err := store.UpsertJob(first); err != nil {
		t.Fatalf("clear base: %v", err)
	}
	_, err = service.Diff("s:diff-chain")
	if !errors.Is(err, ErrMissingDiffBase) || !strings.Contains(err.Error(), "首轮") {
		t.Fatalf("missing base err=%v", err)
	}
}

func findThreadDiffFile(files []ThreadDiffFile, path string) (ThreadDiffFile, bool) {
	for _, file := range files {
		if file.Path == path {
			return file, true
		}
	}
	return ThreadDiffFile{}, false
}

func runWorkbenchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeWorkbenchFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
