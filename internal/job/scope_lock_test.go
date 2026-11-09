package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// gofer-341t：独占 job 的目录锁由 --scope 推出。断言都落在提交后落库的 request_json 与
// 事件上（盖章是提交的产物），不依赖 job 的运行时序。

// scopeLockRepo makes root a git checkout with the given sub directories.
func scopeLockRepo(t *testing.T, root string, dirs ...string) {
	t.Helper()
	initGitRepo(t, root)
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// submitScoped submits a writable agent job with the given scope and waits for it.
func submitScoped(t *testing.T, s *Service, mut func(*JobRequest), scope ...string) JobResult {
	t.Helper()
	req := JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", Prompt: "p", Scope: scope}
	if mut != nil {
		mut(&req)
	}
	res := mustSubmit(t, s, req)
	final, _ := s.Wait(res.ID)
	if final.Status != StatusDone {
		t.Fatalf("job %s = %s (%s), want done", final.ID, final.Status, final.Error)
	}
	return final
}

// lockFallbackReason returns the reason of the job's job.lock_scope_fallback event ("" none).
func lockFallbackReason(t *testing.T, s *Service, jobID string) string {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type != EventJobLockScopeFallback {
			continue
		}
		var d struct {
			Reason string   `json:"reason"`
			Scope  []string `json:"scope"`
		}
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil || d.Reason == "" || len(d.Scope) == 0 {
			t.Fatalf("bad %s detail %q (%v)", e.Type, e.Detail, err)
		}
		return d.Reason
	}
	return ""
}

func TestScopeLockStampsLockPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "src/api", "web/src")
	s := dirlockService(t, root, []string{"exit", "0"}, nil)

	job := submitScoped(t, s, nil, "src/api/**", "web/src/*.vue")
	req := requestOfJob(job)
	if !reflect.DeepEqual(req.LockPaths, []string{"src/api", "web/src"}) || req.LockSource != "scope" {
		t.Fatalf("lock_paths=%v lock_source=%q, want [src/api web/src] / scope", req.LockPaths, req.LockSource)
	}
	if job.LockSource != "scope" {
		t.Fatalf("read view lock_source = %q, want scope", job.LockSource)
	}
	if r := lockFallbackReason(t, s, job.ID); r != "" {
		t.Fatalf("a derived lock must not record a fallback, got %q", r)
	}
}

func TestScopeLockRelativeToProjectRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "sub/x", "other", "sub/inner/a")
	// A nested repository inside the project: its scope is relative to ITS top.
	initGitRepo(t, filepath.Join(root, "sub", "inner"))
	s := dirlockService(t, root, []string{"exit", "0"}, func(cfg *config.Config) {
		p := cfg.Projects["self"]
		p.HostPath = filepath.Join(root, "sub") // project root is a subdirectory of the repo
		cfg.Projects["self"] = p
	})

	in := submitScoped(t, s, nil, "sub/x/**")
	if got := requestOfJob(in).LockPaths; !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("project below the repo top: lock_paths=%v, want [x]", got)
	}
	out := submitScoped(t, s, nil, "other/**")
	if got := requestOfJob(out).LockPaths; len(got) != 0 {
		t.Fatalf("a scope outside the project root must not derive, got %v", got)
	}
	if r := lockFallbackReason(t, s, out.ID); r != "outside_project" {
		t.Fatalf("fallback reason = %q, want outside_project", r)
	}
	nested := submitScoped(t, s, func(r *JobRequest) { r.Cwd = "inner" }, "a/**")
	if got := requestOfJob(nested).LockPaths; !reflect.DeepEqual(got, []string{"inner/a"}) {
		t.Fatalf("cwd in a nested repo: lock_paths=%v, want [inner/a]", got)
	}
}

func TestScopeLockFallbackEvents(t *testing.T) {
	t.Parallel()
	gitRoot := t.TempDir()
	scopeLockRepo(t, gitRoot)
	plainRoot := t.TempDir() // not a git checkout
	cases := []struct {
		name, reason string
		root         string
		scope        []string
	}{
		{"empty prefix", "empty_prefix", gitRoot, []string{"**/*.go"}},
		{"invalid glob", "invalid_glob", gitRoot, []string{"a/../b"}},
		{"not git", "not_git", plainRoot, []string{"src/**"}},
	}
	for _, c := range cases {
		s := dirlockService(t, c.root, []string{"exit", "0"}, nil)
		job := submitScoped(t, s, nil, c.scope...)
		if got := requestOfJob(job).LockPaths; len(got) != 0 {
			t.Errorf("%s: lock_paths=%v, want none (the lock stays on cwd)", c.name, got)
		}
		if r := lockFallbackReason(t, s, job.ID); r != c.reason {
			t.Errorf("%s: fallback reason = %q, want %q", c.name, r, c.reason)
		}
		if job.LockSource != "" {
			t.Errorf("%s: lock_source=%q, want empty", c.name, job.LockSource)
		}
	}
}

// D2: an explicit --lock wins; the scope is not unioned in.
func TestScopeLockExplicitLockWins(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "a", "src")
	s := dirlockService(t, root, []string{"exit", "0"}, nil)
	job := submitScoped(t, s, func(r *JobRequest) { r.LockPaths = []string{"a"} }, "src/**")
	req := requestOfJob(job)
	if !reflect.DeepEqual(req.LockPaths, []string{"a"}) || req.LockSource != "" {
		t.Fatalf("lock_paths=%v lock_source=%q, want [a] and no source", req.LockPaths, req.LockSource)
	}
	if r := lockFallbackReason(t, s, job.ID); r != "" {
		t.Fatalf("explicit lock: no fallback expected, got %q", r)
	}
}

