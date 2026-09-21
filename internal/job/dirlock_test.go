package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// JOB-11 同 cwd 串行锁 + per-agent 并发的测试。
//
// 断言不靠时钟：两个 job 是否**重叠**由 job 自己作证——testcmd 的 `excl-guard`
// （独占拿住一个 marker 文件，发现已被占就 exit 3）在重叠时**失败**，`rendezvous`
// （等同伴落下第二个 marker，超时 exit 4）在不重叠时**失败**。于是"两个都 done"
// / "两个都 failed" 就是对"它们到底有没有同时跑"的确定性回答，与机器快慢无关。

// dirlockService 建一个跑锁测试的 Service：项目 "self" 落在 root；"agent" 是一个
// cli-agent（驱动 testcmd，即"可写 agent job"），外加内置 exec。mutate 在装配前
// 调整配置（dir_lock / max_concurrent / …）。
func dirlockService(t *testing.T, root string, agentArgs []string, mutate func(*config.Config)) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"agent", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"agent": {
				Type:    agent.TypeCLIAgent,
				Command: testcmd.Path(t),
				Args:    agentArgs,
				// 只读 job 的沙箱参数：测试 agent 不在内置表里，不显式给出就无法跑只读
				// （bd h-aii-0ql3 的准入会拒绝）。
				ReadOnlyArgs: []string{"--read-only"},
			},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return newServiceFromCfg(t, root, cfg)
}

// mustSubmit submits a job and fails the test on a rejection (a lock test's setup is
// never expected to be refused).
func mustSubmit(t *testing.T, s *Service, req JobRequest) JobResult {
	t.Helper()
	res, err := s.Submit(req)
	if err != nil {
		t.Fatalf("Submit(%s/%s cwd=%q): %v", req.Agent, req.Runner, req.Cwd, err)
	}
	return res
}

// waitingDirEvent returns the holder named by a job's job.waiting_dir event, and
// whether the job recorded one at all.
func waitingDirEvent(t *testing.T, s *Service, jobID string) (string, bool) {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", jobID, err)
	}
	for _, e := range evs {
		if e.Type != EventJobWaitingDir {
			continue
		}
		var d struct {
			HolderJob string `json:"holder_job"`
			Dir       string `json:"dir"`
		}
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatalf("decode %s detail %q: %v", e.Type, e.Detail, err)
		}
		if d.Dir == "" {
			t.Fatalf("%s detail must name the contended directory: %q", e.Type, e.Detail)
		}
		return d.HolderJob, true
	}
	return "", false
}

// TestDirLockSerializesWritableAgentJobs: 两个可写 agent job 落同一个 cwd 时不得重叠
// ——第二个停在 waiting_dir（事件里点名持有者），等第一个结束才跑。两个 job 前后独占
// 同一个 marker，所以"真的重叠了"会表现为某个 job 失败，而不是一句慢一点的断言。
func TestDirLockSerializesWritableAgentJobs(t *testing.T) {
	root := t.TempDir()
	s := dirlockService(t, root, []string{"excl-guard", "marker", "1500ms"}, nil)

	first := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "first", Cwd: ".", TimeoutSec: 60,
	})
	// 锁在状态翻 running **之前**拿到，所以 running 的第一个 job 可证明握着锁。
	waitForStatus(t, s, first.ID, StatusRunning, 10*time.Second)

	second := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "second", Cwd: ".", TimeoutSec: 60,
	})
	waiting := waitStatus(t, s, second.ID, 10*time.Second, StatusWaitingDir)
	if !waiting.DirExclusive {
		t.Fatal("a writable agent job must be exclusive (dir_exclusive=false on the row)")
	}
	if waiting.WaitingOnJob != first.ID {
		t.Fatalf("waiting_on_job = %q, want the holder %q", waiting.WaitingOnJob, first.ID)
	}
	if holder, ok := waitingDirEvent(t, s, second.ID); !ok || holder != first.ID {
		t.Fatalf("job.waiting_dir holder = %q (found=%v), want %q", holder, ok, first.ID)
	}
	if _, ok := waitingDirEvent(t, s, first.ID); ok {
		t.Fatal("the first job took the lock immediately and must not wait")
	}

	f1, _ := s.Wait(first.ID)
	f2, _ := s.Wait(second.ID)
	if f1.Status != StatusDone || f2.Status != StatusDone {
		t.Fatalf("both jobs must run without overlapping (excl-guard exits 3 on an overlap): first=%s (%s) second=%s (%s)",
			f1.Status, f1.Error, f2.Status, f2.Error)
	}
	if !f1.DirExclusive || !f2.DirExclusive {
		t.Fatalf("dir_exclusive must be persisted on both rows: first=%v second=%v", f1.DirExclusive, f2.DirExclusive)
	}
	if f2.WaitingOnJob != "" {
		t.Fatalf("waiting_on_job is transient and must be cleared once the lock was taken, got %q", f2.WaitingOnJob)
	}
	// waiting_dir is a REAL state the job passed through, in order, on its way to running.
	if types := eventTypes(t, s, second.ID); !hasSubsequence(types, []string{EventJobWaitingDir, EventJobRunning}) {
		t.Fatalf("second job events = %v, want job.waiting_dir before job.running", types)
	}
}

