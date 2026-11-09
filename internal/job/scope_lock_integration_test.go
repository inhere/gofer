package job

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// gofer-341t 集成：真实 Service + 临时 git 仓库 + testcmd 假 agent，走提交到执行的全链路。
// 与 dirlock_test.go 同一套见证：rendezvous（两个 job 必须同时在跑才都成功）与
// excl-guard（重叠即 exit 3），所以结果由 job 自己作证而不靠时钟。

// scopeJob is a writable agent job that declared only a scope (no --lock).
func scopeJob(prompt string, scope ...string) JobRequest {
	return JobRequest{ProjectKey: "self", Agent: "agent", Runner: "local", Cwd: ".", Prompt: prompt, TimeoutSec: 60, Scope: scope}
}

// waitingDirDetail returns the holder and lock_paths of the job's job.waiting_dir event.
func waitingDirDetail(t *testing.T, s *Service, jobID string) (holder string, lockPaths []string, ok bool) {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type != EventJobWaitingDir {
			continue
		}
		var d struct {
			HolderJob string   `json:"holder_job"`
			LockPaths []string `json:"lock_paths"`
		}
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatalf("decode %s: %v", e.Detail, err)
		}
		return d.HolderJob, d.LockPaths, true
	}
	return "", nil, false
}

// 不相交的 scope：同一 checkout 里的两个可写 job 同时运行（rendezvous 证明重叠）。
func TestScopeLockIntegrationDisjointRunConcurrently(t *testing.T) {
	t.Parallel()
	root, rv := t.TempDir(), t.TempDir()
	scopeLockRepo(t, root, "src/api", "web/src")
	s := dirlockService(t, root, []string{"rendezvous", rv, "{{job_id}}", "20s", "2"}, nil)

	first := mustSubmit(t, s, scopeJob("a", "src/api/**"))
	waitForStatus(t, s, first.ID, StatusRunning, 10*time.Second)
	second := mustSubmit(t, s, scopeJob("b", "web/src/*.vue"))

	for _, j := range []JobResult{first, second} {
		final, _ := s.Wait(j.ID)
		if final.Status != StatusDone {
			t.Fatalf("job %s = %s (%s), want done: disjoint scopes must run at the same time", final.ID, final.Status, final.Error)
		}
		if _, _, waited := waitingDirDetail(t, s, j.ID); waited {
			t.Fatalf("job %s queued on the directory lock although its scope is disjoint", j.ID)
		}
	}
}

// 重叠的 scope 仍然排队：第三个 job（src/**）包含正在跑的 src/api，停在 waiting_dir。
func TestScopeLockIntegrationOverlapWaits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "src/api", "web/src")
	s := dirlockService(t, root, []string{"excl-guard", "marker", "5s"}, nil)

	holder := mustSubmit(t, s, scopeJob("a", "src/api/**"))
	waitForStatus(t, s, holder.ID, StatusRunning, 10*time.Second)
	waiter := mustSubmit(t, s, scopeJob("b", "src/**"))
	waiting := waitStatus(t, s, waiter.ID, 10*time.Second, StatusWaitingDir)
	if waiting.WaitingOnJob != holder.ID {
		t.Fatalf("waiting_on_job = %q, want %q", waiting.WaitingOnJob, holder.ID)
	}
	h, paths, ok := waitingDirDetail(t, s, waiter.ID)
	if !ok || h != holder.ID || !reflect.DeepEqual(paths, []string{filepath.Join(root, "src")}) {
		t.Fatalf("job.waiting_dir holder=%q lock_paths=%v found=%v, want %q and the resolved <root>/src", h, paths, ok, holder.ID)
	}
	for _, j := range []JobResult{holder, waiter} {
		if final, _ := s.Wait(j.ID); final.Status != StatusDone {
			t.Fatalf("job %s = %s (%s), want done (they must not overlap)", final.ID, final.Status, final.Error)
		}
	}
}

