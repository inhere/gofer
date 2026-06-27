package commands

import (
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/daemon"
)

// stopExitErr is the process exit code when a stop fails (signal failed /
// timeout). Mirrors serve/worker exit codes.
const stopExitErr = 2

// stopPoll / stopWaitTimeout bound how a stop waits for the target to exit after
// SIGTERM before reporting it may need a harder kill. The timeout is a touch
// above the server's shutdownGrace so a clean drain is not cut short.
const (
	stopPoll        = 200 * time.Millisecond
	stopWaitTimeout = 12 * time.Second
)

// stopDaemon reads the daemon pidfile at pidPath, sends SIGTERM and waits for a
// graceful exit (c44). It backs `serve stop` / `worker stop`. Stopping something
// that is not running is not an error (idempotent): it reports so and clears any
// stale pidfile. label is the human-facing target name (e.g. "serve",
// "worker-<id>").
func stopDaemon(c *gcli.Command, pidPath, label string) error {
	pid, err := daemon.ReadPIDFile(pidPath)
	if err != nil {
		c.Printf("gofer: %s 未在运行（无 pidfile %s）\n", label, pidPath)
		return nil
	}
	if !daemon.PIDAlive(pid) {
		c.Printf("gofer: %s 未在运行（pid=%d 已退出），清理残留 pidfile\n", label, pid)
		daemon.RemovePIDFile(pidPath)
		return nil
	}

	if err := daemon.Terminate(pid); err != nil {
		return errorx.Failf(stopExitErr, "send SIGTERM to %s (pid=%d): %v", label, pid, err)
	}
	c.Printf("gofer: 已向 %s(pid=%d) 发送 SIGTERM，等待退出...\n", label, pid)

	// The detached child removes its own pidfile on graceful exit; we still
	// RemovePIDFile defensively in case it died before cleanup.
	for waited := time.Duration(0); waited < stopWaitTimeout; waited += stopPoll {
		if !daemon.PIDAlive(pid) {
			daemon.RemovePIDFile(pidPath)
			c.Printf("gofer: %s 已停止\n", label)
			return nil
		}
		time.Sleep(stopPoll)
	}
	return errorx.Failf(stopExitErr, "%s(pid=%d) 在 %s 内未退出；可手动 kill -9 %d", label, pid, stopWaitTimeout, pid)
}
