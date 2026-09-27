package job

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func uncommittedGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func uncommittedWrite(t *testing.T, root, name, value string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUncommittedDetectsNewDirtyOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	uncommittedGit(t, repo, "init")
	uncommittedWrite(t, repo, ".gitignore", "ignored.txt\n")
	uncommittedWrite(t, repo, "A.txt", "base")
	uncommittedWrite(t, repo, "D.txt", "base")
	uncommittedGit(t, repo, "add", ".gitignore", "A.txt", "D.txt")
	uncommittedGit(t, repo, "commit", "-m", "base")
	uncommittedWrite(t, repo, "A.txt", "already dirty")
	uncommittedWrite(t, repo, "B.txt", "already untracked")
	before := captureUncommitted(repo)
	uncommittedWrite(t, repo, "A.txt", "changed again")
	uncommittedWrite(t, repo, "C.txt", "new dirty")
	uncommittedWrite(t, repo, "D.txt", "committed change")
	uncommittedWrite(t, repo, "ignored.txt", "ignored")
	uncommittedGit(t, repo, "add", "D.txt")
	uncommittedGit(t, repo, "commit", "-m", "commit D")
	after := captureUncommitted(repo)
	if got, want := diffUncommitted(before, after, nil), []string{"A.txt", "C.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("new dirty = %v, want %v", got, want)
	}
	if got := diffUncommitted(before, after, []string{"C*"}); !reflect.DeepEqual(got, []string{"A.txt"}) {
		t.Fatalf("ignore C* = %v, want [A.txt]", got)
	}
	if got := uncommittedEnabled("exec", "warn"); got {
		t.Fatal("exec job must not detect uncommitted changes")
	}
}

func TestUncommittedNestedRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal(err)
	}
	outer := t.TempDir()
	uncommittedGit(t, outer, "init")
	nested := filepath.Join(outer, "tools", "gofer")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	uncommittedGit(t, nested, "init")
	uncommittedWrite(t, nested, "tracked.txt", "base")
	uncommittedGit(t, nested, "add", "tracked.txt")
	uncommittedGit(t, nested, "commit", "-m", "base")
	before := captureUncommitted(outer)
	uncommittedWrite(t, nested, "tracked.txt", "dirty")
	if got := diffUncommitted(before, captureUncommitted(outer), nil); !reflect.DeepEqual(got, []string{"tools/gofer/tracked.txt"}) {
		t.Fatalf("nested dirty = %v", got)
	}
}

func TestUncommittedPolicyReviewAndResume(t *testing.T) {
	files := []string{"a.go", "b.go"}
	if got := uncommittedDecision("review", files, "sid", 0, 1, false); got != "review" {
		t.Fatalf("review decision = %q", got)
	}
	if got := uncommittedDecision("resume", files, "sid", 0, 1, false); got != "resume" {
		t.Fatalf("resume decision = %q", got)
	}
	if got := uncommittedDecision("resume", files, "sid", 1, 1, true); got != "review" {
		t.Fatalf("still dirty after resume = %q", got)
	}
	if got := uncommittedDecision("resume", files, "", 0, 1, false); got != "review" {
		t.Fatalf("no session decision = %q", got)
	}
	if got := uncommittedDecision("warn", files, "sid", 0, 1, false); got != "warn" {
		t.Fatalf("warn decision = %q", got)
	}
	if prompt := uncommittedResumePrompt(files); !strings.Contains(prompt, "a.go") || !strings.Contains(prompt, "b.go") || !strings.Contains(prompt, "不要 push") {
		t.Fatalf("resume prompt = %q", prompt)
	}
}