// 不带 scope 的提交行为不变：同 cwd 两个可写 job 仍串行。
func TestScopeLockIntegrationNoScopeUnchanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root)
	s := dirlockService(t, root, []string{"excl-guard", "marker", "1500ms"}, nil)

	first := mustSubmit(t, s, scopeJob("a"))
	waitForStatus(t, s, first.ID, StatusRunning, 10*time.Second)
	second := mustSubmit(t, s, scopeJob("b"))
	waitStatus(t, s, second.ID, 10*time.Second, StatusWaitingDir)
	for _, j := range []JobResult{first, second} {
		final, _ := s.Wait(j.ID)
		if final.Status != StatusDone || len(requestOfJob(final).LockPaths) != 0 || final.LockSource != "" {
			t.Fatalf("job %s = %s (%s) lock_paths=%v lock_source=%q, want done with the plain cwd lock",
				final.ID, final.Status, final.Error, requestOfJob(final).LockPaths, final.LockSource)
		}
	}
}

// 空前缀（**/*.go）退回锁 cwd：与另一个带 scope 的 job 冲突，并留下回退事件。
func TestScopeLockIntegrationEmptyPrefixFallsBack(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "src/api")
	s := dirlockService(t, root, []string{"excl-guard", "marker", "2s"}, nil)

	wide := mustSubmit(t, s, scopeJob("a", "**/*.go"))
	waitForStatus(t, s, wide.ID, StatusRunning, 10*time.Second)
	narrow := mustSubmit(t, s, scopeJob("b", "src/api/**"))
	waitStatus(t, s, narrow.ID, 10*time.Second, StatusWaitingDir)
	if holder, _, ok := waitingDirDetail(t, s, narrow.ID); !ok || holder != wide.ID {
		t.Fatalf("the narrow job must wait on the cwd-locked one: holder=%q found=%v", holder, ok)
	}
	if r := lockFallbackReason(t, s, wide.ID); r != "empty_prefix" {
		t.Fatalf("fallback reason = %q, want empty_prefix", r)
	}
	for _, j := range []JobResult{wide, narrow} {
		if final, _ := s.Wait(j.ID); final.Status != StatusDone {
			t.Fatalf("job %s = %s (%s), want done", final.ID, final.Status, final.Error)
		}
	}
}

// 续接 job 与源 job 同样的锁行为：继承 [src/api]，挡住重叠的 src/**，放过不相交的 web。
func TestScopeLockIntegrationResumeKeepsLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	scopeLockRepo(t, root, "src/api", "web/src")
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedAgents: []string{"agent", "exec"}, AllowedRunners: []string{"local"}, AllowExec: true,
		}},
		Agents: map[string]config.AgentConfig{"agent": {
			Type: agent.TypeCLIAgent, Command: bin, Args: []string{"exit", "0", "{{prompt}}"},
			// The resume argv is [Command] + this template, so only the subcommand goes here.
			SessionResume: []string{"excl-guard", "marker", "5s"},
		}},
	}
	s := newServiceFromCfg(t, root, cfg)
	srcReq := scopeJob("source", "src/api/**")
	srcReq.SessionID = "sess-int"
	src := submitAndWait(t, s, srcReq)

	cont, err := s.ResumeJob(src.ID, "continue", "", "caller")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	waitForStatus(t, s, cont.ID, StatusRunning, 10*time.Second)
	overlap := mustSubmit(t, s, scopeJob("overlap", "src/**"))
	waitStatus(t, s, overlap.ID, 10*time.Second, StatusWaitingDir)
	if holder, _, ok := waitingDirDetail(t, s, overlap.ID); !ok || holder != cont.ID {
		t.Fatalf("overlapping job must wait on the continuation: holder=%q found=%v", holder, ok)
	}
	disjoint := mustSubmit(t, s, scopeJob("disjoint", "web/src/*.vue"))
	if final, _ := s.Wait(disjoint.ID); final.Status != StatusDone {
		t.Fatalf("disjoint job = %s (%s), want done while the continuation holds src/api", final.Status, final.Error)
	}
	if r, _ := s.Get(cont.ID); r.Status != StatusRunning {
		t.Fatalf("continuation = %s (%s), want it still running (the disjoint job did not wait for it)", r.Status, r.Error)
	}
	for _, j := range []JobResult{cont, overlap} {
		if final, _ := s.Wait(j.ID); final.Status != StatusDone {
			t.Fatalf("job %s = %s (%s), want done", final.ID, final.Status, final.Error)
		}
	}
}