// TestDirLockAncestorDescendantConflict: 祖先/后代目录是同一把锁——项目根（`.`）上的
// job 与它子目录（`sub`）上的 job 必须互斥。marker 用**绝对路径**，这样两个 cwd 下它是
// 同一个文件，见证才跨得过去；没有祖先规则时两个 job 会同时跑，其中一个必失败。
func TestDirLockAncestorDescendantConflict(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "conflict.marker")
	s := dirlockService(t, root, []string{"excl-guard", marker, "1500ms"}, nil)

	parent := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "parent", Cwd: ".", TimeoutSec: 60,
	})
	waitForStatus(t, s, parent.ID, StatusRunning, 10*time.Second)

	child := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "child", Cwd: "sub", TimeoutSec: 60,
	})
	waitStatus(t, s, child.ID, 10*time.Second, StatusWaitingDir)

	f1, _ := s.Wait(parent.ID)
	f2, _ := s.Wait(child.ID)
	if f1.Status != StatusDone || f2.Status != StatusDone {
		t.Fatalf("a job in a subdirectory must wait for one at its ancestor: parent=%s (%s) child=%s (%s)",
			f1.Status, f1.Error, f2.Status, f2.Error)
	}
}

// TestExecAndReadOnlyJobsShareDir: exec job 与只读 agent job 默认共享目录——它们既不取
// 锁也不被锁挡，所以可以和一个（握着锁的）可写 agent job 同时跑在同一棵树上。
func TestExecAndReadOnlyJobsShareDir(t *testing.T) {
	root := t.TempDir()
	rv := t.TempDir() // 三个 job 共同的会合目录（绝对路径：cwds 都在项目里）
	bin := testcmd.Path(t)
	s := dirlockService(t, root, []string{"rendezvous", rv, "{{job_id}}", "5s", "3"}, nil)

	write := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "writer", Cwd: ".", TimeoutSec: 60,
	})
	waitForStatus(t, s, write.ID, StatusRunning, 10*time.Second)
	execJob := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{bin, "rendezvous", rv, "exec", "5s", "3"},
		Cwd: ".", TimeoutSec: 60,
	})
	readOnly := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "reader", Cwd: ".", TimeoutSec: 60, ReadOnly: true,
	})

	for _, j := range []JobResult{write, execJob, readOnly} {
		final, _ := s.Wait(j.ID)
		if final.Status != StatusDone {
			t.Fatalf("job %s (%s) = %s (%s), want done — all three must overlap (rendezvous exits 4 otherwise)",
				final.ID, final.Agent, final.Status, final.Error)
		}
		if _, ok := waitingDirEvent(t, s, final.ID); ok {
			t.Fatalf("job %s (%s) waited for the directory lock, want none", final.ID, final.Agent)
		}
	}
	// The policy on the rows: only the writable agent job is exclusive.
	if !write.DirExclusive {
		t.Fatal("the writable agent job must be exclusive")
	}
	if execJob.DirExclusive {
		t.Fatal("an exec job is shared by default (dir_exclusive=true)")
	}
	if readOnly.DirExclusive {
		t.Fatal("a read-only agent job is shared (dir_exclusive=true)")
	}
}

// TestExclusiveDirFlagForcesLockOnExec: `--exclusive-dir` 把默认共享的 exec job 变成
// 独占——第二个必须等第一个跑完（否则 excl-guard 会有一个 exit 3）。
func TestExclusiveDirFlagForcesLockOnExec(t *testing.T) {
	root := t.TempDir()
	bin := testcmd.Path(t)
	s := dirlockService(t, root, nil, nil)

	execJob := func() JobRequest {
		return JobRequest{
			ProjectKey: "self", Agent: "exec", Runner: "local",
			Cmd: []string{bin, "excl-guard", "marker", "1500ms"},
			Cwd: ".", TimeoutSec: 60, ExclusiveDir: boolPtr(true),
		}
	}
	first := mustSubmit(t, s, execJob())
	waitForStatus(t, s, first.ID, StatusRunning, 10*time.Second)
	second := mustSubmit(t, s, execJob())
	waitStatus(t, s, second.ID, 10*time.Second, StatusWaitingDir)

	f1, _ := s.Wait(first.ID)
	f2, _ := s.Wait(second.ID)
	if f1.Status != StatusDone || f2.Status != StatusDone {
		t.Fatalf("--exclusive-dir exec jobs must serialize: first=%s (%s) second=%s (%s)", f1.Status, f1.Error, f2.Status, f2.Error)
	}
	if !f1.DirExclusive || !f2.DirExclusive {
		t.Fatalf("--exclusive-dir must persist dir_exclusive=true: first=%v second=%v", f1.DirExclusive, f2.DirExclusive)
	}
}

