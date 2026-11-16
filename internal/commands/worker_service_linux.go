//go:build linux

package commands

import (
	"context"
	"errors"
	"os"
	"os/user"

	"github.com/inhere/gofer/internal/servicemgr"
)

// workerServiceRefuseRoot: a worker service is a USER-level registration, so
// registering it as root would create a unit in root's own user manager that no
// login session ever reaches. The one-line install script has the same rule.
// workerServiceEUID is the effective uid seam: tests cannot run as root.
var workerServiceEUID = os.Geteuid

func workerServiceRefuseRoot() error {
	if workerServiceEUID() == 0 {
		return errors.New("do not register a worker service as root: it must run as the login user " +
			"that owns worker.yaml; rerun as that user (systemd --user, and `loginctl enable-linger <user>` to keep it up while logged out)")
	}
	return nil
}

// workerServiceSpecDefaults fills the backend and owner of a worker registration:
// always the current user's own systemd instance.
func workerServiceSpecDefaults(spec *servicemgr.Spec) error {
	current, err := user.Current()
	if err != nil {
		return err
	}
	spec.Backend = servicemgr.BackendSystemdUser
	spec.Owner = current.Uid
	return nil
}

func workerServiceRegisterNative(ctx context.Context, m *servicemgr.Manager, spec servicemgr.Spec, start bool) error {
	if err := servicemgr.SystemdRegister(ctx, m, spec); err != nil {
		return err
	}
	if !start {
		return nil
	}
	_, err := servicemgr.SystemdStart(ctx, m)
	return err
}

func workerServiceStartNative(ctx context.Context, m *servicemgr.Manager) error {
	_, err := servicemgr.SystemdStart(ctx, m)
	return err
}

func workerServiceStopNative(ctx context.Context, m *servicemgr.Manager) error {
	return servicemgr.SystemdStop(ctx, m)
}

func workerServiceUninstallNative(ctx context.Context, m *servicemgr.Manager) error {
	return servicemgr.SystemdUninstall(ctx, m)
}

func workerServiceStatusNative(ctx context.Context, m *servicemgr.Manager) (workerServiceState, error) {
	status, err := servicemgr.SystemdStatusOf(ctx, m)
	state := workerServiceState{Name: m.Name, Registered: status.Installed, Active: status.Active,
		PID: status.MainPID, NativeState: status.SubState, Verified: status.Active && status.Server.PID > 0}
	return state, err
}
