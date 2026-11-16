//go:build windows

package commands

import (
	"context"

	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/servicemgr"
)

// workerServiceRefuseRoot: Windows has no root, and an administrator registering
// the task is fine because the task itself always runs at the lowest privilege.
func workerServiceRefuseRoot() error { return nil }

func workerServiceSpecDefaults(spec *servicemgr.Spec) error {
	self, err := daemon.CurrentProcessIdentity()
	if err != nil {
		return err
	}
	spec.Backend = servicemgr.BackendWindowsTask
	spec.Owner = self.Owner
	// Worker tasks are logon-triggered and least-privilege by definition.
	spec.Elevated = false
	return nil
}

func workerServiceRegisterNative(ctx context.Context, m *servicemgr.Manager, spec servicemgr.Spec, start bool) error {
	_ = ctx
	return m.WindowsRegister(spec, servicemgr.WindowsRegisterOptions{Start: start})
}

func workerServiceStartNative(_ context.Context, m *servicemgr.Manager) error {
	return m.WindowsStart()
}

func workerServiceStopNative(_ context.Context, m *servicemgr.Manager) error {
	return m.WindowsStop()
}

func workerServiceUninstallNative(_ context.Context, m *servicemgr.Manager) error {
	return m.WindowsUninstall()
}

func workerServiceStatusNative(_ context.Context, m *servicemgr.Manager) (workerServiceState, error) {
	status, err := m.WindowsStatus()
	state := workerServiceState{Name: m.Name, Registered: status.Registered,
		// Task Scheduler state 4 is "running".
		Active: status.TaskState == 4, Verified: status.ServerVerified, PID: status.Server.PID,
		NativeState: taskStateName(status.TaskState), Detail: status.IdentityError}
	return state, err
}

func taskStateName(state int32) string {
	switch state {
	case 1:
		return "disabled"
	case 2:
		return "queued"
	case 3:
		return "ready"
	case 4:
		return "running"
	default:
		return "unknown"
	}
}
