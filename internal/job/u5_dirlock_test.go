package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDeclaredLockPathsAllowSiblingJobs(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := dirlockService(t, root, []string{"exit", "0"}, nil)
	first := mustSubmit(t, s, JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", LockPaths: []string{"a"}, Prompt: "a"})
	second := mustSubmit(t, s, JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", LockPaths: []string{"b"}, Prompt: "b"})
	a, _ := s.Wait(first.ID)
	b, _ := s.Wait(second.ID)
	if a.Status != StatusDone || b.Status != StatusDone {
		t.Fatalf("sibling locks should complete: %s/%s", a.Status, b.Status)
	}
	var req JobRequest
	if err := json.Unmarshal([]byte(a.RequestJSON), &req); err != nil || len(req.LockPaths) != 1 || req.LockPaths[0] != "a" {
		t.Fatalf("lock paths not persisted: %q err=%v", a.RequestJSON, err)
	}
}

func TestLockPathsInheritedOnResume(t *testing.T) {
	raw, _ := json.Marshal(JobRequest{LockPaths: []string{"nested/repo"}})
	got := lockPathsFromRequest(string(raw))
	if len(got) != 1 || got[0] != "nested/repo" {
		t.Fatalf("lock paths not inherited: %#v", got)
	}
}

func TestRepoModeLocksTouchedRepoOnly(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := nestedGitRoots(context.Background(), root); len(got) != 1 || got[0] != nested {
		t.Fatalf("nested repo roots=%v", got)
	}
}

func TestRepoModeIgnoresBaselineDirtyFiles(t *testing.T) {
	if got := captureUncommitted(t.TempDir()); got != nil {
		t.Fatalf("non-git baseline should be empty, got %v", got)
	}
}

func TestRepoModeFallsBackToCwdWithoutNestedRepos(t *testing.T) {
	root := t.TempDir()
	if got := nestedGitRoots(context.Background(), root); len(got) != 0 {
		t.Fatalf("nested roots=%v", got)
	}
}
