package job

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/proctree"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// ACP-02 真机验收缺陷 F12（2026-09-24）：ACP job 的取消与超时都不生效——只杀直接子进程，
// 孙进程还握着 agent 的 stdio 管道，于是 Close 里的 cmd.Wait 永不到头：job 一直 running、
// 一直持着目录锁，直到人工杀掉整棵进程树才立刻转为终态。
//
// 这两个用例用假 ACP agent 复现那个形状：agent 启动时拉一个**孙进程**（继承它的
// stdout/stderr），然后对 session/prompt 永不应答。断言是两条：job 在 5s 内到终态
// （cancelled / timeout），且孙进程已不存在。

// waitChildPID 等假 agent 公布孙进程的 pid（路径由测试给，绝对路径）。agent 是个新进程，
// 写 pid 前还要再拉一个进程：Windows 全量负载下要好几秒，所以等待按负载伸缩；超时时报出
// job 的状态与 agent 的 stderr，区分「agent 已死 / 没起来」与「只是慢」。
func waitChildPID(t *testing.T, s *Service, jobID, path string) int {
	t.Helper()
	pid := 0
	wait.For(t, 20*time.Second, "the fake agent publishing its child pid in "+path, func() (bool, any) {
		if b, err := os.ReadFile(path); err == nil {
			if n, cerr := strconv.Atoi(strings.TrimSpace(string(b))); cerr == nil && n > 0 {
				pid = n
				return true, nil
			}
		}
		return false, agentJobState(s, jobID)
	})
	return pid
}

// agentJobState is what a pid wait reports when it gives up: the job's status and
// error and the agent's stderr (the fake agent logs a failed grandchild spawn there).
func agentJobState(s *Service, jobID string) string {
	jr, ok := s.Get(jobID)
	if !ok {
		return "job " + jobID + " not found"
	}
	state := fmt.Sprintf("job %s status=%s exit=%v error=%q", jobID, jr.Status, jr.ExitCode, jr.Error)
	if jr.ResultDir != "" {
		if b, err := os.ReadFile(filepath.Join(jr.ResultDir, store.StderrFile)); err == nil {
			state += fmt.Sprintf(" stderr=%q", b)
		}
	}
	return state
}

// assertChildGone 等孙进程消失；超时即报出 pid（挂在树上的进程正是 F12 的病灶）。
func assertChildGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !proctree.Alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d (the acp agent's child) is still alive after the job reached its terminal state", pid)
}

// hangingACPService 装配一个 acp job 服务：假 agent 自带孙进程，且卡在 session/prompt。
func hangingACPService(t *testing.T, root, pidFile string) *Service {
	t.Helper()
	return newACPService(t, root, acptest.Options{
		Hang:              true,
		GrandchildPidFile: pidFile,
		GrandchildHold:    time.Minute,
	})
}

// TestACPCancelKillsProcessTree: 取消必须真的把整棵进程树杀掉——agent 挂着不应答，job 仍要
// 在 5s 内成为 cancelled，且它拉起来的孙进程已经不存在（只杀直接子进程会留下它握着管道，
// 于是 Wait 永不返回、job 永远 running）。
func TestACPCancelKillsProcessTree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "acp-child.pid")
	s := hangingACPService(t, root, pidFile)

	res := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 60,
	})
	pid := waitChildPID(t, s, res.ID, pidFile)
	waitForStatus(t, s, res.ID, StatusRunning, 10*time.Second)

	if err := s.Cancel(res.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	final := waitStatus(t, s, res.ID, 5*time.Second,
		StatusCancelled, StatusFailed, StatusTimeout, StatusDone)
	if final.Status != StatusCancelled {
		t.Fatalf("status = %s (err=%s), want cancelled within 5s of the cancel", final.Status, final.Error)
	}
	assertChildGone(t, pid)
}

// TestACPTimeoutKillsProcessTree: 同一个形状下超时也要真的结束 job——2s 的 deadline 过后
// 必须到终态 timeout，孙进程同样不许留下。
func TestACPTimeoutKillsProcessTree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "acp-child.pid")
	s := hangingACPService(t, root, pidFile)

	res := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 2,
	})
	pid := waitChildPID(t, s, res.ID, pidFile)

	// The window is measured from SUBMIT and must cover the 2s deadline plus the
	// cancel/close graces (cancelGrace + waitDelay): the point is that the job ends
	// shortly after its deadline instead of hanging forever.
	final := waitStatus(t, s, res.ID, 8*time.Second,
		StatusTimeout, StatusCancelled, StatusFailed, StatusDone)
	if final.Status != StatusTimeout {
		t.Fatalf("status = %s (err=%s), want timeout shortly after the 2s deadline", final.Status, final.Error)
	}
	assertChildGone(t, pid)
}
