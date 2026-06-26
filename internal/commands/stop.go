package commands

import (
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/daemon"
)

// stopExitErr is the process exit code when stop fails (unknown target / signal
// failed / timeout). Mirrors serve/worker exit codes.
const stopExitErr = 2

// stopPoll / stopWaitTimeout bound how `stop` waits for the target to exit after
// SIGTERM before reporting it may need a harder kill. The timeout is a touch
// above the server's shutdownGrace so a clean drain is not cut short.
const (
	stopPoll        = 200 * time.Millisecond
	stopWaitTimeout = 12 * time.Second
)

// NewStopCmd builds `gofer stop <serve|worker> [<worker-id>]`: read the daemon
// pidfile (<config-dir>/run/...), send SIGTERM and wait for a graceful exit
// (c44). It is the counterpart to `serve -d` / `worker -d`. Stopping something
// that is not running is not an error (idempotent): it reports so and clears any
// stale pidfile.
func NewStopCmd() *gcli.Command {
	return &gcli.Command{
		Name: "stop",
		Desc: "Stop a backgrounded (-d) serve or worker via its pidfile",
		Config: func(c *gcli.Command) {
			c.AddArg("target", "what to stop: serve | worker", true)
			c.AddArg("id", "worker id (required when target=worker)", false)
		},
		Func: runStop,
	}
}

func runStop(c *gcli.Command, _ []string) error {
	target := ""
	if a := c.Arg("target"); a != nil {
		target = strings.ToLower(a.String())
	}

	pidPath, label, err := stopTarget(c, target)
	if err != nil {
		return err
	}

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

// stopTarget resolves the pidfile path + display label for the stop target.
// serve has a fixed pidfile; worker requires a <worker-id> arg (its pidfile is
// id-namespaced).
func stopTarget(c *gcli.Command, target string) (pidPath, label string, err error) {
	switch target {
	case "serve":
		return servePIDFile(), "serve", nil
	case "worker":
		id := ""
		if a := c.Arg("id"); a != nil {
			id = a.String()
		}
		if id == "" {
			return "", "", errorx.Failf(stopExitErr, "stop worker requires a <worker-id> argument")
		}
		return workerPIDFile(id), "worker-" + id, nil
	default:
		return "", "", errorx.Failf(stopExitErr, "unknown stop target %q (use: serve | worker <id>)", target)
	}
}
