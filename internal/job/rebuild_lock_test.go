package job

import (
	"strings"
	"testing"
)

// TestRebuildOverridesLockWorktreeAndWait: the gofer-jkd2 overrides reach the rebuilt
// request — a lock set replaces the inherited one (and asks for the lock), a wait cap
// replaces the pair submit stamped (the legacy dir_wait_max_sec must go, or submit
// refuses the mismatch), and worktree is a plain boolean.
func TestRebuildOverridesLockWorktreeAndWait(t *testing.T) {
	t.Parallel()
	base := JobRequest{
		ProjectKey: "self", Agent: "exec", Cwd: ".",
		LockPaths:     []string{"old"},
		LockWaitSec:   intPtr(30),
		DirWaitMaxSec: intPtr(30),
	}
	shared := false
	base.ExclusiveDir = &shared

	locks := []string{"gookit/ini", "gookit/cli"}
	wait := 5
	worktree := true
	applyOverrides(&base, RebuildOverrides{LockPaths: &locks, LockWaitSec: &wait, Worktree: &worktree})

	if got := strings.Join(base.LockPaths, ","); got != "gookit/ini,gookit/cli" {
		t.Fatalf("lock_paths = %q, want the override", got)
	}
	if base.ExclusiveDir == nil || !*base.ExclusiveDir {
		t.Fatalf("a declared lock must ask for the lock: %v", base.ExclusiveDir)
	}
	if base.LockWaitSec == nil || *base.LockWaitSec != 5 {
		t.Fatalf("lock_wait_sec = %v, want 5", base.LockWaitSec)
	}
	if base.DirWaitMaxSec != nil {
		t.Fatalf("the legacy dir_wait_max_sec copy must be dropped, got %v", *base.DirWaitMaxSec)
	}
	if !base.Worktree {
		t.Fatal("worktree override did not reach the request")
	}

	// An explicit empty list clears the declaration-but keeps the request valid: the job
	// then locks its whole cwd, the submit default.
	empty := []string{}
	applyOverrides(&base, RebuildOverrides{LockPaths: &empty})
	if len(base.LockPaths) != 0 {
		t.Fatalf("lock_paths = %#v, want cleared", base.LockPaths)
	}
}

// TestRebuildJobLockPathsOverrideReachSubmit drives the override through the real
// rebuild + submit path (gofer-jkd2): the rebuilt row carries the new lock set and the
// resolved exclusive decision.
func TestRebuildJobLockPathsOverrideReachSubmit(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})

	locks := []string{"sub"}
	rebuilt, err := s.RebuildJob(src.ID, RebuildOverrides{LockPaths: &locks}, "caller-new", "127.0.0.1")
	if err != nil {
		t.Fatalf("RebuildJob lock override: %v", err)
	}
	req := requestFromJSON(t, rebuilt.RequestJSON)
	if got := strings.Join(req.LockPaths, ","); got != "sub" {
		t.Fatalf("rebuilt lock_paths = %q, want sub", got)
	}
	if req.ExclusiveDir == nil || !*req.ExclusiveDir {
		t.Fatalf("rebuilt exclusive_dir = %v, want an explicit true", req.ExclusiveDir)
	}
	drainJobs(t, s)
}