// TestSharedDirFlagBypassesLock: `--shared-dir` 让可写 agent job 放弃锁（自担风险），
// 于是同 cwd 的两个 agent job 真并发（rendezvous 证明重叠）。
func TestSharedDirFlagBypassesLock(t *testing.T) {
	root := t.TempDir()
	rv := t.TempDir()
	s := dirlockService(t, root, []string{"rendezvous", rv, "{{job_id}}", "5s", "2"}, nil)

	shared := func(prompt string) JobRequest {
		return JobRequest{
			ProjectKey: "self", Agent: "agent", Runner: "local",
			Prompt: prompt, Cwd: ".", TimeoutSec: 60, ExclusiveDir: boolPtr(false),
		}
	}
	first := mustSubmit(t, s, shared("a"))
	waitForStatus(t, s, first.ID, StatusRunning, 10*time.Second)
	second := mustSubmit(t, s, shared("b"))

	for _, j := range []JobResult{first, second} {
		final, _ := s.Wait(j.ID)
		if final.Status != StatusDone {
			t.Fatalf("--shared-dir job %s = %s (%s), want done (they must overlap)", final.ID, final.Status, final.Error)
		}
		if final.DirExclusive {
			t.Fatalf("--shared-dir must persist dir_exclusive=false, job %s has true", final.ID)
		}
		if _, ok := waitingDirEvent(t, s, final.ID); ok {
			t.Fatalf("--shared-dir job %s waited for the directory lock, want none", final.ID)
		}
	}
}

// TestWorktreeJobsDoNotConflict: 受管 worktree 的 job 各有自己的目录，因此既互不冲突，
// 也不被主 checkout（项目根 cwd）上的 job 挡住——正是 WT-01「并行编辑同一 checkout」的前提。
func TestWorktreeJobsDoNotConflict(t *testing.T) {
	repo, _ := gitRepo(t)
	state := t.TempDir()
	rv := t.TempDir()
	s := worktreeLockService(t, repo, state, []string{"rendezvous", rv, "{{job_id}}", "5s", "3"})

	main := mustSubmit(t, s, JobRequest{
		ProjectKey: "repo", Agent: "agent", Runner: "local",
		Prompt: "main", Cwd: ".", TimeoutSec: 90,
	})
	waitForStatus(t, s, main.ID, StatusRunning, 15*time.Second)
	wt1 := mustSubmit(t, s, JobRequest{
		ProjectKey: "repo", Agent: "agent", Runner: "local",
		Prompt: "wt1", Cwd: ".", TimeoutSec: 90, Worktree: true,
	})
	wt2 := mustSubmit(t, s, JobRequest{
		ProjectKey: "repo", Agent: "agent", Runner: "local",
		Prompt: "wt2", Cwd: ".", TimeoutSec: 90, Worktree: true,
	})

	finals := make([]JobResult, 0, 3)
	for _, j := range []JobResult{main, wt1, wt2} {
		final, _ := s.Wait(j.ID)
		if final.Status != StatusDone {
			t.Fatalf("job %s (%s) = %s (%s), want done — worktree dirs are independent", final.ID, final.Agent, final.Status, final.Error)
		}
		if _, ok := waitingDirEvent(t, s, final.ID); ok {
			t.Fatalf("job %s waited for the directory lock although its directory is its own", final.ID)
		}
		finals = append(finals, final)
	}
	for _, wt := range finals[1:] {
		if wt.WorktreePath == "" {
			t.Fatalf("worktree job %s has no managed worktree path", wt.ID)
		}
	}
}

// worktreeLockService 建一个能跑 --worktree 的 Service：项目 "repo" 指向一个临时 git
// 仓库，agent "agent" 驱动 testcmd。
func worktreeLockService(t *testing.T, repo, state string, agentArgs []string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: state},
		Projects: map[string]config.ProjectConfig{
			"repo": {
				HostPath:       repo,
				AllowedAgents:  []string{"agent", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"agent": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: agentArgs},
		},
	}
	return newServiceFromCfg(t, state, cfg)
}

