//go:build windows

package servicemgr

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/inhere/gofer/internal/daemon"
	"golang.org/x/sys/windows"
)

type WindowsRegisterOptions struct {
	Adopt bool
	Start bool
}

type WindowsStatus struct {
	Registered     bool
	TaskState      int32
	StopRequested  bool
	Supervisor     daemon.ProcessIdentity
	Server         daemon.ProcessIdentity
	ServerVerified bool
	IdentityError  string
}

var windowsNativeRegister = registerScheduledTask
var windowsPersistSpec = (*Manager).SaveSpec

func validateWindowsRegistration(spec Spec) error {
	if spec.Backend != BackendWindowsTask {
		return fmt.Errorf("backend %q is not Windows Task Scheduler", spec.Backend)
	}
	if err := spec.CheckFiles(); err != nil {
		return err
	}
	self, err := daemon.CurrentProcessIdentity()
	if err != nil {
		return err
	}
	if self.Owner != spec.Owner {
		return fmt.Errorf("Windows task must use the current interactive user's SID")
	}
	if spec.Elevated && !windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("elevated task registration requires an elevated process")
	}
	return nil
}

// WindowsRegister creates or updates only a task whose managed identity has
// been verified. It never starts by default. Existing unknown tasks are left
// untouched; the legacy-script exception requires explicit Adopt.
func (m *Manager) WindowsRegister(spec Spec, opts WindowsRegisterOptions) error {
	if spec.Name != m.Name || spec.ConfigDir != m.ConfigDir {
		return ErrIdentityMismatch
	}
	if err := validateWindowsRegistration(spec); err != nil {
		return err
	}
	lock, err := m.Acquire()
	if err != nil {
		return err
	}
	defer lock.Release()
	previousTask, found, err := readScheduledTask(m.Name)
	if err != nil {
		return err
	}
	previousSpec, specErr := m.LoadSpec()
	if specErr != nil && !errors.Is(specErr, os.ErrNotExist) {
		return specErr
	}
	if found {
		if specErr == nil && previousSpec == spec &&
			verifyOwnedTask(previousTask.XML, spec, m.SupervisorPath(), m.SpecPath()) == nil {
			if _, err := os.Stat(m.SupervisorPath()); err == nil {
				if opts.Start {
					return m.windowsStartLocked(spec)
				}
				return nil
			}
		}
		if specErr == nil {
			if err := verifyOwnedTask(previousTask.XML, previousSpec, m.SupervisorPath(), m.SpecPath()); err != nil {
				return err
			}
		} else if !opts.Adopt {
			return fmt.Errorf("task %q exists but is not registered by this Gofer instance", m.Name)
		} else if err := verifyAdoptableLegacyTask(previousTask.XML, spec); err != nil {
			return err
		}
		if previousTask.State == 4 && (specErr != nil || previousSpec != spec) {
			return ErrRunningSpecChange
		}
		if specErr == nil {
			if state, stateErr := m.LoadState(); stateErr == nil {
				if state.Supervisor.PID > 0 && sameLiveProcess(state.Supervisor) {
					return ErrRunningSpecChange
				}
			} else if !errors.Is(stateErr, os.ErrNotExist) {
				return stateErr
			}
			if pid, pidErr := daemon.ReadPIDFile(filepath.Join(previousSpec.RuntimeDir, "serve.pid")); pidErr == nil && daemon.PIDAlive(pid) {
				return ErrRunningSpecChange
			} else if pidErr != nil && !errors.Is(pidErr, os.ErrNotExist) {
				return pidErr
			}
		}
	}
	xmlText, err := buildTaskXML(spec, m.SupervisorPath(), m.SpecPath())
	if err != nil {
		return err
	}
	if err := validateScheduledTaskXML(m.Name, xmlText, spec.Owner); err != nil {
		return err
	}
	restoreSupervisor, clearBackup, err := snapshotSupervisor(m.SupervisorPath())
	if err != nil {
		return err
	}
	defer clearBackup()
	if err := copySupervisorBinary(spec.Exe, m.SupervisorPath()); err != nil {
		return errors.Join(err, restoreSupervisor())
	}
	if err := windowsNativeRegister(m.Name, xmlText, spec.Owner, found); err != nil {
		return errors.Join(err, restoreSupervisor())
	}
	if err := windowsPersistSpec(m, spec); err != nil {
		var rollbackErr error
		if found {
			owner := spec.Owner
			if specErr == nil {
				owner = previousSpec.Owner
			}
			rollbackErr = windowsNativeRegister(m.Name, previousTask.XML, owner, true)
		} else {
			rollbackErr = deleteScheduledTask(m.Name)
		}
		return errors.Join(fmt.Errorf("save managed spec after task registration: %w", err), rollbackErr, restoreSupervisor())
	}
	if opts.Start {
		return m.windowsStartLocked(spec)
	}
	return nil
}

