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
)

// ACP-02 真机验收缺陷 F12 的同源审计：local runner 有 WaitDelay（Wait 不会永挂），但
// exec.CommandContext 只杀直接子进程，孙进程会活下来——一个被取消/超时的 job 会留下孤儿进程。
// 用例：命令拉一个继承 stdio 的孙进程，取消 ctx 后 Run 立即返回，且孙进程已被一起杀掉。

// waitChildPID 等孙进程公布自己的 pid。
func waitChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, cerr := strconv.Atoi(strings.TrimSpace(string(b))); cerr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the command never published its child pid in %s", path)
	return 0
}

// TestLocalCancelKillsProcessTree: ctx 取消 → Run 返回 ctx 错误，且命令拉起来的孙进程
// （继承 stdio、拿着管道）已不存在。
func TestLocalCancelKillsProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "local-child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		res runner.Result
	}
	done := make(chan result, 1)
	go func() {
		done <- result{New().Run(ctx, runner.Request{
			Command: testcmd.Path(t),
			Args:    []string{"spawn-child", pidFile, "1m"},
			WorkDir: t.TempDir(),
			// Non-*os.File writers: os/exec copies through a pipe, which is exactly the
			// shape an orphaned descendant can wedge (see stdioWaitDelay).
			Stdout: io.Discard,
			Stderr: io.Discard,
		})}
	}()

	pid := waitChildPID(t, pidFile)
	cancel()

	select {
	case got := <-done:
		if !errors.Is(got.res.Err, context.Canceled) {
			t.Fatalf("Run err = %v (code=%d), want context.Canceled", got.res.Err, got.res.ExitCode)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after the ctx was cancelled")
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !proctree.Alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d (the command's child) survived the cancelled job", pid)
}
