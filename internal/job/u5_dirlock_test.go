package job

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
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

func TestRepoModeRequiresDeclaredLock(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"repo-a", "repo-b"} {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := dirlockService(t, root, []string{"exit", "0"}, func(cfg *config.Config) {
		cfg.Projects["self"] = config.ProjectConfig{
			HostPath: root, DirLockMode: "repo",
			AllowedAgents: []string{"agent", "exec"}, AllowedRunners: []string{"local"}, AllowExec: true,
		}
	})
	_, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "repo-a") || !strings.Contains(err.Error(), "repo-b") || !strings.Contains(err.Error(), "--lock <repo>") {
		t.Fatalf("repo-mode admission error = %v, want both nested repos and --lock example", err)
	}
	for _, req := range []JobRequest{
		{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", LockPaths: []string{"repo-a"}, Prompt: "lock"},
		{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", ExclusiveDir: boolPtr(true), Prompt: "exclusive"},
		{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", ExclusiveDir: boolPtr(false), Prompt: "shared"},
		{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", ReadOnly: true, Prompt: "readonly"},
	} {
		if _, err := s.Submit(req); err != nil {
			t.Fatalf("repo-mode exempt request %#v rejected: %v", req, err)
		}
	}
}

func TestLockPathsInheritedOnResume(t *testing.T) {
	raw, _ := json.Marshal(JobRequest{LockPaths: []string{"nested/repo"}})
	got := lockPathsFromRequest(string(raw))
	if len(got) != 1 || got[0] != "nested/repo" {
		t.Fatalf("lock paths not inherited: %#v", got)
	}
}

func TestRepoModeResumeKeepsSourceLock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo-a", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, DirLockMode: "repo", AllowedAgents: []string{"agent", "exec"},
			AllowedRunners: []string{"local"}, AllowExec: true,
		}},
		Agents: map[string]config.AgentConfig{"agent": {
			Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"exit", "0", "{{prompt}}"},
			SessionResume: []string{"printf", "resumed {{session_id}}: {{prompt}}"},
		}},
	}
	s := newServiceFromCfg(t, root, cfg)
	src := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", LockPaths: []string{"repo-a"}, Prompt: "source", SessionID: "sess-repo"})
	cont, err := s.ResumeJob(src.ID, "continue", "", "caller")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	got := lockPathsFromRequest(cont.RequestJSON)
	if len(got) != 1 || got[0] != "repo-a" {
		t.Fatalf("resumed lock paths=%v, want [repo-a]", got)
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
	s := dirlockService(t, root, []string{"exit", "0"}, func(cfg *config.Config) {
		proj := cfg.Projects["self"]
		proj.DirLockMode = "repo"
		cfg.Projects["self"] = proj
	})
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", Prompt: "cwd"}); err != nil {
		t.Fatalf("repo mode without nested repositories rejected: %v", err)
	}
}

// TestDeclaredLockMakesExecJobExclusive: exec jobs run unlocked by default, but a
// declared --lock is an explicit request for the lock, so it must be honored; and
// combining it with an explicit --shared-dir is contradictory and rejected.
func TestDeclaredLockMakesExecJobExclusive(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestService(t, root)
	res := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: ".", LockPaths: []string{"a"}, Cmd: []string{"true"}, TimeoutSec: 30})
	if res.Status != StatusDone || !res.DirExclusive {
		t.Fatalf("exec job with --lock: status=%s dir_exclusive=%v, want done/true", res.Status, res.DirExclusive)
	}
	shared := false
	_, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: ".", LockPaths: []string{"a"}, ExclusiveDir: &shared, Cmd: []string{"true"}, TimeoutSec: 30})
	if err == nil || !strings.Contains(err.Error(), "--shared-dir") {
		t.Fatalf("--lock with --shared-dir: err=%v, want rejection naming --shared-dir", err)
	}
}
