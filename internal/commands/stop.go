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
// the stop request before reporting that a manual kill may be needed. The timeout
// is a touch above the server's shutdownGrace so a clean drain is not cut short.
const (
	stopPoll        = 200 * time.Millisecond
	stopWaitTimeout = 12 * time.Second
	// stopRequestRetry bounds how long a stop keeps re-asking a LIVE target that
	// has not accepted the request yet. A Windows process publishes its pidfile at
	// spawn time but can only be stopped once it created its stop event (see
	// internal/daemon), so a stop issued right after `serve -d` — or by a supervisor
	// right after a restart — would otherwise fail on a process that is alive and
	// about to become stoppable. On unix Terminate succeeds on the first attempt, so
	// the retry never runs there.
	stopRequestRetry = 3 * time.Second
)

// stopDaemon reads the daemon pidfile at pidPath, asks the process to stop and
// waits for a graceful exit (c44). It backs `serve stop` / `worker stop`. What
// "ask to stop" means is platform-specific (SIGTERM on unix, the named stop event
// on Windows — see internal/daemon), and the manual fallback it prints comes from
// daemon.KillHint so no unix-only command leaks into the Windows output. Because a
// pidfile can be visible before the process is able to accept a stop, the request
// is repeated for a short window while the target is alive (stopRequestRetry).
// Stopping something that is not running is not an error (idempotent): it reports
// so and clears any stale pidfile. label is the human-facing target name (e.g.
// "serve", "worker-<id>").
func stopDaemon(c *gcli.Command, pidPath, label string) error {
	pid, err := daemon.ReadPIDFile(pidPath)
	if err != nil {
		c.Printf("%s\n", cliText("cli.daemon.stop.not_running_no_pidfile", "label", label, "path", pidPath))
		return nil
	}
	if !daemon.PIDAlive(pid) {
		c.Printf("%s\n", cliText("cli.daemon.stop.not_running_exited", "label", label, "pid", pid))
		daemon.RemovePIDFile(pidPath)
		return nil
	}

	var stopErr error
	for waited := time.Duration(0); waited < stopRequestRetry; waited += stopPoll {
		stopErr = daemon.Terminate(pid)
		// A failed request is only worth repeating while the target is still alive;
		// if it exited meanwhile the wait loop below reports the stop.
		if stopErr == nil || !daemon.PIDAlive(pid) {
			break
		}
		time.Sleep(stopPoll)
	}
	if stopErr != nil && daemon.PIDAlive(pid) {
		return errorx.Failf(stopExitErr, "stop %s (pid=%d): %v", label, pid, stopErr)
	}
	if stopErr == nil {
		c.Printf("%s\n", cliText("cli.daemon.stop.signal_sent", "label", label, "pid", pid))
	}

	// The stopped process removes its own pidfile on graceful exit; we still
	// RemovePIDFile defensively in case it died before cleanup.
	for waited := time.Duration(0); waited < stopWaitTimeout; waited += stopPoll {
		if !daemon.PIDAlive(pid) {
			daemon.RemovePIDFile(pidPath)
			c.Printf("%s\n", cliText("cli.daemon.stop.stopped", "label", label))
			return nil
		}
		time.Sleep(stopPoll)
	}
	return errorx.Failf(stopExitErr, "%s", cliText("cli.daemon.stop.timeout",
		"label", label, "pid", pid, "timeout", stopWaitTimeout, "hint", daemon.KillHint(pid)))
}