// D3: --scope with --shared-dir is accepted, takes no lock and records no event.
func TestScopeLockSharedDirNoLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "src")
	s := dirlockService(t, root, []string{"exit", "0"}, nil)
	job := submitScoped(t, s, func(r *JobRequest) { r.ExclusiveDir = boolPtr(false) }, "src/**")
	if job.DirExclusive || len(requestOfJob(job).LockPaths) != 0 {
		t.Fatalf("dir_exclusive=%v lock_paths=%v, want shared and no lock", job.DirExclusive, requestOfJob(job).LockPaths)
	}
	if r := lockFallbackReason(t, s, job.ID); r != "" {
		t.Fatalf("shared job must not record a fallback, got %q", r)
	}
}

// D4: a managed worktree takes no directory lock, so nothing is derived.
func TestScopeLockWorktreeNoLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "src")
	s := dirlockService(t, root, []string{"exit", "0"}, nil)
	job := submitScoped(t, s, func(r *JobRequest) { r.Worktree = true }, "src/**")
	req := requestOfJob(job)
	if len(req.LockPaths) != 0 || req.LockSource != "" {
		t.Fatalf("worktree job: lock_paths=%v lock_source=%q, want none", req.LockPaths, req.LockSource)
	}
	if r := lockFallbackReason(t, s, job.ID); r != "" {
		t.Fatalf("worktree job must not record a fallback, got %q", r)
	}
}

// D1: read-only and exec jobs stay shared; the scope does not make them exclusive.
func TestScopeLockReadOnlyAndExecStayShared(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "src")
	s := dirlockService(t, root, []string{"exit", "0"}, nil)
	ro := submitScoped(t, s, func(r *JobRequest) { r.ReadOnly = true }, "src/**")
	ex := submitScoped(t, s, func(r *JobRequest) {
		r.Agent, r.Prompt, r.Cmd = "exec", "", []string{testcmd.Path(t), "exit", "0"}
	}, "src/**")
	for name, job := range map[string]JobResult{"read-only": ro, "exec": ex} {
		if job.DirExclusive || len(requestOfJob(job).LockPaths) != 0 || job.LockSource != "" {
			t.Errorf("%s: dir_exclusive=%v lock_paths=%v lock_source=%q, want shared and unlocked",
				name, job.DirExclusive, requestOfJob(job).LockPaths, job.LockSource)
		}
	}
}

// lock_source is a server stamp: whatever an outside caller sends is dropped.
func TestScopeLockCallerLockSourceCleared(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "a")
	s := dirlockService(t, root, []string{"exit", "0"}, nil)
	var req JobRequest
	body := `{"project_key":"self","agent":"agent","runner":"local","cwd":".","prompt":"p","lock_paths":["a"],"lock_source":"scope"}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	res := mustSubmit(t, s, req)
	final, _ := s.Wait(res.ID)
	if got := requestOfJob(final); got.LockSource != "" || !reflect.DeepEqual(got.LockPaths, []string{"a"}) {
		t.Fatalf("lock_source=%q lock_paths=%v, want the forged source cleared and the lock kept", got.LockSource, got.LockPaths)
	}
}

// 仓库模式：嵌套仓库下只带 --scope 就够——推出的锁视同已声明。
func TestRepoModeAcceptsScopeDerivedLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root)
	if err := os.MkdirAll(filepath.Join(root, "repo-a", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := dirlockService(t, root, []string{"exit", "0"}, func(cfg *config.Config) {
		p := cfg.Projects["self"]
		p.DirLockMode = "repo"
		cfg.Projects["self"] = p
	})
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", Prompt: "x"}); err == nil {
		t.Fatal("precondition: without --scope or --lock repo mode must refuse")
	}
	job := submitScoped(t, s, nil, "repo-a/**")
	if got := requestOfJob(job).LockPaths; !reflect.DeepEqual(got, []string{"repo-a"}) {
		t.Fatalf("lock_paths=%v, want [repo-a]", got)
	}
}

// 续接继承推出的锁与来源；`resume --lock` 换锁后来源清空。
func TestResumeInheritsScopeDerivedLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "src", "other")
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedAgents: []string{"agent", "exec"}, AllowedRunners: []string{"local"}, AllowExec: true,
		}},
		Agents: map[string]config.AgentConfig{"agent": {
			Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"exit", "0", "{{prompt}}"},
			SessionResume: []string{"printf", "resumed {{session_id}}: {{prompt}}"},
		}},
	}
	s := newServiceFromCfg(t, root, cfg)
	src := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".",
		Prompt: "source", SessionID: "sess-scope", Scope: []string{"src/**"}})
	if got := requestOfJob(src); got.LockSource != "scope" || !reflect.DeepEqual(got.LockPaths, []string{"src"}) {
		t.Fatalf("source: lock_paths=%v lock_source=%q", got.LockPaths, got.LockSource)
	}

	cont, err := s.ResumeJob(src.ID, "continue", "", "caller")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	if got := requestOfJob(cont); got.LockSource != "scope" || !reflect.DeepEqual(got.LockPaths, []string{"src"}) {
		t.Fatalf("continuation: lock_paths=%v lock_source=%q, want inherited [src] / scope", got.LockPaths, got.LockSource)
	}

	swapped, err := s.ResumeJobWith(src.ID, "again", "", "caller", ResumeOptions{LockPaths: []string{"other"}})
	if err != nil {
		t.Fatalf("ResumeJobWith --lock: %v", err)
	}
	if got := requestOfJob(swapped); got.LockSource != "" || !reflect.DeepEqual(got.LockPaths, []string{"other"}) {
		t.Fatalf("resume --lock: lock_paths=%v lock_source=%q, want [other] and no source", got.LockPaths, got.LockSource)
	}
}