// TestCancelWhileWaitingDir: 停在 waiting_dir 的 job 走既有取消路径——它是 `cancelled`，
// 不会因为按目录排队就变顽固；持有锁的那个 job 照常跑完。
func TestCancelWhileWaitingDir(t *testing.T) {
	root := t.TempDir()
	s := dirlockService(t, root, []string{"excl-guard", "marker", "3s"}, nil)

	holder := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "holder", Cwd: ".", TimeoutSec: 60,
	})
	waitForStatus(t, s, holder.ID, StatusRunning, 10*time.Second)

	waiter := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "waiter", Cwd: ".", TimeoutSec: 60,
	})
	waitStatus(t, s, waiter.ID, 10*time.Second, StatusWaitingDir)

	if err := s.Cancel(waiter.ID); err != nil {
		t.Fatalf("Cancel while waiting_dir: %v", err)
	}
	final := waitStatus(t, s, waiter.ID, 10*time.Second, StatusCancelled)
	if final.Status != StatusCancelled {
		t.Fatalf("cancelled waiter status = %s, want %s", final.Status, StatusCancelled)
	}

	held, _ := s.Wait(holder.ID)
	if held.Status != StatusDone {
		t.Fatalf("the lock holder = %s (%s), want done (a waiter's cancel must not disturb it)", held.Status, held.Error)
	}
}

// TestDirLockDisabledByConfig: `server.dir_lock: false` 关掉整套目录锁——同 cwd 的两个
// 可写 agent job 直接并发（rendezvous 证明），行上 dir_exclusive 为 false。
func TestDirLockDisabledByConfig(t *testing.T) {
	root := t.TempDir()
	rv := t.TempDir()
	s := dirlockService(t, root, []string{"rendezvous", rv, "{{job_id}}", "5s", "2"}, func(cfg *config.Config) {
		cfg.Server.DirLock = boolPtr(false)
	})

	first := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "a", Cwd: ".", TimeoutSec: 60,
	})
	waitForStatus(t, s, first.ID, StatusRunning, 10*time.Second)
	second := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "b", Cwd: ".", TimeoutSec: 60,
	})

	for _, j := range []JobResult{first, second} {
		final, _ := s.Wait(j.ID)
		if final.Status != StatusDone {
			t.Fatalf("job %s = %s (%s), want done (dir_lock is off, they must overlap)", final.ID, final.Status, final.Error)
		}
		if final.DirExclusive {
			t.Fatalf("dir_lock disabled must persist dir_exclusive=false, job %s has true", final.ID)
		}
	}
}

// TestAgentMaxConcurrentQueues: `agents.<k>.max_concurrent` 用与项目/caller 相同的信号量
// 机制给单个 agent 限流——超出的 job **排队**（状态仍是 queued，不是 waiting_dir），
// 所以这两个 job 会被串行执行（excl-guard 会因重叠而 exit 3）。
func TestAgentMaxConcurrentQueues(t *testing.T) {
	root := t.TempDir()
	s := dirlockService(t, root, []string{"excl-guard", "marker", "1200ms"}, func(cfg *config.Config) {
		a := cfg.Agents["agent"]
		a.MaxConcurrent = 1
		cfg.Agents["agent"] = a
	})

	// --shared-dir 把目录锁排除在外：串行只可能来自 agent 信号量。
	shared := func(prompt string) JobRequest {
		return JobRequest{
			ProjectKey: "self", Agent: "agent", Runner: "local",
			Prompt: prompt, Cwd: ".", TimeoutSec: 60, ExclusiveDir: boolPtr(false),
		}
	}
	first := mustSubmit(t, s, shared("a"))
	waitForStatus(t, s, first.ID, StatusRunning, 10*time.Second)
	second := mustSubmit(t, s, shared("b"))

	// While the first job holds the agent slot, the second is QUEUED — queuing is the
	// documented semantics of a saturated concurrency gate (not a rejection, and not a
	// directory wait).
	for {
		f1, _ := s.Get(first.ID)
		if isTerminal(f1.Status) {
			break
		}
		f2, _ := s.Get(second.ID)
		if f2.Status != StatusQueued {
			t.Fatalf("second job status = %q while the agent slot is taken, want %q", f2.Status, StatusQueued)
		}
		time.Sleep(20 * time.Millisecond)
	}

	for _, j := range []JobResult{first, second} {
		final, _ := s.Wait(j.ID)
		if final.Status != StatusDone {
			t.Fatalf("job %s = %s (%s), want done (max_concurrent=1 must serialize them)", final.ID, final.Status, final.Error)
		}
		if _, ok := waitingDirEvent(t, s, final.ID); ok {
			t.Fatalf("job %s waited on the directory lock; the agent semaphore must be what serialized it", final.ID)
		}
	}
}
