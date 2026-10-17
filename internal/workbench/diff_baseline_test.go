package workbench

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestThreadDiffLiveStartsAtContinuationBaseline: a job that continues a previous
// plan item measures its own changes from JobRequest.BaselineSHA, so the live diff
// of the running turn must start there instead of at the branch base (which would
// credit it with the previous item's commits).
func TestThreadDiffLiveStartsAtContinuationBaseline(t *testing.T) {
	repo := t.TempDir()
	runWorkbenchGit(t, repo, "init")
	runWorkbenchGit(t, repo, "config", "user.email", "gofer-test@example.invalid")
	runWorkbenchGit(t, repo, "config", "user.name", "gofer test")
	writeWorkbenchFile(t, repo, "base.txt", "base\n")
	runWorkbenchGit(t, repo, "add", "base.txt")
	runWorkbenchGit(t, repo, "commit", "-m", "base")
	worktreeBase := strings.TrimSpace(runWorkbenchGit(t, repo, "rev-parse", "HEAD"))

	store := openWorkbenchStore(t)
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "item-1", ProjectKey: "self", Agent: "cli", Runner: "local", Cwd: repo,
		SessionID: "diff-baseline", Interactive: true, Status: job.StatusDone,
		StartedAt: 10, UpdatedAt: 11, BaseSHA: worktreeBase, WorktreeBaseSHA: worktreeBase,
		ResultDir: filepath.Join(t.TempDir(), "item-1"),
	}, "item one", "item one")

	// The first item's committed work sits between the branch base and the point
	// the continuation starts from.
	writeWorkbenchFile(t, repo, "item-one.txt", "committed by item one\n")
	runWorkbenchGit(t, repo, "add", "item-one.txt")
	runWorkbenchGit(t, repo, "commit", "-m", "item one")
	baseline := strings.TrimSpace(runWorkbenchGit(t, repo, "rev-parse", "HEAD"))

	putWorkbenchContinuation(t, store, jobstore.JobRecord{
		ID: "item-2", ProjectKey: "self", Agent: "cli", Runner: "local", Cwd: repo,
		SessionID: "diff-baseline", Interactive: true, ResumedFrom: "item-1",
		Status: job.StatusRunning, StartedAt: 20, UpdatedAt: 25,
		BaseSHA: baseline, WorktreeBaseSHA: worktreeBase,
		ResultDir: filepath.Join(t.TempDir(), "item-2"),
	}, "item two", "item two", baseline)

	writeWorkbenchFile(t, repo, "base.txt", "base\nworking tree of item two\n")

	service := NewService(store, nil, nil)
	live, err := service.Diff("s:diff-baseline")
	if err != nil {
		t.Fatalf("Diff live continuation: %v", err)
	}
	if live.Source != DiffSourceLive || live.Base != baseline {
		t.Fatalf("continuation identity = source=%q base=%q, want live/%q", live.Source, live.Base, baseline)
	}
	if !strings.Contains(live.Patch, "working tree of item two") {
		t.Fatalf("live patch missing the continuation's own change:\n%s", live.Patch)
	}
	if strings.Contains(live.Patch, "committed by item one") {
		t.Fatalf("live patch credited the previous item's commit:\n%s", live.Patch)
	}
	if _, ok := findThreadDiffFile(live.Files, "base.txt"); !ok {
		t.Fatalf("live files missing base.txt: %+v", live.Files)
	}
	if _, ok := findThreadDiffFile(live.Files, "item-one.txt"); ok {
		t.Fatalf("live files list the previous item's file: %+v", live.Files)
	}
}

// TestThreadDiffLiveWithoutBaselineKeepsThreadBase: everything else (a plain job,
// a resume without a baseline) still spans the whole thread from the first turn's
// base — worktree branch base first, as before.
func TestThreadDiffLiveWithoutBaselineKeepsThreadBase(t *testing.T) {
	repo := t.TempDir()
	runWorkbenchGit(t, repo, "init")
	runWorkbenchGit(t, repo, "config", "user.email", "gofer-test@example.invalid")
	runWorkbenchGit(t, repo, "config", "user.name", "gofer test")
	writeWorkbenchFile(t, repo, "base.txt", "base\n")
	runWorkbenchGit(t, repo, "add", "base.txt")
	runWorkbenchGit(t, repo, "commit", "-m", "base")
	branchBase := strings.TrimSpace(runWorkbenchGit(t, repo, "rev-parse", "HEAD"))

	store := openWorkbenchStore(t)
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "turn-1", ProjectKey: "self", Agent: "cli", Runner: "local", Cwd: repo,
		SessionID: "diff-no-baseline", Interactive: true, Status: job.StatusDone,
		StartedAt: 10, UpdatedAt: 11, BaseSHA: branchBase, WorktreeBaseSHA: branchBase,
		ResultDir: filepath.Join(t.TempDir(), "turn-1"),
	}, "turn one", "turn one")

	writeWorkbenchFile(t, repo, "turn-one.txt", "committed by turn one\n")
	runWorkbenchGit(t, repo, "add", "turn-one.txt")
	runWorkbenchGit(t, repo, "commit", "-m", "turn one")
	head := strings.TrimSpace(runWorkbenchGit(t, repo, "rev-parse", "HEAD"))

	// A resume with no baseline_sha in its request: unchanged behavior.
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "turn-2", ProjectKey: "self", Agent: "cli", Runner: "local", Cwd: repo,
		SessionID: "diff-no-baseline", Interactive: true, ResumedFrom: "turn-1",
		Status: job.StatusRunning, StartedAt: 20, UpdatedAt: 25,
		BaseSHA: head, WorktreeBaseSHA: branchBase,
		ResultDir: filepath.Join(t.TempDir(), "turn-2"),
	}, "turn two", "turn two")

	writeWorkbenchFile(t, repo, "base.txt", "base\nworking tree of turn two\n")

	service := NewService(store, nil, nil)
	live, err := service.Diff("s:diff-no-baseline")
	if err != nil {
		t.Fatalf("Diff live: %v", err)
	}
	if live.Source != DiffSourceLive || live.Base != branchBase || live.Head != head {
		t.Fatalf("identity = source=%q base=%q head=%q, want live/%q/%q",
			live.Source, live.Base, live.Head, branchBase, head)
	}
	for _, marker := range []string{"committed by turn one", "working tree of turn two"} {
		if !strings.Contains(live.Patch, marker) {
			t.Fatalf("live patch missing %q:\n%s", marker, live.Patch)
		}
	}
}

// putWorkbenchContinuation stores rec with a request JSON carrying baseline_sha,
// the persisted wire field of JobRequest.BaselineSHA. It is written as raw JSON so
// the test pins the field name the workbench reads.
func putWorkbenchContinuation(t *testing.T, store *jobstore.Store, rec jobstore.JobRecord, title, prompt, baseline string) {
	t.Helper()
	req := map[string]string{
		"project_key":  rec.ProjectKey,
		"agent":        rec.Agent,
		"runner":       rec.Runner,
		"prompt":       prompt,
		"title":        title,
		"cwd":          rec.Cwd,
		"baseline_sha": baseline,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal continuation request: %v", err)
	}
	rec.RequestJSON = string(raw)
	if rec.ResultDir == "" {
		rec.ResultDir = filepath.Join(t.TempDir(), rec.ID)
	}
	if err := store.UpsertJob(rec); err != nil {
		t.Fatalf("upsert continuation job: %v", err)
	}
}
