package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/proctree"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// ACP-02 真机验收缺陷 F12 的同源审计：local runner 有 WaitDelay（Wait 不会永挂），但
// exec.CommandContext 只杀直接子进程，孙进程会活下来——一个被取消/超时的 job 会留下孤儿进程。
// 用例：命令拉一个继承 stdio 的孙进程，取消 ctx 后 Run 立即返回，且孙进程已被一起杀掉。

// waitChildPID 等孙进程公布自己的 pid。Run 若先返回（命令起不来、提前退出），直接带着它的
// 错误失败，而不是空等到超时。
func waitChildPID(t *testing.T, path string, done <-chan runner.Result) int {
	t.Helper()
	var pid int
	var early *runner.Result
	wait.For(t, 30*time.Second, "the command publishes its child pid in "+path, func() (bool, any) {
		select {
		case res := <-done:
			early = &res
			return true, nil
		default:
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0, string(b)
	})
	if early != nil {
		t.Fatalf("Run returned before the child published its pid: err=%v code=%d", early.Err, early.ExitCode)
	}
	return pid
}

// TestLocalCancelKillsProcessTree: ctx 取消 → Run 返回 ctx 错误，且命令拉起来的孙进程
// （继承 stdio、拿着管道）已不存在。
func TestLocalCancelKillsProcessTree(t *testing.T) {
	// 先取助手程序：首次调用可能要做新鲜度检查、等构建锁甚至 go build（负载下可超 30s），
	// 不能算进下面等孙进程的时间；t.Fatalf 也只能在测试 goroutine 里调。
	bin := testcmd.Path(t)
	pidFile := filepath.Join(t.TempDir(), "local-child.pid")
	workDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan runner.Result, 1)
	go func() {
		done <- New().Run(ctx, runner.Request{
			Command: bin,
			Args:    []string{"spawn-child", pidFile, "1m"},
			WorkDir: workDir,
			// Non-*os.File writers: os/exec copies through a pipe, which is exactly the
			// shape an orphaned descendant can wedge (see stdioWaitDelay).
			Stdout: io.Discard,
			Stderr: io.Discard,
		})
	}()

	pid := waitChildPID(t, pidFile, done)
	cancel()

	select {
	case res := <-done:
		if !errors.Is(res.Err, context.Canceled) {
			t.Fatalf("Run err = %v (code=%d), want context.Canceled", res.Err, res.ExitCode)
		}
	case <-time.After(wait.Timeout(t, 30*time.Second)):
		t.Fatal("Run did not return after the ctx was cancelled")
	}

	wait.For(t, 30*time.Second, "the command's child is killed with the cancelled job", func() (bool, any) {
		return !proctree.Alive(pid), pid
	})
}