func snapshotSupervisor(path string) (restore func() error, cleanup func(), err error) {
	backup, err := os.CreateTemp(filepath.Dir(path), ".supervisor-backup-*.exe")
	if err != nil {
		return nil, nil, err
	}
	backupPath := backup.Name()
	cleanup = func() { _ = os.Remove(backupPath) }
	old, openErr := os.Open(path)
	if errors.Is(openErr, os.ErrNotExist) {
		backup.Close()
		return func() error {
			if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}, cleanup, nil
	}
	if openErr != nil {
		backup.Close()
		cleanup()
		return nil, nil, openErr
	}
	_, copyErr := io.Copy(backup, old)
	closeOldErr := old.Close()
	syncErr := backup.Sync()
	closeBackupErr := backup.Close()
	if err := errors.Join(copyErr, closeOldErr, syncErr, closeBackupErr); err != nil {
		cleanup()
		return nil, nil, err
	}
	return func() error {
		in, err := os.Open(backupPath)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.CreateTemp(filepath.Dir(path), ".supervisor-restore-*.exe")
		if err != nil {
			return err
		}
		defer os.Remove(out.Name())
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Rename(out.Name(), path)
	}, cleanup, nil
}

func copySupervisorBinary(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	// Preserve an identical copy, including if a currently running supervisor
	// has the Windows image open. Updates require a stopped instance.
	if existing, err := os.Open(target); err == nil {
		oldHash := sha256.New()
		newHash := sha256.New()
		_, oldErr := io.Copy(oldHash, existing)
		_, newErr := io.Copy(newHash, in)
		existing.Close()
		if oldErr == nil && newErr == nil && string(oldHash.Sum(nil)) == string(newHash.Sum(nil)) {
			return nil
		}
		if _, err := in.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	out, err := os.CreateTemp(filepath.Dir(target), ".supervisor-*.exe")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), target)
}

func (m *Manager) ownedWindowsTask(spec Spec) (schedulerTask, error) {
	task, found, err := readScheduledTask(m.Name)
	if err != nil {
		return task, err
	}
	if !found {
		return task, os.ErrNotExist
	}
	if err := verifyOwnedTask(task.XML, spec, m.SupervisorPath(), m.SpecPath()); err != nil {
		return task, err
	}
	return task, nil
}

func (m *Manager) WindowsStart() error {
	lock, err := m.Acquire()
	if err != nil {
		return err
	}
	defer lock.Release()
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	return m.windowsStartLocked(spec)
}

func (m *Manager) windowsStartLocked(spec Spec) error {
	task, err := m.ownedWindowsTask(spec)
	if err != nil {
		return err
	}
	if task.State == 4 {
		state, stateErr := m.LoadState()
		if stateErr == nil && state.Supervisor.PID > 0 {
			if live, err := daemon.InspectProcess(state.Supervisor.PID); err == nil && live == state.Supervisor {
				return nil
			}
		}
		return fmt.Errorf("task %q is running but supervisor identity is unverified", m.Name)
	}
	_, err = m.mutateWindowsState(func(state *State) error {
		state.StopRequested = false
		state.Supervisor = daemon.ProcessIdentity{}
		state.Server = daemon.ProcessIdentity{}
		return nil
	})
	if err != nil {
		return err
	}
	if err := runScheduledTask(m.Name); err != nil {
		_, _ = m.mutateWindowsState(func(state *State) error { state.StopRequested = true; return nil })
		return fmt.Errorf("start task %q: %w", m.Name, err)
	}
	return nil
}

// WindowsStop records intent before signaling. Unknown/reused PIDs are never
// terminated. A timeout leaves the task registered and reports a failure.
func (m *Manager) WindowsStop() error {
	lock, err := m.Acquire()
	if err != nil {
		return err
	}
	defer lock.Release()
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	return m.windowsStopLocked(spec)
}

