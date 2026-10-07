//go:build windows

package servicemgr

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/util"
)

func verifyManagedForUpgrade(_ context.Context, m *Manager, spec Spec) error {
	status, err := m.WindowsStatus()
	if err != nil {
		return err
	}
	if !status.Registered || !status.ServerVerified || status.Supervisor.PID == 0 {
		return fmt.Errorf("managed Windows server identity is not verified")
	}
	return nil
}

func launchIndependentHelper(_ context.Context, m *Manager, spec Spec, image, receiptPath string) error {
	logPath := filepath.Join(m.ConfigDir, "run", "upgrade", filepath.Base(image)+".log")
	cmd, err := daemon.StartDetachedStrict(image, []string{"serve", "upgrade-helper", "--receipt", receiptPath},
		util.Environ(map[string]string{config.EnvConfigDir: spec.ConfigDir}), logPath)
	if err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func inspectIndependentHelper(m *Manager, spec Spec, id string, expected daemon.ProcessIdentity) (bool, error) {
	live, err := daemon.InspectProcess(expected.PID)
	if err != nil {
		return false, err
	}
	if live != expected || !daemon.SameExecutable(live.Exe, helperImagePath(m, id)) || live.Owner != spec.Owner {
		return false, ErrIdentityMismatch
	}
	return true, nil
}

func platformStopForUpgrade(_ context.Context, m *Manager) error  { return m.WindowsStop() }
func platformStartForUpgrade(_ context.Context, m *Manager) error { return m.WindowsStart() }
func refreshSupervisorForUpgrade(m *Manager, spec Spec) error {
	return copySupervisorBinary(spec.Exe, m.SupervisorPath())
}
func prepareRollbackStart(context.Context, *Manager, Spec) error { return nil }
func backupSupervisorForUpgrade(m *Manager, _ Spec) (func() error, func(), error) {
	return snapshotSupervisor(m.SupervisorPath())
}
