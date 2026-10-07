//go:build linux

package servicemgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/procattr"
)

func verifyManagedForUpgrade(ctx context.Context, m *Manager, _ Spec) error {
	status, err := SystemdStatusOf(ctx, m)
	if err != nil {
		return err
	}
	if !status.Installed || !status.Active || status.Server.PID == 0 {
		return errors.New("managed systemd server identity is not verified")
	}
	return nil
}

func launchIndependentHelper(ctx context.Context, m *Manager, spec Spec, image, receiptPath string) error {
	id := strings.TrimSuffix(filepath.Base(receiptPath), ".json")
	unit := "gofer-upgrade-" + id
	logPath := filepath.Join(m.ConfigDir, "run", "upgrade", id+"-helper.log")
	args := []string{"--unit=" + unit, "--collect", "--service-type=exec", "--setenv=GOFER_CONFIG_DIR=" + spec.ConfigDir,
		"--working-directory=" + spec.WorkDir, "--property=StandardOutput=append:" + logPath, "--property=StandardError=append:" + logPath}
	if spec.Backend == BackendSystemdUser {
		args = append([]string{"--user"}, args...)
	}
	if spec.Backend == BackendSystemdSystem {
		args = append(args, "--property=User="+spec.RunAs)
	}
	args = append(args, "--", image, "serve", "upgrade-helper", "--receipt", receiptPath)
	cmd := exec.CommandContext(ctx, "systemd-run", args...)
	procattr.Background(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start independent systemd upgrade unit: %w: %s", err, strings.TrimSpace(string(out)))
	}
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
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(expected.PID), "cgroup"))
	if err != nil {
		return false, err
	}
	if !strings.Contains(string(data), "gofer-upgrade-"+id+".service") {
		return false, errors.New("upgrade helper is not in its independent systemd unit")
	}
	return true, nil
}

func platformStopForUpgrade(ctx context.Context, m *Manager) error { return SystemdStop(ctx, m) }
func platformStartForUpgrade(ctx context.Context, m *Manager) error {
	_, err := SystemdStart(ctx, m)
	return err
}
func refreshSupervisorForUpgrade(*Manager, Spec) error { return nil }
func prepareRollbackStart(ctx context.Context, m *Manager, spec Spec) error {
	return prepareRollbackStartWithBackend(ctx, m, spec, nativeSystemd())
}
func prepareRollbackStartWithBackend(ctx context.Context, m *Manager, spec Spec, backend systemdBackend) error {
	if _, _, err := backend.readRegistered(ctx, spec); err != nil {
		return err
	}
	_, err := backend.ctl(ctx, spec, "reset-failed", spec.Name+".service")
	return err
}
func backupSupervisorForUpgrade(*Manager, Spec) (func() error, func(), error) {
	return func() error { return nil }, func() {}, nil
}
