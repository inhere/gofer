package job

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckoutAdminNameReadsForeignGitdir: the admin dir name comes from the `.git` file
// alone, whatever OS wrote the path — a container worker writes `/d/...`, the Windows host
// `D:\...` or `D:/...` (gofer-0eks: git on the host cannot follow a container's gitdir).
func TestCheckoutAdminNameReadsForeignGitdir(t *testing.T) {
	cases := map[string]string{
		"gitdir: /d/work/repo/.git/worktrees/job-a\n":    "job-a",
		"gitdir: D:/work/repo/.git/worktrees/job-b\n":    "job-b",
		"gitdir: D:\\work\\repo\\.git\\worktrees\\job-c": "job-c",
		"gitdir: /d/work/repo/.git/modules/sub\n":        "",
		"not a gitdir line\n":                            "",
	}
	for content, want := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := checkoutAdminName(dir); got != want {
			t.Errorf("checkoutAdminName(%q) = %q, want %q", content, got, want)
		}
	}
	if got := checkoutAdminName(t.TempDir()); got != "" {
		t.Errorf("no .git file: got %q, want empty", got)
	}
}

// TestForeignCheckoutFallsBackToAdminDirUnderTop: when git cannot resolve the checkout
// (its gitdir points at a path of another OS), the stamp is still found under top.
func TestForeignCheckoutFallsBackToAdminDirUnderTop(t *testing.T) {
	top := t.TempDir()
	admin := filepath.Join(top, ".git", "worktrees", "job-x")
	if err := os.MkdirAll(admin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, checkoutOwnerFile), []byte("other-service\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(top, ".worktrees", "job-x")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: /nowhere/on/this/os/.git/worktrees/job-x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Service{}
	s.ownerOnce.Do(func() {})
	s.owner = "this-service"
	if !s.foreignCheckout(t.Context(), top, path) {
		t.Fatal("a checkout stamped by another service must be foreign even when git cannot resolve it")
	}
	s.owner = "other-service"
	if s.foreignCheckout(t.Context(), top, path) {
		t.Fatal("our own stamp is not foreign")
	}
}
