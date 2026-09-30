package job

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// JOB-11 + F12（2026-09-24 真机）：排队等目录锁的时间**不算**进 job 的执行超时。执行超时从
// 拿到锁、状态翻 running 那一刻才开始计；等锁期间只受 server.dir_lock_max_wait_sec 约束
// （默认 3600s，0 = 不限）。取消在等锁期间依旧立即生效（见 TestCancelWhileWaitingDir）。

// dirWaitTimeoutEvent 取 job.dir_wait_timeout 事件里点名的持有者。
func dirWaitTimeoutEvent(t *testing.T, s *Service, jobID string) (string, bool) {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", jobID, err)
	}
	for _, e := range evs {
		if e.Type != EventJobDirWaitTimeout {
			continue
		}
		var d struct {
			HolderJob string `json:"holder_job"`
			Dir       string `json:"dir"`
		}
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatalf("decode %s detail %q: %v", e.Type, e.Detail, err)
		}
		return d.HolderJob, true
	}
	return "", false
}

// exclusiveCmd 是"我等到了锁、现在跑我自己的活"的 exec job：独占目录 + 自己的 deadline。
func exclusiveCmd(t *testing.T, timeoutSec int) JobRequest {
	t.Helper()
	return JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "sleep", "1s"},
		Cwd: ".", TimeoutSec: timeoutSec, ExclusiveDir: boolPtr(true),
	}
}

// TestDirLockWaitDoesNotConsumeTimeout: job A 持锁 4s；job B（timeout=2）排队等锁，拿到锁
// 后必须仍能完整跑完自己的 1s 预算 → B 是 done，不是 timeout（旧行为：等锁的 2s 就把它判死）。
func TestDirLockWaitDoesNotConsumeTimeout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := dirlockService(t, root, []string{"excl-guard", "marker", "4s"}, nil)

	holder := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "holder", Cwd: ".", TimeoutSec: 60,
	})
	waitForStatus(t, s, holder.ID, StatusRunning, 10*time.Second)

	waiter := mustSubmit(t, s, exclusiveCmd(t, 2))
	final := waitStatus(t, s, waiter.ID, 30*time.Second,
		StatusDone, StatusTimeout, StatusFailed, StatusCancelled)
	if final.Status != StatusDone {
		t.Fatalf("waiter status = %s (err=%s), want done: the wait for the lock must not consume its own 2s budget",
			final.Status, final.Error)
	}
	if _, ok := waitingDirEvent(t, s, waiter.ID); !ok {
		t.Fatal("the waiter never recorded job.waiting_dir — it never queued, so this test proves nothing")
	}
	if held, _ := s.Wait(holder.ID); held.Status != StatusDone {
		t.Fatalf("the holder = %s (%s), want done", held.Status, held.Error)
	}
}

// TestDirLockWaitHasItsOwnCap: server.dir_lock_max_wait_sec 是等锁的独立上限——holder 还要
// 3s 才放手、上限 1s，B 等超 1s 即终态 failed，error 说明是等锁超限，并且事件
// job.dir_wait_timeout 点名持有者（谁把它堵住的）。
func TestDirLockWaitHasItsOwnCap(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := dirlockService(t, root, []string{"excl-guard", "marker", "3s"}, func(cfg *config.Config) {
		cfg.Server.DirLockMaxWaitSec = intPtr(1)
	})

	holder := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "agent", Runner: "local",
		Prompt: "holder", Cwd: ".", TimeoutSec: 60,
	})
	waitForStatus(t, s, holder.ID, StatusRunning, 10*time.Second)

	waiter := mustSubmit(t, s, exclusiveCmd(t, 30))
	final := waitStatus(t, s, waiter.ID, 10*time.Second,
		StatusFailed, StatusDone, StatusTimeout, StatusCancelled)
	if final.Status != StatusFailed {
		t.Fatalf("waiter status = %s (err=%s), want failed: the lock wait exceeded its 1s cap", final.Status, final.Error)
	}
	if !strings.Contains(final.Error, "dir lock wait exceeded") {
		t.Fatalf("waiter error = %q, want it to name the exceeded directory-lock wait", final.Error)
	}
	if got, ok := dirWaitTimeoutEvent(t, s, waiter.ID); !ok || got != holder.ID {
		t.Fatalf("job.dir_wait_timeout holder = %q (found=%v), want %q", got, ok, holder.ID)
	}
	if held, _ := s.Wait(holder.ID); held.Status != StatusDone {
		t.Fatalf("the holder = %s (%s), want done (a waiter's cap must not disturb it)", held.Status, held.Error)
	}
}