func (m *Manager) windowsStopLocked(spec Spec) error {
	if _, err := m.ownedWindowsTask(spec); err != nil {
		return err
	}
	state, err := m.mutateWindowsState(func(state *State) error { state.StopRequested = true; return nil })
	if err != nil {
		return err
	}
	if state.Server.PID > 0 && sameLiveProcess(state.Server) {
		identity, err := m.InspectServer()
		if err != nil {
			return fmt.Errorf("refuse to stop unverified server: %w", err)
		}
		if identity != state.Server {
			return ErrIdentityMismatch
		}
		if err := requestVerifiedWindowsStop(identity); err != nil {
			return err
		}
	} else if state.Server.PID > 0 && daemon.PIDAlive(state.Server.PID) {
		return fmt.Errorf("refuse to stop pid=%d after its creation identity changed", state.Server.PID)
	} else if state.Server.PID == 0 {
		pid, readErr := daemon.ReadPIDFile(filepath.Join(spec.RuntimeDir, "serve.pid"))
		if readErr == nil && daemon.PIDAlive(pid) {
			return fmt.Errorf("refuse to stop pid=%d without recorded server identity", pid)
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("read server pidfile: %w", readErr)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		serverAlive := state.Server.PID > 0 && sameLiveProcess(state.Server)
		supervisorAlive := state.Supervisor.PID > 0 && sameLiveProcess(state.Supervisor)
		if !serverAlive && !supervisorAlive {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("managed task %q did not stop within 15s; manual inspection required", m.Name)
}

// The supervisor can publish the process identity before serve creates its
// named stop event. Retry only this startup window while the same process is
// alive; never substitute a hard kill or signal a reused PID.
func requestVerifiedWindowsStop(expected daemon.ProcessIdentity) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		if !sameLiveProcess(expected) {
			return nil
		}
		err := daemon.Terminate(expected.PID)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func sameLiveProcess(expected daemon.ProcessIdentity) bool {
	got, err := daemon.InspectProcess(expected.PID)
	return err == nil && got == expected
}

func (m *Manager) WindowsUninstall() error {
	lock, err := m.Acquire()
	if err != nil {
		return err
	}
	defer lock.Release()
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	if err := m.windowsStopLocked(spec); err != nil {
		return err
	}
	return deleteScheduledTask(m.Name)
}

func (m *Manager) WindowsStatus() (WindowsStatus, error) {
	var status WindowsStatus
	task, found, err := readScheduledTask(m.Name)
	if err != nil || !found {
		return status, err
	}
	status.Registered = true
	status.TaskState = task.State
	spec, err := m.LoadSpec()
	if err != nil {
		return status, err
	}
	if err := verifyOwnedTask(task.XML, spec, m.SupervisorPath(), m.SpecPath()); err != nil {
		return status, err
	}
	state, err := m.LoadState()
	if err == nil {
		status.StopRequested = state.StopRequested
		if state.Supervisor.PID > 0 && sameLiveProcess(state.Supervisor) {
			status.Supervisor = state.Supervisor
		}
		if state.Server.PID > 0 {
			if server, checkErr := m.InspectServer(); checkErr == nil {
				status.Server = server
				status.ServerVerified = true
			} else {
				status.IdentityError = checkErr.Error()
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return status, err
	}
	return status, nil
}

func (m *Manager) windowsLogPath() string {
	return filepath.Join(m.serviceDir(), m.Name+".supervisor.log")
}

func (m *Manager) windowsOutPath(spec Spec) string {
	return filepath.Join(spec.RuntimeDir, "serve.out.log")
}

// The state lock is separate from the registration lock: stop may hold the
// latter while waiting for the supervisor to observe intent and exit.
func (m *Manager) mutateWindowsState(change func(*State) error) (State, error) {
	lock, err := AcquireLock(m.StatePath() + ".lock")
	if err != nil {
		return State{}, err
	}
	defer lock.Release()
	state, err := m.LoadState()
	if errors.Is(err, os.ErrNotExist) {
		state = State{SchemaVersion: StateSchema, Name: m.Name}
	} else if err != nil {
		return state, err
	}
	if err := change(&state); err != nil {
		return state, err
	}
	return state, m.SaveState(state)
}
