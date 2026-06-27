package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// gitRun runs a git subcommand under dir with a deterministic identity so commits
// succeed in a bare CI environment (no global git config). It fails the test on
// any error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// initRepo creates a git repo at dir (default branch "main") with one commit, and
// returns dir. A README.md is committed so log/branch are non-empty.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gitRun(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hello\n\ninitial\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "init")
	return dir
}

// cfgFor builds a Config + ProjectConfig whose ExecPath resolves to host (default
// path_view), so ExecPath(proj) == host.
func cfgFor(host string) (*config.Config, config.ProjectConfig) {
	proj := config.ProjectConfig{HostPath: host}
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{"p": proj}}
	return cfg, proj
}

func TestProjectGit_Repo(t *testing.T) {
	dir := initRepo(t, t.TempDir())
	cfg, proj := cfgFor(dir)

	st, err := ProjectGit(cfg, proj)
	if err != nil {
		t.Fatalf("ProjectGit err: %v", err)
	}
	if !st.IsGitRepo {
		t.Fatalf("expected IsGitRepo=true")
	}
	if st.Branch != "main" {
		t.Fatalf("branch=%q, want main", st.Branch)
	}
	if st.Dirty {
		t.Fatalf("clean repo reported dirty")
	}
	if len(st.RecentCommits) != 1 {
		t.Fatalf("expected 1 commit, got %d: %+v", len(st.RecentCommits), st.RecentCommits)
	}
	c := st.RecentCommits[0]
	if c.Hash == "" || c.Subject != "init" || c.Author != "test" || c.Ts == 0 {
		t.Fatalf("unexpected commit parse: %+v", c)
	}
}

func TestProjectGit_Dirty(t *testing.T) {
	dir := initRepo(t, t.TempDir())
	// Add an untracked file → status --porcelain non-empty → dirty.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, proj := cfgFor(dir)

	st, err := ProjectGit(cfg, proj)
	if err != nil {
		t.Fatalf("ProjectGit err: %v", err)
	}
	if !st.Dirty {
		t.Fatalf("expected dirty=true after untracked file")
	}
}

func TestProjectGit_NotARepo(t *testing.T) {
	cfg, proj := cfgFor(t.TempDir()) // plain dir, no git init

	st, err := ProjectGit(cfg, proj)
	if err != nil {
		t.Fatalf("ProjectGit err: %v", err)
	}
	if st.IsGitRepo {
		t.Fatalf("non-git dir reported IsGitRepo=true")
	}
	if st.RecentCommits == nil {
		t.Fatalf("RecentCommits should be a non-nil slice")
	}
}

func TestDiscoverRepos_NestedDepthSkip(t *testing.T) {
	root := t.TempDir()
	// root itself is a repo (depth 0).
	initRepo(t, root)
	// sub1 at depth 1.
	initRepo(t, filepath.Join(root, "sub1"))
	// nested at depth 2.
	initRepo(t, filepath.Join(root, "a", "nested"))
	// too deep: depth 4 (a2/b/c/deep) — beyond repoScanDepth(3), must be skipped.
	initRepo(t, filepath.Join(root, "a2", "b", "c", "deep"))
	// inside a skip dir: node_modules/pkg — must be skipped.
	initRepo(t, filepath.Join(root, "node_modules", "pkg"))

	cfg, proj := cfgFor(root)
	repos, err := DiscoverRepos(cfg, proj)
	if err != nil {
		t.Fatalf("DiscoverRepos err: %v", err)
	}

	got := map[string]RepoInfo{}
	for _, r := range repos {
		got[r.RelPath] = r
	}
	// Expected hits: root("."), sub1, a/nested.
	for _, want := range []string{".", "sub1", "a/nested"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing expected repo %q; got %v", want, keysOf(got))
		}
	}
	// Skipped: too-deep repo + node_modules repo.
	for _, bad := range []string{"a2/b/c/deep", "node_modules/pkg"} {
		if _, ok := got[bad]; ok {
			t.Fatalf("repo %q should have been skipped (depth/skip-dir); got %v", bad, keysOf(got))
		}
	}
	// Branch+dirty populated for discovered repos.
	if got["sub1"].Branch != "main" {
		t.Fatalf("sub1 branch=%q, want main", got["sub1"].Branch)
	}
}

func keysOf(m map[string]RepoInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestReadKeyFile_Allowed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Title\nbody\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, proj := cfgFor(dir)

	fc, err := ReadKeyFile(cfg, proj, "README.md")
	if err != nil {
		t.Fatalf("ReadKeyFile err: %v", err)
	}
	if fc.Name != "README.md" || fc.Truncated || fc.Content != "# Title\nbody\n" {
		t.Fatalf("unexpected FileContent: %+v", fc)
	}
	if fc.Size != int64(len("# Title\nbody\n")) {
		t.Fatalf("size=%d, want %d", fc.Size, len("# Title\nbody\n"))
	}
}

func TestReadKeyFile_Forbidden(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, proj := cfgFor(dir)

	_, err := ReadKeyFile(cfg, proj, ".env")
	if !errors.Is(err, ErrForbiddenFile) {
		t.Fatalf("err=%v, want ErrForbiddenFile", err)
	}
}

func TestReadKeyFile_Traversal(t *testing.T) {
	dir := t.TempDir()
	// Plant a whitelisted-name file ABOVE the project root; traversal must not reach it.
	parent := filepath.Dir(dir)
	outside := filepath.Join(parent, "README.md")
	if err := os.WriteFile(outside, []byte("LEAK"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	cfg, proj := cfgFor(dir)

	// basename is whitelisted (README.md) so it passes the allowlist, but SafeJoin
	// must reject the ../ escape → a non-sentinel error (handler → 404).
	_, err := ReadKeyFile(cfg, proj, "../README.md")
	if err == nil {
		t.Fatalf("traversal must be rejected")
	}
	if errors.Is(err, ErrForbiddenFile) || errors.Is(err, ErrBinary) {
		t.Fatalf("expected a SafeJoin escape error, got sentinel: %v", err)
	}
}

func TestReadKeyFile_Truncated(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("a", maxKeyFileBytes+1024)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(big), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, proj := cfgFor(dir)

	fc, err := ReadKeyFile(cfg, proj, "README.md")
	if err != nil {
		t.Fatalf("ReadKeyFile err: %v", err)
	}
	if !fc.Truncated {
		t.Fatalf("expected Truncated=true for oversize file")
	}
	if len(fc.Content) != maxKeyFileBytes {
		t.Fatalf("content len=%d, want cap %d", len(fc.Content), maxKeyFileBytes)
	}
	if fc.Size != int64(len(big)) {
		t.Fatalf("size=%d, want full %d", fc.Size, len(big))
	}
}

func TestReadKeyFile_Binary(t *testing.T) {
	dir := t.TempDir()
	// Whitelisted name but binary content (NUL byte) → ErrBinary.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte{0x00, 0x01, 0x02, 'a'}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, proj := cfgFor(dir)

	_, err := ReadKeyFile(cfg, proj, "README")
	if !errors.Is(err, ErrBinary) {
		t.Fatalf("err=%v, want ErrBinary", err)
	}
}

func TestReadKeyFile_NotFound(t *testing.T) {
	dir := t.TempDir() // no README present
	cfg, proj := cfgFor(dir)

	_, err := ReadKeyFile(cfg, proj, "README.md")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err=%v, want os.ErrNotExist", err)
	}
}
