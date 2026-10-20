package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/util"
)

// TestWorktreeLayoutTemplatesAndName proves the project's worktree.dir / worktree.branch
// templates are rendered with {name} (from --worktree-name) and {job_id}, so a job can
// land in a readable checkout instead of the historical job-id path.
func TestWorktreeLayoutTemplatesAndName(t *testing.T) {
	repo, _ := gitRepo(t)
	state := t.TempDir()
	s := newWorktreeServiceWithProject(t, repo, state, func(p *config.ProjectConfig) {
		p.Worktree = &config.ProjectWorktree{Dir: ".worktrees/{name}", Branch: "feat/{name}-{job_id}"}
	})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 60,
		Worktree: true, WorktreeName: "layout",
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}
	wantPath := filepath.Join(repo, ".worktrees", "layout")
	if util.RealPath(final.WorktreePath) != util.RealPath(wantPath) {
		t.Fatalf("worktree path = %q, want %q", final.WorktreePath, wantPath)
	}
	wantBranch := "feat/layout-" + final.ID
	if final.WorktreeBranch != wantBranch {
		t.Fatalf("branch = %q, want %q", final.WorktreeBranch, wantBranch)
	}
	// The checkout really is the configured layout: its HEAD is the new branch.
	if got := gitOutIn(t, final.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD"); got != wantBranch {
		t.Fatalf("worktree HEAD = %q, want %q", got, wantBranch)
	}
}

// TestWorktreeBranchFlagOverridesTemplate proves --branch wins over the project's
// worktree.branch template.
func TestWorktreeBranchFlagOverridesTemplate(t *testing.T) {
	repo, _ := gitRepo(t)
	state := t.TempDir()
	s := newWorktreeServiceWithProject(t, repo, state, func(p *config.ProjectConfig) {
		p.Worktree = &config.ProjectWorktree{Branch: "feat/{name}"}
	})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 60,
		Worktree: true, WorktreeName: "topic", WorktreeBranch: "hotfix/explicit",
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}
	if final.WorktreeBranch != "hotfix/explicit" {
		t.Fatalf("branch = %q, want the --branch override", final.WorktreeBranch)
	}
}

// TestWorktreeSameNameRefused proves a second job cannot take an existing worktree dir
// or branch: two jobs in one checkout would fight over one index — exactly what
// --worktree exists to prevent.
func TestWorktreeSameNameRefused(t *testing.T) {
	repo, _ := gitRepo(t)
	state := t.TempDir()
	s := newWorktreeServiceWithProject(t, repo, state, func(p *config.ProjectConfig) {
		p.Worktree = &config.ProjectWorktree{Dir: ".worktrees/{name}"}
	})
	req := JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 60,
		Worktree: true, WorktreeName: "shared",
	}
	first := submitAndWait(t, s, req)
	if first.Status != StatusDone {
		t.Fatalf("first job = %s (err=%s)", first.Status, first.Error)
	}
	if _, err := s.Submit(req); err == nil {
		t.Fatal("second submit with the same --worktree-name must be refused")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %q, want it to say the worktree dir already exists", err)
	}
	if !isDir(first.WorktreePath) {
		t.Fatal("the refused submit must leave the first worktree alone")
	}
}

// TestWorktreeRejectsUnsafeName proves a --worktree-name that cannot be a path is
// refused at submit, before anything is created.
func TestWorktreeRejectsUnsafeName(t *testing.T) {
	repo, _ := gitRepo(t)
	state := t.TempDir()
	s := newWorktreeService(t, repo, state)
	_, err := s.Submit(JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 60,
		Worktree: true, WorktreeName: "../escape",
	})
	if err == nil || !strings.Contains(err.Error(), "worktree name") {
		t.Fatalf("err = %v, want a refusal naming the worktree name", err)
	}
}

// TestWorktreeLinksCreatedAndCleanupKeepsMainCheckout is the safety test for the whole
// feature: the dependency directory linked into the worktree is the MAIN checkout's
// (its contents readable from the job's cwd), and removing the worktree unlinks it
// without touching the main copy.
func TestWorktreeLinksCreatedAndCleanupKeepsMainCheckout(t *testing.T) {
	repo, _ := gitRepo(t)
	writeRepoFile(t, repo, "web/node_modules/marker.txt", "dependency bytes\n")
	writeRepoFile(t, repo, ".gitignore", "tmp/\nweb/node_modules/\n")
	state := t.TempDir()
	s := newWorktreeServiceWithProject(t, repo, state, func(p *config.ProjectConfig) {
		p.Worktree = &config.ProjectWorktree{Links: []string{"web/node_modules", "absent/dir"}}
	})
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "repo", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 60, Worktree: true,
	})
	if final.Status != StatusDone {
		t.Fatalf("a missing link source must not fail the job: status = %s (err=%s)", final.Status, final.Error)
	}
	linkDir := filepath.Join(final.WorktreePath, "web", "node_modules")
	fi, err := os.Lstat(linkDir)
	if err != nil {
		t.Fatalf("link %s was not created: %v", linkDir, err)
	}
	if !isDirLink(fi, linkDir) {
		t.Fatalf("%s is not a link (a junction/symlink is what makes it the main checkout's copy)", linkDir)
	}
	body, err := os.ReadFile(filepath.Join(linkDir, "marker.txt"))
	if err != nil {
		t.Fatalf("read through the link: %v", err)
	}
	if string(body) != "dependency bytes\n" {
		t.Fatalf("link content = %q, want the main checkout's file", body)
	}

	if _, err := s.RemoveWorktree(final.ID, true, true); err != nil {
		t.Fatalf("remove worktree with links: %v", err)
	}
	if isDir(final.WorktreePath) {
		t.Fatalf("worktree %s still present after rm", final.WorktreePath)
	}
	mainFile := filepath.Join(repo, "web", "node_modules", "marker.txt")
	if got, err := os.ReadFile(mainFile); err != nil || string(got) != "dependency bytes\n" {
		t.Fatalf("main checkout's linked directory was damaged by the cleanup: content=%q err=%v", got, err)
	}
}

// TestRemoveWorktreeLinksOnlyUnlinks proves the unlink step removes the LINK and
// nothing else: the target keeps its files, and a real directory by the same
// project-relative name is left for git to handle.
func TestRemoveWorktreeLinksOnlyUnlinks(t *testing.T) {
	main := t.TempDir()
	target := filepath.Join(main, "web", "node_modules")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, target, "keep.txt", "keep me\n")

	wtDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wtDir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(wtDir, "web", "node_modules")
	if err := createDirLink(target, link); err != nil {
		t.Fatalf("create link: %v", err)
	}
	// A real directory under a configured link name must be left alone.
	realDir := filepath.Join(wtDir, "docs")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}

	removed := removeWorktreeLinks(wtDir, []string{"web/node_modules", "docs"})
	if len(removed) != 1 || removed[0] != "web/node_modules" {
		t.Fatalf("removed = %v, want only the link", removed)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("link %s still present (err=%v)", link, err)
	}
	if _, err := os.Stat(filepath.Join(target, "keep.txt")); err != nil {
		t.Fatalf("target content was removed with the link: %v", err)
	}
	if !isDir(realDir) {
		t.Fatal("a real directory named like a configured link must not be deleted")
	}
}
