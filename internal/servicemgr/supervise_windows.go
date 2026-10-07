//go:build windows

package servicemgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/util"
)

const maxFastFailures = 3

var fastFailure = 5 * time.Second
var restartPause = 2 * time.Second

// Supervise runs the registered server from the copied supervisor image. It
// records process identity before reporting readiness, and stops after a finite
// fast-failure budget. Upgrade rollback is owned by the separate upgrade helper.
func Supervise(ctx context.Context, specPath string) error {
	spec, err := LoadSpec(specPath)
	if err != nil {
		return err
	}
	if spec.Backend != BackendWindowsTask {
		return fmt.Errorf("supervise requires Windows Task Scheduler spec")
	}
	m, err := NewManager(spec.ConfigDir, spec.Name)
	if err != nil {
		return err
	}
	if filepath.Clean(specPath) != m.SpecPath() {
		return ErrIdentityMismatch
	}
	self, err := daemon.CurrentProcessIdentity()
	if err != nil {
		return err
	}
	if !daemon.SameExecutable(self.Exe, m.SupervisorPath()) || self.Owner != spec.Owner {
		return ErrIdentityMismatch
	}
	logFile, err := os.OpenFile(m.windowsLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	writeLog := func(message string, args ...any) {
		_, _ = fmt.Fprintf(logFile, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(message, args...))
		_ = logFile.Sync()
	}
	state, err := m.mutateWindowsState(func(state *State) error {
		if state.StopRequested {
			return errStopRequested
		}
		state.Supervisor = self
		return nil
	})
	if errors.Is(err, errStopRequested) {
		return nil
	}
	if err != nil {
		return err
	}
	if state.StopRequested {
		return nil
	}
	defer func() {
		_, _ = m.mutateWindowsState(func(state *State) error {
			if state.Supervisor == self {
				state.Supervisor = daemon.ProcessIdentity{}
			}
			return nil
		})
	}()
	failures := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		state, err := m.LoadState()
		if err != nil {
			return err
		}
		if state.StopRequested {
			return nil
		}
		if failures >= maxFastFailures {
			return fmt.Errorf("server failed %d times within %s", failures, fastFailure)
		}
		args, err := spec.ServerArgs()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(m.windowsOutPath(spec), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		cmd := exec.Command(spec.Exe, args...)
		procattr.Background(cmd)
		cmd.Dir = spec.WorkDir
		cmd.Env = util.Environ(map[string]string{config.EnvConfigDir: spec.ConfigDir})
		cmd.Stdin = nil
		cmd.Stdout, cmd.Stderr = out, out
		started := time.Now()
		if err := cmd.Start(); err != nil {
			out.Close()
			failures++
			writeLog("server start failed: %v", err)
		} else {
			identity, inspectErr := daemon.InspectProcess(cmd.Process.Pid)
			if inspectErr != nil || !daemon.SameExecutable(identity.Exe, spec.Exe) || identity.Owner != spec.Owner {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				out.Close()
				return fmt.Errorf("server identity could not be verified: %v", inspectErr)
			}
			if _, err := m.mutateWindowsState(func(state *State) error {
				if state.StopRequested {
					return errStopRequested
				}
				state.Server = identity
				return nil
			}); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				out.Close()
				return err
			}
			writeLog("server started pid=%d", identity.PID)
			waitErr := cmd.Wait()
			out.Close()
			_, _ = m.mutateWindowsState(func(state *State) error {
				if state.Server == identity {
					state.Server = daemon.ProcessIdentity{}
				}
				return nil
			})
			writeLog("server exited pid=%d uptime=%s error=%v", identity.PID, time.Since(started).Round(time.Millisecond), waitErr)
			if time.Since(started) < fastFailure {
				failures++
			} else {
				failures = 0
			}
		}
		state, err = m.LoadState()
		if err != nil {
			return err
		}
		if state.StopRequested {
			return nil
		}
		timer := time.NewTimer(restartPause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

var errStopRequested = errors.New("managed service stop requested")
